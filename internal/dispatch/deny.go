package dispatch

import (
	"regexp"
	"strings"
)

// cmdBlocked ports _hh_cmd_blocked. It returns a reason and true when the
// command must not run. The built-in checks look at " <cmd> " (a single space
// each side); the HERMES_HANDS_DENY globs are matched against the raw command.
func cmdBlocked(cmd string, deny []string) (string, bool) {
	c := " " + cmd + " "

	if containsAny(c, " ssh ", " scp ", " sftp ", " rsync ") {
		return "ssh/scp/rsync to other hosts", true
	}
	if containsAny(c, " sudo ", " doas ") {
		return "privilege escalation", true
	}
	if containsAny(c, "rm -rf /", "rm -fr /", ":(){ :|:& };:") {
		return "destructive", true
	}
	if containsAny(c, " curl ", " wget ") &&
		containsAny(c, "| sh", "|sh", "| bash", "|bash") {
		return "pipe-to-shell download", true
	}
	for _, g := range deny {
		if g == "" {
			continue
		}
		if globMatch(g, cmd) {
			return "matches HERMES_HANDS_DENY (" + g + ")", true
		}
	}
	return "", false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// globMatch implements bash `[[ "$s" == $pattern ]]`: * matches any run
// (including /), ? matches one char, [set] / [!set] are char classes. The whole
// string must match. A malformed pattern falls back to a literal comparison.
func globMatch(pattern, s string) bool {
	re, err := regexp.Compile(globToRegex(pattern))
	if err != nil {
		return pattern == s
	}
	return re.MatchString(s)
}

func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '[':
			j := i + 1
			if j < len(glob) && (glob[j] == '!' || glob[j] == '^') {
				j++
			}
			if j < len(glob) && glob[j] == ']' {
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j >= len(glob) {
				b.WriteString(regexp.QuoteMeta("["))
				continue
			}
			set := glob[i+1 : j]
			if strings.HasPrefix(set, "!") {
				set = "^" + set[1:]
			}
			b.WriteByte('[')
			b.WriteString(set)
			b.WriteByte(']')
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteByte('$')
	return b.String()
}
