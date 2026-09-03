// Package prompt ports the approval gate lib/util.sh:hh_confirm. Three answers:
// Approve, Deny, AbortTurn (bash return codes 0 / 1 / 2).
package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Zesales/hermes-hands/internal/ttyio"
)

// Decision is the outcome of an approval request.
type Decision int

const (
	Approve Decision = iota
	Deny
	AbortTurn
)

// Approver decides whether a pending shell command / file write may proceed.
type Approver interface {
	Confirm(summary, detail string) Decision
}

// AutoApprover always approves — used for HERMES_HANDS_APPROVE=auto / --yolo
// and in tests.
type AutoApprover struct{}

// Confirm always returns Approve.
func (AutoApprover) Confirm(string, string) Decision { return Approve }

// TTYApprover is the interactive gate. One instance lives for the whole
// process, so an "all" answer latches for the rest of the run (bash
// HH_APPROVE_ALL), across turns.
type TTYApprover struct {
	Mode    string // HERMES_HANDS_APPROVE: ask | auto | never (anything else == ask)
	Warnf   func(format string, a ...any)
	allDone bool

	// Out is where the summary + detail are rendered (default os.Stderr).
	Out io.Writer

	// AskLine, when set, reads the y/n/a/q answer instead of a raw /dev/tty
	// read. The REPL points this at its liner so the prompt shares the one
	// terminal owner (a second reader on /dev/tty deadlocks against liner's
	// input goroutine — the "can't type / Ctrl-C does nothing" hang). It
	// returns an error on Ctrl-C, which is treated as "quit turn".
	AskLine func(prompt string) (string, error)

	// Pause, when set, is called before the prompt is drawn; its return value
	// is called after the answer is read. The REPL uses it to freeze the
	// animated "working" spinner so it doesn't overwrite the prompt line.
	Pause func() (resume func())

	// openTTY is indirected for tests; nil means the real /dev/tty.
	openTTY func() (io.ReadWriteCloser, error)
}

// Confirm ports hh_confirm exactly:
//
//	auto            -> Approve
//	never           -> warn, Deny
//	latched "all"   -> Approve
//	no /dev/tty     -> warn, Deny
//	prompt on /dev/tty; y -> Approve; a -> latch + Approve;
//	q or read failure (EOF) -> AbortTurn; anything else -> Deny
func (a *TTYApprover) Confirm(summary, detail string) Decision {
	switch a.Mode {
	case "auto":
		return Approve
	case "never":
		a.warn("denied by policy (HERMES_HANDS_APPROVE=never): %s", summary)
		return Deny
	}
	if a.allDone {
		return Approve
	}

	if a.Pause != nil { // freeze the "working" spinner while the prompt is up
		defer a.Pause()()
	}

	out := a.Out
	if out == nil {
		out = os.Stderr
	}
	const q = "  [y]es  [n]o  [a]ll  [q]uit turn > "

	// Preferred path: the REPL's liner reads the answer, so there is exactly
	// one owner of the terminal.
	if a.AskLine != nil {
		fmt.Fprintf(out, "\n  ⚠  %s\n", summary)
		if detail != "" {
			io.WriteString(out, indentLines(detail, "      "))
		}
		line, err := a.AskLine(q)
		if err != nil { // Ctrl-C / EOF at the prompt
			return AbortTurn
		}
		return decide(line, a)
	}

	// Fallback: raw /dev/tty (used only when there is no line reader, e.g. a
	// non-REPL caller that still left Mode=ask).
	tty, err := a.open()
	if err != nil {
		a.warn("no tty for approval, denying: %s", summary)
		return Deny
	}
	defer tty.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "\n  ⚠  %s\n", summary)
	if detail != "" {
		b.WriteString(indentLines(detail, "      "))
	}
	b.WriteString(q)
	io.WriteString(tty, b.String())

	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return AbortTurn
	}
	return decide(line, a)
}

func decide(line string, a *TTYApprover) Decision {
	switch strings.Trim(line, " \t\r\n") {
	case "y", "Y":
		return Approve
	case "a", "A":
		a.allDone = true
		return Approve
	case "q", "Q":
		return AbortTurn
	default:
		return Deny
	}
}

func (a *TTYApprover) open() (io.ReadWriteCloser, error) {
	if a.openTTY != nil {
		return a.openTTY()
	}
	f, err := ttyio.OpenControllingTTY()
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (a *TTYApprover) warn(format string, args ...any) {
	if a.Warnf != nil {
		a.Warnf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, "hermes-hands: WARNING: "+format+"\n", args...)
}

// indentLines mirrors `printf '%s\n' "$detail" | sed 's/^/      /'`.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}
