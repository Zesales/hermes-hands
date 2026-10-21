package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermes-hands/internal/api"
	"hermes-hands/internal/dispatch"
	"hermes-hands/internal/knowsystem"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/redact"
	"hermes-hands/internal/session"
	"hermes-hands/internal/shell"
)

// --- scripted Asker ---

type scriptAsker struct {
	replies []api.AskResult
	errs    []error
	msgs    []string
	i       int
}

func (s *scriptAsker) Ask(_ context.Context, msg, _, _ string) (api.AskResult, error) {
	s.msgs = append(s.msgs, msg)
	idx := s.i
	s.i++
	if idx < len(s.errs) && s.errs[idx] != nil {
		return api.AskResult{}, s.errs[idx]
	}
	if idx >= len(s.replies) {
		idx = len(s.replies) - 1 // repeat the last reply
	}
	return s.replies[idx], nil
}

func reply(text string, threaded bool) api.AskResult {
	return api.AskResult{State: "completed", Text: text, RunID: "run_x", SessionID: "", Threaded: threaded}
}

func rec() *session.Record {
	return &session.Record{HermesSessionID: "hh-abc-ts", HermesSessionKey: "hermes-hands:key"}
}

func run(t *testing.T, a Asker, d *dispatch.Dispatcher, maxRounds int) (Outcome, [][2]string, *scriptAsker) {
	t.Helper()
	var persisted [][2]string
	sa, _ := a.(*scriptAsker)
	l := &Loop{API: a, Dispatch: d, MaxRounds: maxRounds}
	out := l.Run(context.Background(), "hello", rec(), func(r, s string, _ int) {
		persisted = append(persisted, [2]string{r, s})
	})
	return out, persisted, sa
}

func realDisp(t *testing.T, ap prompt.Approver) *dispatch.Dispatcher {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "foo.txt"), []byte("filecontent"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &dispatch.Dispatcher{RepoRoot: root, Approver: ap, MaxOutput: 20000, RunTimeout: 5 * time.Second}
}

// --- extractObj ---

