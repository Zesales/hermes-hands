// Package guard decides whether one of Hermes' OWN server-side tool calls
// (terminal, execute_code, read_file, …) is aimed at the operator's workspace
// — which only the hands can reach — or is legitimate server-side work (a
// scratch computation, a download, a cron job's shell).
//
// Hermes keeps every tool: hermes-hands only steers a run back to the hands
// when a call clearly means the operator's machine. Deterministic, no model:
//
//   - it echoes a hands envelope through a tool ({"calls": …})
//   - it names the operator's workspace or home path
//   - it runs make, or a repo-local git command (status, diff, commit, …)
//   - it names a file or directory that exists in the operator's repo
//
// Anything else is shown to the operator and left alone.
package guard

import (
	"os"
	"path/filepath"
	"strings"
)

// Modes for HERMES_HANDS_SANDBOX.
const (
	Smart = "smart" // steer only calls aimed at the operator's workspace (default)
	Steer = "steer" // steer every server-side exec / file call
	Show  = "show"  // never steer — only show them
)

// repoGit are git subcommands that only make sense on an existing work tree —
// on the server sandbox there is none of the operator's, so they mean theirs.
var repoGit = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "add": true, "commit": true,
	"push": true, "pull": true, "fetch": true, "checkout": true, "switch": true, "branch": true,
	"rev-parse": true, "describe": true, "tag": true, "stash": true, "rebase": true,
	"merge": true, "reset": true, "restore": true, "blame": true, "grep": true, "ls-files": true,
}

// Targets reports whether a server-side tool call (its tool.started preview)
// is aimed at the operator's workspace, and why. workspace is the repo root
// hermes-hands stands in, home the operator's $HOME ("" skips that check).
func Targets(tool, preview, workspace, home string) (bool, string) {
	p := strings.TrimSpace(preview)
	if p == "" {
		return false, ""
	}
	if strings.Contains(p, `"calls"`) {
		return true, "it echoes a hands envelope — the envelope is your reply text, not a tool call"
	}
	for _, root := range []string{workspace, home} {
		if len(root) > 1 && strings.Contains(p, root) {
			return true, "it names " + root + " — a path on the operator's machine"
		}
	}
	words := tokens(p)
	for i, w := range words {
		cmdPos := i == 0 || isSep(words[i-1])
		if cmdPos && w == "make" {
			return true, "`make` — in a hands run that is the operator's repo"
		}
		if cmdPos && w == "git" && i+1 < len(words) && repoGit[words[i+1]] {
			return true, "`git " + words[i+1] + "` — the only work tree here is the operator's"
		}
	}
	if workspace != "" {
		for _, w := range words {
			if name := relPath(w); name != "" {
				if _, err := os.Stat(filepath.Join(workspace, name)); err == nil {
					return true, "it names " + name + ", which exists in the operator's repo"
				}
			}
		}
	}
	return false, ""
}

// tokens splits a command-ish preview into words, shell separators kept as
// their own tokens.
func tokens(s string) []string {
	for _, sep := range []string{"&&", "||", ";", "|", "(", ")", "\n"} {
		s = strings.ReplaceAll(s, sep, " "+strings.TrimSpace(sep)+" ")
	}
	var out []string
	for _, w := range strings.Fields(s) {
		out = append(out, strings.Trim(w, `"'`+"`"))
	}
	return out
}

func isSep(w string) bool { return w == "&&" || w == "||" || w == ";" || w == "|" || w == "(" }

// relPath turns a word into a candidate repo-relative path ("" when it is not
// one): flags, URLs, absolute paths and bare dots are skipped; VAR=value
// checks the value.
func relPath(w string) string {
	if i := strings.IndexByte(w, '='); i > 0 {
		w = w[i+1:]
	}
	w = strings.TrimSuffix(strings.TrimPrefix(w, "./"), "/")
	switch {
	case w == "", w == ".", w == "..", strings.HasPrefix(w, "-"), strings.HasPrefix(w, "/"),
		strings.HasPrefix(w, "~"), strings.HasPrefix(w, "$"), strings.Contains(w, "://"),
		strings.HasPrefix(w, ".."):
		return ""
	}
	if strings.ContainsAny(w, "*?{}<>") {
		return ""
	}
	return w
}
