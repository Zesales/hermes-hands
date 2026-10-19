package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The transcript: once hermes-hands has compacted a session (internal/compact),
// it — not hermes-agent — holds the conversation the model sees, and sends it
// along with every run (api.Runs.AskWith). It is rendered from the event log
// like every other view: the operator's messages, Hermes' replies (KindReply),
// the hands' calls and results, the answers. A compaction is an event too
// (KindCompact): it never rewrites the log, it only says how much of it is
// sent elided. Nothing is thrown away — an elided output stays in out/<ref>.txt
// and the model reads it back with local_output.

// Msg is one message of the transcript.
type Msg struct{ Role, Content string }

// TranscriptOpts says how to render the transcript.
type TranscriptOpts struct {
	// Upto: events with Seq <= Upto are sent elided — a call's output as a
	// stub naming its #ref, a reply without its long arguments. Collapse:
	// turns <= Collapse are sent as the operator's message and the answer
	// only. Both come from the compaction in effect (LastCompact).
	Upto, Collapse int
	// Output reads the stored output of call #ref for a verbatim result.
	Output func(ref int) string
	// RoundOutput is the round output budget, applied to a verbatim round
	// like the live round had it (FitRound).
	RoundOutput int
}

// LastCompact is the compaction in effect: the last KindCompact event of evs.
// ok is false while hermes-agent holds the transcript (none yet).
func LastCompact(evs []Event) (Event, bool) {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == KindCompact {
			return evs[i], true
		}
	}
	return Event{}, false
}

// elidedLimits: what an elided reply keeps of its prose and of each string
// argument (a local_write's content, a long command).
const (
	elideProse = 600
	elideArg   = 400
)

// ElideMin is the smallest output a compaction elides: a shorter one costs
// less than the stub that would name it.
const ElideMin = 300

// Transcript renders evs as the conversation the model sees, oldest first,
// strictly alternating user / assistant (consecutive messages of one role
// are merged). The caller drops a trailing user message when it is about to
// send that one as the run's input.
func Transcript(evs []Event, o TranscriptOpts) []Msg {
	var t transcript
	for _, turn := range Turns(evs) {
		if len(turn) == 0 || turn[0].Turn == 0 {
			continue // events outside any turn (a /compact before the first one)
		}
		if turn[0].Turn <= o.Collapse {
			t.collapsed(turn)
			continue
		}
		t.turn(turn, o)
	}
	return t.msgs
}

type transcript struct {
	msgs []Msg
}

func (t *transcript) add(role, content string) {
	if content == "" {
		return
	}
	if n := len(t.msgs); n > 0 && t.msgs[n-1].Role == role {
		t.msgs[n-1].Content += "\n\n" + content
		return
	}
	t.msgs = append(t.msgs, Msg{Role: role, Content: content})
}

// collapsed sends a turn as its question and its answer, with a line that
// says which calls ran in between — each still readable with local_output.
func (t *transcript) collapsed(turn []Event) {
	var refs []int
	tools := map[string]int{}
	answer, ok := "", true
	for _, e := range turn {
		switch e.Kind {
		case KindUser:
			t.add("user", "[operator] "+e.Text)
		case KindCall:
			refs = append(refs, e.Ref)
			tools[e.Tool]++
		case KindAnswer:
			answer, ok = e.Text, e.OK == nil || *e.OK
		}
	}
	var b strings.Builder
	if len(refs) > 0 {
		fmt.Fprintf(&b, "[turn %d, compacted by hermes-hands: %s ran as calls #%d–#%d; local_output {\"ref\":N} reads any of their outputs]\n",
			turn[0].Turn, toolCounts(tools), refs[0], refs[len(refs)-1])
	}
	switch {
	case answer == "":
		b.WriteString("(no answer — the turn ended before one)")
	case ok:
		b.WriteString(answer)
	default:
		b.WriteString("(the turn stopped: " + answer + ")")
	}
	t.add("assistant", b.String())
}

// turn sends a turn round by round: the operator's message, every reply,
// every round's results.
func (t *transcript) turn(turn []Event, o TranscriptOpts) {
	var (
		calls   []Event           // the batch in flight: its calls …
		results = map[int]Event{} // … and their results, by #ref
		server  []string          // Hermes' own server tools before the next reply
		replied bool              // this turn's log has Hermes' replies (older logs do not)
		final   bool              // the last reply asked for nothing more: it was the answer
	)
	// withServer puts the server tools Hermes ran in this round in front of
	// what it said next.
	withServer := func(text string) string {
		if len(server) == 0 {
			return text
		}
		text = strings.Join(server, "\n") + "\n" + text
		server = nil
		return text
	}
	flush := func() {
		if len(calls) == 0 {
			return
		}
		if n := len(t.msgs); n == 0 || t.msgs[n-1].Role != "assistant" {
			// a log without replies: the envelope is rebuilt from the
			// calls it asked for
			t.add("assistant", withServer(envelope(calls, calls[0].Seq <= o.Upto)))
		}
		t.add("user", t.results(calls, results, o))
		calls, results = nil, map[int]Event{}
	}
	for _, e := range turn {
		switch e.Kind {
		case KindUser:
			flush()
			t.add("user", "[operator] "+e.Text)
		case KindHermesTool:
			server = append(server, fmt.Sprintf("[your server tool %s: %s — %s]", e.Tool, clip(e.Text, 160), firstOr(e.Action, "ran")))
		case KindReply:
			flush()
			text := e.Text
			if e.Seq <= o.Upto {
				text = elideReply(text)
			}
			t.add("assistant", withServer(text))
			replied, final = true, true
		case KindCall:
			calls = append(calls, e)
			final = false
		case KindResult:
			results[e.Ref] = e
		case KindAnswer:
			flush()
			switch {
			case e.OK != nil && !*e.OK, replied && !final:
				// BLOCKED, or the turn ended while Hermes still asked
				// for calls: hermes-hands said this, not the model
				t.add("user", "[hermes-hands: the turn stopped — "+e.Text+"]")
			case !replied:
				t.add("assistant", withServer(e.Text)) // an older log: the answer is the only reply kept
			}
		}
	}
	flush()
}

