package compact

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermes-hands/internal/api"
	"hermes-hands/internal/session"
)

// newKeeper is a keeper over a fresh session with turns big rounds in its
// log: each turn one call with a 30 KB output, then the final.
func newKeeper(t *testing.T, turns int) (*Keeper, *session.Log) {
	t.Helper()
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Warnf = func(string, ...any) {}
	rec, err := st.Resolve("new", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log, _ := st.OpenLog(rec)
	big := strings.Repeat("output line\n", 2500)
	ref := 0
	for i := 0; i < turns; i++ {
		ref++
		log.BeginTurn()
		log.Add(session.Event{Kind: session.KindUser, Text: "step"})
		log.Add(session.Event{Kind: session.KindReply, Round: 1, Text: `{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"make"}}],"final":null}`})
		log.Add(session.Event{Kind: session.KindCall, Round: 1, Ref: ref, ID: "c1", Tool: "local_shell", Args: json.RawMessage(`{"cmd":"make"}`)})
		log.Add(session.Event{Kind: session.KindResult, Round: 1, Ref: ref, Tool: "local_shell", Exit: session.Int(0), Out: big})
		log.Add(session.Event{Kind: session.KindReply, Round: 2, Text: `{"calls":[],"final":"done"}`})
		log.Add(session.Event{Kind: session.KindAnswer, Text: "done", OK: session.Bool(true)})
	}
	return &Keeper{Store: st, Rec: rec, Window: 131072, At: 45}, log
}

// the trigger is At percent of the window, but leaves room above the request
// for the reply (20K) and a buffer (4K) — unless the window is too small
func TestTriggerLeavesRoomForTheReply(t *testing.T) {
	k := &Keeper{Window: 131072, At: 69, RoundOutput: 60000}
	if got := k.trigger(); got != 131072*69/100 { // 90439 — room 106496 is plenty
		t.Errorf("trigger = %d", got)
	}
	k.At = 95
	if got, want := k.trigger(), 131072-24576; got != want {
		t.Errorf("capped trigger = %d, want %d", got, want)
	}
	k = &Keeper{Window: 32768, At: 90, RoundOutput: 60000} // room < half the window: the share stays
	if got := k.trigger(); got != 32768*90/100 {
		t.Errorf("small window trigger = %d", got)
	}
	if (&Keeper{Window: 131072}).trigger() != 0 || (*Keeper)(nil).trigger() != 0 {
		t.Error("no share or no keeper: no trigger")
	}
}

func TestMeterMeasuresASingleCallRun(t *testing.T) {
	k, _ := newKeeper(t, 0)
	// the session's first run: the prompt is the fixed part plus msg
	k.Observe(api.AskResult{Prompt: 20000, Text: strings.Repeat("r", 300)}, strings.Repeat("m", 3000), nil, 0)
	if !k.Rec.Measured || k.Rec.Context != 20000+100 || k.Rec.Overhead != 20000-1000 {
		t.Errorf("rec = context %d measured %v overhead %d", k.Rec.Context, k.Rec.Measured, k.Rec.Overhead)
	}
	// Hermes ran a server tool: two model calls, the sum says nothing exact
	k.Rec.Turns = 1
	k.Observe(api.AskResult{Prompt: 90000, HermesTools: 1, Text: "rrr"}, "m", nil, 25000)
	if k.Rec.Measured || k.Rec.Context != 25001 || k.Rec.Overhead != 19000 {
		t.Errorf("multi-call run = context %d measured %v overhead %d", k.Rec.Context, k.Rec.Measured, k.Rec.Overhead)
	}
}

func TestPredictAndDue(t *testing.T) {
	k, _ := newKeeper(t, 0)
	if k.Predict("msg", nil) != 0 || k.Due(0) {
		t.Error("nothing measured yet: no prediction, nothing due")
	}
	k.Rec.Context = 58000
	p := k.Predict(strings.Repeat("x", 3000), nil)
	if p != 59000 || !k.Due(p) { // 45% of 131072 = 58982
		t.Errorf("predict = %d, due = %v", p, k.Due(p))
	}
	k.At = 0
	if k.Due(p) {
		t.Error("At 0: only /compact")
	}
	var nilK *Keeper
	if nilK.Due(1e9) || nilK.History() != nil || nilK.Predict("x", nil) != 0 {
		t.Error("a nil keeper is off")
	}
}

// The error of a run refused for its size moves the meter to the size it
// names, so the next request is compacted first.
func TestOverflowSetsTheMeter(t *testing.T) {
	const reason = "run run_x failed: HTTP 400: prompt (150000 tokens) leaves no room to answer in the context (131072)"
	if !IsOverflow(reason) {
		t.Fatal("overflow not recognised")
	}
	for _, other := range []string{"HTTP 401 at /v1/runs - HERMES_API_KEY rejected.", "run run_x failed: tool loop guardrail"} {
		if IsOverflow(other) {
			t.Errorf("%q taken for an overflow", other)
		}
	}
	for _, s := range []string{"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens", "context_length_exceeded", "the request exceeds the available context size"} {
		if !IsOverflow(s) {
			t.Errorf("%q not recognised", s)
		}
	}
	k, log := newKeeper(t, 1)
	k.Overflow(reason)
	if k.Rec.Context != 150000 || !k.Due(k.Predict("continue", nil)) {
		t.Errorf("context = %d", k.Rec.Context)
	}
	// also once hermes-hands holds the transcript: the refused size wins
	// over the estimate, the next request is compacted again
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(ev)
	if !strings.HasPrefix(ev.Text, "compacted #1 · ~150k → ") { // measured: the refused size
		t.Errorf("note = %q", ev.Text)
	}
	k.Overflow(reason)
	if !k.Due(k.Predict("continue", k.History())) {
		t.Error("owned: an overflow must make the next request compact first")
	}
}

func TestHistoryOnlyOnceCompacted(t *testing.T) {
	k, log := newKeeper(t, 3)
	if k.History() != nil || k.Owned() {
		t.Fatal("hermes-agent holds the transcript until the first compaction")
	}
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(ev)
	if !k.Owned() {
		t.Fatal("compacted: hermes-hands holds it")
	}
	log.BeginTurn()
	log.Add(session.Event{Kind: session.KindUser, Text: "next question"})
	h := k.History()
	if len(h) == 0 || h[len(h)-1].Role != "assistant" {
		t.Fatalf("the operator's pending message must go as input, not history: %+v", h[len(h)-1])
	}
	for _, m := range h {
		if strings.Contains(m.Content, "output line\noutput line") {
			t.Fatalf("an output was sent whole after /compact: %.200s", m.Content)
		}
	}
	if k.Rec.Compactions != 1 || k.Rec.Context >= ev.Before {
		t.Errorf("rec after compaction = %+v", k.Rec)
	}
}

// Automatic compaction shrinks only as far as its target needs: with room
// to spare the latest rounds stay whole.
func TestCompactAutoKeepsTheLatestRounds(t *testing.T) {
	k, log := newKeeper(t, 4)
	k.Window, k.At = 400000, 45 // a target the latest two rounds fit
	ev, err := k.Compact(Auto)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(ev)
	evs, _ := k.Store.LoadEvents(k.Rec)
	last := evs[len(evs)-2] // the last answer
	if ev.Upto >= last.Seq || ev.Collapse != 0 {
		t.Errorf("auto compaction went further than needed: %+v", ev)
	}
	if !strings.Contains(ev.Text, "outputs #1–#3 as lines") {
		t.Errorf("note = %q", ev.Text)
	}
	// a window too tight for the lines: everything elided, the older turns
	// collapsed — never the latest
	k.Window = 40000
	ev, err = k.Compact(Auto)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Collapse != 3 {
		t.Errorf("tight: collapse = %d, want 3 (never the latest turn)", ev.Collapse)
	}
	if k.ElidedNow != "#4" {
		t.Errorf("tight: the latest turn's output elided now = %q, want #4", k.ElidedNow)
	}
}

// an automatic compaction that would save less than minReclaim is skipped:
// it would only cost the server's prompt cache
func TestCompactAutoSkipsWhenItSavesTooLittle(t *testing.T) {
	k, _ := newKeeper(t, 4)
	k.Window, k.At = 400000, 45
	k.Rec.Context = 1 // the prompt is already as small as compaction would make it
	if _, err := k.Compact(Auto); !errors.Is(err, ErrNothing) {
		t.Errorf("err = %v, want ErrNothing", err)
	}
	if _, err := k.Compact(Now); err != nil { // asked for: no gate
		t.Errorf("now: %v", err)
	}
}

// /compact elides every output but collapses no turn while the lines leave
// the prompt clearly below the trigger; /compact-aggressive collapses every
// turn but the latest — and says so.
func TestCompactNowCollapsesOnlyWhenNeeded(t *testing.T) {
	k, log := newKeeper(t, 4)
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	ev = log.Add(ev)
	if ev.Collapse != 0 || !strings.Contains(ev.Text, "outputs #1–#4 as lines") {
		t.Errorf("now = collapse %d, %q", ev.Collapse, ev.Text)
	}
	ev, err = k.Compact(Aggressive)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Collapse != 3 || !strings.HasPrefix(ev.Text, "compacted #2 (aggressive) · ") || !strings.Contains(ev.Text, "turns 1–3 as question + answer") {
		t.Errorf("aggressive = collapse %d, %q", ev.Collapse, ev.Text)
	}
	cp, _ := k.Store.LoadChatCheckpoint(k.Rec, ev.Checkpoint)
	if cp.Depth != "aggressive" {
		t.Errorf("depth = %q", cp.Depth)
	}
	log.Add(ev)
	if _, err := k.Compact(Aggressive); !errors.Is(err, ErrNothing) {
		t.Errorf("nothing left to compact: err = %v", err)
	}
}

// Without a meter reading the "before" is the whole log in full — said so,
// it is more than hermes-agent ever held.
func TestCompactSaysWhereBeforeComesFrom(t *testing.T) {
	k, _ := newKeeper(t, 2)
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev.Text, " · log ~") {
		t.Errorf("no meter: %q", ev.Text)
	}
	k2, _ := newKeeper(t, 2)
	k2.Rec.Context, k2.Rec.Measured = 50000, true
	ev, _ = k2.Compact(Now)
	if !strings.Contains(ev.Text, " · ~50k → ") {
		t.Errorf("measured: %q", ev.Text)
	}
	cp, _ := k2.Store.LoadChatCheckpoint(k2.Rec, ev.Checkpoint)
	if cp.BeforeFrom != "measured" {
		t.Errorf("before_from = %q", cp.BeforeFrom)
	}
}

