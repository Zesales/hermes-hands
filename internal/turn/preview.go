package turn

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// preview turns the raw SSE token stream (which carries the JSON
// envelope `{"calls":...,"final":"..."}`) into a live preview of just the
// `final` answer text. On a tool-call round (non-empty "calls") it emits
// nothing — the tool lines speak for it.
type preview struct {
	emit    func(string) // e.g. ui.Delta
	raw     strings.Builder
	started bool // found the opening quote of the final value
	done    bool // hit its closing quote, or it's a calls-round
	pos     int  // index in raw where unparsed value bytes begin
}

func (f *preview) reset() { *f = preview{emit: f.emit} }

func (f *preview) feed(chunk string) {
	if f.done {
		return
	}
	f.raw.WriteString(chunk)
	s := f.raw.String()

	if !f.started {
		// a real tool-call round -> never a final to stream
		if i := strings.Index(s, `"calls"`); i >= 0 {
			after := strings.TrimLeft(s[i+7:], " :\t\r\n")
			if strings.HasPrefix(after, "[") {
				rest := strings.TrimLeft(after[1:], " \t\r\n")
				if rest == "" {
					return // the chunk ended at "[" — can't tell an empty array yet
				}
				if !strings.HasPrefix(rest, "]") {
					f.done = true
					return
				}
			}
		}
		k := strings.Index(s, `"final"`)
		if k < 0 {
			return
		}
		v := strings.TrimLeft(s[k+7:], " \t\r\n")
		if v == "" {
			return
		}
		if v[0] != ':' {
			f.done = true // not a key after all — nothing safe to preview
			return
		}
		v = strings.TrimLeft(v[1:], " \t\r\n")
		if v == "" {
			return
		}
		if v[0] != '"' { // "final": null — a calls-round written final-first
			f.done = true
			return
		}
		f.started = true
		f.pos = len(s) - len(v) + 1
	}

	// walk from f.pos, emitting decoded chars until an unescaped closing quote.
	// An escape cut off by the chunk boundary (`\` or `\u00` at the very end)
	// waits for the next chunk — emitting its `\` early used to print `\n`
	// literally and could take a later `\"` for the closing quote.
	var out strings.Builder
	i := f.pos
	for i < len(s) {
		c := s[i]
		if c == '\\' {
			r, n := jsonEscape(s[i:])
			if n == 0 {
				break // incomplete escape: wait for more
			}
			out.WriteString(r)
			i += n
			continue
		}
		if c == '"' { // end of the final string
			f.done = true
			i++
			break
		}
		out.WriteByte(c)
		i++
	}
	f.pos = i
	if out.Len() > 0 && f.emit != nil {
		f.emit(out.String())
	}
}

// jsonEscape decodes the JSON string escape at the start of s (s[0] == '\\')
// and returns its text and length — n == 0 when s ends before the escape does.
// \uXXXX handles UTF-16 surrogate pairs; a malformed escape yields its char.
func jsonEscape(s string) (string, int) {
	if len(s) < 2 {
		return "", 0
	}
	switch s[1] {
	case 'n':
		return "\n", 2
	case 't':
		return "\t", 2
	case 'r':
		return "\r", 2
	case 'b':
		return "\b", 2
	case 'f':
		return "\f", 2
	case 'u':
		if len(s) < 6 {
			return "", 0
		}
		r1, err := strconv.ParseUint(s[2:6], 16, 32)
		if err != nil {
			return "u", 2
		}
		if utf16.IsSurrogate(rune(r1)) {
			if len(s) < 12 {
				return "", 0
			}
			if s[6] == '\\' && s[7] == 'u' {
				if r2, err := strconv.ParseUint(s[8:12], 16, 32); err == nil {
					if r := utf16.DecodeRune(rune(r1), rune(r2)); r != utf8.RuneError {
						return string(r), 12
					}
				}
			}
			return string(utf8.RuneError), 6
		}
		return string(rune(r1)), 6
	default: // \" \\ \/ and anything malformed
		return s[1:2], 2
	}
}