func TestExtractObj(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"json fence", "```json\n{\"a\":1}\n```", "\n{\"a\":1}\n", true},
		{"bare fence", "```\n{\"a\":1}\n```", "\n{\"a\":1}\n", true},
		{"prose then braces", "sure, here: {\"a\":1} done", "{\"a\":1}", true},
		{"no braces", "just some prose", "", false},
		{"leading newline object", "\n{\"a\":1}", "\n{\"a\":1}", true},
		{"trailing prose after }", "{\"a\":1}\nthanks!", "{\"a\":1}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractObj(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("extractObj(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCallArgs(t *testing.T) {
	cases := []struct {
		spec callSpec
		want string
	}{
		{callSpec{Args: json.RawMessage(`{"a":1}`)}, `{"a":1}`},
		{callSpec{Arguments: json.RawMessage(`{"b": 2}`)}, `{"b":2}`},
		{callSpec{Args: json.RawMessage(`null`), Arguments: json.RawMessage(`{"c":3}`)}, `{"c":3}`},
		{callSpec{}, `{}`},
		{callSpec{Args: json.RawMessage(`"{\"d\":4}"`)}, `{"d":4}`}, // JSON string, unwrapped once
		{callSpec{Args: json.RawMessage(`"not json"`)}, `{}`},
	}
	for _, tc := range cases {
		if got := string(callArgs(tc.spec)); got != tc.want {
			t.Errorf("callArgs(%+v) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}

func TestLtrimFirstLine(t *testing.T) {
	if got := ltrimFirstLine("   \t hi\n  world"); got != "hi\n  world" {
		t.Errorf("ltrimFirstLine = %q", got)
	}
	if got := ltrimFirstLine("   only line"); got != "only line" {
		t.Errorf("ltrimFirstLine no-newline = %q", got)
	}
}

// --- Run: terminal outcomes ---

func TestRunPlainFinal(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"hi there"}`, true)}}
	out, persisted, _ := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK || out.Answer != "hi there" {
		t.Errorf("out = %+v", out)
	}
	if len(persisted) != 1 || persisted[0][0] != "run_x" {
		t.Errorf("persist calls = %v", persisted)
	}
}

func TestRunProse(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{reply("just a prose answer, no envelope", true)}}
	out, _, _ := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK || out.Answer != "just a prose answer, no envelope" {
		t.Errorf("out = %+v", out)
	}
}

func TestRunInputRequiredStripAndLtrim(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{reply("[INPUT_REQUIRED]  \n  keep indent", true)}}
	out, _, _ := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK || out.Answer != "\n  keep indent" {
		t.Errorf("out.Answer = %q", out.Answer)
	}
}

func TestRunFixupThenAccept(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply("```json\n{\"tool\": broken", true),
		reply(`{"calls":[],"final":"ok now"}`, true),
	}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK || out.Answer != "ok now" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.HasPrefix(s.msgs[1], fixupMsg) {
		t.Errorf("round 2 msg = %q, want it to start with the fixup error", s.msgs[1])
	}
	if !strings.Contains(s.msgs[1], `verbatim, was: "hello"`) {
		t.Errorf("round 2 msg should still restate the operator's question, got: %q", s.msgs[1])
	}
}

// TestRunFixupNudgeCarriesOperatorQuestion pins a real production bug seen
// against the live gateway: round 1 produced a botched envelope while trying
// to do actual work (write a file with an explanation around it - exactly the
// kind of reply where a small model tangles prose and JSON), the loop sent
// back the bare fixupMsg with zero task context, and the model - now able to
// format correctly but with no idea what it was asked - answered "what can I
// do for you?" instead of finishing the task. The fixup nudge must restate
// the question so recovering the format doesn't also mean abandoning the job.
func TestRunFixupNudgeCarriesOperatorQuestion(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply("```json\n{\"tool\": broken", true), // round 1: botched envelope
		reply(`{"calls":[],"final":"created test.md"}`, true),
	}}
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 8}
	task := "lege eine test.md mit einer hallo-world bash anleitung an"
	out := l.Run(context.Background(), task, rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "created test.md" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.HasPrefix(sa.msgs[1], fixupMsg) {
		t.Errorf("round 2 msg = %q, want it to start with the fixup error", sa.msgs[1])
	}
	if !strings.Contains(sa.msgs[1], task) {
		t.Errorf("fixup nudge should restate the operator's question, got: %q", sa.msgs[1])
	}
}

func TestRunFixupExhaustedFallsToProse(t *testing.T) {
	botched := reply("```json\n{\"tool\": still broken", true)
	sa := &scriptAsker{replies: []api.AskResult{botched, botched, botched}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK {
		t.Fatalf("expected prose acceptance, got %+v", out)
	}
	if !strings.Contains(out.Answer, "still broken") {
		t.Errorf("answer = %q", out.Answer)
	}
	if len(s.msgs) != maxFixups+1 {
		t.Errorf("want %d rounds (%d fixups then accept), got %d", maxFixups+1, maxFixups, len(s.msgs))
	}
}

// TestRunFixupBudgetCoversThreeAttempts pins another real production
// observation: after the fixup nudge started restating the task, a model
// spent two attempts on genuinely malformed JSON, then landed a third attempt
// that was a real write_file call with real content but truncated - missing
// its closing brackets. Under the old 2-attempt budget that third reply was
// already past the limit, so the loop gave up and showed the raw, unexecuted
// JSON to the operator as if it were the final answer (nothing was ever
// written). maxFixups=3 gives that close-but-truncated attempt one more shot.
func TestRunFixupBudgetCoversThreeAttempts(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply("```json\n{\"tool\": broken1", true),
		reply("```json\n{\"tool\": broken2", true),
		reply(`{"calls":[{"id":"c1","tool":"write_file","args":{"path":"foo.txt","content":"z"}}]`, true), // truncated: missing the final "}"
		reply(`{"calls":[],"final":"created"}`, true),
	}}
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 8}
	out := l.Run(context.Background(), "write foo.txt", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "created" {
		t.Fatalf("out = %+v", out)
	}
	if len(sa.msgs) != maxFixups+1 {
		t.Errorf("want %d rounds (%d fixups then success), got %d", maxFixups+1, maxFixups, len(sa.msgs))
	}
}

func TestRunEmptyEnvelopeNudgeThenBlocked(t *testing.T) {
	empty := reply(`{"calls":[],"final":null}`, true)
	sa := &scriptAsker{replies: []api.AskResult{empty, empty, empty}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if out.OK || out.Answer != "BLOCKED: Hermes returned an empty envelope repeatedly." {
		t.Fatalf("out = %+v", out)
	}
	for i, m := range []string{s.msgs[1], s.msgs[2]} {
		if !strings.HasPrefix(m, emptyMsg) {
			t.Errorf("nudge message %d = %q, want it to start with the empty-envelope error", i+1, m)
		}
		if !strings.Contains(m, `verbatim, was: "hello"`) {
			t.Errorf("nudge message %d should still restate the operator's question, got: %q", i+1, m)
		}
	}
}

func TestRoundBudgetNotes(t *testing.T) {
	callEnv := reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true)
	sa := &scriptAsker{replies: []api.AskResult{callEnv}}
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 7}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if out.OK {
		t.Fatalf("want the capped BLOCKED outcome, got %+v", out)
	}
	// Ask 6 (5 rounds ran): inside the finish window — finish countdown.
	if !strings.Contains(sa.msgs[5], "5/7 rounds used, 2 left") {
		t.Errorf("ask 6 should carry the countdown, got: %q", trunc(sa.msgs[5], 300))
	}
	// Ask 7 (6 rounds ran): its reply is the last round whose calls run.
	if !strings.Contains(sa.msgs[6], "LAST round (7/7)") {
		t.Errorf("ask 7 should demand a final after the last round, got: %q", trunc(sa.msgs[6], 300))
	}
	// Ask 8 (7 rounds ran): nothing left — final only (a final here still
	// lands, it is only a calls envelope that gets capped).
	if !strings.Contains(sa.msgs[7], "no rounds remain") {
		t.Errorf("ask 8 should allow only a final, got: %q", trunc(sa.msgs[7], 300))
	}
	if !strings.Contains(l.PendingNote, "round budget (7 rounds)") {
		t.Errorf("a capped turn should leave a resume note, got: %q", l.PendingNote)
	}
}

