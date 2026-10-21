package repl

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"hermes-hands/internal/ttyio"
	"hermes-hands/internal/ui"
)

// box is the REPL's one input, from the start to /exit, as in Claude Code:
// the input line of the dock under the conversation (ui). It reads the
// terminal in character mode — no echo, no signals: Ctrl-C is a key here —
// and edits a line; Enter hands it to the REPL, during a turn too. A
// question — an approval — is answered in it (Ask) and waits while the
// operator types: until their line is sent or cleared and the keys rest.
type box struct {
	ui        *ui.UI
	fd        int
	restore   func()
	in        chan input            // what Enter sends; Ctrl-D on an empty line between turns (eof)
	interrupt func() bool           // Ctrl-C, no question up: cancel the running turn (false: none runs)
	busy      func() bool           // a turn is running
	complete  func(string) []string // the /commands a prefix can become
	stop      chan struct{}
	done      chan struct{}

	mu       sync.Mutex
	draft    editLine // what the operator types
	answer   editLine // the answer to the question up
	asks     []*ask   // questions waiting — for the draft to be sent or cleared
	asking   *ask     // the question up
	hist     []string // what was sent, oldest first
	histAt   int      // where ↑/↓ stand in hist; len(hist) = on the draft
	histKeep []rune   // the draft from before ↑
	lastKey  time.Time
	pasting  bool // inside a bracketed paste: Enter is a new line
	tabbed   bool // the last key was a Tab that had several commands
	held     bool // suspended: the terminal is someone else's
	parked   bool // the reader saw held and reads nothing
	closed   bool
}

// input is a line Enter sent, or the end (Ctrl-D, stdin closed).
type input struct {
	text string
	eof  bool
}

type ask struct {
	q     string
	keys  string // the answers it offers: the letters in its [y]es [n]o …
	note  string // why the last answer was none of them
	reply chan askReply
}

var choiceRe = regexp.MustCompile(`\[(\w)\]`)

func newAsk(q string) *ask {
	a := &ask{q: q, reply: make(chan askReply, 1)}
	for _, m := range choiceRe.FindAllStringSubmatch(q, -1) {
		a.keys += strings.ToLower(m[1])
	}
	return a
}

// takes reports whether line answers a: one of its letters, or nothing (no).
// Anything else — the start of a message typed on — is no answer.
func (a *ask) takes(line string) bool {
	line = strings.TrimSpace(line)
	return line == "" || a.fits(line)
}

// fits reports whether s is one of a's letters, as typed.
func (a *ask) fits(s string) bool {
	return a.keys == "" || utf8.RuneCountInString(s) == 1 && strings.Contains(a.keys, strings.ToLower(s))
}

type askReply struct {
	line string
	err  error
}

// restKeys is how long the keys must rest, the line empty, before a waiting
// question takes the input line: what the operator types is never an answer
// by accident.
const restKeys = 700 * time.Millisecond

var errAskInterrupted = errors.New("interrupted")

const (
	pasteOn  = "\x1b[?2004h" // bracketed paste: a pasted newline is no Enter
	pasteOff = "\x1b[?2004l"
)

// startBox takes the terminal for the box; nil when stdin or stderr is no
// terminal — the REPL then reads plain lines.
func startBox(u *ui.UI, interrupt, busy func() bool, complete func(string) []string) *box {
	if !ttyio.IsTerminal(os.Stdin) || !ttyio.IsTerminal(os.Stderr) {
		return nil
	}
	fd := int(os.Stdin.Fd())
	restore, err := ttyio.CharMode(fd)
	if err != nil {
		return nil
	}
	b := &box{ui: u, fd: fd, restore: restore, in: make(chan input, 16), interrupt: interrupt, busy: busy,
		complete: complete, stop: make(chan struct{}), done: make(chan struct{})}
	io.WriteString(os.Stderr, pasteOn)
	u.SetDocked(true)
	b.mu.Lock()
	b.histAt = 0
	b.render()
	b.mu.Unlock()
	go b.read()
	go b.resized()
	return b
}

// read polls the terminal and feeds what arrives; it reads nothing while the
// box is suspended. A lone Esc is dropped once no more bytes follow it.
func (b *box) read() {
	defer close(b.done)
	var rest []byte // a rune or an escape sequence not complete yet
	buf := make([]byte, 4096)
	for {
		select {
		case <-b.stop:
			return
		default:
		}
		b.mu.Lock()
		b.parked = b.held
		held := b.held
		b.mu.Unlock()
		if held {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if !ttyio.Readable(b.fd, 50) {
			b.mu.Lock()
			if len(rest) == 1 && rest[0] == 0x1b {
				rest = nil
			}
			b.offer()
			b.mu.Unlock()
			continue
		}
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			b.in <- input{eof: true}
			return
		}
		b.mu.Lock()
		var after []func()
		rest, after = b.feed(append(rest, buf[:n]...))
		b.mu.Unlock()
		for _, f := range after {
			f()
		}
	}
}

