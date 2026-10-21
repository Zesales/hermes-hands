package repl

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The box's keys, as a shell's line editor has them: ←/→ (Ctrl-B/F) and
// Alt-←/→ (Alt-B/F) move, Home/End (Ctrl-A/E), Backspace, Delete (Ctrl-D on
// text), Ctrl-W and Alt-Backspace a word, Ctrl-U to the start, Ctrl-K to the
// end, ↑/↓ (Ctrl-P/N) the lines sent before, Tab completes a /command,
// Ctrl-L clears the screen. Enter sends; \ before it, Ctrl-J or Alt-Enter
// is a new line instead, and a paste keeps its newlines. Ctrl-C cancels the
// running turn — between turns it clears the line; Ctrl-D on an empty line
// between turns quits.

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyDelete
	keyWordLeft
	keyWordRight
	keyKillWord
	keyNewline
	keyPasteStart
	keyPasteEnd
)

// feed handles the complete keys in p and returns the bytes of one not
// complete yet. Called with b.mu held; what must happen outside the lock —
// a line sent, a turn interrupted — comes back as after.
func (b *box) feed(p []byte) (rest []byte, after []func()) {
	defer b.render()
	for len(p) > 0 {
		b.lastKey = time.Now()
		c := p[0]
		switch {
		case c == 0x1b:
			n, k := escKey(p)
			if n == 0 {
				return p, after
			}
			p = p[n:]
			b.key(k)
			continue
		case b.pasting && (c == '\r' || c == '\n'):
			if c == '\r' && len(p) > 1 && p[1] == '\n' {
				p = p[1:]
			}
			b.typeRune('\n')
		case b.pasting && c == '\t':
			b.typeRune('\t')
		case c < 0x20 || c == 0x7f:
			if !b.pasting {
				after = append(after, b.control(c)...)
			}
		default:
			if !utf8.FullRune(p) {
				return p, after
			}
			r, size := utf8.DecodeRune(p)
			b.typeRune(r)
			b.tabbed = false
			p = p[size:]
			continue
		}
		p = p[1:]
	}
	return nil, after
}

// typeRune puts r in the line being edited. A question up takes nothing but
// one of its letters: any other key — a message typed on as it came up —
// sends it back to wait, and what was typed goes to the operator's line.
func (b *box) typeRune(r rune) {
	if a := b.asking; a != nil {
		next := editLine{r: append([]rune(nil), b.answer.r...), at: b.answer.at}
		next.insert(r)
		if !b.pasting && a.fits(string(next.r)) {
			b.answer = next
			return
		}
		b.asking, b.asks = nil, append([]*ask{a}, b.asks...)
		b.draft, b.answer = next, editLine{}
		return
	}
	b.draft.insert(r)
}

// line is the line the keys edit: the answer when a question is up.
func (b *box) line() *editLine {
	if b.asking != nil {
		return &b.answer
	}
	return &b.draft
}

// key handles a key from an escape sequence.
func (b *box) key(k key) {
	l := b.line()
	switch k {
	case keyPasteStart:
		b.pasting = true
	case keyPasteEnd:
		b.pasting = false
	case keyUp:
		b.history(-1)
	case keyDown:
		b.history(1)
	case keyLeft:
		l.left()
	case keyRight:
		l.right()
	case keyHome:
		l.at = 0
	case keyEnd:
		l.at = len(l.r)
	case keyDelete:
		l.del()
	case keyWordLeft:
		l.wordLeft()
	case keyWordRight:
		l.wordRight()
	case keyKillWord:
		l.killWord()
	case keyNewline:
		b.typeRune('\n')
	}
	if k != keyNone {
		b.tabbed = false
	}
}

// control handles a control key.
func (b *box) control(c byte) (after []func()) {
	l := b.line()
	if c != '\t' {
		b.tabbed = false
	}
	switch c {
	case 0x01:
		l.at = 0
	case 0x02:
		l.left()
	case 0x03:
		return b.ctrlC()
	case 0x04:
		return b.ctrlD()
	case 0x05:
		l.at = len(l.r)
	case 0x06:
		l.right()
	case 0x08, 0x7f:
		l.backspace()
	case '\t':
		b.tab()
	case '\n': // Ctrl-J
		b.typeRune('\n')
	case 0x0b:
		l.r = l.r[:l.at]
	case 0x0c:
		b.ui.ClearScreen()
	case '\r':
		return b.enter()
	case 0x0e:
		b.history(1)
	case 0x10:
		b.history(-1)
	case 0x15:
		l.r, l.at = append([]rune(nil), l.r[l.at:]...), 0
	case 0x17:
		l.killWord()
	}
	return nil
}

