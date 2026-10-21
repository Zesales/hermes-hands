package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"hermes-hands/internal/ttyio"
)

// The dock is the live block under the conversation, as in Claude Code. In
// the REPL it is there from the start (SetDocked): a rule against the
// conversation above, the input line of the REPL's box, a closing rule and
// a footer. While a turn runs the working line stands over it, and the
// messages queued for the turn's next round are listed, numbered:
//
//	   ...working  ·  12s  ·  round 2/25 · calls 3/100  ·  Ctrl+C to cancel
//	──────────────────────────────────────────────────────────────────────
//	  1  look at the README too
//	  2  and the Makefile
//	you› type to add a message to the run
//	──────────────────────────────────────────────────────────────────────
//	  ⏵ approvals auto  ·  Enter queues it for the next round
//
// Everything else is printed above it: sync takes the dock off, writes, and
// puts it back. Every dock line is cut to the terminal's width — a line
// that wrapped would throw the wipe off by a row. Not docked (--rpc, no
// terminal to type in) the dock is the working line alone.

// maxQueued is how many queued messages the dock lists; more are counted.
const maxQueued = 5

// queuedMsg is a message the operator queued during the turn, with its
// number in the turn.
type queuedMsg struct {
	n    int
	text string
}

// Input is the dock's input line as the REPL's box has it.
type Input struct {
	Prompt string // before the text: "you› ", or the question up
	Text   []rune
	Cursor int    // where the cursor stands in Text
	Hint   string // dim, in place of an empty Text
	Ask    bool   // Prompt is a question waiting for its answer
	Note   string // a line over the input ("" = none)
	Footer string // in place of the footer SetFooter set ("" = that one)
}

var frames = []string{".  ", ".. ", "...", " ..", "  .", "   "}

// sync runs f as the sole writer: the dock comes off first, so f's output
// lands above it, and goes back on under it.
func (u *UI) sync(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	live := u.live()
	if live {
		io.WriteString(u.w, u.wipe())
	}
	f()
	if live {
		io.WriteString(u.w, u.draw())
	}
}

// change runs f, which changes what the dock shows, and brings the screen
// up to date in one write: the dock wiped and drawn anew, put up, or taken
// off.
func (u *UI) change(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	was := u.live()
	var s string
	if was {
		s = u.wipe()
	}
	f()
	switch {
	case u.live():
		s += "\r" + u.draw()
	case was:
		s += "\x1b[?25h"
	}
	io.WriteString(u.w, s)
}

// live reports whether the dock is on screen: docked or a turn working, and
// no prompt holding the terminal.
func (u *UI) live() bool { return (u.docked || u.working) && u.held == 0 }

// repaint draws the dock anew, in one write.
func (u *UI) repaint() { io.WriteString(u.w, u.wipe()+u.draw()) }

// wipe takes the dock off: back up to its first row, clear from there down.
func (u *UI) wipe() string {
	s := "\x1b[?25l"
	if u.up > 0 {
		s += fmt.Sprintf("\x1b[%dA", u.up)
	}
	u.up = 0
	return s + "\r\x1b[J"
}

// draw puts the dock at the start of an empty row and leaves the cursor on
// its input line, after what is typed.
func (u *UI) draw() string {
	lines, row, col := u.dock(u.width())
	if len(lines) == 0 {
		return "\x1b[?25h"
	}
	var b strings.Builder
	b.WriteString(strings.Join(lines, "\n"))
	u.up = len(lines) - 1
	if row >= 0 {
		if back := len(lines) - 1 - row; back > 0 {
			fmt.Fprintf(&b, "\x1b[%dA", back)
		}
		b.WriteByte('\r')
		if col > 0 {
			fmt.Fprintf(&b, "\x1b[%dC", col)
		}
		u.up = row
	}
	b.WriteString("\x1b[?25h")
	return b.String()
}

// dock is the dock's lines, each cut to w columns, and the row and column of
// the cursor on its input line (row -1: no input line).
func (u *UI) dock(w int) (lines []string, row, col int) {
	if u.streamOpen && u.streamTail != "" {
		lines = append(lines, fit("   "+u.streamTail, w))
	}
	if u.working {
		lines = append(lines, fit(u.workingLine(), w))
	}
	if !u.docked {
		return lines, -1, 0
	}
	rule := u.cDim + strings.Repeat("─", w-1) + u.cR
	lines = append(lines, rule)
	shown := u.queue
	if len(shown) > maxQueued {
		shown = shown[:maxQueued-1]
	}
	for _, q := range shown {
		lines = append(lines, fit(fmt.Sprintf("  %s%d%s  %s", u.cAcc, q.n, u.cR, strings.Join(strings.Fields(q.text), " ")), w))
	}
	if more := len(u.queue) - len(shown); more > 0 {
		lines = append(lines, fmt.Sprintf("%s     … %d more queued%s", u.cDim, more, u.cR))
	}
	if u.in.Note != "" {
		lines = append(lines, fit(u.cAcc+"  ⚠ "+u.in.Note+u.cR, w))
	}
	in, c := u.inputLine(w)
	row, col = len(lines), c
	lines = append(lines, in, rule)
	footer := u.footer
	if u.in.Footer != "" {
		footer = u.in.Footer
	}
	if footer != "" {
		lines = append(lines, fit(u.cDim+"  "+footer+u.cR, w))
	}
	return lines, row, col
}

// inputLine is the input line cut to w columns — the text scrolled so the
// cursor stays in view — and the column of the cursor.
func (u *UI) inputLine(w int) (string, int) {
	p := u.in.Prompt
	if textWidth(p) > w/2 { // a long question
		p = fit(p, w/2)
	}
	col := textWidth(p)
	colour := u.cB + u.cYou
	if u.in.Ask {
		colour = u.cB + u.cAcc
	}
	head := colour + p + u.cR
	if len(u.in.Text) == 0 {
		return fit(head+u.cDim+u.in.Hint+u.cR, w), col
	}
	view, at := inputView(u.in.Text, u.in.Cursor, w-1-col-1)
	return head + view, col + at
}

// workingLine is the line over the dock while a turn works: what it waits
// on, how long, where it stands against its budget.
func (u *UI) workingLine() string {
	waiting := time.Duration(0)
	if u.awaiting > 0 {
		waiting = time.Since(u.awaitSince)
	}
	el := int((time.Since(u.start) - u.paused - waiting).Seconds())
	label := "working"
	switch {
	case u.awaiting > 0:
		label = "waiting for your answer"
	case u.activity != "":
		label = u.activity + " running"
		el = int(time.Since(u.activitySince).Seconds())
	}
	budget := ""
	if u.budget != "" {
		budget = "  ·  " + u.budget
	}
	return fmt.Sprintf("%s   %s%s%s%s%s  ·  %ds%s  ·  Ctrl+C to cancel%s",
		u.cDim, u.cAcc, frames[u.frame%len(frames)], label, u.cR, u.cDim, el, budget, u.cR)
}

// width is the terminal's width when the UI writes to one, else cols().
func (u *UI) width() int {
	if u.fd >= 0 {
		if w := ttyio.Width(u.fd); w >= 20 {
			return w
		}
	}
	return u.cols()
}