// results renders one batch's results the way the hands sent them: verbatim
// (the stored output, fitted to the round budget again) or elided (a stub).
func (t *transcript) results(calls []Event, results map[int]Event, o TranscriptOpts) string {
	type elem struct {
		ID       string          `json:"id"`
		Ref      int             `json:"ref"`
		Tool     string          `json:"tool"`
		Args     json.RawMessage `json:"args"`
		ExitCode int             `json:"exit_code"`
		Output   string          `json:"output"`
	}
	elems := make([]elem, 0, len(calls))
	var outs []string
	var refs, verbatim []int
	for _, c := range calls {
		r, ran := results[c.Ref]
		e := elem{ID: c.ID, Ref: c.Ref, Tool: c.Tool, Args: c.Args}
		switch {
		case !ran:
			e.ExitCode, e.Output = 125, "(not run — the turn stopped before this call)"
		case r.Seq <= o.Upto && r.Bytes >= ElideMin:
			e.ExitCode = intOr(r.Exit, 0)
			e.Args = elideArgs(c.Args)
			e.Output = fmt.Sprintf(`[elided by hermes-hands: %d lines, %d bytes — local_output {"ref":%d} reads it]`, r.Lines, r.Bytes, c.Ref)
		default:
			e.ExitCode = intOr(r.Exit, 0)
			if o.Output != nil {
				e.Output = o.Output(c.Ref)
			}
			verbatim = append(verbatim, len(elems))
			outs, refs = append(outs, e.Output), append(refs, c.Ref)
		}
		elems = append(elems, e)
	}
	for i, out := range FitRound(outs, refs, o.RoundOutput) {
		elems[verbatim[i]].Output = out
	}
	b, _ := marshalNoEscape(map[string]any{"results": elems})
	return "[hands results]\n" + string(b)
}

// envelope rebuilds the reply that asked for calls.
func envelope(calls []Event, elided bool) string {
	type call struct {
		ID   string          `json:"id"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	cs := make([]call, 0, len(calls))
	for _, c := range calls {
		args := c.Args
		if elided {
			args = elideArgs(args)
		}
		cs = append(cs, call{ID: c.ID, Tool: c.Tool, Args: args})
	}
	b, _ := marshalNoEscape(map[string]any{"calls": cs, "final": nil})
	return string(b)
}

// elideReply shortens an old reply: its prose clipped, the envelope's long
// string arguments replaced by their size. The final stays whole — it is what
// the operator was told.
func elideReply(text string) string {
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return clipKeep(text, elideProse)
	}
	prose := strings.TrimSpace(text[:start])
	var env map[string]json.RawMessage
	if json.Unmarshal([]byte(text[start:end+1]), &env) != nil {
		return clipKeep(text, elideProse)
	}
	if raw, ok := env["calls"]; ok {
		var calls []map[string]json.RawMessage
		if json.Unmarshal(raw, &calls) == nil {
			for _, c := range calls {
				for _, k := range []string{"args", "arguments"} {
					if a, ok := c[k]; ok {
						c[k] = elideArgs(a)
					}
				}
			}
			if b, err := marshalNoEscape(calls); err == nil {
				env["calls"] = b
			}
		}
	}
	b, err := marshalNoEscape(env)
	if err != nil {
		return clipKeep(text, elideProse)
	}
	if prose == "" {
		return string(b)
	}
	return clipKeep(prose, elideProse) + "\n" + string(b)
}

// elideArgs replaces every string argument longer than elideArg by its size.
func elideArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return raw
	}
	changed := false
	for k, v := range args {
		if s, ok := v.(string); ok && len(s) > elideArg {
			args[k] = fmt.Sprintf("[%d bytes — elided by hermes-hands]", len(s))
			changed = true
		}
	}
	if !changed {
		return raw
	}
	b, err := marshalNoEscape(args)
	if err != nil {
		return raw
	}
	return b
}

// toolCounts is "local_shell ×4, local_read ×2".
func toolCounts(m map[string]int) string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s ×%d", n, m[n]))
	}
	return strings.Join(parts, ", ")
}

// clipKeep cuts s to n bytes (on a rune boundary), keeping its line breaks.
func clipKeep(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:runeStart(s, n)] + " […]"
}

func intOr(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

func firstOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func marshalNoEscape(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(b.String(), "\n")), nil
}
