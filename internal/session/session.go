// Package session ports lib/session.sh (plus _hh_session_write from
// lib/api.sh): the local session INDEX. The conversation itself is server-side;
// this is a thin JSON record per session so `-c`, `--session <id>` and
// `sessions` work offline. State lives under <state>/sessions/.
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

	path string // on-disk path; not serialised
}

// Store is the <state>/sessions directory.
type Store struct {
	dir   string
	Warnf func(string, ...any)
	now   func() time.Time
	rnd   func() int
}

// Open ensures <stateDir>/sessions/by-cwd exists and returns the store.
// stateDir is config.Config.StateDir (already resolved from the process env).
func Open(stateDir string) (*Store, error) {
	dir := stateDir + "/sessions"
	if err := os.MkdirAll(dir+"/by-cwd", 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(id string) string    { return s.dir + "/" + id + ".json" }
func (s *Store) cwdPtr(cwd string) string { return s.dir + "/by-cwd/" + sha1hex(cwd) + ".id" }

// New mints a session rooted at cwd (hh_session_new): a local index id, the
// stable per-repo hermes_session_id / hermes_session_key, and the by-cwd
// pointer. ts is shared between the local id and the hermes id.
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
	if err := os.WriteFile(s.cwdPtr(cwd), []byte(rec.ID), 0o644); err != nil {
		return nil, err
	}
	return rec, nil
}

// LatestForCwd ports hh_session_latest_for: the id in the by-cwd pointer, but
// only if its session file still exists.
func (s *Store) LatestForCwd(cwd string) (string, bool) {
	b, err := os.ReadFile(s.cwdPtr(cwd))
	if err != nil {
		return "", false
	}
	id := strings.TrimRight(string(b), "\n")
	if _, err := os.Stat(s.path(id)); err != nil {
		return "", false
	}
	return id, true
}

// Resolve ports hh_session_resolve. mode is "new" | "" | "continue" | <id>.
// It always refreshes the by-cwd pointer to the resolved session.
func (s *Store) Resolve(mode, cwd string) (*Record, error) {
	var rec *Record
	var err error
	switch mode {
	case "continue":
		if id, ok := s.LatestForCwd(cwd); ok {
			rec, err = s.load(id)
		} else {
			// first time in this directory: just start one, quietly - the
			// REPL banner already shows the new session id.
			rec, err = s.New(cwd)
		}
	case "new", "":
		rec, err = s.New(cwd)
	default:
		rec, err = s.load(mode)
		if err != nil {
			return nil, fmt.Errorf("no such session: %s", mode)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.cwdPtr(cwd), []byte(rec.ID), 0o644); err != nil {
		return nil, err
	}
	return rec, nil
}

// BumpTurn ports _hh_session_write: after a completed run, bump turns, record
// the run id and time, and adopt the server's session_id if it handed back a
// different one. Mutates rec in place so the loop's next round uses the
// adopted id.
func (s *Store) BumpTurn(rec *Record, runID, serverSID string) error {
	rec.LastRunID = runID
	rec.Updated = s.nowFn().UTC().Format("2006-01-02T15:04:05Z")
	rec.Turns++
	if serverSID != "" && serverSID != rec.HermesSessionID {
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
	matches, err := filepath.Glob(s.dir + "/*.json")
	if err != nil {
		return nil, err
	}
	var recs []Record
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil || !json.Valid(b) {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		r.path = p
		recs = append(recs, r)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Updated > recs[j].Updated })
	return recs, nil
}

// FormatList renders the session index newest-first: one row per record with
// the local id, turn count, last-updated (clipped to the date+time), the repo
// directory name, and the title. An untitled session shows a blank title (not
// its cwd) — the DIR column carries the location instead.
func FormatList(recs []Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s  %5s  %-19s  %-16s  %s\n", "ID", "TURNS", "UPDATED", "DIR", "TITLE")
	for _, r := range recs {
		upd := r.Updated
		if len(upd) > 19 {
			upd = upd[:19]
		}
		dir := filepath.Base(r.Cwd)
		if dir == "." || dir == "/" || dir == "" {
			dir = r.Cwd
		}
		if len(dir) > 16 {
			dir = dir[:15] + "…"
		}
		fmt.Fprintf(&b, "%-24s  %5d  %-19s  %-16s  %s\n", r.ID, r.Turns, upd, dir, r.Title)
	}
	return b.String()
}

// load reads the record for id. The id is taken from the filename (bash uses
// `basename`), authoritative over any .id inside. A readable-but-corrupt file
// yields a bare record, matching bash's jq-fails-to-empty behaviour.
func (s *Store) load(id string) (*Record, error) {
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
