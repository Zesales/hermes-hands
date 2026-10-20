package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermes-hands/internal/api"
	"hermes-hands/internal/compact"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/session"
)

// historyAsker is a scriptAsker that also takes the transcript along, like
// api.Runs.AskWith; it records what each request carried.
type historyAsker struct {
	scriptAsker
	histories [][]api.Message
}

func (h *historyAsker) AskWith(ctx context.Context, msg, sess, skey string, history []api.Message) (api.AskResult, error) {
	h.histories = append(h.histories, history)
	return h.scriptAsker.Ask(ctx, msg, sess, skey)
}

func (h *historyAsker) Ask(ctx context.Context, msg, sess, skey string) (api.AskResult, error) {
	h.histories = append(h.histories, nil)
	return h.scriptAsker.Ask(ctx, msg, sess, skey)
}

// keeperLoop wires a loop to a session log and its context keeper the way the
// turn runner does: every event the loop emits lands in the log.
func keeperLoop(t *testing.T, a Asker) (*Loop, *compact.Keeper, *session.Log) {
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
	k := &compact.Keeper{Store: st, Rec: rec, Window: 131072, At: 45}
	d := realDisp(t, prompt.AutoApprover{})
	d.Recall = func(ref int) (string, error) { return st.LoadOutput(rec, ref) }
	l := &Loop{API: a, Dispatch: d, MaxRounds: 8, Ctx: k, Emit: func(ev session.Event) { log.Add(ev) }}
	return l, k, log
}

