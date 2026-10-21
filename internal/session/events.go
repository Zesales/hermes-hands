package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EventsVersion is the schema version written into every event (Event.V). A
// reader that meets a higher version renders what it knows and skips the
// rest; bump it only for an incompatible change.
const EventsVersion = 1

// Event kinds, in the order a turn produces them.
const (
	KindUser       = "user"        // the operator's message (Text)
	KindAdded      = "added"       // what the operator wrote while the turn ran, sent with the next round (Text)
	KindSay        = "say"         // Hermes' one sentence to the operator beside a round's calls (Text, Round)
	KindCall       = "call"        // a hands call, written BEFORE it runs (Ref, ID, Tool, Args)
	KindNotRun     = "not_run"     // a call of an envelope the hands did not run (ID, Tool, Args, Text = why, Round)
	KindResult     = "result"      // its outcome (Ref, Tool, Exit, Ms, Lines, Bytes; output in out/<Ref>.txt)
	KindHermesTool = "hermes_tool" // Hermes used one of its OWN server tools (Tool, Text = preview, Action)
	KindApproval   = "approval"    // an approval prompt the operator answered (Text = summary, Detail, Action = answer, Ms = wait)
	KindAnswer     = "answer"      // the turn's answer (Text, OK, Tokens)
	KindNote       = "note"        // anything else the terminal said in a turn (Text)
	KindTurnEnd    = "turn_end"    // the turn footer (Ms, Outcome: "" | interrupted | timeout, Limit, LocalMs, WaitMs, Rounds, Calls)
	KindReply      = "reply"       // Hermes' reply of one round, as it arrived (Text, Round) — not shown, the transcript is built from it
	KindCompact    = "compact"     // hermes-hands compacted the transcript (Text = the note, Upto, Collapse, Tokens = after, Before, Checkpoint)
)

// Event is one line of <id>/events.jsonl: one thing the terminal showed. The
// log is append-only and the single source for rendering a session — live and
// on resume — so new views (only the last answer, a compacted summary, an
// export) are renderers over the same events, never a second store.
//
// Turn groups events: every event of operator turn N carries Turn N. Seq is
// the per-session order. Ref is the session-wide call number the operator sees
// as #Ref; Hermes sees the same number in its results, so both sides can
// point at a call. A future compaction can append a summary event covering a
// Seq range without rewriting history.
type Event struct {
	V    int    `json:"v"`
	Seq  int    `json:"seq"`
	At   string `json:"at"`
	Turn int    `json:"turn"`
	Kind string `json:"kind"`

	Text    string          `json:"text,omitempty"`
	Detail  string          `json:"detail,omitempty"` // approval: the diff / command detail the prompt showed
	Ref     int             `json:"ref,omitempty"`
	ID      string          `json:"id,omitempty"` // Hermes' own call id (c1, c2, …)
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Exit    *int            `json:"exit,omitempty"`
	Ms      int64           `json:"ms,omitempty"`
	Lines   int             `json:"lines,omitempty"`
	Bytes   int             `json:"bytes,omitempty"`
	Hash    string          `json:"hash,omitempty"` // a result's output, for "same output as #n" (OutputHash; ElideMin bytes and up)
	OK      *bool           `json:"ok,omitempty"`
	Tokens  int             `json:"tokens,omitempty"`
	Action  string          `json:"action,omitempty"`   // hermes_tool: "" | server | steered | stopped | repeat | approved | denied; approval: yes | no | all | quit | once | session | deny
	Outcome string          `json:"outcome,omitempty"`  // turn_end: "" | interrupted | timeout
	Limit   int             `json:"limit,omitempty"`    // turn_end timeout: the response timeout in seconds
	LocalMs int64           `json:"local_ms,omitempty"` // turn_end: time the hands' calls ran
	WaitMs  int64           `json:"wait_ms,omitempty"`  // turn_end: time spent waiting on the operator's approvals
	Rounds  int             `json:"rounds,omitempty"`   // turn_end: requests to Hermes this turn took
	Calls   int             `json:"calls,omitempty"`    // turn_end: hands calls this turn ran

	Round    int `json:"round,omitempty"`    // call / result / reply: the request to Hermes within the turn
	Upto     int `json:"upto,omitempty"`     // compact: events up to this Seq are sent elided
	Collapse int `json:"collapse,omitempty"` // compact: turns up to this one are sent as question + answer only
	Before   int `json:"before,omitempty"`   // compact: estimated tokens before it

	Checkpoint string `json:"checkpoint,omitempty"` // compact: its chat checkpoint, relative to the session directory

	// Out is a result's output in flight, never serialised: the recorder
	// stores it in out/<Ref>.txt and keeps only Lines / Bytes in the event.
	Out string `json:"-"`
}

// Int / Bool box a value for the optional pointer fields.
func Int(n int) *int    { return &n }
func Bool(b bool) *bool { return &b }

// maxOutputFile caps one stored call output. The hands already cap what they
// send to Hermes; this only bounds the local copy.
const maxOutputFile = 256 * 1024

