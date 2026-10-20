package prompt

import (
	"io"
	"strings"
	"testing"
	"time"
)

type fakeTTY struct {
	in  *strings.Reader
	out *strings.Builder
}

func (f *fakeTTY) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeTTY) Write(p []byte) (int, error) { return f.out.Write(p) }
func (f *fakeTTY) Close() error                { return nil }

func withInput(input string) (*TTYApprover, *strings.Builder) {
	out := &strings.Builder{}
	a := &TTYApprover{Mode: "ask"}
	a.openTTY = func() (io.ReadWriteCloser, error) {
		return &fakeTTY{in: strings.NewReader(input), out: out}, nil
	}
	return a, out
}

func TestConfirmAutoAndNever(t *testing.T) {
	if (&TTYApprover{Mode: "auto"}).Confirm("x", "") != Approve {
		t.Errorf("auto must Approve")
	}
	var warned int
	a := &TTYApprover{Mode: "never", Warnf: func(string, ...any) { warned++ }}
	if a.Confirm("shell: rm", "") != Deny {
		t.Errorf("never must Deny")
	}
	if warned != 1 {
		t.Errorf("never must warn once, warned=%d", warned)
	}
}

// plan asks like ask (shell commands still need the operator); it only
// reports Planning, which keeps dispatch from writing
func TestConfirmPlanAsks(t *testing.T) {
	a, out := withInput("y\n")
	a.Mode = "plan"
	if !a.Planning() || (&TTYApprover{Mode: "ask"}).Planning() {
		t.Errorf("Planning: plan = %v, ask = %v", a.Planning(), (&TTYApprover{Mode: "ask"}).Planning())
	}
	if a.Confirm("local_shell: ls", "") != Approve || !strings.Contains(out.String(), "local_shell: ls") {
		t.Errorf("plan must ask like ask, prompt = %q", out.String())
	}
}

func TestConfirmAnswerMapping(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Decision
	}{
		{"y\n", Approve},
		{"Y\n", Approve},
		{"a\n", Approve},
		{"n\n", Deny},
		{"\n", Deny},
		{"yes\n", Deny}, // only exact y/Y approves
		{"q\n", AbortTurn},
		{"", AbortTurn},  // EOF, no line
		{"y", AbortTurn}, // partial line then EOF -> bash `read` fails -> q
	} {
		a, _ := withInput(tc.in)
		if got := a.Confirm("shell: ls", ""); got != tc.want {
			t.Errorf("Confirm(input=%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestConfirmPromptTextByteExact(t *testing.T) {
	a, out := withInput("y\n")
	a.Confirm("shell: ls -la", "diff line 1\ndiff line 2")
	want := "\n  ⚠  shell: ls -la\n" +
		"      diff line 1\n      diff line 2\n" +
		"  [y]es  [n]o  [a]ll  [q]uit turn > "
	if out.String() != want {
		t.Errorf("prompt =\n%q\nwant\n%q", out.String(), want)
	}

	a, out = withInput("n\n")
	a.Confirm("write_file: x", "")
	want = "\n  ⚠  write_file: x\n  [y]es  [n]o  [a]ll  [q]uit turn > "
	if out.String() != want {
		t.Errorf("prompt (no detail) =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestConfirmAllLatchesForProcess(t *testing.T) {
	a, _ := withInput("a\n")
	if a.Confirm("shell: first", "") != Approve {
		t.Fatalf("'a' must Approve")
	}
	// Latched: a further prompt must not even open the tty.
	a.openTTY = func() (io.ReadWriteCloser, error) {
		t.Fatal("tty opened despite latched 'all'")
		return nil, nil
	}
	if a.Confirm("shell: second", "") != Approve {
		t.Errorf("latched 'all' must keep approving")
	}
}

func TestConfirmNoTTYDenies(t *testing.T) {
	var warned int
	a := &TTYApprover{Mode: "ask", Warnf: func(string, ...any) { warned++ }}
	a.openTTY = func() (io.ReadWriteCloser, error) { return nil, io.ErrUnexpectedEOF }
	if a.Confirm("shell: ls", "") != Deny {
		t.Errorf("no tty must Deny")
	}
	if warned != 1 {
		t.Errorf("no tty must warn once, warned=%d", warned)
	}
}

func TestAutoApprover(t *testing.T) {
	if (AutoApprover{}).Confirm("anything", "detail") != Approve {
		t.Errorf("AutoApprover must Approve")
	}
}

func TestConfirmCallsPauseAndResume(t *testing.T) {
	var paused, resumed int
	a := &TTYApprover{
		Mode:    "ask",
		AskLine: func(string) (string, error) { return "y", nil },
		Pause: func() func() {
			paused++
			return func() { resumed++ }
		},
	}
	if got := a.Confirm("shell: ls", ""); got != Approve {
		t.Fatalf("Confirm = %v", got)
	}
	if paused != 1 || resumed != 1 {
		t.Errorf("pause/resume = %d/%d, want 1/1", paused, resumed)
	}
	// auto never reaches the prompt -> no pause
	paused, resumed = 0, 0
	a.Mode = "auto"
	_ = a.Confirm("x", "")
	if paused != 0 {
		t.Errorf("auto mode must not pause, got %d", paused)
	}
}

func TestConfirmOnAnswerOnlyForAnsweredPrompts(t *testing.T) {
	var heard []string
	rec := func(summary, _, answer string, _ time.Duration) { heard = append(heard, summary+"="+answer) }

	for _, tc := range []struct{ in, want string }{
		{"y\n", "yes"}, {"a\n", "all"}, {"n\n", "no"}, {"x\n", "no"}, {"q\n", "quit"}, {"", "quit"},
	} {
		heard = nil
		a, _ := withInput(tc.in)
		a.OnAnswer = rec
		a.Confirm("local_shell: ls", "")
		if len(heard) != 1 || heard[0] != "local_shell: ls="+tc.want {
			t.Errorf("input %q: heard %v, want [local_shell: ls=%s]", tc.in, heard, tc.want)
		}
	}

	// no prompt shown -> nothing to record: auto, never, the [a]ll latch
	heard = nil
	(&TTYApprover{Mode: "auto", OnAnswer: rec}).Confirm("x", "")
	(&TTYApprover{Mode: "never", OnAnswer: rec, Warnf: func(string, ...any) {}}).Confirm("x", "")
	a, _ := withInput("a\n")
	a.Confirm("first", "") // latches, OnAnswer not set yet
	a.OnAnswer = rec
	a.Confirm("second", "")
	if len(heard) != 0 {
		t.Errorf("unprompted decisions recorded: %v", heard)
	}
}