func TestBudgetTiers(t *testing.T) {
	l := &Loop{MaxRounds: 25, MaxCalls: 100}
	for _, tc := range []struct {
		next, calls int
		want        string
	}{
		{next: 10, calls: 10, want: ""},                                      // far from both limits
		{next: 16, calls: 10, want: "15/25 rounds used, 10 left"},            // last 40% of the rounds
		{next: 16, calls: 10, want: "the operator's limit is getting close"}, // … a heads-up
		{next: 5, calls: 75, want: "75/100 hands calls used, 25 left"},       // 75% of the calls
		{next: 21, calls: 10, want: "Finish now"},                            // last 20% of the rounds
		{next: 5, calls: 90, want: "Finish now"},                             // 90% of the calls
		{next: 25, calls: 10, want: "LAST round (25/25)"},
		{next: 26, calls: 10, want: "no rounds remain"},
		{next: 5, calls: 100, want: "no hands calls remain"},
	} {
		l.calls = tc.calls
		got := l.roundBudgetNote(tc.next)
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("roundBudgetNote(next=%d, calls=%d) = %q, want it to contain %q", tc.next, tc.calls, got, tc.want)
		}
	}
	if f := l.frameBudget(); !strings.Contains(f, "the operator limited this turn to 25 rounds") || !strings.Contains(f, "100 hands calls") {
		t.Errorf("frameBudget = %q", f)
	}
	if (&Loop{}).frameBudget() != "" || (&Loop{}).roundBudgetNote(99) != "" {
		t.Errorf("no limits set must mean no budget text")
	}
}

// an envelope over the per-envelope cap: the first calls run, the rest come
// back not run — Hermes is told which and why, the operator sees a note
func TestOversizeEnvelopeRunsTheFirstCalls(t *testing.T) {
	big := `{"calls":[{"id":"c1","tool":"read_file","args":{"path":"foo.txt"}},{"id":"c2","tool":"read_file","args":{"path":"foo.txt"}},{"id":"c3","tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`
	sa := &scriptAsker{replies: []api.AskResult{reply(big, true), reply(`{"calls":[],"final":"done"}`, true)}}
	var ran, notRun int
	var notes []string
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 8, MaxCallsPerRound: 2,
		Emit: func(ev session.Event) {
			switch ev.Kind {
			case session.KindCall:
				ran++
			case session.KindNotRun:
				notRun++
			case session.KindNote:
				notes = append(notes, ev.Text)
			}
		}}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	if ran != 2 || notRun != 1 || out.Calls != 2 || out.Rounds != 2 {
		t.Errorf("the first 2 run, 1 not: ran=%d notRun=%d calls=%d rounds=%d", ran, notRun, out.Calls, out.Rounds)
	}
	want := `{"id":"c3","tool":"local_read","args":{"path":"foo.txt"},"exit_code":-1,"output":"","not_run":"over the budget: the operator allows 2 calls per envelope — send it again if it is still needed"}`
	if !strings.Contains(sa.msgs[1], want) {
		t.Errorf("Hermes should be told which and why, got: %q", trunc(sa.msgs[1], 900))
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "the first 2 run, 1 sent back to Hermes") {
		t.Errorf("operator notes = %q", notes)
	}
}

