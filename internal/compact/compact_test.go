package compact

import (
	"encoding/json"
	"errors"
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
	if !strings.HasPrefix(ev.Text, "compacted: ~150k → ") {
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
	if !strings.Contains(ev.Text, "outputs #1–#3 elided") {
		t.Errorf("note = %q", ev.Text)
	}
	// a tight window: everything elided, the oldest turns collapsed
	k.Window = 60000
	ev, err = k.Compact(Auto)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Collapse == 0 || ev.Collapse >= 4 {
		t.Errorf("tight: collapse = %d (never the latest turn)", ev.Collapse)
	}
}

func TestCompactNothingYet(t *testing.T) {
	k, _ := newKeeper(t, 0)
	if _, err := k.Compact(Now); !errors.Is(err, ErrNothing) {
		t.Errorf("err = %v", err)
	}
}
