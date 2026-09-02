package prompt

import (
	"io"
	"strings"
	"testing"
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