// enter answers the question up, or sends the line — unless it ends in \:
// that is a new line.
func (b *box) enter() []func() {
	if a := b.asking; a != nil {
		if line := string(b.answer.r); !a.takes(line) {
			a.note = "“" + clipLine(line, 30) + "” is no answer — " + strings.Join(strings.Split(a.keys, ""), " / ") + ", or Enter alone for no"
			b.answer = editLine{}
			return nil
		}
		b.answered(string(b.answer.r), nil)
		b.offer()
		return nil
	}
	d := &b.draft
	if d.at == len(d.r) && d.at > 0 && d.r[d.at-1] == '\\' {
		d.r[d.at-1] = '\n'
		return nil
	}
	text := string(d.r)
	b.draft, b.histKeep = editLine{}, nil
	if strings.TrimSpace(text) == "" {
		b.histAt = len(b.hist)
		return nil
	}
	if len(b.hist) == 0 || b.hist[len(b.hist)-1] != text {
		b.hist = append(b.hist, text)
	}
	b.histAt = len(b.hist)
	return []func(){func() { b.send(input{text: text}) }}
}

// send hands a line to the REPL — or nothing once the box is closing.
func (b *box) send(in input) {
	select {
	case b.in <- in:
	case <-b.stop:
	}
}

// ctrlC quits the question up (a quit turn), else cancels the running turn
// — the questions still waiting with it — else clears the line.
func (b *box) ctrlC() []func() {
	switch {
	case b.asking != nil:
		b.answered("", errAskInterrupted)
	case b.busy():
		b.failAsks(errAskInterrupted)
		return []func(){func() { b.interrupt() }}
	case len(b.draft.r) > 0:
		b.draft = editLine{}
		b.histAt = len(b.hist)
	default:
		b.ui.Note("(Ctrl+D on an empty line, or /exit, quits)")
	}
	return nil
}

// ctrlD deletes the character under the cursor; on an empty line between
// turns, with no question up, it quits.
func (b *box) ctrlD() []func() {
	l := b.line()
	if len(l.r) > 0 {
		l.del()
		return nil
	}
	if b.asking != nil || b.busy() {
		return nil
	}
	return []func(){func() { b.send(input{eof: true}) }}
}

// history steps through the lines sent before (-1 back, 1 forward); past
// the newest is the draft from before.
func (b *box) history(step int) {
	if b.asking != nil {
		return
	}
	at := b.histAt + step
	if at < 0 || at > len(b.hist) {
		return
	}
	if b.histAt == len(b.hist) {
		b.histKeep = append([]rune(nil), b.draft.r...)
	}
	b.histAt = at
	if at == len(b.hist) {
		b.draft.set(b.histKeep)
		return
	}
	b.draft.set([]rune(b.hist[at]))
}

// tab completes the /command being typed: the one it can become, else what
// they all share; a second Tab lists them.
func (b *box) tab() {
	d := &b.draft
	s := string(d.r)
	if b.asking != nil || !strings.HasPrefix(s, "/") || strings.ContainsAny(s, " \n") || d.at != len(d.r) {
		return
	}
	names := b.complete(s)
	switch {
	case len(names) == 1:
		d.set([]rune(names[0] + " "))
	case len(names) > 1:
		if common := sharedPrefix(names); len(common) > len(s) {
			d.set([]rune(common))
		} else if b.tabbed {
			b.ui.Note(strings.Join(names, "  "))
		}
		b.tabbed = true
	}
}

func sharedPrefix(names []string) string {
	sort.Strings(names)
	first, last := names[0], names[len(names)-1]
	i := 0
	for i < len(first) && i < len(last) && first[i] == last[i] {
		i++
	}
	return first[:i]
}
