package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"hermes-hands/internal/ttyio"
)

// The dock is the live block under the conversation while a turn runs, as in
// Claude Code: the working line, and — when the operator can type during the
// turn (TypeAhead) — a rule against the conversation above, the messages they
// queued, numbered, the input line, a closing rule and a footer with the
// turn's approval mode (SetApprovals):
//
//	   ...working  ·  12s  ·  round 2/25 · calls 3/100  ·  Ctrl+C to cancel
//	──────────────────────────────────────────────────────────────────────
//	  1  look at the README too
//	  2  and the Makefile
//	you› type to add a message to the run
//	──────────────────────────────────────────────────────────────────────
//	  ⏵ approvals auto  ·  Enter queues it for the next round
//
// Everything else is printed above it: sync wipes the dock, writes, and
// draws it again. Every dock line is cut to the terminal's width — a line
// that wrapped would throw the wipe off by a row.

// maxQueued is how many queued messages the dock lists; more are counted.
const maxQueued = 5

// queuedMsg is a message the operator queued during the turn, with its
// number in the turn.
type queuedMsg struct {
	n    int
	text string
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

// live reports whether the dock is on screen: a turn is working and no
// prompt holds the terminal.
func (u *UI) live() bool { return u.working && u.held == 0 }

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
	lines = append(lines, fit(u.workingLine(), w))
	if !u.typeAhead {
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
	const prompt = "you› "
	col = textWidth(prompt)
	in := u.cB + u.cYou + prompt + u.cR
	if u.typing == "" {
		in += u.cDim + "type to add a message to the run" + u.cR
	} else {
		typed := tailFit(u.typing, w-1-col-1)
		in += typed
		col += textWidth(typed)
	}
	row = len(lines)
	lines = append(lines, fit(in, w), rule)
	if u.approvals != "" {
		lines = append(lines, fit(fmt.Sprintf("%s  ⏵ approvals %s%s%s%s  ·  Enter queues it for the next round%s",
			u.cDim, u.cR, u.cAcc, u.approvals, u.cR+u.cDim, u.cR), w))
	}
	return lines, row, col
}

// workingLine is the dock's first line: what the turn waits on, how long,
// where it stands against its budget.
func (u *UI) workingLine() string {
	el := int((time.Since(u.start) - u.paused).Seconds())
	label := "working"
	if u.activity != "" {
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

// StartWorking puts the dock up until the returned stop is called
// (idempotent, safe from any goroutine). Non-tty: the old static line.
func (u *UI) StartWorking() (stop func()) {
	if !u.tty {
		u.Working() // the plain static line; no goroutine, no repaint
		return func() {}
	}
	u.mu.Lock()
	u.working, u.start, u.frame = true, time.Now(), 0
	u.paused = 0 // fresh turn: no approval waits counted against it yet
	if u.live() {
		io.WriteString(u.w, "\r"+u.draw())
	}
	u.mu.Unlock()
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(220 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				u.mu.Lock()
				u.frame++
				if u.live() {
					u.repaint()
				}
				u.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			u.mu.Lock()
			if u.live() {
				io.WriteString(u.w, u.wipe()+"\x1b[?25h")
			}
			u.working = false
			u.mu.Unlock()
		})
	}
}

// Hold takes the dock off until the returned resume runs. Wrap it around a
// foreground prompt that owns the terminal — e.g. the approval y/n/a/q gate —
// so the dock can't overwrite it. The held interval is excluded from the
// elapsed counter (both the live one and the turn's final "worked for Xs",
// via Paused()): waiting on the operator's answer is not hermes-agent
// taking time to work.
func (u *UI) Hold() (resume func()) {
	u.mu.Lock()
	if u.live() {
		io.WriteString(u.w, u.wipe()+"\x1b[?25h")
	}
	u.held++
	heldSince := time.Now()
	u.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			u.paused += time.Since(heldSince)
			u.held--
			if u.live() {
				io.WriteString(u.w, "\r"+u.draw())
			}
			u.mu.Unlock()
		})
	}
}

// Paused reports how long the current turn has spent held (approval prompts)
// so far. Subtract it from a wall-clock turn duration to get actual working
// time, e.g. before TurnDone.
func (u *UI) Paused() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.paused
}

// TypeAhead says whether the operator can type during the turn (repl): the
// dock then has its input line, and lists what they queue. Either way the
// queue starts empty.
func (u *UI) TypeAhead(on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.typeAhead, u.typing, u.queue, u.nQueued = on, "", nil, 0
	if u.live() {
		u.repaint()
	}
}

// SetApprovals names the approval mode the dock's footer shows ("" = no
// footer); the REPL sets it as a turn starts — /approve switches it between
// turns.
func (u *UI) SetApprovals(mode string) {
	u.mu.Lock()
	u.approvals = mode
	u.mu.Unlock()
}

// SetTyping puts what the operator is typing during a turn on the dock's
// input line ("" brings the hint back); Enter queues it (Queue).
func (u *UI) SetTyping(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.typing = s
	if u.live() {
		u.repaint()
	}
}

// Queue lists a message the operator queued during the turn in the dock,
// numbered; Added takes it off as it goes out with the next round. With the
// dock off it is a line in the conversation instead.
func (u *UI) Queue(text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.nQueued++
	u.queue = append(u.queue, queuedMsg{u.nQueued, text})
	if u.live() {
		u.repaint()
		return
	}
	fmt.Fprintf(u.w, " %squeued %d for the next round: %s%s\n", u.cDim, u.nQueued, clipWidth(strings.Join(strings.Fields(text), " "), 100), u.cR)
}

// Print writes s as it is, above the dock — hermes-hands' own warnings that
// come up while a turn runs.
func (u *UI) Print(s string) { u.sync(func() { io.WriteString(u.w, s) }) }

// fit cuts s — colour codes and all — to fewer than w columns.
func fit(s string, w int) string {
	limit := w - 1
	if textWidth(s) <= limit {
		return s
	}
	var b strings.Builder
	cols := 0
	for i := 0; i < len(s); {
		if n := colourLen(s[i:]); n > 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if cols+runeWidth(r) > limit-1 {
			break
		}
		cols += runeWidth(r)
		b.WriteString(s[i : i+size])
		i += size
	}
	b.WriteString("…")
	if strings.Contains(s, "\x1b") {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// tailFit keeps the end of s that fits in w columns — the end of a line
// being typed is where the cursor is.
func tailFit(s string, w int) string {
	if w < 8 {
		w = 8
	}
	if textWidth(s) <= w {
		return s
	}
	r := []rune(s)
	cols, i := 1, len(r) // 1: the "…"
	for i > 0 && cols+runeWidth(r[i-1]) <= w {
		i--
		cols += runeWidth(r[i])
	}
	return "…" + string(r[i:])
}

// textWidth is the columns s takes on screen, colour codes left out.
func textWidth(s string) int {
	cols := 0
	for i := 0; i < len(s); {
		if n := colourLen(s[i:]); n > 0 {
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		cols += runeWidth(r)
		i += size
	}
	return cols
}

// colourLen is the length of the escape sequence s starts with, 0 for none.
func colourLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return len(s)
}

// runeWidth is the columns r takes: 0 for a combining mark, 2 in the wide
// East Asian and emoji ranges, else 1 — near enough to keep a line from
// wrapping.
func runeWidth(r rune) int {
	switch {
	case unicode.Is(unicode.Mn, r), r == 0x200d, r >= 0xfe00 && r <= 0xfe0f:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}
