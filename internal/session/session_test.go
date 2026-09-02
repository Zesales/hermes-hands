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

func TestNewWritesFileAndPointer(t *testing.T) {
	st := newStore(t)
	cwd := "/some/dir"
	rec, _ := st.New(cwd)

	if _, err := os.Stat(rec.path); err != nil {
		t.Errorf("session file missing: %v", err)
	}
	ptr := st.cwdPtr(cwd)
	if !strings.HasSuffix(ptr, "/by-cwd/"+sha1hex(cwd)+".id") {
		t.Errorf("cwdPtr = %q", ptr)
	}
	b, err := os.ReadFile(ptr)
	if err != nil {
		t.Fatalf("pointer missing: %v", err)
	}
	if string(b) != rec.ID {
		t.Errorf("pointer content = %q, want %q (no newline)", string(b), rec.ID)
	}
}

func TestLatestForCwd(t *testing.T) {
	st := newStore(t)
	cwd := "/repo/a"

	if _, ok := st.LatestForCwd(cwd); ok {
		t.Errorf("absent pointer should not resolve")
	}
	rec, _ := st.New(cwd)
	if id, ok := st.LatestForCwd(cwd); !ok || id != rec.ID {
		t.Errorf("LatestForCwd = %q,%v want %q,true", id, ok, rec.ID)
	}
	// dangling: pointer present, session file gone
	os.Remove(rec.path)
	if _, ok := st.LatestForCwd(cwd); ok {
		t.Errorf("dangling pointer should not resolve")
	}
}

func TestResolve(t *testing.T) {
	st := newStore(t)
	cwd := "/resolve/dir"

	r1, err := st.Resolve("new", cwd)
	if err != nil || r1 == nil {
		t.Fatalf("Resolve(new): %v", err)
	}

	var warned int
	st.Warnf = func(string, ...any) { warned++ }
	// continue with a DIFFERENT dir that has no session -> quietly starts one
	r2, err := st.Resolve("continue", "/resolve/other")
	if err != nil || r2 == nil {
		t.Fatalf("Resolve(continue, none): %v", err)
	}
	if warned != 0 {
		t.Errorf("continue-with-none should be silent now, got %d warnings", warned)
	}

	// explicit missing id -> error
	if _, err := st.Resolve("hh_does_not_exist", cwd); err == nil ||
		err.Error() != "no such session: hh_does_not_exist" {
		t.Errorf("explicit-missing err = %v", err)
	}

	// continue now finds r1 for its dir
	got, err := st.Resolve("continue", cwd)
	if err != nil || got.ID != r1.ID {
		t.Errorf("Resolve(continue) = %v / %v, want %s", got, err, r1.ID)
	}
}

func TestBumpTurnAdoptsAndCounts(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/bump/dir")
	orig := rec.HermesSessionID

	if err := st.BumpTurn(rec, "run_1", ""); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 1 || rec.LastRunID != "run_1" {
		t.Errorf("after bump#1: turns=%d last=%q", rec.Turns, rec.LastRunID)
	}
	if rec.HermesSessionID != orig {
		t.Errorf("empty server sid must not change hermes id")
	}

	if err := st.BumpTurn(rec, "run_2", "srv-sid-999"); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 2 {
		t.Errorf("turns = %d, want 2", rec.Turns)
	}
	if rec.HermesSessionID != "srv-sid-999" {
		t.Errorf("differing server sid should be adopted, got %q", rec.HermesSessionID)
	}

	// same sid again -> no-op adopt, still counts
	if err := st.BumpTurn(rec, "run_3", "srv-sid-999"); err != nil {
		t.Fatal(err)
	}
	if rec.Turns != 3 || rec.HermesSessionID != "srv-sid-999" {
		t.Errorf("after bump#3: turns=%d sid=%q", rec.Turns, rec.HermesSessionID)
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
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "DIR") || !strings.HasSuffix(lines[0], "TITLE") {
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
}