// "stop_on_error": true — after the first call that fails the rest of the
// envelope comes back not run; without it every call runs
func TestStopOnErrorLeavesTheRestNotRun(t *testing.T) {
	for _, stop := range []bool{true, false} {
		env := fmt.Sprintf(`{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"false"}},{"id":"c2","tool":"local_shell","args":{"cmd":"echo second"}}],"stop_on_error":%v,"final":null}`, stop)
		sa := &scriptAsker{replies: []api.AskResult{reply(env, true), reply(`{"calls":[],"final":"done"}`, true)}}
		d := realDisp(t, prompt.AutoApprover{})
		d.Shell = shell.New(d.RepoRoot, 20000)
		t.Cleanup(d.Shell.Stop)
		ran := 0
		l := &Loop{API: sa, Dispatch: d, MaxRounds: 5, Emit: func(ev session.Event) {
			if ev.Kind == session.KindCall {
				ran++
			}
		}}
		if out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {}); !out.OK {
			t.Fatalf("out = %+v", out)
		}
		skipped := strings.Contains(sa.msgs[1], `"not_run":"stop_on_error: c1 (#1) failed with exit 1`)
		if stop && (ran != 1 || !skipped) || !stop && (ran != 2 || skipped) {
			t.Errorf("stop_on_error %v: ran %d, skipped %v — %s", stop, ran, skipped, trunc(sa.msgs[1], 600))
		}
	}
}

// say goes to the operator before the round's calls; beside a final it is
// not shown — the answer is the final
func TestSayGoesToTheOperatorBeforeTheCalls(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"say":"Reading foo first.","calls":[{"id":"c1","tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"say":"not shown","calls":[],"final":"done"}`, true),
	}}
	var kinds, said []string
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 5, Emit: func(ev session.Event) {
		switch ev.Kind {
		case session.KindSay:
			said = append(said, ev.Text)
			kinds = append(kinds, ev.Kind)
		case session.KindCall:
			kinds = append(kinds, ev.Kind)
		}
	}}
	if out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {}); !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	if strings.Join(kinds, " ") != "say call" || len(said) != 1 || said[0] != "Reading foo first." {
		t.Errorf("kinds %q, said %q", kinds, said)
	}
}

func TestRunCallsCap(t *testing.T) {
	callEnv := reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true)
	sa := &scriptAsker{replies: []api.AskResult{callEnv}} // repeats forever
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxCalls: 3}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if out.OK || out.Answer != "BLOCKED: Hermes still requesting tool calls after 3 calls." || out.Calls != 3 {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(l.PendingNote, "call budget (3 hands calls)") {
		t.Errorf("a capped turn should leave a resume note, got: %q", l.PendingNote)
	}
}

func TestRunRoundsCap(t *testing.T) {
	callEnv := reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true)
	sa := &scriptAsker{replies: []api.AskResult{callEnv}} // repeats forever
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if out.OK || out.Answer != "BLOCKED: Hermes still requesting data after 8 rounds." {
		t.Fatalf("out = %+v", out)
	}
	if len(s.msgs) != 9 {
		t.Errorf("want 9 Ask calls (8 executed rounds + the capped 9th), got %d", len(s.msgs))
	}
}

func TestRunRecapLatchesAfterUnthreadedRound(t *testing.T) {
	call := `{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`
	sa := &scriptAsker{replies: []api.AskResult{
		reply(call, true),                          // round 1
		reply(call, false),                         // round 2: server had to reset -> recap latches after this
		reply(`{"calls":[],"final":"done"}`, true), // round 3
	}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	if strings.HasPrefix(s.msgs[1], "[conversation so far this turn]") {
		t.Errorf("round 2 msg should NOT be recapped yet: %q", s.msgs[1])
	}
	if !strings.HasPrefix(s.msgs[2], "[conversation so far this turn]\n") {
		t.Errorf("round 3 msg should be recapped: %q", s.msgs[2])
	}
	if !strings.Contains(s.msgs[2], "[latest tool results]\n[hands results") ||
		!strings.Contains(s.msgs[2], `{"results":`) {
		t.Errorf("recap wrapper missing latest-results section: %q", s.msgs[2])
	}
}

// TestRunOperatorQuestionSurvivesFalseThreading pins the fix for a real
// gateway bug: a server can accept our session_id (so Threaded=true on every
// round) without actually replaying the transcript to the model. Before this
// fix that meant round 2+ carried only "[hands results] {...}" with no trace
// of what the operator asked, and the model fell back to describing the repo
// instead of answering. The reminder below must survive even though every
// round here reports Threaded:true (recap never latches).
func TestRunOperatorQuestionSurvivesFalseThreading(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 8}
	out := l.Run(context.Background(), "check the version on GitHub, in one sentence", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	for i, m := range sa.msgs[1:] {
		if !strings.Contains(m, "check the version on GitHub, in one sentence") {
			t.Errorf("round %d msg should restate the operator's question, got: %q", i+2, m)
		}
		if strings.HasPrefix(m, "[conversation so far this turn]") {
			t.Errorf("round %d: recap should not have latched (every round is Threaded:true), got: %q", i+2, m)
		}
	}
}

// every results message says how to answer it: the reply text is the
// envelope — seen live, a model mid-turn reached for tool_call instead
func TestRunResultsSayTheReplyIsTheEnvelope(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	if out, _, _ := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8); !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(sa.msgs[1], "your reply text, the envelope; no tool of yours reaches the worker") {
		t.Errorf("results message = %q", trunc(sa.msgs[1], 600))
	}
}

