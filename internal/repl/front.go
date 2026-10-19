package repl

import (
	"fmt"
	"os"
	"strings"
	"time"

	"hermes-hands/internal/loop"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/session"
	"hermes-hands/internal/ui"
)

// front shows a turn in the terminal: every event through ui.Show (the
// same drawing a replay uses), the "working" line from the operator's message
// until the answer starts streaming, the running call's name on it.
type front struct {
	ui   *ui.UI
	stop func() // stops the "working" line; set by the operator's message
}

func (f *front) Show(ev session.Event) {
	f.ui.Show(ev, ev.Out)
	switch ev.Kind {
	case session.KindUser:
		f.stop = f.ui.StartWorking()
	case session.KindCall:
		f.ui.SetActivity(fmt.Sprintf("#%d %s", ev.Ref, ev.Tool))
	case session.KindResult:
		f.ui.SetActivity("")
	}
}

// Preview: the first streamed chunk means output is flowing — retire the
// "working" line.
func (f *front) Preview(s string) {
	f.quiet()
	f.ui.Delta(s)
}

func (f *front) EndPreview() bool { return f.ui.StreamDone() }

func (f *front) Quiet() {
	f.quiet()
	f.ui.SetActivity("")
	f.ui.SetBudget("")
}

// Progress keeps "round 3/25 · calls 12/100" on the working line.
func (f *front) Progress(p loop.Progress) {
	f.ui.SetBudget(ui.BudgetLabel(p.Round, p.MaxRounds, p.Calls, p.MaxCalls))
}

func (f *front) quiet() {
	if f.stop != nil {
		f.stop()
	}
}

// askServerApproval puts an approval request of Hermes' OWN server-side tool
// to the operator. It is a separate trust domain from the hands' approvals,
// so their [a]ll latch never answers it. o = once, s = for this Hermes
// session, anything else = deny. Without an interactive prompt (approvals
// auto / never) it is a deny — a server command nobody looked at never runs.
func askServerApproval(ap prompt.Approver, tool, what string) string {
	ta, ok := ap.(*prompt.TTYApprover)
	if !ok || ta.Mode != "ask" || ta.AskLine == nil {
		return "deny"
	}
	if ta.Pause != nil {
		defer ta.Pause()()
	}
	out := ta.Out
	if out == nil {
		out = os.Stderr
	}
	if tool == "" {
		tool = "a tool"
	}
	summary := "hermes wants to run " + tool + " on its SERVER sandbox (not your machine):"
	asked := time.Now()
	fmt.Fprintf(out, "\n  ⚠  %s\n      %s\n", summary, what)
	line, err := ta.AskLine("  [o]nce  [s]ession  [n]o > ")
	answer := "deny"
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "o", "y", "once":
			answer = "once"
		case "s", "session":
			answer = "session"
		}
	}
	if ta.OnAnswer != nil {
		ta.OnAnswer(summary, what, answer, time.Since(asked))
	}
	return answer
}

// maxApprovalDetail caps the diff an approval event keeps for its replay.
const maxApprovalDetail = 200

// clipLines keeps the first n lines of s and says how many it dropped.
func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
}
