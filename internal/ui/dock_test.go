package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// the dock as in Claude Code: the working line, a rule, the queued messages
// numbered, the input line with the cursor after what is typed, a rule, the
// footer — no line as wide as the terminal; a queued message leaves the
// dock as it goes out, its number on the "you (added)" label
func TestDockQueuesNumberedAboveTheInputLine(t *testing.T) {
	t.Setenv("COLUMNS", "60")
	var b strings.Builder
	u := newUI(&b, false, true)
	u.working, u.start = true, time.Now()
	u.SetDocked(true)
	u.SetFooter("⏵ approvals ask  ·  Enter queues it for the next round")
	u.Queue("look at the README too")
	u.Queue("and   the\tMakefile")
	u.SetInput(Input{Prompt: "you› ", Text: []rune("grüß dich"), Cursor: 4})

	lines, row, col := u.dock(u.width())
	rule := strings.Repeat("─", 59)
	want := []string{"", rule, "  1  look at the README too", "  2  and the Makefile", "you› grüß dich", rule,
		"  ⏵ approvals ask  ·  Enter queues it for the next round"}
	if len(lines) != len(want) {
		t.Fatalf("dock = %q", lines)
	}
	if !strings.Contains(lines[0], "working") || !strings.Contains(lines[0], "Ctrl+C to cancel") {
		t.Errorf("working line = %q", lines[0])
	}
	for i := 1; i < len(want); i++ {
		if lines[i] != want[i] {
			t.Errorf("dock line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if row != 4 || col != len([]rune("you› grüß")) {
		t.Errorf("cursor at row %d col %d, want row 4 col 9 — after \"grüß\"", row, col)
	}
	for _, l := range lines {
		if textWidth(l) >= 60 {
			t.Errorf("dock line wraps: %q", l)
		}
	}

	u.Added("look at the README too")
	if !strings.Contains(b.String(), "you (added · 1)\n   look at the README too\n") {
		t.Errorf("added label = %q", b.String())
	}
	if len(u.queue) != 1 || u.queue[0].n != 2 {
		t.Errorf("queue after one went out = %+v", u.queue)
	}
	b.Reset()
	u.SetInput(Input{Prompt: "you› ", Hint: "type to add a message to the run"})
	if !strings.Contains(b.String(), "you› type to add a message to the run") || u.up != 3 {
		t.Errorf("the hint, the cursor 3 rows under the dock's top: up %d, %q", u.up, b.String())
	}
	u.ClearQueue()
	u.working = false
	if lines, _, _ := u.dock(u.width()); len(lines) != 4 || u.queue != nil {
		t.Errorf("between turns: rule, input, rule, footer — %q", lines)
	}
	u.SetDocked(false)
	if lines, _, _ := u.dock(u.width()); len(lines) != 0 {
		t.Errorf("not docked, no turn: no dock — %q", lines)
	}
}

// not docked (--rpc, no terminal to type in) the dock is the working line
// alone: off while a prompt holds the terminal and after the turn, and a
// queued message is a line of its own
func TestDockOffWhenHeldAndAfterStop(t *testing.T) {
	var b lockedBuf
	u := newUI(&b, false, true)
	stop := u.StartWorking()
	resume := u.Hold()
	u.mu.Lock()
	live := u.live()
	u.mu.Unlock()
	if live {
		t.Error("a held dock must be off")
	}
	resume()
	stop()
	n := len(b.String())
	u.Note("after the turn")
	u.Queue("one more thing")
	got := b.String()[n:]
	if strings.Contains(got, "\x1b[") {
		t.Errorf("no dock after stop: %q", got)
	}
	if !strings.Contains(got, "queued 1 for the next round: one more thing") {
		t.Errorf("a queued message with the dock off = %q", got)
	}
}

// a question answered in the box: the dock stays, the working line says it
// waits, and the wait is left out of the turn's time
func TestAwaitKeepsTheDockAndPausesTheClock(t *testing.T) {
	var b lockedBuf
	u := newUI(&b, false, true)
	u.SetDocked(true)
	stop := u.StartWorking()
	defer stop()
	resume := u.Await()
	u.mu.Lock()
	live, line := u.live(), u.workingLine()
	u.mu.Unlock()
	if !live || !strings.Contains(line, "waiting for your answer") {
		t.Errorf("awaiting: live %v, %q", live, line)
	}
	time.Sleep(60 * time.Millisecond)
	resume()
	resume() // once
	if p := u.Paused(); p < 50*time.Millisecond || p > 500*time.Millisecond {
		t.Errorf("Paused() = %v, want the ~60ms wait", p)
	}
}

func TestFitCutsToTheWidth(t *testing.T) {
	if got := fit("short", 40); got != "short" {
		t.Errorf("fit kept %q", got)
	}
	if got := fit("\x1b[2mabcdefghij\x1b[0m", 8); got != "\x1b[2mabcdef…\x1b[0m" {
		t.Errorf("fit with colour = %q", got)
	}
	if got := fit("日本語のテキスト", 8); textWidth(got) > 7 {
		t.Errorf("wide runes: %q is %d columns", got, textWidth(got))
	}
	if w := textWidth("\x1b[1m\x1b[36myou›\x1b[0m e\u0301"); w != 6 {
		t.Errorf("textWidth = %d, want 6 (colour codes and the combining accent take no column)", w)
	}
}

// the input line scrolls: the cursor always in view, a cut marked, a
// newline shown as ↵
func TestInputViewKeepsTheCursorInView(t *testing.T) {
	if v, c := inputView([]rune("two\nlines"), 3, 20); v != "two↵lines" || c != 3 {
		t.Errorf("short: %q col %d", v, c)
	}
	long := []rune(strings.Repeat("abcdefghij", 10))
	for _, cur := range []int{0, 37, 50, 99, 100} {
		v, c := inputView(long, cur, 20)
		if w := textWidth(v); w > 20 {
			t.Errorf("cursor %d: %q is %d columns, room 20", cur, v, w)
		}
		if c < 0 || c > textWidth(v) {
			t.Errorf("cursor %d: column %d outside %q", cur, c, v)
		}
		if cur > 0 && cur < 100 && string([]rune(v)[c-1]) != string(long[cur-1]) {
			t.Errorf("cursor %d: column %d is not after %q in %q", cur, c, long[cur-1], v)
		}
	}
	if v, _ := inputView(long, 100, 20); strings.HasSuffix(v, "…") || !strings.HasPrefix(v, "…") {
		t.Errorf("at the end: cut only before — %q", v)
	}
}

// a turn on a terminal, played back: the conversation grows above the dock,
// the dock stays at the bottom with the cursor on its input line, the
// working line goes when the turn stops, and undocked nothing is left
func TestDockStaysUnderTheConversation(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	var b lockedBuf
	u := newUI(&b, false, true)
	u.SetDocked(true)
	u.SetFooter("⏵ approvals auto  ·  Enter queues it for the next round")
	hint := Input{Prompt: "you› ", Hint: "type to add a message to the run"}
	u.SetInput(hint)
	u.You("fix the tests")
	stop := u.StartWorking()
	u.Queue("look at the README too")
	u.CallStart(1, "shell", "ls", nil)
	u.SetInput(Input{Prompt: "you› ", Text: []rune("and the"), Cursor: 7})
	u.SetInput(hint)
	u.Queue("and the Makefile")
	u.Added("look at the README too")
	u.Note("a note")

	rule := strings.Repeat("─", 79)
	got, row, col := screen(b.String())
	want := []string{"you", "   fix the tests", " #1 shell  ls", "you (added · 1)", "   look at the README too", " a note",
		"working", rule, "  2  and the Makefile", "you› type to add a message to the run", rule,
		"  ⏵ approvals auto  ·  Enter queues it for the next round"}
	if len(got) != len(want) {
		t.Fatalf("screen:\n%s", strings.Join(got, "\n"))
	}
	for i, w := range want {
		if w == "working" && strings.Contains(got[i], "working") && strings.Contains(got[i], "Ctrl+C to cancel") {
			continue
		}
		if got[i] != w {
			t.Errorf("screen line %d = %q, want %q", i, got[i], w)
		}
	}
	if row != 9 || col != 5 {
		t.Errorf("cursor at %d:%d, want 9:5 — after \"you› \"", row, col)
	}

	stop()
	u.ClearQueue()
	got, _, _ = screen(b.String())
	if len(got) != 10 || got[6] != rule || !strings.HasPrefix(got[7], "you›") {
		t.Errorf("after the turn the dock alone under the conversation:\n%s", strings.Join(got, "\n"))
	}
	u.SetDocked(false)
	got, _, _ = screen(b.String())
	if len(got) != 6 || got[5] != " a note" {
		t.Errorf("undocked, the conversation alone:\n%s", strings.Join(got, "\n"))
	}
}

// docked, a streamed answer goes above the dock a whole line at a time; the
// line not complete yet stands in the dock
func TestDockedStreamKeepsTheDock(t *testing.T) {
	t.Setenv("COLUMNS", "60")
	var b lockedBuf
	u := newUI(&b, false, true)
	u.SetDocked(true)
	u.SetInput(Input{Prompt: "you› "})
	u.Delta("Hello wor")
	got, _, _ := screen(b.String())
	if len(got) != 6 || got[1] != "hermes" || got[2] != "   Hello wor" || !strings.HasPrefix(got[3], "──") {
		t.Fatalf("the open line in the dock:\n%s", strings.Join(got, "\n"))
	}
	u.Delta("ld\nsecond")
	if !u.StreamDone() || u.StreamDone() {
		t.Error("StreamDone: true once")
	}
	got, _, _ = screen(b.String())
	if len(got) != 7 || got[2] != "   Hello world" || got[3] != "   second" || !strings.HasPrefix(got[4], "──") {
		t.Errorf("the whole answer above the dock:\n%s", strings.Join(got, "\n"))
	}
}

// screen plays what the UI wrote on a terminal: text, CR, LF (as CR LF, the
// tty's ONLCR), and the escapes the dock uses — cursor up and right, clear
// to the end of the screen or the line; colours and cursor show/hide take no
// room. It returns the lines, trailing blanks cut, and the cursor.
func screen(out string) (lines []string, row, col int) {
	var rows [][]rune
	grow := func() {
		for len(rows) <= row {
			rows = append(rows, nil)
		}
	}
	for i := 0; i < len(out); {
		switch out[i] {
		case '\r':
			col = 0
			i++
			continue
		case '\n':
			row, col = row+1, 0
			i++
			continue
		case 0x1b:
			j := i + 2
			for j < len(out) && (out[j] < 0x40 || out[j] > 0x7e) {
				j++
			}
			param, n := out[i+2:j], 1
			if v, err := strconv.Atoi(param); err == nil {
				n = v
			}
			grow()
			switch out[j] {
			case 'A':
				row = max(row-n, 0)
			case 'C':
				col += n
			case 'J':
				rows = rows[:row+1]
				fallthrough
			case 'K':
				if col < len(rows[row]) {
					rows[row] = rows[row][:col]
				}
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(out[i:])
		grow()
		for len(rows[row]) < col {
			rows[row] = append(rows[row], ' ')
		}
		if col < len(rows[row]) {
			rows[row][col] = r
		} else {
			rows[row] = append(rows[row], r)
		}
		col++
		i += size
	}
	for _, r := range rows {
		lines = append(lines, strings.TrimRight(string(r), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, row, col
}
