package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

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

// inputView is the part of text that fits in room columns with the cursor
// in it — a newline shown as ↵, a cut marked … — and the cursor's column.
func inputView(text []rune, cursor, room int) (string, int) {
	if room < 8 {
		room = 8
	}
	shown := make([]rune, len(text))
	total := 0
	for i, r := range text {
		switch r {
		case '\n':
			r = '↵'
		case '\t':
			r = ' '
		}
		shown[i] = r
		total += runeWidth(r)
	}
	cursor = min(max(cursor, 0), len(shown))
	if total <= room {
		return string(shown), widthOf(shown[:cursor])
	}
	room -= 2 // the … on either side
	from, to, used := cursor, cursor, 0
	for from > 0 && used+runeWidth(shown[from-1]) <= room*2/3 { // what comes before the cursor first
		from--
		used += runeWidth(shown[from])
	}
	for to < len(shown) && used+runeWidth(shown[to]) <= room {
		used += runeWidth(shown[to])
		to++
	}
	for from > 0 && used+runeWidth(shown[from-1]) <= room { // the end is near: more before it
		from--
		used += runeWidth(shown[from])
	}
	view, col := string(shown[from:to]), widthOf(shown[from:cursor])
	if from > 0 {
		view, col = "…"+view, col+1
	}
	if to < len(shown) {
		view += "…"
	}
	return view, col
}

func widthOf(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runeWidth(r)
	}
	return n
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
