// Package session is the local session store. One directory per session,
// under <state>/sessions/<id>/:
//
//	<id>/session.json       — the thin index record (`--session <id>` and
//	                          `sessions` work offline from these)
//	<id>/transcript.jsonl   — the machine transcript: canonical, append-only,
//	                          one JSON object per completed exchange
//	<id>/session-history.md — the FULL chat history rendered as readable
//	                          markdown, regenerated from the transcript after
//	                          every exchange (for the operator to read and for
//	                          a hermes-remote to fetch)
//
// The live conversation lives on the Hermes server; the transcript is hermes-
// hands' own backup mirror of it and session-history.md is that transcript's
// pure rendering (hand edits to the .md never leak into a resume replay).
// Open() migrates the pre-directory layout (<id>.json plus
// transcripts/<id>.jsonl) into this layout automatically on next start.
package session

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record is one session's index entry. The JSON field order matches what
// `jq -n` writes in hh_session_new (and the appended last_run_id from
// _hh_session_write).
type Record struct {
	ID               string `json:"id"`
	HermesSessionID  string `json:"hermes_session_id"`
	HermesSessionKey string `json:"hermes_session_key"`
	Cwd              string `json:"cwd"`
	Title            string `json:"title"`
	Created          string `json:"created"`
	Updated          string `json:"updated"`
	Turns            int    `json:"turns"`
	LastRunID        string `json:"last_run_id,omitempty"`
	LastTokens       int    `json:"last_tokens,omitempty"` // best-effort tokens from the last run's usage
	Splits           int    `json:"splits,omitempty"`      // times Hermes handed back a different session id (~ compactions)

	path string // on-disk path; not serialised
}

// Store is the <state>/sessions directory.
type Store struct {
	dir   string
	Warnf func(string, ...any)
	now   func() time.Time
	rnd   func() int
}

// Open ensures <stateDir>/sessions exists, migrates any pre-directory session
// files into <id>/ directories, and returns the store. stateDir is
// config.Config.StateDir (already resolved from the process env). Sessions are
// addressed by id only — the old per-directory "last session" pointer is gone
// (an editor/plugin keeps the id from the ready event; see Resolve).
func Open(stateDir string) (*Store, error) {
	s := &Store{dir: stateDir + "/sessions"}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	s.migrate()
	return s, nil
}

func (s *Store) sessionDir(id string) string     { return s.dir + "/" + id }
func (s *Store) path(id string) string           { return s.dir + "/" + id + "/session.json" }
func (s *Store) transcriptPath(id string) string { return s.dir + "/" + id + "/transcript.jsonl" }

// HistoryPath is where the FULL chat history lives as a readable document:
// <state>/sessions/<id>/session-history.md — one directory per session.
func (s *Store) HistoryPath(id string) string {
	return s.sessionDir(id) + "/session-history.md"
}

// migrate converts the flat pre-directory layout (<id>.json plus
// transcripts/<id>.jsonl) into <id>/session.json + <id>/transcript.jsonl +
// <id>/session-history.md. It runs on every Open and is idempotent: a session
// already in the directory layout is skipped. A per-session failure is skipped
// silently (the load fallbacks still read the legacy files), so a half-
// migrated state never hard-fails a run.
func (s *Store) migrate() {
	legacy, _ := filepath.Glob(s.dir + "/*.json")
	for _, p := range legacy {
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		if id == "" || id == "session" {
			continue
		}
		if _, err := os.Stat(s.path(id)); err == nil {
			continue // already migrated
		}
		if err := os.MkdirAll(s.sessionDir(id), 0o755); err != nil {
			continue
		}
		if err := os.Rename(p, s.path(id)); err != nil {
			continue
		}
		if b, err := os.ReadFile(s.dir + "/transcripts/" + id + ".jsonl"); err == nil {
			if os.WriteFile(s.transcriptPath(id), b, 0o600) == nil {
				_ = os.Remove(s.dir + "/transcripts/" + id + ".jsonl")
			}
		}
		rec := &Record{ID: id}
		if rb, err := os.ReadFile(s.path(id)); err == nil && json.Valid(rb) {
			_ = json.Unmarshal(rb, rec)
			rec.ID = id
		}
		if turns, _ := s.LoadTranscript(rec); len(turns) > 0 {
			_ = os.WriteFile(s.HistoryPath(id), []byte(RenderHistory(rec, turns)), 0o644)
		}
	}
	// tidy the emptied legacy transcripts directory
	if entries, err := os.ReadDir(s.dir + "/transcripts"); err == nil && len(entries) == 0 {
		_ = os.Remove(s.dir + "/transcripts")
	}
	// the legacy by-cwd "last session" pointer is obsolete — sessions are
	// addressed by id, so the pointer dir is dropped on Open
	_ = os.RemoveAll(s.dir + "/by-cwd")
}

