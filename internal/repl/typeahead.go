package repl

import (
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"hermes-hands/internal/loop"
	"hermes-hands/internal/ttyio"
	"hermes-hands/internal/ui"
)

// typeahead reads what the operator types while a turn runs, as in Claude
// Code: it stands on the working line, Enter queues it for the turn's next
// round (loop.Inbox), and what is still being typed when the turn ends —
// with any /command typed meanwhile — waits for the prompt. An approval
// prompt takes the terminal over (hold); Ctrl-C still cancels the turn.
type typeahead struct {
	ui      *ui.UI
	inbox   *loop.Inbox
	fd      int
	restore func()
	stop    chan struct{}
	done    chan struct{}

	mu     sync.Mutex
	buf    []rune
	held   bool // an approval prompt owns the terminal
	parked bool // the reader saw held and reads nothing
	cmds   []string
}

// startTypeahead begins reading the terminal for the turn about to run; nil
// when stdin is no terminal (every method is then a no-op).
func startTypeahead(u *ui.UI, inbox *loop.Inbox) *typeahead {
	if inbox == nil || !ttyio.IsTerminal(os.Stdin) {
		return nil
	}
	fd := int(os.Stdin.Fd())
	restore, err := ttyio.CharMode(fd)
	if err != nil {
		return nil
	}
	t := &typeahead{ui: u, inbox: inbox, fd: fd, restore: restore, stop: make(chan struct{}), done: make(chan struct{})}
	u.TypeAhead(true)
	go t.read()
	return t
}

// read polls the terminal and feeds what arrives; it reads nothing while an
// approval prompt holds the terminal.
func (t *typeahead) read() {
	defer close(t.done)
	var rest []byte // a rune or an escape sequence not complete yet
	b := make([]byte, 256)
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		t.mu.Lock()
		t.parked = t.held
		held := t.held
		t.mu.Unlock()
		if held {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if !ttyio.Readable(t.fd, 50) {
			continue
		}
		n, err := os.Stdin.Read(b)
		if err != nil || n == 0 {
			return
		}
		rest = t.feed(append(rest, b[:n]...))
	}
}

// feed handles complete keys and returns the bytes of an incomplete one.
func (t *typeahead) feed(b []byte) []byte {
	for len(b) > 0 {
		c := b[0]
		switch {
		case c == 0x1b: // arrows and the like: skipped whole
			n := escLen(b)
			if n == 0 {
				return b
			}
			b = b[n:]
			continue
		case c == '\r' || c == '\n':
			t.enter()
		case c == 0x7f || c == 0x08: // backspace
			t.edit(func(r []rune) []rune {
				if len(r) > 0 {
					return r[:len(r)-1]
				}
				return r
			})
		case c == 0x15: // Ctrl-U: the whole line
			t.edit(func([]rune) []rune { return nil })
		case c == 0x17: // Ctrl-W: the last word
			t.edit(func(r []rune) []rune {
				s := strings.TrimRight(string(r), " ")
				if i := strings.LastIndexByte(s, ' '); i >= 0 {
					return []rune(s[:i+1])
				}
				return nil
			})
		case c == '\t':
			t.edit(func(r []rune) []rune { return append(r, ' ') })
		case c < 0x20: // other control keys: nothing to type
		default:
			if !utf8.FullRune(b) {
				return b
			}
			r, size := utf8.DecodeRune(b)
			t.edit(func(rs []rune) []rune { return append(rs, r) })
			b = b[size:]
			continue
		}
		b = b[1:]
	}
	return nil
}

// escLen is the length of the escape sequence b starts with, 0 when it is
// not complete yet.
func escLen(b []byte) int {
	switch {
	case len(b) == 1:
		return 1 // a lone Esc
	case b[1] == '[':
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		return 0
	case b[1] == 'O':
		if len(b) < 3 {
			return 0
		}
		return 3
	}
	return 2 // Alt+key
}

func (t *typeahead) edit(f func([]rune) []rune) {
	t.mu.Lock()
	t.buf = f(t.buf)
	s := string(t.buf)
	t.mu.Unlock()
	t.ui.SetTyping(s)
}

// enter queues the line for the next round; a /command waits for the
// prompt — commands run between turns.
func (t *typeahead) enter() {
	t.mu.Lock()
	line := strings.TrimSpace(string(t.buf))
	t.buf = nil
	t.mu.Unlock()
	t.ui.SetTyping("")
	switch {
	case line == "":
	case strings.HasPrefix(line, "/"):
		t.mu.Lock()
		t.cmds = append(t.cmds, line)
		t.mu.Unlock()
		t.ui.Note(line + " runs when the turn is done — commands run between turns")
	default:
		t.inbox.Push(line)
		t.ui.Note("queued for the next round: " + clipLine(line, 100))
	}
}

// hold hands the terminal to an approval prompt: the reader parks, the old
// mode is back for the prompt; resume takes the terminal again.
func (t *typeahead) hold() (resume func()) {
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	t.held = true
	t.mu.Unlock()
	for deadline := time.Now().Add(200 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		t.mu.Lock()
		parked := t.parked
		t.mu.Unlock()
		if parked {
			break
		}
	}
	t.restore()
	return func() {
		if restore, err := ttyio.CharMode(t.fd); err == nil {
			t.restore = restore
		}
		t.mu.Lock()
		t.held = false
		t.mu.Unlock()
	}
}

// finish stops reading and gives the terminal back: what was being typed
// and the /commands typed meanwhile, for the prompt.
func (t *typeahead) finish() (typed string, cmds []string) {
	if t == nil {
		return "", nil
	}
	close(t.stop)
	<-t.done
	t.restore()
	t.ui.TypeAhead(false)
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf)), t.cmds
}

// clipLine shortens s to n runes, marking the cut.
func clipLine(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
