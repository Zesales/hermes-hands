package repl

import "strings"

// escKey reads the escape sequence p starts with: its length (0 = not
// complete yet) and the key it is (keyNone: one the box does not use).
func escKey(p []byte) (int, key) {
	if len(p) < 2 {
		return 0, keyNone
	}
	switch p[1] {
	case '[':
		for i := 2; i < len(p); i++ {
			if p[i] >= 0x40 && p[i] <= 0x7e {
				return i + 1, csiKey(string(p[2:i]), p[i])
			}
		}
		return 0, keyNone
	case 'O':
		if len(p) < 3 {
			return 0, keyNone
		}
		return 3, csiKey("", p[2])
	case 0x7f, 0x08:
		return 2, keyKillWord
	case '\r':
		return 2, keyNewline
	case 'b':
		return 2, keyWordLeft
	case 'f':
		return 2, keyWordRight
	case 0x1b:
		return 1, keyNone // Esc, then another sequence
	}
	return 2, keyNone // another Alt+key
}

// csiKey maps a CSI (or SS3) sequence's parameters and final byte to a key.
func csiKey(param string, final byte) key {
	word := strings.HasSuffix(param, ";5") || strings.HasSuffix(param, ";3") // Ctrl- or Alt-
	switch final {
	case 'A':
		return keyUp
	case 'B':
		return keyDown
	case 'C':
		if word {
			return keyWordRight
		}
		return keyRight
	case 'D':
		if word {
			return keyWordLeft
		}
		return keyLeft
	case 'H':
		return keyHome
	case 'F':
		return keyEnd
	case '~':
		switch param {
		case "1", "7":
			return keyHome
		case "4", "8":
			return keyEnd
		case "3":
			return keyDelete
		case "200":
			return keyPasteStart
		case "201":
			return keyPasteEnd
		}
	}
	return keyNone
}

// editLine is a line being edited: its runes and the cursor in them.
type editLine struct {
	r  []rune
	at int
}

func (l *editLine) set(r []rune) { l.r, l.at = append([]rune(nil), r...), len(r) }

func (l *editLine) insert(r rune) {
	l.r = append(l.r, 0)
	copy(l.r[l.at+1:], l.r[l.at:])
	l.r[l.at] = r
	l.at++
}

func (l *editLine) backspace() {
	if l.at > 0 {
		l.r = append(l.r[:l.at-1], l.r[l.at:]...)
		l.at--
	}
}

func (l *editLine) del() {
	if l.at < len(l.r) {
		l.r = append(l.r[:l.at], l.r[l.at+1:]...)
	}
}

func (l *editLine) left() {
	if l.at > 0 {
		l.at--
	}
}

func (l *editLine) right() {
	if l.at < len(l.r) {
		l.at++
	}
}

func space(r rune) bool { return r == ' ' || r == '\n' || r == '\t' }

func (l *editLine) wordLeft() {
	for l.at > 0 && space(l.r[l.at-1]) {
		l.at--
	}
	for l.at > 0 && !space(l.r[l.at-1]) {
		l.at--
	}
}

func (l *editLine) wordRight() {
	for l.at < len(l.r) && space(l.r[l.at]) {
		l.at++
	}
	for l.at < len(l.r) && !space(l.r[l.at]) {
		l.at++
	}
}

// killWord deletes the word before the cursor, and the spaces after it.
func (l *editLine) killWord() {
	end := l.at
	l.wordLeft()
	l.r = append(l.r[:l.at], l.r[end:]...)
}

// clipLine shortens s to n runes, on one line, marking the cut.
func clipLine(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
