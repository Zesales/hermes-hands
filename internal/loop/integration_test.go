package loop_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/dispatch"
	"github.com/Zesales/hermes-hands/internal/hermesmock"
	"github.com/Zesales/hermes-hands/internal/loop"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/redact"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/shell"
)

type harness struct {
	client *api.Client
	store  *session.Store
	disp   *dispatch.Dispatcher
	loop   *loop.Loop
	shell  *shell.Shell
}

func newHarness(t *testing.T, mode string) *harness {
	t.Helper()
	config.Warnf = func(string, ...any) {} // silence the loopback-http notice in test output

	srv := hermesmock.New(mode)
	t.Cleanup(srv.Close)

	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("ai-stack readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "init", "-q").Run()

	store, err := session.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}

	cl := &api.Client{
		HTTP:         api.NewHTTPClient(2*time.Second, 5*time.Second),
		BaseURL:      srv.URL,
		Key:          "testkey",
		AllowHTTP:    true,
		Retries:      1,
		PollInterval: time.Millisecond,
		RunTimeout:   5 * time.Second,
	}
	sh := shell.New(repo, 40000)
	t.Cleanup(sh.Stop)
	d := &dispatch.Dispatcher{
		RepoRoot:   repo,
		Shell:      sh,
		Approver:   prompt.AutoApprover{},
		MaxOutput:  20000,
		RunTimeout: 10 * time.Second,
	}
	l := &loop.Loop{API: cl, Dispatch: d, MaxRounds: 8, Scrub: redact.Scrub}
	return &harness{client: cl, store: store, disp: d, loop: l, shell: sh}
}

// turn mimics bin/hermes-hands:run_turn.
func (h *harness) turn(t *testing.T, mode, msg string) (loop.Outcome, *session.Record) {
	t.Helper()
	rec, err := h.store.Resolve(mode, h.disp.RepoRoot)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out := h.loop.Run(context.Background(), msg, rec, func(runID, sid string, tok int) {
		if err := h.store.BumpTurn(rec, runID, sid, tok); err != nil {
			t.Fatalf("bump: %v", err)
		}
	})
	if out.OK {
		_, _ = h.store.SetTitleLocal(rec, msg)
	}
	return out, rec
}

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

func TestIntegration_PlainFinal(t *testing.T) {
	out, _ := newHarness(t, "plain").turn(t, "new", "hi")
	if !out.OK || !strings.Contains(out.Answer, "plain answer from the mock brain") {
		t.Errorf("out = %+v", out)
	}
}

func TestIntegration_ProseAccepted(t *testing.T) {
	out, _ := newHarness(t, "prose").turn(t, "new", "hi")
	if !out.OK || !strings.Contains(out.Answer, "plain-prose answer") {
		t.Errorf("out = %+v", out)
	}
}

func TestIntegration_BadJSONRecovery(t *testing.T) {
	out, _ := newHarness(t, "badjson").turn(t, "new", "hi")
	if !out.OK || !strings.Contains(out.Answer, "recovered and answered") {
		t.Errorf("out = %+v", out)
	}
}

func TestIntegration_DelegateLoop(t *testing.T) {
	requireBash(t)
	out, _ := newHarness(t, "delegate").turn(t, "new", "was steht in der README?")
	if !out.OK || !strings.Contains(out.Answer, "saw_results=True") {
		t.Errorf("out = %+v", out)
	}
}

func TestIntegration_ShellCwdPersists(t *testing.T) {
	requireBash(t)
	out, _ := newHarness(t, "shellstate").turn(t, "new", "cd around")
	if !out.OK || !strings.Contains(out.Answer, "cwd_persisted=True") {
		t.Errorf("out = %+v", out)
	}
}

func TestIntegration_SessionContinuity(t *testing.T) {
	h := newHarness(t, "plain")

	if _, rec := h.turn(t, "new", "one"); rec.Turns != 1 {
		t.Fatalf("after turn 1, Turns = %d", rec.Turns)
	}
	_, rec := h.turn(t, "continue", "two")
	if rec.Turns != 2 {
		t.Errorf("after -c turn 2, Turns = %d, want 2", rec.Turns)
	}
	if !strings.HasPrefix(rec.HermesSessionID, "hh-") {
		t.Errorf("HermesSessionID = %q, want hh- prefix", rec.HermesSessionID)
	}
	if !strings.HasPrefix(rec.HermesSessionKey, "hermes-hands:") {
		t.Errorf("HermesSessionKey = %q, want hermes-hands: prefix", rec.HermesSessionKey)
	}

	recs, _ := h.store.List()
	if len(recs) != 1 {
		t.Errorf("want exactly one session for the dir across -c, got %d", len(recs))
	}
}