// plan mode is said at the start of every turn in it, and once when it is
// over — else the plan phase's "change nothing" would linger in the chat
func TestPlanModeNotes(t *testing.T) {
	ap := &prompt.TTYApprover{Mode: "plan"}
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"the plan"}`, true)}}
	l := &Loop{API: sa, Dispatch: &dispatch.Dispatcher{Approver: ap}, MaxRounds: 8}
	turn := func(msg string) { l.Run(context.Background(), msg, rec(), func(string, string, int) {}) }
	turn("plan it")
	ap.Mode = "auto"
	turn("do it")
	turn("and the docs")
	if !strings.Contains(sa.msgs[0], "[plan mode — the operator wants a plan first") || strings.Contains(sa.msgs[0], "plan mode is over") {
		t.Errorf("turn in plan mode = %q", sa.msgs[0])
	}
	if !strings.Contains(sa.msgs[1], "[plan mode is over: local_write / local_edit apply again") || strings.Contains(sa.msgs[1], "wants a plan first") {
		t.Errorf("first turn after plan mode = %q", sa.msgs[1])
	}
	if strings.Contains(sa.msgs[2], "plan mode") {
		t.Errorf("a later turn still mentions plan mode: %q", sa.msgs[2])
	}
}

func stoppedRun() api.AskResult {
	return api.AskResult{State: "stopped", RunID: "run_s", Threaded: true, Stopped: "`tool_call` Local Shell"}
}

// a run hermes-hands stopped (Hermes kept calling its own tool for the
// hands' work past its steers) is asked again: the same message, a
// correction in front — not the end of the turn
func TestRunStoppedRunIsAskedAgain(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{stoppedRun(), reply(`{"calls":[],"final":"done"}`, true)}}
	var notes []string
	l := &Loop{API: sa, MaxRounds: 8, Emit: func(ev session.Event) {
		if ev.Kind == session.KindNote {
			notes = append(notes, ev.Text)
		}
	}}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "done" || out.Rounds != 2 {
		t.Fatalf("out = %+v", out)
	}
	want := fmt.Sprintf(stoppedNote, "`tool_call` Local Shell") + "hello"
	if sa.msgs[1] != want {
		t.Errorf("round 2 msg = %q, want %q", sa.msgs[1], want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "run stopped") || !strings.Contains(notes[0], "(1/2)") {
		t.Errorf("notes = %q", notes)
	}
}

// past maxStops the turn ends — and a run stopped twice is asked again with
// one correction, not two stacked
func TestRunStoppedTooOftenBlocks(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{stoppedRun()}} // repeats
	out, _, _ := run(t, sa, nil, 8)
	if out.OK || !strings.Contains(out.Answer, "BLOCKED: Hermes kept calling its own tools") ||
		!strings.Contains(out.Answer, "stopped 3 runs") || len(sa.msgs) != maxStops+1 {
		t.Fatalf("out = %+v after %d requests", out, len(sa.msgs))
	}
	if n := strings.Count(sa.msgs[2], stoppedMark); n != 1 || !strings.HasSuffix(sa.msgs[2], "hello") {
		t.Errorf("round 3 msg carries %d corrections: %q", n, sa.msgs[2])
	}
}

func TestRunResultsShape(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"seen"}`, true),
	}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK {
		t.Fatalf("out = %+v", out)
	}
	var payload struct {
		Results []struct {
			ID       string          `json:"id"`
			Tool     string          `json:"tool"`
			Args     json.RawMessage `json:"args"`
			ExitCode int             `json:"exit_code"`
			Output   string          `json:"output"`
			Context  *string         `json:"context"`
		} `json:"results"`
	}
	m1 := s.msgs[1]
	if !strings.HasPrefix(m1, "[hands results") {
		t.Errorf("round 2 msg should lead with the hands-results reminder: %q", m1)
	}
	if i := strings.IndexByte(m1, '{'); i >= 0 {
		m1 = m1[i:]
	}
	if err := json.Unmarshal([]byte(m1), &payload); err != nil {
		t.Fatalf("round 2 msg is not a results payload: %v (%q)", err, s.msgs[1])
	}
	if len(payload.Results) != 1 {
		t.Fatalf("results len = %d", len(payload.Results))
	}
	r := payload.Results[0]
	if r.ID != "c1" || r.Tool != "local_read" || r.ExitCode != 0 || r.Output != "filecontent" || // old name in, canonical name out
		string(r.Args) != `{"path":"foo.txt"}` || r.Context != nil {
		t.Errorf("result element = %+v (args %s)", r, r.Args)
	}
}

