package repl

import (
	"regexp"
	"strings"
)

// splitCmd separates a REPL line into its first word (the /command) and the
// rest. A plain message returns (firstWord, rest) too; only the switch's
// /command cases act on it, everything else is sent as-is.
func splitCmd(s string) (cmd, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

var slashCmdRe = regexp.MustCompile(`^/[A-Za-z?][A-Za-z0-9_-]*$`)

// isSlashCmd reports whether the first word looks like a REPL command
// ("/quit"), as opposed to a message that starts with a path ("/etc/hosts …").
func isSlashCmd(cmd string) bool { return slashCmdRe.MatchString(cmd) }
