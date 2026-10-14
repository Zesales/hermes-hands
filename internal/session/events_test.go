package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendAndLoadEvents(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/home/x/repo")
	for _, ev := range []*Event{
		{Turn: 1, Kind: KindUser, Text: "why is CI red?"},
		{Turn: 1, Kind: KindCall, Ref: 1, ID: "c1", Tool: "shell", Args: json.RawMessage(`{"cmd":"make test"}`)},
		{Turn: 1, Kind: KindResult, Ref: 1, Tool: "shell", Exit: Int(0), Ms: 1200, Lines: 3, Bytes: 40},
		{Turn: 1, Kind: KindAnswer, Text: "it is green", OK: Bool(true)},
		{Turn: 1, Kind: KindTurnEnd, Ms: 3000},
	} {
		if err := st.AppendEvent(rec, ev); err != nil {
			t.Fatal(err)
		}
	}
	// a reopened record continues the sequence from the file
	again, _ := st.Load(rec.ID)
	if err := st.AppendEvent(again, &Event{Turn: 2, Kind: KindUser, Text: "and now?"}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.LoadEvents(again)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 6 {
		t.Fatalf("got %d events", len(evs))
	}
	for i, e := range evs {
		if e.Seq != i+1 || e.V != EventsVersion || e.At == "" {
			t.Errorf("event %d stamped wrong: %+v", i, e)
		}
	}
	if evs[2].Exit == nil || *evs[2].Exit != 0 || string(evs[1].Args) != `{"cmd":"make test"}` {
		t.Errorf("payload lost: %+v / %+v", evs[1], evs[2])
	}
	if LastRef(evs) != 1 || LastTurn(evs) != 2 || len(Turns(evs)) != 2 {
		t.Errorf("LastRef=%d LastTurn=%d Turns=%d", LastRef(evs), LastTurn(evs), len(Turns(evs)))
	}
	fi, err := os.Stat(st.eventsPath(rec.ID))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("events.jsonl perms: %v %v", fi, err)
	}
}

func TestLoadEventsSkipsTornLines(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/r")
	_ = st.AppendEvent(rec, &Event{Turn: 1, Kind: KindUser, Text: "a"})
	f, _ := os.OpenFile(st.eventsPath(rec.ID), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"v":1,"seq":2,"kind":"res` + "\n")
	f.Close()
	evs, err := st.LoadEvents(rec)
	if err != nil || len(evs) != 1 {
		t.Fatalf("evs=%+v err=%v", evs, err)
	}
}

func TestSaveLoadOutput(t *testing.T) {
	st := newStore(t)
	rec, _ := st.New("/r")
	if err := st.SaveOutput(rec, 7, "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	out, err := st.LoadOutput(rec, 7)
	if err != nil || out != "line1\nline2" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	fi, _ := os.Stat(st.sessionDir(rec.ID) + "/out/7.txt")
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("output perms = %v", fi.Mode().Perm())
	}
	big := strings.Repeat("x", maxOutputFile+10)
	_ = st.SaveOutput(rec, 8, big)
	out, _ = st.LoadOutput(rec, 8)
	if len(out) > maxOutputFile+200 || !strings.Contains(out, "truncated by hermes-hands") {
		t.Errorf("big output not capped: %d bytes", len(out))
	}
}

func TestRecapUsesOperatorAndAnswersOnly(t *testing.T) {
	evs := []Event{
		{Turn: 1, Kind: KindUser, Text: "fix the build"},
		{Turn: 1, Kind: KindCall, Ref: 1, Tool: "shell"},
		{Turn: 1, Kind: KindAnswer, Text: "fixed:\n  Makefile"},
	}
	r := Recap(evs)
	if !strings.Contains(r, "[operator] fix the build") || !strings.Contains(r, "[answer] fixed: Makefile") || strings.Contains(r, "shell") {
		t.Errorf("recap = %q", r)
	}
	if Recap(nil) != "" {
		t.Error("an empty session has no recap")
	}
}

func TestPruneRemovesIdleSessions(t *testing.T) {
	st := newStore(t) // now = 2026-09-02T10:11:12Z
	old, _ := st.New("/old")
	old.Updated = "2026-08-01T00:00:00Z"
	_ = st.write(old)
	_ = st.AppendEvent(old, &Event{Turn: 1, Kind: KindUser, Text: "s3cret typed by mistake"})
	_ = st.SaveOutput(old, 1, "out")
	fresh, _ := st.New("/fresh")
	fresh.Updated = "2026-08-30T00:00:00Z"
	_ = st.write(fresh)

	gone := st.Prune(14 * 24 * time.Hour)
	if len(gone) != 1 || gone[0] != old.ID {
		t.Fatalf("pruned %v, want only %s", gone, old.ID)
	}
	if _, err := os.Stat(st.sessionDir(old.ID)); !os.IsNotExist(err) {
		t.Error("the idle session's whole directory (events, outputs) must be gone")
	}
	if _, err := st.Load(fresh.ID); err != nil {
		t.Errorf("fresh session pruned: %v", err)
	}
	if st.Prune(0) != nil {
		t.Error("maxAge 0 must keep everything")
	}
}

func TestValidIDRejectsTraversal(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"../x", "a/b", "..", ".hidden", ""} {
		if _, err := st.Load(id); err == nil {
			t.Errorf("Load(%q) must fail", id)
		}
	}
	if validID("hh_20261013T215047_6fcc0d") != nil {
		t.Error("a real id must be valid")
	}
}
