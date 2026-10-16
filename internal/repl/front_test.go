package repl

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

func TestAskServerApproval(t *testing.T) {
	ask := func(mode, answer string, err error) string {
		var out strings.Builder
		ta := &prompt.TTYApprover{Mode: mode, Out: &out, AskLine: func(string) (string, error) { return answer, err }}
		return askServerApproval(ta, "terminal", "rm -rf /tmp/build")
	}
	for _, tc := range []struct {
		mode, answer string
		err          error
		want         string
	}{
		{"ask", "o", nil, "once"},
		{"ask", "s", nil, "session"},
		{"ask", "n", nil, "deny"},
		{"ask", "", nil, "deny"},
		{"ask", "o", io.EOF, "deny"}, // Ctrl-C / EOF at the prompt
		{"auto", "o", nil, "deny"},   // yolo never approves a server command unseen
		{"never", "o", nil, "deny"},
	} {
		if got := ask(tc.mode, tc.answer, tc.err); got != tc.want {
			t.Errorf("mode %s answer %q -> %q, want %q", tc.mode, tc.answer, got, tc.want)
		}
	}
	// the hands' [a]ll latch is another trust domain: it must not answer this
	ta := &prompt.TTYApprover{Mode: "ask", AskLine: func(string) (string, error) { return "a", nil }}
	if askServerApproval(ta, "terminal", "x") != "deny" {
		t.Error("'a' (the hands' all-latch) must not approve a server command")
	}
}

// A replayed approval prompt reads exactly as the live one did — the prompt
// text plus the answer the operator typed (echoed by the line reader).
func TestApprovalReplayMatchesLivePrompt(t *testing.T) {
	render := func(f func(u *ui.UI)) string {
		tmp, err := os.CreateTemp(t.TempDir(), "ui")
		if err != nil {
			t.Fatal(err)
		}
		f(ui.New(tmp))
		b, _ := os.ReadFile(tmp.Name())
		tmp.Close()
		return string(b)
	}
	for _, tc := range []struct {
		name, typed, answer string
		ask                 func(ta *prompt.TTYApprover)
	}{
		{"hands", "a", "all", func(ta *prompt.TTYApprover) { ta.Confirm("local_shell: git log", "") }},
		{"hands diff", "n", "no", func(ta *prompt.TTYApprover) { ta.Confirm("local_write: x.go", "+line 1\n+line 2") }},
		{"server", "s", "session", func(ta *prompt.TTYApprover) { askServerApproval(ta, "terminal", "rm -rf /tmp/x") }},
	} {
		var live strings.Builder
		var ev session.Event
		ta := &prompt.TTYApprover{Mode: "ask", Out: &live}
		ta.AskLine = func(q string) (string, error) { live.WriteString(q + tc.typed + "\n"); return tc.typed, nil }
		ta.OnAnswer = func(summary, detail, answer string, _ time.Duration) {
			ev = session.Event{Kind: session.KindApproval, Text: summary, Detail: detail, Action: answer}
		}
		tc.ask(ta)
		if ev.Action != tc.answer {
			t.Errorf("%s: recorded answer %q, want %q", tc.name, ev.Action, tc.answer)
		}
		if got := render(func(u *ui.UI) { u.Approval(ev.Text, ev.Detail, ev.Action) }); got != live.String() {
			t.Errorf("%s: replay\n%q\nlive\n%q", tc.name, got, live.String())
		}
	}
}