// AppendEvent stamps ev (version, sequence, time) and appends it to the
// session's events.jsonl. It mutates ev so the caller can render the stamped
// event. Callers treat a failure as a warning — the turn goes on.
func (s *Store) AppendEvent(rec *Record, ev *Event) error {
	if err := validID(rec.ID); err != nil {
		return err
	}
	if !rec.seqLoaded {
		evs, _ := s.LoadEvents(rec)
		if n := len(evs); n > 0 {
			rec.seq = evs[n-1].Seq
		}
		rec.seqLoaded = true
	}
	rec.seq++
	ev.V = EventsVersion
	ev.Seq = rec.seq
	if ev.At == "" {
		ev.At = s.nowFn().UTC().Format(time.RFC3339Nano)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.sessionDir(rec.ID), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.eventsPath(rec.ID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// LoadEvents reads a session's events in order. A missing log is an empty
// session; an unreadable line (a torn write) is skipped.
func (s *Store) LoadEvents(rec *Record) ([]Event, error) {
	if err := validID(rec.ID); err != nil {
		return nil, err
	}
	f, err := os.Open(s.eventsPath(rec.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind == "" {
			continue
		}
		evs = append(evs, ev)
	}
	return evs, sc.Err()
}

// SaveOutput stores the full output of call #ref (already scrubbed by the
// caller), capped at maxOutputFile.
func (s *Store) SaveOutput(rec *Record, ref int, out string) error {
	if err := validID(rec.ID); err != nil {
		return err
	}
	if len(out) > maxOutputFile {
		out = out[:maxOutputFile] + "\n[… truncated by hermes-hands at " + strconv.Itoa(maxOutputFile) + " bytes]"
	}
	dir := s.sessionDir(rec.ID) + "/out"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(dir+"/"+strconv.Itoa(ref)+".txt", []byte(out), 0o600)
}

// LoadOutput reads the stored output of call #ref.
func (s *Store) LoadOutput(rec *Record, ref int) (string, error) {
	if err := validID(rec.ID); err != nil {
		return "", err
	}
	b, err := os.ReadFile(s.sessionDir(rec.ID) + "/out/" + strconv.Itoa(ref) + ".txt")
	return string(b), err
}

// DebugPath is where /debug writes the session's redacted wire trace.
func (s *Store) DebugPath(rec *Record) string { return s.sessionDir(rec.ID) + "/debug.jsonl" }

func (s *Store) eventsPath(id string) string { return s.sessionDir(id) + "/events.jsonl" }

// LastRef / LastTurn are the highest call number and turn in evs — where a
// reopened session continues counting.
func LastRef(evs []Event) int {
	n := 0
	for _, e := range evs {
		if e.Ref > n {
			n = e.Ref
		}
	}
	return n
}

func LastTurn(evs []Event) int {
	n := 0
	for _, e := range evs {
		if e.Turn > n {
			n = e.Turn
		}
	}
	return n
}

// Turns groups evs by turn, oldest first.
func Turns(evs []Event) [][]Event {
	var out [][]Event
	cur := -1
	for _, e := range evs {
		if len(out) == 0 || e.Turn != cur {
			out = append(out, nil)
			cur = e.Turn
		}
		out[len(out)-1] = append(out[len(out)-1], e)
	}
	return out
}

// Recap renders the operator's messages and the answers of evs as the block
// Hermes is shown when a resumed session's server-side history is gone: the
// last turns, each clipped so the replay stays token-friendly. Tool traffic
// is left out — the answers carry what was concluded.
func Recap(evs []Event) string {
	const maxTurns = 20
	turns := Turns(evs)
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	var b strings.Builder
	b.WriteString("[resumed session — the Hermes-side history of this session is gone, so hermes-hands replays its local record of it. Treat it as your own history: continue where it left off instead of re-doing finished steps.]\n")
	n := 0
	for _, t := range turns {
		for _, e := range t {
			switch e.Kind {
			case KindUser:
				b.WriteString("[operator] " + clip(e.Text, 300) + "\n")
				n++
			case KindAnswer:
				b.WriteString("[answer] " + clip(e.Text, 800) + "\n")
			}
		}
	}
	if n == 0 {
		return ""
	}
	return b.String()
}

func clip(s string, n int) string { return trunc(strings.Join(strings.Fields(s), " "), n) }

// Prune deletes every session idle longer than maxAge — the whole directory:
// events, outputs, debug trace. Idle is measured from the record's last update
// (its creation for a session that never ran a turn). maxAge <= 0 keeps
// everything. Returns the ids it removed.
func (s *Store) Prune(maxAge time.Duration) []string {
	if maxAge <= 0 {
		return nil
	}
	recs, err := s.List()
	if err != nil {
		return nil
	}
	now := s.nowFn()
	var gone []string
	for i := range recs {
		r := &recs[i]
		if IdleFor(r, now) <= maxAge || validID(r.ID) != nil {
			continue
		}
		if err := os.RemoveAll(s.sessionDir(r.ID)); err != nil {
			s.warnf("retention: %v", err)
			continue
		}
		gone = append(gone, r.ID)
	}
	// directories without a readable session.json (a crash mid-New) age out
	// by their modification time
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if !e.IsDir() || validID(e.Name()) != nil {
			continue
		}
		dir := s.dir + "/" + e.Name()
		if _, err := os.Stat(dir + "/session.json"); err == nil {
			continue
		}
		if fi, err := os.Stat(dir); err == nil && now.Sub(fi.ModTime()) > maxAge {
			if os.RemoveAll(dir) == nil {
				gone = append(gone, e.Name())
			}
		}
	}
	return gone
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validID rejects ids that could escape the sessions directory ("../x", "a/b").
func validID(id string) error {
	if !idRe.MatchString(id) || strings.Contains(id, "..") || filepath.Base(id) != id {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}
