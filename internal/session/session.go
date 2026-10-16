// Package session is the local session store. One directory per session,
// under <state>/sessions/<id>/ (0700, every file 0600):
//
//	<id>/session.json  — the thin index record (`--session <id>` and
//	                     `sessions` work offline from these)
//	<id>/events.jsonl  — everything the terminal showed, as an append-only
//	                     event log (see Event): the operator's messages, every
//	                     hands call and result, Hermes' own tool attempts, the
//	                     answers, the turn footers. The REPL renders a live turn
//	                     and a reopened session from the same events, so a
//	                     resume shows the session exactly as it ran.
//	<id>/out/<ref>.txt — the full (scrubbed) output of call #ref; events only
//	                     carry its size, the excerpt is rendered from this
//	<id>/debug.jsonl   — the /debug wire trace, redacted (only while /debug is on)
//
// The conversation the model sees lives on the Hermes server; this store is
// the operator's local record of it and never syncs anywhere. Sessions idle
// longer than the retention window are deleted on start (Prune).
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

	path      string // on-disk path; not serialised
	seq       int    // last event Seq written (see AppendEvent)
	seqLoaded bool
}

// Store is the <state>/sessions directory.
type Store struct {
	dir   string
	Warnf func(string, ...any)
	now   func() time.Time
	rnd   func() int
}

// Open ensures <stateDir>/sessions exists, tightens permissions left by older
// versions, and returns the store. stateDir is config.Config.StateDir (already
// resolved from the process env). Sessions are addressed by id only (an
// editor/plugin keeps the id from the ready event; see Resolve).
func Open(stateDir string) (*Store, error) {
	s := &Store{dir: stateDir + "/sessions"}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	s.tidy()
	return s, nil
}

func (s *Store) sessionDir(id string) string { return s.dir + "/" + id }
func (s *Store) path(id string) string       { return s.dir + "/" + id + "/session.json" }

// tidy runs on every Open: it drops the obsolete by-cwd pointer directory and
// tightens what older versions left world-readable.
func (s *Store) tidy() {
	// the legacy by-cwd "last session" pointer is obsolete — sessions are
	// addressed by id, so the pointer dir is dropped on Open
	_ = os.RemoveAll(s.dir + "/by-cwd")

	// The store holds chat history and command output: 0700 dirs, 0600 files.
	// Older versions wrote 0755 / 0644, and os.WriteFile keeps an existing
	// file's mode — so tighten what is already on disk.
	tighten(s.dir, 0o700)
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		p := s.dir + "/" + e.Name()
		switch {
		case e.IsDir():
			tighten(p, 0o700)
			files, _ := os.ReadDir(p)
			for _, f := range files {
				if f.Type().IsRegular() {
					tighten(p+"/"+f.Name(), 0o600)
				}
			}
		case e.Type().IsRegular():
			tighten(p, 0o600)
		}
	}
}

// tighten chmods p to want when it carries any permission bit beyond want.
func tighten(p string, want os.FileMode) {
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&^want != 0 {
		_ = os.Chmod(p, want)
	}
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
	var recs []Record
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil || !json.Valid(b) {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		r.ID = filepath.Base(filepath.Dir(p))
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
	if err := validID(id); err != nil {
		return nil, err
	}
	p := s.path(id)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
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
	if err := os.MkdirAll(filepath.Dir(rec.path), 0o700); err != nil {
		return err
	}
	tmp := rec.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
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