// A request that would come too close to the window is compacted BEFORE it
// goes out — and from then on the transcript goes with every request.
func TestRunCompactsBeforeARequestThatWouldNotFit(t *testing.T) {
	big := strings.Repeat("x", 20000) // local_read's cap (MaxOutput)
	ha := &historyAsker{scriptAsker: scriptAsker{replies: []api.AskResult{
		{Text: `{"calls":[{"id":"c1","tool":"local_read","args":{"path":"big.txt"}}],"final":null}`, Prompt: 55000, Threaded: true},
		{Text: `{"calls":[],"final":"done"}`, Prompt: 30000, Threaded: true},
	}}}
	l, k, log := keeperLoop(t, ha)
	if err := os.WriteFile(filepath.Join(l.Dispatch.RepoRoot, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	log.BeginTurn()
	log.Add(session.Event{Kind: session.KindUser, Text: "read it"})
	// not the session's first run: the 55k measured are mostly the chat
	// hermes-agent holds, not a fixed part no compaction could shrink (the
	// minReclaim gate would skip that one, rightly)
	k.Rec.Turns = 1
	out := l.Run(context.Background(), "read it", k.Rec, func(string, string, int) {})
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	// round 1: measured 55k; round 2 adds 20 KB of results (~7k) — past 45%
	evs, _ := k.Store.LoadEvents(k.Rec)
	if _, ok := session.LastCompact(evs); !ok {
		t.Fatal("no compaction before round 2")
	}
	if len(ha.histories) != 2 || ha.histories[0] != nil || len(ha.histories[1]) == 0 {
		t.Fatalf("histories = %d / %v", len(ha.histories), ha.histories)
	}
	if !strings.Contains(ha.msgs[1], big) {
		t.Error("the pending results must go out whole as the input")
	}
}

// overflow is a model server refusing a request for its size.
var overflow = errors.New("run run_x failed: HTTP 400: prompt (150000 tokens) leaves no room to answer in the context (131072)")

// readBig is a turn that reads a 20 KB file in round 1 and answers in round
// 2; errs are the errors of its requests, in order.
func readBig(t *testing.T, errs ...error) (*historyAsker, *Loop, *compact.Keeper, Outcome) {
	t.Helper()
	ha := &historyAsker{scriptAsker: scriptAsker{
		replies: []api.AskResult{
			{Text: `{"calls":[{"id":"c1","tool":"local_read","args":{"path":"big.txt"}}],"final":null}`, Prompt: 30000, Threaded: true},
			{Text: `{"calls":[],"final":"done"}`, Threaded: true},
		},
		errs: errs,
	}}
	l, k, log := keeperLoop(t, ha)
	if err := os.WriteFile(filepath.Join(l.Dispatch.RepoRoot, "big.txt"), []byte(strings.Repeat("y", 20000)), 0o644); err != nil {
		t.Fatal(err)
	}
	log.BeginTurn()
	log.Add(session.Event{Kind: session.KindUser, Text: "read it"})
	out := l.Run(context.Background(), "read it", k.Rec, func(string, string, int) {})
	return ha, l, k, out
}

// A request refused for its size is handled locally: compacted as far as it
// goes and sent again — same turn, same round, no BLOCKED.
func TestRunRecoversARefusedRequestLocally(t *testing.T) {
	ha, _, k, out := readBig(t, nil, overflow)
	if !out.OK || out.Answer != "done" || out.Rounds != 2 {
		t.Fatalf("out = %+v", out)
	}
	if len(ha.msgs) != 3 || ha.histories[1] != nil || len(ha.histories[2]) == 0 {
		t.Fatalf("requests = %d, histories = %v", len(ha.msgs), ha.histories)
	}
	if ha.msgs[2] != ha.msgs[1] {
		t.Error("the same request goes again — the transcript is what shrank")
	}
	evs, _ := k.Store.LoadEvents(k.Rec)
	if c, ok := session.LastCompact(evs); !ok || c.Upto == 0 {
		t.Errorf("no compaction: %+v", c)
	}
}

// Still too large after compacting: the results the request carries are
// fitted tighter — their full outputs stay local.
func TestRunRefitsWhenCompactingIsNotEnough(t *testing.T) {
	ha, _, _, out := readBig(t, nil, overflow, overflow)
	if !out.OK || len(ha.msgs) != 4 {
		t.Fatalf("out = %+v after %d requests", out, len(ha.msgs))
	}
	if len(ha.msgs[3]) >= len(ha.msgs[2]) || !strings.Contains(ha.msgs[3], `local_output {\"ref\":1`) {
		t.Errorf("results not fitted tighter: %d -> %d bytes", len(ha.msgs[2]), len(ha.msgs[3]))
	}
}

// Nothing left to make smaller: BLOCKED, saying why, and the meter full.
func TestRunGivesUpAfterTheLocalRetries(t *testing.T) {
	ha, _, k, out := readBig(t, nil, overflow, overflow, overflow)
	if out.OK || !strings.Contains(out.Answer, "even compacted locally") {
		t.Errorf("answer = %q", out.Answer)
	}
	if len(ha.msgs) != 1+1+maxRecover {
		t.Errorf("requests = %d", len(ha.msgs))
	}
	if k.Rec.Context != 150000 {
		t.Errorf("meter = %d", k.Rec.Context)
	}
}

// A round over its output budget: Hermes gets the outputs fitted, the
// operator a note, the session the whole output (local_output reads it).
func TestRunFitsTheRoundOutputBudget(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"id":"c1","tool":"local_read","args":{"path":"a.txt"}},{"id":"c2","tool":"local_read","args":{"path":"b.txt"}}],"final":null}`, true),
		reply(`{"calls":[{"id":"c3","tool":"local_output","args":{"ref":2,"offset":3000}}],"final":null}`, true),
		reply(`{"calls":[],"final":"ok"}`, true),
	}}
	l, k, log := keeperLoop(t, sa)
	l.MaxRoundOutput = 6000
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(l.Dispatch.RepoRoot, f), []byte(strings.Repeat(f[:1], 9000)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log.BeginTurn()
	out := l.Run(context.Background(), "read both", k.Rec, func(string, string, int) {})
	if !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if len(sa.msgs[1]) > 9000 || !strings.Contains(sa.msgs[1], `local_output {\"ref\":2,\"offset\":`) {
		t.Errorf("round 1 results not fitted (%d bytes)", len(sa.msgs[1]))
	}
	if !strings.Contains(sa.msgs[2], "[#2: bytes 3000–") || !strings.Contains(sa.msgs[2], strings.Repeat("b", 2000)) {
		t.Errorf("local_output did not read the stored output back: %.300s", sa.msgs[2])
	}
	evs, _ := k.Store.LoadEvents(k.Rec)
	noted := false
	for _, e := range evs {
		noted = noted || e.Kind == session.KindNote && strings.Contains(e.Text, "#1 #2 cut for Hermes")
	}
	if !noted {
		t.Error("no operator note about the cut")
	}
}
