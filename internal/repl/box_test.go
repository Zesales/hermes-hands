package repl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermes-hands/internal/ui"
)

// testBox is a box without a terminal: keys go straight to feed.
type testBox struct {
	*box
	t         *testing.T
	turn      bool // a turn runs
	cancelled int
}

func newTestBox(t *testing.T) *testBox {
	f, err := os.Create(filepath.Join(t.TempDir(), "ui"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	tb := &testBox{t: t}
	tb.box = &box{ui: ui.New(f), in: make(chan input, 16), complete: completeCommand, stop: make(chan struct{}),
		busy:      func() bool { return tb.turn },
		interrupt: func() bool { tb.cancelled++; return tb.turn }}
	return tb
}

// keys feeds s as typed; nothing may be left over.
func (tb *testBox) keys(s string) {
	tb.t.Helper()
	tb.mu.Lock()
	rest, after := tb.feed([]byte(s))
	tb.mu.Unlock()
	for _, f := range after {
		f()
	}
	if len(rest) != 0 {
		tb.t.Fatalf("keys %q left %q", s, rest)
	}
}

func (tb *testBox) draftIs(want string) {
	tb.t.Helper()
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if got := string(tb.draft.r); got != want {
		tb.t.Errorf("draft = %q, want %q", got, want)
	}
}

func (tb *testBox) sent() input {
	tb.t.Helper()
	select {
	case in := <-tb.in:
		return in
	default:
		tb.t.Fatal("nothing sent")
		return input{}
	}
}

// the line editor's keys: moving, deleting, words, the ends — and Enter sends
func TestBoxEditsLikeAShell(t *testing.T) {
	tb := newTestBox(t)
	tb.keys("also chekc\x7f\x7fck the tests")
	tb.draftIs("also check the tests")
	tb.keys("\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x7f") // ←×5, Backspace: the space before "tests"
	tb.draftIs("also check thetests")
	tb.keys(" \x01X\x05Y") // the space again, Home, End
	tb.draftIs("Xalso check the testsY")
	tb.keys("\x1bb\x17") // Alt-B, Ctrl-W: "the " goes
	tb.draftIs("Xalso check testsY")
	tb.keys("\x1b[1;5D\x1b[3~") // Ctrl-←, Delete
	tb.draftIs("Xalso heck testsY")
	tb.keys("\x0b") // Ctrl-K: to the end
	tb.draftIs("Xalso ")
	tb.keys("grüß")
	tb.mu.Lock()
	rest, _ := tb.feed([]byte("\xc3")) // the start of an ü
	tb.mu.Unlock()
	if len(rest) != 1 {
		t.Fatalf("an incomplete rune must wait: rest = %q", rest)
	}
	tb.keys("\xc3\xbc\r")
	if in := tb.sent(); in.text != "Xalso grüßü" {
		t.Errorf("sent %q", in.text)
	}
	tb.draftIs("")
	if n, _ := escKey([]byte("\x1b[1;5")); n != 0 {
		t.Error("an escape sequence not complete yet must wait")
	}
}

// ↑/↓ walk the lines sent before and come back to the draft
func TestBoxHistory(t *testing.T) {
	tb := newTestBox(t)
	tb.keys("first\rsecond\r")
	tb.sent()
	tb.sent()
	tb.keys("draft\x1b[A")
	tb.draftIs("second")
	tb.keys("\x10\x10") // Ctrl-P twice: there is no line before the first
	tb.draftIs("first")
	tb.keys("\x1b[B\x0e")
	tb.draftIs("draft")
}

// a paste keeps its newlines; \ before Enter, Ctrl-J and Alt-Enter are new
// lines too
func TestBoxPasteAndNewlines(t *testing.T) {
	tb := newTestBox(t)
	tb.keys("\x1b[200~line one\r\nline two\x1b[201~")
	tb.draftIs("line one\nline two")
	tb.keys("\\\r three\x0afour\x1b\rfive\r")
	if in := tb.sent(); in.text != "line one\nline two\n three\nfour\nfive" {
		t.Errorf("sent %q", in.text)
	}
}

// Tab completes a /command, else what the commands share; a second Tab
// lists them
func TestBoxTabCompletesCommands(t *testing.T) {
	tb := newTestBox(t)
	tb.keys("/appr\t")
	tb.draftIs("/approve ")
	tb.keys("\x15/compa\t") // /compact, /compact-aggressive
	tb.draftIs("/compact")
	tb.keys("\t\t")
	tb.draftIs("/compact")
	tb.keys("\x15/comp\t") // /compress too: nothing more they share
	tb.draftIs("/comp")
	tb.keys("\x15hello /ap\t")
	tb.draftIs("hello /ap")
}

// an approval waits while the operator types: until the line is sent or
// cleared and the keys rest; then its answer is typed in the box. Ctrl-C
// quits it.
func TestBoxQuestionWaitsForTheTyping(t *testing.T) {
	tb := newTestBox(t)
	tb.turn = true
	tb.keys("half a thought")
	type reply struct {
		line string
		err  error
	}
	ask := func() chan reply {
		ch := make(chan reply, 1)
		go func() { l, err := tb.Ask("  [y]es  [n]o  [a]ll  [q]uit turn > "); ch <- reply{l, err} }()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			tb.mu.Lock()
			n := len(tb.asks) + map[bool]int{true: 1}[tb.asking != nil]
			tb.mu.Unlock()
			if n > 0 {
				break
			}
		}
		return ch
	}
	rest := func() { // the keys rested long enough
		tb.mu.Lock()
		tb.lastKey = time.Now().Add(-time.Second)
		tb.offer()
		tb.mu.Unlock()
	}
	ch := ask()
	rest()
	tb.mu.Lock()
	up := tb.asking != nil
	tb.mu.Unlock()
	if up {
		t.Fatal("a question must wait while a line is being typed")
	}
	tb.keys("\x15")
	rest()
	tb.keys("/approve\r") // typed on as the question came up: it steps back, the line is theirs
	if in := tb.sent(); in.text != "/approve" {
		t.Errorf("the line typed on: %q", in.text)
	}
	tb.mu.Lock()
	up, waiting := tb.asking != nil, len(tb.asks)
	tb.mu.Unlock()
	if up || waiting != 1 {
		t.Fatalf("the question must wait again: up %v, waiting %d", up, waiting)
	}
	rest()
	tb.keys("ye") // a second letter is no answer either
	tb.draftIs("ye")
	tb.keys("\x15")
	rest()
	tb.keys("y\r")
	if r := <-ch; r.line != "y" || r.err != nil {
		t.Errorf("answer %q, %v", r.line, r.err)
	}
	tb.draftIs("")

	ch = ask()
	rest()
	tb.keys("\x03")
	if r := <-ch; r.err != errAskInterrupted {
		t.Errorf("Ctrl-C at the question: %v", r.err)
	}
	if tb.cancelled != 0 {
		t.Error("Ctrl-C at a question quits it, not the turn")
	}

	tb.keys("typing")
	ch = ask()
	tb.keys("\x03") // still waiting for the line: the turn goes, the question with it
	if r := <-ch; r.err != errAskInterrupted || tb.cancelled != 1 {
		t.Errorf("Ctrl-C during the turn: %v, cancelled %d", r.err, tb.cancelled)
	}
}

// between turns Ctrl-C clears the line, never quits; Ctrl-D on an empty line
// quits — not during a turn
func TestBoxCtrlCAndCtrlD(t *testing.T) {
	tb := newTestBox(t)
	tb.keys("some text\x03")
	tb.draftIs("")
	if tb.cancelled != 0 {
		t.Error("no turn to cancel")
	}
	tb.turn = true
	tb.keys("\x04")
	select {
	case <-tb.in:
		t.Error("Ctrl-D during a turn must not quit")
	default:
	}
	tb.turn = false
	tb.keys("ab\x02\x04")
	tb.draftIs("a")
	tb.keys("\x15\x04")
	if in := tb.sent(); !in.eof {
		t.Errorf("Ctrl-D on an empty line: %+v", in)
	}
}