func TestCompactNothingYet(t *testing.T) {
	k, _ := newKeeper(t, 0)
	if _, err := k.Compact(Now); !errors.Is(err, ErrNothing) {
		t.Errorf("err = %v", err)
	}
}

// A compaction freezes what Hermes reads in a chat checkpoint; the log stays
// the whole chat. Later turns go behind the checkpoint verbatim — its
// messages, the prefix of every later request, never change.
func TestCompactWritesAChatCheckpoint(t *testing.T) {
	k, log := newKeeper(t, 3)
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	ev = log.Add(ev)
	if ev.Checkpoint != "chat-checkpoints/0001.json" || !strings.HasSuffix(ev.Text, ev.Checkpoint) {
		t.Fatalf("event = %+v", ev)
	}
	cp, err := k.Store.LoadChatCheckpoint(k.Rec, ev.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if cp.N != 1 || cp.Depth != "now" || cp.Elided != "#1–#3" || cp.Seq != ev.Seq-1 || len(cp.Messages) == 0 {
		t.Errorf("checkpoint = n %d depth %s elided %s seq %d/%d, %d messages", cp.N, cp.Depth, cp.Elided, cp.Seq, ev.Seq, len(cp.Messages))
	}
	before := k.History()

	// a new turn: behind the checkpoint, verbatim
	log.BeginTurn()
	log.Add(session.Event{Kind: session.KindUser, Text: "next"})
	log.Add(session.Event{Kind: session.KindReply, Round: 1, Text: `{"calls":[],"final":"fine"}`})
	log.Add(session.Event{Kind: session.KindAnswer, Text: "fine", OK: session.Bool(true)})
	after := k.History()
	if len(after) <= len(before) {
		t.Fatalf("history did not grow: %d -> %d", len(before), len(after))
	}
	for i := range before[:len(before)-1] { // the last one may take the next message in at the seam
		if after[i] != before[i] {
			t.Fatalf("message %d of the frozen prefix changed", i)
		}
	}
	if last := after[len(after)-1]; last.Role != "assistant" || !strings.Contains(last.Content, `"final":"fine"`) {
		t.Errorf("tail = %+v", last)
	}

	v := k.View()
	if !v.Owned || v.N != 1 || v.Checkpoint != ev.Checkpoint || len(v.Messages) != len(cp.Messages) || len(v.Since) == 0 {
		t.Errorf("view = owned %v n %d %s, %d + %d messages", v.Owned, v.N, v.Checkpoint, len(v.Messages), len(v.Since))
	}
}

// A checkpoint that cannot be read is rendered from the log again.
func TestHistoryWithoutItsCheckpoint(t *testing.T) {
	k, log := newKeeper(t, 2)
	ev, err := k.Compact(Now)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(ev)
	want := k.History()
	if err := os.Remove(filepath.Join(k.Store.DebugPath(k.Rec), "..", ev.Checkpoint)); err != nil {
		t.Fatal(err)
	}
	if got := k.History(); len(got) != len(want) {
		t.Errorf("fallback history = %d messages, want %d", len(got), len(want))
	}
}

func TestViewBeforeAnyCompaction(t *testing.T) {
	k, _ := newKeeper(t, 1)
	if v := k.View(); v.Owned || v.Checkpoint != "" || v.Messages != nil {
		t.Errorf("view = %+v", v)
	}
}