// resized draws the dock again when the terminal changes its size.
func (b *box) resized() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-b.stop:
			return
		case <-ch:
			b.mu.Lock()
			u := b.ui
			b.mu.Unlock()
			u.Repaint()
		}
	}
}

// Ask puts the question q to the operator in the box and returns their
// answer — an approval's y/n/a/q. It waits for the line being typed to be
// sent or cleared first. Ctrl-C answers with an error.
func (b *box) Ask(q string) (string, error) {
	a := newAsk(q)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return "", io.EOF
	}
	b.asks = append(b.asks, a)
	b.render()
	b.offer()
	b.mu.Unlock()
	r := <-a.reply
	return r.line, r.err
}

// offer puts the first waiting question up once the line is empty and the
// keys rest. Called with b.mu held.
func (b *box) offer() {
	if b.asking != nil || len(b.asks) == 0 || len(b.draft.r) > 0 || time.Since(b.lastKey) < restKeys {
		return
	}
	b.asking, b.asks = b.asks[0], b.asks[1:]
	b.render()
}

// answered takes the question up down with the operator's answer (err nil)
// or Ctrl-C; the answered prompt stays in the conversation, as a replay
// shows it. Called with b.mu held.
func (b *box) answered(line string, err error) {
	a := b.asking
	b.asking, b.answer = nil, editLine{}
	shown := line
	if err != nil {
		shown = "q"
	}
	b.ui.Print(a.q + shown + "\n")
	a.reply <- askReply{line: line, err: err}
	b.render()
}

// failAsks answers every waiting question with err — a turn interrupted
// while they waited for the draft, the box closing.
func (b *box) failAsks(err error) {
	for _, a := range b.asks {
		a.reply <- askReply{err: err}
	}
	b.asks = nil
	if b.asking != nil {
		b.answered("", err)
	}
}

// render puts the box's state on the dock's input line. Called with b.mu held.
func (b *box) render() {
	in := ui.Input{Prompt: "you› ", Hint: "type a message for Hermes  ·  /help for commands"}
	if b.busy() {
		in.Hint = "type to add a message to the run"
	}
	l := &b.draft
	switch {
	case b.asking != nil:
		l = &b.answer
		in.Prompt, in.Ask, in.Hint = "⚠ "+strings.TrimLeft(b.asking.q, " "), true, ""
		in.Footer = "⏵ Enter answers  ·  Ctrl+C quits the turn"
		in.Note = b.asking.note
	case len(b.asks) > 0:
		in.Note = "an approval waits — Enter sends your line, Ctrl+U clears it"
	}
	in.Text, in.Cursor = append([]rune(nil), l.r...), l.at
	b.ui.SetInput(in)
}

// redraw renders the box anew — the REPL's state changed (a turn started or
// ended).
func (b *box) redraw() {
	b.mu.Lock()
	b.render()
	b.mu.Unlock()
}

// prepend puts text in front of the line being typed — what a cancelled
// turn did not take, to send again or change.
func (b *box) prepend(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.draft.r) > 0 {
		text += " " + string(b.draft.r)
	}
	b.draft.set([]rune(text))
	b.render()
}

// use moves the box to another UI — /setup built a new app.
func (b *box) use(u *ui.UI) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ui = u
	u.SetDocked(true)
	b.render()
}

// suspend hands the terminal to someone else — the setup wizard: the reader
// parks, the old mode is back; resume takes the terminal again.
func (b *box) suspend() (resume func()) {
	b.mu.Lock()
	b.held = true
	b.mu.Unlock()
	for deadline := time.Now().Add(200 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		b.mu.Lock()
		parked := b.parked
		b.mu.Unlock()
		if parked {
			break
		}
	}
	io.WriteString(os.Stderr, pasteOff)
	b.restore()
	return func() {
		if restore, err := ttyio.CharMode(b.fd); err == nil {
			b.restore = restore
		}
		io.WriteString(os.Stderr, pasteOn)
		b.mu.Lock()
		b.held = false
		b.mu.Unlock()
	}
}

// release gives the terminal its old mode back at once — the process is
// going down.
func (b *box) release() {
	io.WriteString(os.Stderr, pasteOff+"\x1b[?25h\n")
	b.restore()
}

// close gives the terminal back: the reader stops, the old mode is back,
// the dock is gone; a question still waiting is answered with io.EOF.
func (b *box) close() {
	b.mu.Lock()
	b.closed = true
	b.failAsks(io.EOF)
	b.mu.Unlock()
	close(b.stop)
	<-b.done
	io.WriteString(os.Stderr, pasteOff)
	b.restore()
	b.ui.SetDocked(false)
}