// New mints a session rooted at cwd (hh_session_new): a local index id and the
// stable per-repo hermes_session_id / hermes_session_key. ts is shared between
// the local id and the hermes id.
func (s *Store) New(cwd string) (*Record, error) {
	now := s.nowFn().UTC()
	ts := now.Format("20060102T150405")
	nowStr := now.Format("2006-01-02T15:04:05Z")

	rec := &Record{
		ID:               "hh_" + ts + "_" + sha1hex(cwd + strconv.Itoa(s.rndFn()))[:6],
		HermesSessionID:  "hh-" + sha1hex(cwd)[:8] + "-" + ts,
		HermesSessionKey: "hermes-hands:" + sha1hex(cwd)[:16],
		Cwd:              cwd,
		Created:          nowStr,
		Updated:          nowStr,
	}
	rec.path = s.path(rec.ID)
	if err := s.write(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// Resolve picks the session a run works on. mode is "new" | "" | <id>: either
// a fresh session or the EXACTLY requested one. There is no implicit "latest
// in this directory" any more — resume is always explicit by id (CLI
// `--session <id>`, REPL `/session <id>`, rpc {"type":"use"}), which is also
// how editor plugins work: they keep the id from the ready event.
func (s *Store) Resolve(mode, cwd string) (*Record, error) {
	if mode == "new" || mode == "" {
		return s.New(cwd)
	}
	rec, err := s.load(mode)
	if err != nil {
		return nil, fmt.Errorf("no such session: %s", mode)
	}
	return rec, nil
}

// Load reads one session record (reading a session's history must not switch
// the REPL to it).
func (s *Store) Load(id string) (*Record, error) { return s.load(id) }

// BumpTurn ports _hh_session_write: after a completed run, bump turns, record
// the run id / time / token count, and adopt the server's session_id if it
// handed back a different one (counting that as a split ~ compaction). Mutates
// rec in place so the loop's next round uses the adopted id.
func (s *Store) BumpTurn(rec *Record, runID, serverSID string, tokens int) error {
	rec.LastRunID = runID
	rec.Updated = s.nowFn().UTC().Format("2006-01-02T15:04:05Z")
	rec.Turns++
	if tokens > 0 {
		rec.LastTokens = tokens
	}
	if serverSID != "" && serverSID != rec.HermesSessionID {
		rec.Splits++
		rec.HermesSessionID = serverSID
	}
	return s.write(rec)
}

// SetTitleLocal ports the local half of hh_session_set_title: set .title once
// (only while empty) to the message with newlines flattened to spaces and
// truncated to 72 bytes. Reports whether it changed (so the caller can mirror
// the title into Hermes).
func (s *Store) SetTitleLocal(rec *Record, msg string) (bool, error) {
	if rec.Title != "" {
		return false, nil
	}
	rec.Title = trunc(strings.ReplaceAll(msg, "\n", " "), 72)
	if err := s.write(rec); err != nil {
		return false, err
	}
	return true, nil
}

// List ports hh_session_list's data half: every valid record, newest first.
func (s *Store) List() ([]Record, error) {
	dirs, err := filepath.Glob(s.dir + "/*/session.json")
	if err != nil {
		return nil, err
	}
	flat, _ := filepath.Glob(s.dir + "/*.json") // legacy files, if migration skipped them
	var recs []Record
	for _, p := range append(dirs, flat...) {
		b, err := os.ReadFile(p)
		if err != nil || !json.Valid(b) {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		if id == "session" {
			id = filepath.Base(filepath.Dir(p))
		}
		r.ID = id
		r.path = p
		recs = append(recs, r)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Updated > recs[j].Updated })
	return recs, nil
}

// FormatList renders the session index newest-first: one row per record with
// the local id, turn count, last-updated (clipped to the date+time), idle time
// (informational cache hint, * past StaleAfter), the repo directory name, and
// the title. An untitled session shows a blank title (not its cwd) — the DIR
// column carries the location instead.
func FormatList(recs []Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s  %5s  %8s  %-19s  %-7s  %-14s  %s\n", "ID", "TURNS", "TOKENS", "UPDATED", "IDLE", "DIR", "TITLE")
	anyStale := false
	for _, r := range recs {
		upd := r.Updated
		if len(upd) > 19 {
			upd = upd[:19]
		}
		dir := filepath.Base(r.Cwd)
		if dir == "." || dir == "/" || dir == "" {
			dir = r.Cwd
		}
		if len(dir) > 14 {
			dir = dir[:13] + "…"
		}
		tok := "-"
		if r.LastTokens > 0 {
			tok = "~" + strconv.Itoa(r.LastTokens)
		}
		idle := IdleFor(&r, time.Now())
		idleCol := idle.Round(time.Minute).String()
		if idle >= 24*time.Hour {
			idleCol = strconv.Itoa(int(idle.Hours()/24)) + "d"
		}
		if idle > StaleAfter {
			idleCol += "*"
			anyStale = true
		}
		fmt.Fprintf(&b, "%-24s  %5d  %8s  %-19s  %-7s  %-14s  %s\n", r.ID, r.Turns, tok, upd, idleCol, dir, r.Title)
	}
	if anyStale {
		fmt.Fprintf(&b, "\n* idle >1h: the gateway/model prompt cache has likely expired — the first resumed turn re-reads the full transcript and can cost full-price tokens. Informational only.\n")
	}
	return b.String()
}

// StaleAfter is an informational idle threshold, not a hard expiry: gateway
// and model layers (a model gateway, hermes-agent, Claude-style provider prompt caches
// with a ~1h TTL) cache the running conversation. After 1h idle that cache has
// very likely expired, so resuming re-reads the whole transcript — the first
// turn then pays full input tokens instead of cached ones. The flag reminds
// the operator of that cost (matters when Hermes runs on a billed model like
// Claude); the history re-sync itself always happens regardless.
//
// The 1h is a plain informational threshold shown in `sessions` (IDLE column)
// and `/session`. Future: ask hermes-agent which model + provider a session
// actually runs on and use that provider's official prompt-cache TTL instead
// of this fixed hour.
const StaleAfter = time.Hour

// IdleFor reports how long the session has been idle, measured from its last
// update. An unparseable timestamp reports zero idle time.
func IdleFor(rec *Record, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, rec.Updated)
	if err != nil {
		return 0
	}
	if d := now.UTC().Sub(t); d > 0 {
		return d
	}
	return 0
}

// FormatDetail renders one session for the in-REPL `/session` view: local id,
// the hermes-agent session id, turn count, split count (~ compactions), the
// last run's token count, and timestamps.
//
// Hermes does not yet expose real context-window usage or a compaction count
// via the API (NousResearch/hermes-agent#15618); "tokens" is the last run's
// cumulative billing usage and "splits" counts how many times Hermes handed
// back a new session id (which a compaction/session-split causes).
func FormatDetail(r *Record) string {
	tok := "unknown"
	if r.LastTokens > 0 {
		tok = "~" + strconv.Itoa(r.LastTokens) + " (last run, billing usage)"
	}
	title := r.Title
	if title == "" {
		title = "(none yet)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  hermes-hands session : %s\n", r.ID)
	fmt.Fprintf(&b, "  hermes-agent session : %s\n", orDash(r.HermesSessionID))
	fmt.Fprintf(&b, "  title                : %s\n", title)
	fmt.Fprintf(&b, "  turns                : %d\n", r.Turns)
	fmt.Fprintf(&b, "  splits (~compactions): %d\n", r.Splits)
	fmt.Fprintf(&b, "  tokens               : %s\n", tok)
	fmt.Fprintf(&b, "  created / updated    : %s  /  %s\n", r.Created, r.Updated)
	idle := IdleFor(r, time.Now())
	note := ""
	if idle > StaleAfter {
		note = "  (>1h idle: model prompt-cache likely expired — resume re-reads the full transcript, may cost full-price tokens)"
	}
	fmt.Fprintf(&b, "  idle                 : %s%s\n", idle.Round(time.Minute), note)
	fmt.Fprintf(&b, "  dir                  : %s\n", r.Cwd)
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// load reads the record for id. The id is taken from the filename (bash uses
// `basename`), authoritative over any .id inside. A readable-but-corrupt file
// yields a bare record, matching bash's jq-fails-to-empty behaviour.
func (s *Store) load(id string) (*Record, error) {
	p := s.path(id)
	b, err := os.ReadFile(p)
	if err != nil {
		// legacy flat record, if Open's migration never saw this file
		if b2, e2 := os.ReadFile(s.dir + "/" + id + ".json"); e2 == nil {
			b = b2
		} else {
			return nil, err
		}
	}
	rec := &Record{ID: id, path: p}
	if json.Valid(b) {
		_ = json.Unmarshal(b, rec)
		rec.ID = id
		rec.path = p
	}
	return rec, nil
}

func (s *Store) write(rec *Record) error {
	b, err := marshalRecord(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rec.path), 0o755); err != nil {
		return err
	}
	tmp := rec.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, rec.path)
}