func TestRunCallIDs(t *testing.T) {
	// Hermes-sent ids are kept; missing / duplicate ones get positional c<N>.
	env := `{"calls":[` +
		`{"id":"read-a","tool":"read_file","args":{"path":"foo.txt"}},` +
		`{"tool":"read_file","args":{"path":"foo.txt"}},` +
		`{"id":"read-a","tool":"read_file","args":{"path":"foo.txt"}}` +
		`],"final":null}`
	sa := &scriptAsker{replies: []api.AskResult{
		reply(env, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if !out.OK {
		t.Fatalf("out = %+v", out)
	}
	var payload struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	m1 := s.msgs[1]
	if i := strings.IndexByte(m1, '{'); i >= 0 {
		m1 = m1[i:]
	}
	if err := json.Unmarshal([]byte(m1), &payload); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	got := []string{payload.Results[0].ID, payload.Results[1].ID, payload.Results[2].ID}
	want := []string{"read-a", "c2", "c3"} // kept / synthesized / dup-collapsed-to-positional
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result[%d].id = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	if seen := map[string]bool{}; func() bool {
		for _, id := range got {
			if seen[id] {
				return true
			}
			seen[id] = true
		}
		return false
	}() {
		t.Errorf("ids not unique: %v", got)
	}
}

func TestRunResultsHTMLNotEscapedAndScrubbed(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(root, "x.txt"), []byte("a<b>&c api_key: supersecretvalue"), 0o644)
	d := &dispatch.Dispatcher{RepoRoot: root, Approver: prompt.AutoApprover{}, MaxOutput: 20000, RunTimeout: 5 * time.Second}

	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"x.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"ok"}`, true),
	}}
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 8, Scrub: redact.Scrub}
	l.Run(context.Background(), "hi", rec(), func(string, string, int) {})

	got := sa.msgs[1]
	if !strings.Contains(got, "a<b>&c") {
		t.Errorf("HTML chars must survive verbatim (no \\u003c): %q", got)
	}
	if strings.Contains(got, "supersecretvalue") {
		t.Errorf("secret not scrubbed from the results payload: %q", got)
	}
}

func TestRunAPIError(t *testing.T) {
	sa := &scriptAsker{errs: []error{fmt.Errorf("POST /v1/runs -> HTTP 500: boom")}}
	out, persisted, _ := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if out.OK || out.Answer != "BLOCKED: POST /v1/runs -> HTTP 500: boom" {
		t.Errorf("out = %+v", out)
	}
	if len(persisted) != 0 {
		t.Errorf("persist must not be called on API failure, got %v", persisted)
	}
}

func TestRunTurnAbortedByOperator(t *testing.T) {
	d := realDisp(t, abortApprover{})
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"write_file","args":{"path":"new.txt","content":"z"}}],"final":null}`, true),
	}}
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 8}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if !out.OK || !strings.Contains(out.Answer, "declined local_write") {
		t.Errorf("out = %+v", out)
	}
	if !strings.Contains(l.PendingNote, "local_write") || !strings.Contains(l.PendingNote, "cancelled") {
		t.Errorf("PendingNote = %q, want a cancellation note naming local_write", l.PendingNote)
	}
	// the note is consumed and prepended to the next turn's message
	sa.replies = []api.AskResult{reply(`{"calls":[],"final":"ok"}`, true)}
	_ = l.Run(context.Background(), "next task", rec(), func(string, string, int) {})
	if l.PendingNote != "" {
		t.Errorf("PendingNote should be cleared after the next Run, got %q", l.PendingNote)
	}
	if got := sa.msgs[len(sa.msgs)-1]; !strings.Contains(got, "previous turn was cancelled") {
		t.Errorf("next turn message should carry the cancellation note, got:\n%s", got)
	}
}

type abortApprover struct{}

func (abortApprover) Confirm(string, string) prompt.Decision { return prompt.AbortTurn }

func TestRunFramesFirstMessage(t *testing.T) {
	d := realDisp(t, prompt.AutoApprover{})
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"ok"}`, true)}}
	l := &Loop{
		API: sa, Dispatch: d, MaxRounds: 8, Scrub: redact.Scrub,
		RepoRoot:  "/home/me/proj",
		GitBranch: func() string { return "main" },
	}
	out := l.Run(context.Background(), "was hälst du von der readme ?", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "ok" {
		t.Fatalf("out = %+v", out)
	}
	got := sa.msgs[0]
	for _, want := range []string{
		"you are the operator's assistant", "hermes-hands is your execution\nworker (the hands)",
		"persistent bash shell",
		"MESSAGE from the operator — working directory: cwd /home/me/proj (git branch: main)",
		"was hälst du von der readme ?", // the message, verbatim, as the body
		"answer it in final, as in a chat — look\nfirst only when the answer needs facts from their machine",
		"your instruction is the envelope: `local_shell` (ls / git / rg) or\n`local_read` in its calls",
		"think it through first, not in the reply — then\nONE JSON object",
		`{"id":"c1","tool":"local_shell|local_read|local_write|local_edit|local_output"`,
		`"final":null}`,
		"Not a description of yourself or your tools unless asked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("framed round-1 message missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "operator: was hälst") {
		t.Errorf("message should stand as the body, not be prefixed 'operator:':\n%s", got)
	}
}

