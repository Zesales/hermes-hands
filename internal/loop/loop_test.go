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

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/dispatch"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/redact"
	"github.com/Zesales/hermes-hands/internal/session"
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
	out := l.Run(context.Background(), "hello", rec(), func(r, s string) {
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

func TestPreviewOf(t *testing.T) {
	if got := previewOf(json.RawMessage(`{"cmd":"ls -la\nfoo"}`)); got != "ls -la foo" {
		t.Errorf("previewOf cmd = %q", got)
	}
	if got := previewOf(json.RawMessage(`{"path":"src/x.go"}`)); got != "src/x.go" {
		t.Errorf("previewOf path = %q", got)
	}
	long := strings.Repeat("z", 100)
	if got := previewOf(json.RawMessage(`{"pattern":"` + long + `"}`)); len(got) != 72 {
		t.Errorf("previewOf clip len = %d", len(got))
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
	if s.msgs[1] != fixupMsg {
		t.Errorf("round 2 msg = %q, want the fixup error", s.msgs[1])
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
	if len(s.msgs) != 3 {
		t.Errorf("want 3 rounds (2 fixups then accept), got %d", len(s.msgs))
	}
}

func TestRunEmptyEnvelopeNudgeThenBlocked(t *testing.T) {
	empty := reply(`{"calls":[],"final":null}`, true)
	sa := &scriptAsker{replies: []api.AskResult{empty, empty, empty}}
	out, _, s := run(t, sa, realDisp(t, prompt.AutoApprover{}), 8)
	if out.OK || out.Answer != "BLOCKED: Hermes returned an empty envelope repeatedly." {
		t.Fatalf("out = %+v", out)
	}
	if s.msgs[1] != emptyMsg || s.msgs[2] != emptyMsg {
		t.Errorf("nudge messages = %q / %q", s.msgs[1], s.msgs[2])
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
	if !strings.Contains(s.msgs[2], "[latest tool results]\n{\"results\":") {
		t.Errorf("recap wrapper missing latest-results section: %q", s.msgs[2])
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
			Tool     string          `json:"tool"`
			Args     json.RawMessage `json:"args"`
			ExitCode int             `json:"exit_code"`
			Output   string          `json:"output"`
			Context  *string         `json:"context"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(s.msgs[1]), &payload); err != nil {
		t.Fatalf("round 2 msg is not a results payload: %v (%q)", err, s.msgs[1])
	}
	if len(payload.Results) != 1 {
		t.Fatalf("results len = %d", len(payload.Results))
	}
	r := payload.Results[0]
	if r.Tool != "read_file" || r.ExitCode != 0 || r.Output != "filecontent" ||
		string(r.Args) != `{"path":"foo.txt"}` || r.Context != nil {
		t.Errorf("result element = %+v (args %s)", r, r.Args)
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
	l.Run(context.Background(), "hi", rec(), func(string, string) {})

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
	out, _, _ := run(t, sa, d, 8)
	if !out.OK || out.Answer != "(turn aborted by operator at a write_file approval)" {
		t.Errorf("out = %+v", out)
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
	out := l.Run(context.Background(), "was hälst du von der readme ?", rec(), func(string, string) {})
	if !out.OK || out.Answer != "ok" {
		t.Fatalf("out = %+v", out)
	}
	got := sa.msgs[0]
	for _, want := range []string{
		"remote worker session", "you are NOT local",
		"cwd: /home/me/proj", "git branch: main",
		`{"calls":[{"tool","args"}],"final":null}`,
		"operator: was hälst du von der readme ?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("framed round-1 message missing %q\n---\n%s", want, got)
		}
	}
}

func TestRunNoFrameWithoutRepoRoot(t *testing.T) {
	d := realDisp(t, prompt.AutoApprover{})
	sa := &scriptAsker{replies: []api.AskResult{reply(`{"calls":[],"final":"ok"}`, true)}}
	l := &Loop{API: sa, Dispatch: d, MaxRounds: 8, Scrub: redact.Scrub}
	_ = l.Run(context.Background(), "hello", rec(), func(string, string) {})
	if sa.msgs[0] != "hello" {
		t.Errorf("without RepoRoot the message must pass through, got %q", sa.msgs[0])
	}
}