func (s *Store) warnf(f string, a ...any) {
	if s.Warnf != nil {
		s.Warnf(f, a...)
		return
	}
	fmt.Fprintf(os.Stderr, "hermes-hands: WARNING: "+f+"\n", a...)
}

func (s *Store) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Store) rndFn() int {
	if s.rnd != nil {
		return s.rnd()
	}
	return rand.Intn(32768)
}

// marshalRecord writes the record like jq: 2-space indent, trailing newline,
// no HTML escaping (so `<`, `>`, `&` in a title survive).
func marshalRecord(r *Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sha1hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --- local transcript -------------------------------------------------------
//
// The live conversation lives on the Hermes server. The transcript is hermes-
// hands' own backup copy of it (JSONL, one object per completed exchange), so
// resuming a session can still load its history when the server-side record
// was lost (retention, migration, session purge).

// Turn is one completed exchange in the local transcript.
type Turn struct {
	At     string `json:"at"`
	User   string `json:"user"`
	Answer string `json:"answer"`
}

// AppendTurn adds one completed exchange to the session's transcript. Call
// sites treat it as best-effort: a failure warns and never breaks the turn.
func (s *Store) AppendTurn(rec *Record, userMsg, answer string) error {
	return s.SaveTranscript(rec, []Turn{{
		At:     s.nowFn().UTC().Format("2006-01-02T15:04:05Z"),
		User:   userMsg,
		Answer: answer,
	}})
}

// SaveTranscript appends turns to the session's transcript file (used for
// per-turn appends and for seeding the local copy from the server).
func (s *Store) SaveTranscript(rec *Record, turns []Turn) error {
	if rec.ID == "" || len(turns) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.sessionDir(rec.ID), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.transcriptPath(rec.ID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, t := range turns {
		line, err := json.Marshal(t)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return s.UpdateHistory(rec)
}

// LoadTranscript reads a session's transcript turns (oldest first). A missing
// transcript is not an error — it yields an empty slice.
func (s *Store) LoadTranscript(rec *Record) ([]Turn, error) {
	b, err := os.ReadFile(s.transcriptPath(rec.ID))
	if err != nil {
		if os.IsNotExist(err) {
			// legacy location, if migration never saw this session
			if b2, e2 := os.ReadFile(s.dir + "/transcripts/" + rec.ID + ".jsonl"); e2 == nil {
				b = b2
			} else {
				return nil, nil
			}
		} else {
			return nil, err
		}
	}
	var turns []Turn
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !json.Valid([]byte(line)) {
			continue
		}
		var t Turn
		if json.Unmarshal([]byte(line), &t) != nil {
			continue
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// HasTranscript reports whether a non-empty transcript file exists.
func (s *Store) HasTranscript(rec *Record) bool {
	st, err := os.Stat(s.transcriptPath(rec.ID))
	return err == nil && st.Size() > 0
}

// ReplaceTranscript overwrites the transcript with turns — used by the resume
// sync when the server-side copy is more complete than the local mirror (the
// server is the source of truth; the local transcript is its mirror).
func (s *Store) ReplaceTranscript(rec *Record, turns []Turn) error {
	if rec.ID == "" {
		return nil
	}
	if err := os.MkdirAll(s.sessionDir(rec.ID), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, t := range turns {
		line, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(s.transcriptPath(rec.ID), []byte(b.String()), 0o600); err != nil {
		return err
	}
	return s.UpdateHistory(rec)
}

// UpdateHistory regenerates <state>/sessions/<id>/session-history.md from the
// transcript: the whole conversation, oldest first, unclipped. The transcript
// is canonical; the .md is a pure rendering, so it is rebuilt on every append
// and can be recreated from scratch at any time.
func (s *Store) UpdateHistory(rec *Record) error {
	if rec.ID == "" {
		return nil
	}
	turns, err := s.LoadTranscript(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.sessionDir(rec.ID), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.HistoryPath(rec.ID), []byte(RenderHistory(rec, turns)), 0o644)
}

// RenderHistory renders the full chat history of a session as markdown — for
// the operator to read and for a future hermes-remote to fetch.
func RenderHistory(rec *Record, turns []Turn) string {
	title := rec.Title
	if title == "" {
		title = "(untitled)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- hermes-hands session: %s\n", rec.ID)
	fmt.Fprintf(&b, "- hermes-agent session: %s\n", orDash(rec.HermesSessionID))
	fmt.Fprintf(&b, "- dir: %s\n", rec.Cwd)
	fmt.Fprintf(&b, "- created / updated: %s / %s\n", rec.Created, rec.Updated)
	fmt.Fprintf(&b, "- exchanges: %d\n\n", len(turns))
	for i, t := range turns {
		at := t.At
		if at == "" {
			at = "(undated)"
		}
		fmt.Fprintf(&b, "## %d. %s — operator\n\n", i+1, at)
		b.WriteString(strings.TrimSpace(t.User) + "\n\n")
		b.WriteString("**hermes-hands answer**\n\n")
		b.WriteString(strings.TrimSpace(t.Answer) + "\n\n")
	}
	return b.String()
}

// ResumeRecap renders the local transcript as the block Hermes is shown when a
// resumed session's server-side history was unavailable: the last exchanges,
// oldest first, each clipped so the replay stays token-friendly.
func ResumeRecap(turns []Turn) string {
	const maxTurns = 20
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	var b strings.Builder
	b.WriteString("[resumed session — the Hermes-side transcript of this session was not available, so hermes-hands replays its local copy of the conversation so far. Treat it as your own history: continue where it left off instead of re-doing finished steps.]\n")
	for _, t := range turns {
		b.WriteString("[operator] " + oneLine(t.User, 300) + "\n")
		b.WriteString("[hermes-hands answer] " + oneLine(t.Answer, 800) + "\n")
	}
	return b.String()
}

func oneLine(s string, n int) string {
	return trunc(strings.Join(strings.Fields(s), " "), n)
}