func TestRunNoFrameWithoutRepoRoot(t *testing.T) {
	d := realDisp(t, prompt.AutoApprover{})
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"ok"}`, true)}}
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 8, Scrub: redact.Scrub}
	_ = l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if sa.msgs[0] != "hello" {
		t.Errorf("without RepoRoot the message must pass through, got %q", sa.msgs[0])
	}
}

func TestRunEmitsCallBeforeResultWithSessionRefs(t *testing.T) {
	// Two calls in one envelope: each is announced (KindCall) before it runs
	// and reported (KindResult) after, numbered on from the session's last
	// ref; Hermes sees the same number as "ref" in its results.
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"id":"c1","tool":"read_file","args":{"path":"foo.txt"}},{"id":"c2","tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	var evs []session.Event
	l := &Loop{API: sa, Dispatch: realDisp(t, prompt.AutoApprover{}), MaxRounds: 5, Ref: 41,
		Emit: func(ev session.Event) {
			if ev.Kind != session.KindReply { // every reply is recorded too; this test is about the calls
				evs = append(evs, ev)
			}
		}}
	out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	var got []string
	for _, e := range evs {
		got = append(got, fmt.Sprintf("%s#%d", e.Kind, e.Ref))
	}
	if strings.Join(got, " ") != "call#42 result#42 call#43 result#43" {
		t.Errorf("events = %v", got)
	}
	if evs[1].Exit == nil || *evs[1].Exit != 0 || evs[1].Out != "filecontent" || evs[0].ID != "c1" {
		t.Errorf("result payload = %+v / call = %+v", evs[1], evs[0])
	}
	if l.Ref != 43 {
		t.Errorf("l.Ref = %d, want 43", l.Ref)
	}
	if !strings.Contains(sa.msgs[1], `"id":"c1","ref":42`) || !strings.Contains(sa.msgs[1], `"id":"c2","ref":43`) {
		t.Errorf("results must carry the refs: %s", trunc(sa.msgs[1], 400))
	}
}

func TestRunWriteHeldUntilRulesSeen(t *testing.T) {
	// round 1 writes into a directory with its own rules, unseen: held back,
	// the rules come along; round 2 sends the write again and it goes through
	d := realDisp(t, prompt.AutoApprover{})
	os.MkdirAll(filepath.Join(d.RepoRoot, "api"), 0o755)
	os.WriteFile(filepath.Join(d.RepoRoot, "api", "AGENTS.md"), []byte("every file here starts with // owner: api"), 0o644)
	d.Rules = (&knowsystem.System{Cwd: d.RepoRoot}).Scoped()
	write := `{"calls":[{"tool":"local_write","args":{"path":"api/ping.go","content":"// owner: api\n"}}],"final":null}`
	sa := &scriptAsker{replies: []api.AskResult{
		reply(write, true),
		reply(write, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	out, _, s := run(t, sa, d, 8)
	if !out.OK || len(s.msgs) != 3 {
		t.Fatalf("out = %+v, %d asks", out, len(s.msgs))
	}
	if !strings.Contains(s.msgs[1], "held back") || !strings.Contains(s.msgs[1], "owner: api") {
		t.Errorf("round 2 msg lacks the held-back result with the rules: %q", s.msgs[1])
	}
	if !strings.Contains(s.msgs[2], "wrote api/ping.go") {
		t.Errorf("round 3 msg: the repeated write did not go through: %q", s.msgs[2])
	}
}

// waitApprover approves after "waiting on the operator" for d, reporting the
// wait the way the REPL's spinner hold does.
type waitApprover struct {
	d      time.Duration
	paused *time.Duration
}

func (w waitApprover) Confirm(string, string) prompt.Decision {
	time.Sleep(w.d)
	*w.paused += w.d
	return prompt.Approve
}

func TestRunCallMsLeavesOutApprovalWait(t *testing.T) {
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"id":"c1","tool":"local_shell","args":{"cmd":"true"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	var paused time.Duration
	d := realDisp(t, waitApprover{d: 300 * time.Millisecond, paused: &paused})
	d.Shell = shell.New(d.RepoRoot, 20000)
	t.Cleanup(d.Shell.Stop)
	var res session.Event
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 5,
		Paused: func() time.Duration { return paused },
		Emit: func(ev session.Event) {
			if ev.Kind == session.KindResult {
				res = ev
			}
		}}
	if out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {}); !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if res.Exit == nil || *res.Exit != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.Ms >= 250 {
		t.Errorf("call Ms = %d — the 300ms approval wait must not count as run time", res.Ms)
	}
}

// the note on outputs of this turn a compaction just elided
func TestElidedNote(t *testing.T) {
	if elidedNote("") != "" {
		t.Error("nothing elided: no note")
	}
	if n := elidedNote("#6–#8"); !strings.Contains(n, "outputs #6–#8 of this turn are one line each now") || !strings.Contains(n, "do not") {
		t.Errorf("note = %q", n)
	}
}

// what the operator writes while a turn runs goes out with the next round,
// after the results, marked as theirs — and is logged as "added"; it is
// part of the turn's message from then on: every later round restates it
func TestRunTakesWhatTheOperatorAdded(t *testing.T) {
	inbox := &Inbox{}
	sa := &scriptAsker{replies: []api.AskResult{
		reply(`{"calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"say":"Reading foo again.","calls":[{"tool":"read_file","args":{"path":"foo.txt"}}],"final":null}`, true),
		reply(`{"calls":[],"final":"done"}`, true),
	}}
	var added []string
	d := realDisp(t, prompt.AutoApprover{})
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 8, Inbox: inbox, Emit: func(ev session.Event) {
		if ev.Kind == session.KindAdded {
			added = append(added, ev.Text)
		}
	}}
	typed := false
	l.OnResult = func(string, string, int) { // typed while the hands ran, once
		if !typed {
			typed = true
			inbox.Push("also check the tests")
		}
	}
	out := l.Run(context.Background(), "read foo", rec(), func(string, string, int) {})
	if !out.OK || out.Answer != "done" {
		t.Fatalf("out = %+v", out)
	}
	if strings.Contains(sa.msgs[0], "also check the tests") {
		t.Errorf("round 1 cannot carry it yet: %q", sa.msgs[0])
	}
	m := sa.msgs[1]
	if i, j := strings.Index(m, `"results"`), strings.Index(m, session.AddedMark+"also check the tests"); i < 0 || j < i {
		t.Errorf("round 2 must carry it after the results: %q", m)
	}
	if len(added) != 1 || added[0] != "also check the tests" || len(inbox.Take()) != 0 {
		t.Errorf("added events = %q", added)
	}
	if !strings.Contains(sa.msgs[2], `was: "read foo" + added: "also check the tests".`) {
		t.Errorf("round 3 must restate the message with what was added: %q", trunc(sa.msgs[2], 600))
	}
}

// what the turn did not take — it ended first — stays for the next turn
func TestInboxKeepsWhatTheTurnDidNotTake(t *testing.T) {
	inbox := &Inbox{}
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"done"}`, true)}}
	l := &Loop{API: sa, MaxRounds: 8, Inbox: inbox}
	sa2 := &pushOnAsk{scriptAsker: sa, push: func() { inbox.Push("next idea") }}
	l.API = sa2
	if out := l.Run(context.Background(), "hello", rec(), func(string, string, int) {}); !out.OK {
		t.Fatalf("out = %+v", out)
	}
	if got := inbox.Take(); len(got) != 1 || got[0] != "next idea" {
		t.Errorf("inbox after the turn = %q, want the line for the next turn", got)
	}
}

// pushOnAsk runs push while a request is out — the operator typing while
// Hermes thinks.
type pushOnAsk struct {
	*scriptAsker
	push func()
}

func (p *pushOnAsk) Ask(ctx context.Context, msg, sess, skey string) (api.AskResult, error) {
	p.push()
	return p.scriptAsker.Ask(ctx, msg, sess, skey)
}
