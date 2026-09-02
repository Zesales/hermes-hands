package config

import (
	"strconv"
	"strings"
)

// parseAssignments reads a deliberately narrowed subset of a shell file
// (was: `set -a; . "$file"; set +a`). Blank lines and `# comments` are skipped;
// `KEY=value` and `export KEY=value` are honoured, with the value optionally
// wrapped in "..." , '...' or bash $'...' (ANSI-C) quoting, or left bare with
// `printf %q`-style `\<char>` escaping. Any other line is ignored — bash would
// execute it; we do not. Later assignments win.
func parseAssignments(data []byte) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimLeft(rest, " \t")
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || !isIdent(line[:eq]) {
			continue
		}
		if v, ok := parseValue(line[eq+1:]); ok {
			out[line[:eq]] = v
		}
	}
	return out
}

func isIdent(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return s != ""
}

func parseValue(v string) (string, bool) {
	switch {
	case strings.HasPrefix(v, "$'"):
		return scanANSIC(v[2:])
	case strings.HasPrefix(v, "'"):
		if i := strings.IndexByte(v[1:], '\''); i >= 0 {
			return v[1 : 1+i], true
		}
		return "", false // unterminated
	case strings.HasPrefix(v, `"`):
		return scanDQ(v[1:])
	default:
		return scanBare(v), true
	}
}

// scanBare stops at the first unescaped space/tab (bash: `KEY=a b` assigns only
// `a`; a trailing `# comment` is likewise dropped) and unescapes `\<char>`.
func scanBare(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '\\' && i+1 < len(v):
			b.WriteByte(v[i+1])
			i++
		case c == ' ' || c == '\t':
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// scanDQ parses the body after an opening `"`; bash only treats `\` as special
// before " \ $ ` and newline.
func scanDQ(v string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '"' {
			return b.String(), true
		}
		if c == '\\' && i+1 < len(v) {
			switch n := v[i+1]; n {
			case '"', '\\', '$', '`':
				b.WriteByte(n)
				i++
				continue
			case '\n':
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return "", false // unterminated
}

// scanANSIC parses the body after an opening `$'` (ANSI-C quoting, as emitted by
// bash `printf %q` for values with control characters).
func scanANSIC(v string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\'' {
			return b.String(), true
		}
		if c != '\\' || i+1 >= len(v) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := v[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case 'e', 'E':
			b.WriteByte(0x1b)
		case '\\', '\'', '"', '?', '`':
			b.WriteByte(e)
		case 'x':
			j := i + 1
			for j < len(v) && j < i+3 && isHex(v[j]) {
				j++
			}
			if j > i+1 {
				n, _ := strconv.ParseUint(v[i+1:j], 16, 32)
				b.WriteByte(byte(n))
				i = j - 1
			} else {
				b.WriteByte('x')
			}
		default:
			if e >= '0' && e <= '7' {
				j := i
				for j < len(v) && j < i+3 && v[j] >= '0' && v[j] <= '7' {
					j++
				}
				n, _ := strconv.ParseUint(v[i:j], 8, 32)
				b.WriteByte(byte(n))
				i = j - 1
			} else {
				b.WriteByte('\\')
				b.WriteByte(e)
			}
		}
	}
	return "", false // unterminated
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
