package session

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.now = func() time.Time { return time.Date(2026, 9, 2, 10, 11, 12, 0, time.UTC) }
	st.rnd = func() int { return 4242 }
	st.Warnf = func(string, ...any) {}
	return st
}

func TestNewMintsIDs(t *testing.T) {
	st := newStore(t)
	cwd := "/home/x/repo"
	rec, err := st.New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sh := func(s string) string { h := sha1.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

	wantID := "hh_20260902T101112_" + sh(cwd + "4242")[:6]
	if rec.ID != wantID {
		t.Errorf("ID = %q, want %q", rec.ID, wantID)
	}
	wantHSID := "hh-" + sh(cwd)[:8] + "-20260902T101112"
	if rec.HermesSessionID != wantHSID {
		t.Errorf("HermesSessionID = %q, want %q", rec.HermesSessionID, wantHSID)
	}
	if rec.HermesSessionKey != "hermes-hands:"+sh(cwd)[:16] {
		t.Errorf("HermesSessionKey = %q", rec.HermesSessionKey)
	}
	// shared ts between local id and hermes id
	if !strings.Contains(rec.ID, "20260902T101112") || !strings.HasSuffix(rec.HermesSessionID, "20260902T101112") {
		t.Errorf("ts not shared: %q / %q", rec.ID, rec.HermesSessionID)
	}
	if rec.Created != "2026-09-02T10:11:12Z" || rec.Updated != "2026-09-02T10:11:12Z" {
		t.Errorf("timestamps = %q / %q, want RFC3339 with literal Z", rec.Created, rec.Updated)
	}
}

func TestSha1HexNoTrailingNewline(t *testing.T) {
	// bash: printf '%s' "$1" | sha1sum  -- no trailing newline
	want := "0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33" // sha1("foo")
	if got := sha1hex("foo"); got != want {
		t.Errorf("sha1hex(foo) = %q, want %q", got, want)
	}
}

func TestNewWritesSessionDir(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/some/dir")

	if _, err := os.Stat(rec.path); err != nil {
		t.Errorf("session file missing: %v", err)
	}
	if _, err := os.Stat(st.sessionDir(rec.ID)); err != nil {
		t.Errorf("session dir missing: %v", err)
	}
	// sessions are addressed by id only — no per-directory pointer exists
	if _, err := os.Stat(st.dir + "/by-cwd"); !os.IsNotExist(err) {
		t.Errorf("by-cwd pointer dir must not exist")
	}
}

func TestOpenDropsLegacyByCwd(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(state+"/sessions/by-cwd", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state+"/sessions/by-cwd/deadbeef.id", []byte("hh_x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state + "/sessions/by-cwd"); !os.IsNotExist(err) {
		t.Errorf("legacy by-cwd dir should be dropped on Open")
	}
}

func TestOpenTightensPermissions(t *testing.T) {
	// what an older version left world-readable is tightened on Open
	state := t.TempDir()
	dir := state + "/sessions/hh_old"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state+"/sessions", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/session.json", []byte(`{"id":"hh_old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(state); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{state + "/sessions": 0o700, dir: 0o700, dir + "/session.json": 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s perms = %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
}

func TestResolveIsExplicit(t *testing.T) {
	st := newStore(t)
	cwd := "/resolve/dir"

	r1, err := st.Resolve("new", cwd)
	if err != nil || r1 == nil {
		t.Fatalf("Resolve(new): %v", err)
	}
	// the exact id resolves to itself (the plugin pattern)
	got, err := st.Resolve(r1.ID, cwd)
	if err != nil || got.ID != r1.ID {
		t.Errorf("Resolve(id) = %v / %v, want %s", got, err, r1.ID)
	}
	// explicit missing id -> error
	if _, err := st.Resolve("hh_does_not_exist", cwd); err == nil ||
		err.Error() != "no such session: hh_does_not_exist" {
		t.Errorf("explicit-missing err = %v", err)
	}
	// "continue" is gone — it is not an id, so it errors like one
	if _, err := st.Resolve("continue", cwd); err == nil {
		t.Errorf("Resolve(continue) must error now")
	}
	// no id in any directory: just a fresh session
	r2, err := st.Resolve("", "/resolve/other")
	if err != nil || r2 == nil || r2.ID == r1.ID {
		t.Errorf("Resolve('', other dir): %v %+v", err, r2)
	}
}

func TestIdleForAndStale(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fresh := &Record{Updated: "2026-09-02T11:30:00Z"}
	if d := IdleFor(fresh, now); d != 30*time.Minute {
		t.Errorf("IdleFor fresh = %v, want 30m", d)
	}
	if IdleFor(fresh, now) > StaleAfter {
		t.Error("30m idle must not count as stale")
	}
	old := &Record{Updated: "2026-09-02T10:00:00Z"}
	if IdleFor(old, now) <= StaleAfter {
		t.Error("2h idle must count as stale")
	}
	if d := IdleFor(&Record{Updated: "garbage"}, now); d != 0 {
		t.Errorf("unparseable timestamp should idle 0, got %v", d)
	}
}

func TestBumpTurnAdoptsAndCounts(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/bump/dir")
	orig := rec.HermesSessionID

	if err := st.BumpTurn(rec, "run_1", "", 1200); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 1 || rec.LastRunID != "run_1" || rec.LastTokens != 1200 || rec.Splits != 0 {
		t.Errorf("after bump#1: turns=%d last=%q tok=%d splits=%d", rec.Turns, rec.LastRunID, rec.LastTokens, rec.Splits)
	}
	if rec.HermesSessionID != orig {
		t.Errorf("empty server sid must not change hermes id")
	}

	if err := st.BumpTurn(rec, "run_2", "srv-sid-999", 0); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 2 || rec.LastTokens != 1200 { // 0 tokens must not clobber
		t.Errorf("after bump#2: turns=%d tok=%d", rec.Turns, rec.LastTokens)
	}
	if rec.HermesSessionID != "srv-sid-999" || rec.Splits != 1 {
		t.Errorf("differing server sid should be adopted + counted, got %q splits=%d", rec.HermesSessionID, rec.Splits)
	}

	// same sid again -> no-op adopt, no new split, still counts the turn
	if err := st.BumpTurn(rec, "run_3", "srv-sid-999", 2048); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 3 || rec.Splits != 1 || rec.LastTokens != 2048 {
		t.Errorf("after bump#3: turns=%d splits=%d tok=%d", rec.Turns, rec.Splits, rec.LastTokens)
	}
}

func TestSetTitleLocal(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/title/dir")

	changed, err := st.SetTitleLocal(rec, "first line\nsecond line")
	if err != nil || !changed {
		t.Fatalf("SetTitleLocal: changed=%v err=%v", changed, err)
	}
	if rec.Title != "first line second line" {
		t.Errorf("Title = %q, want newlines flattened", rec.Title)
	}

	// only-if-empty: a second call must not overwrite
	changed, _ = st.SetTitleLocal(rec, "totally different")
	if changed || rec.Title != "first line second line" {
		t.Errorf("second SetTitleLocal changed the title: %q", rec.Title)
	}

	// 72-byte truncation
	rec2, _ := st.New("/title/dir2")
	long := strings.Repeat("x", 100)
	st.SetTitleLocal(rec2, long)
	if len(rec2.Title) != 72 {
		t.Errorf("truncated title len = %d, want 72", len(rec2.Title))
	}
}

func TestListOrderAndFallback(t *testing.T) {
	st := newStore(t)

	rA, _ := st.New("/list/a")
	rA.Updated = "2026-09-01T00:00:00Z"
	rA.Title = "older with title"
	st.write(rA)

	rB, _ := st.New("/list/b")
	rB.Updated = "2026-09-03T00:00:00Z"
	rB.Title = "" // fallback to cwd
	st.write(rB)

	recs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("List returned %d records", len(recs))
	}
	if recs[0].ID != rB.ID {
		t.Errorf("newest first: got %q, want %q", recs[0].ID, rB.ID)
	}

	out := FormatList(recs)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "IDLE") || !strings.Contains(lines[0], "DIR") || !strings.HasSuffix(lines[0], "TITLE") {
		t.Errorf("header = %q", lines[0])
	}
	// newest row: no title -> the TITLE column is blank, and the DIR column
	// carries the location basename ("b"), not the full cwd.
	if !strings.Contains(lines[1], "2026-09-03T00:00:00") || !strings.HasSuffix(strings.TrimRight(lines[1], " "), "  b") {
		t.Errorf("row 1 (newest, blank title, dir=b) = %q", lines[1])
	}
	if strings.Contains(lines[1], "/list/b") {
		t.Errorf("row 1 must not show the full cwd as a title: %q", lines[1])
	}
	if !strings.Contains(lines[2], "older with title") {
		t.Errorf("row 2 (title) = %q", lines[2])
	}
	if strings.Contains(lines[1], "Z ") {
		t.Errorf("updated should be clipped to 19 chars (no trailing Z): %q", lines[1])
	}
	// both sessions are long idle vs. real wall-clock now -> idle marker * +
	// the informational legend must be present
	if !strings.Contains(lines[1], "*") || !strings.Contains(out, "idle >1h") {
		t.Errorf("stale rows must carry the idle * marker and the legend: %q", out)
	}
}
