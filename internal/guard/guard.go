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
// Anything else is shown to the operator and left alone. Separately, a hands
// tool (local_shell, …) that Hermes tries to call as one of its own — via its
// tool_call / tool_search deferral mechanism, say — is steered too.
package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Modes for HERMES_HANDS_SANDBOX.
const (
	Smart = "smart" // steer only calls aimed at the operator's workspace (default)
	Steer = "steer" // steer every server-side exec / file call
	Show  = "show"  // never steer — only show them
)

// SandboxTools are Hermes' OWN tools that act on the API server's sandbox — a
// shell and filesystem that are NOT the operator's machine. They stay usable
// for server-side work; Check decides per call. `process` is the pre-v0.22
// name of process_manage, still accepted by hermes-agent.
var SandboxTools = map[string]bool{
	"terminal": true, "process_manage": true, "process": true,
	"execute_code": true, "read_file": true, "write_file": true, "patch": true, "search_files": true,
}

// Verdict is the guard's answer for one of Hermes' tool.started events. Kind
// groups steers so a run is steered at most once per kind of mistake.
type Verdict struct {
	Steer   bool
	Kind    string // "sandbox" | "hands-tool"
	Message string // the steer text sent to the run
}

// handsToolRe matches the hands' tool names (and their display forms, as in
// Hermes' tool_call preview "Local Shell").
var handsToolRe = regexp.MustCompile(`(?i)\blocal[ _-]?(shell|read|write|edit)\b`)

const handsToolMsg = "[hermes-hands] `local_shell` / `local_read` / `local_write` / `local_edit` are not tools of yours — not even via tool_call or tool_search. They are names you put in your reply envelope, and the hands run them on the operator's machine: reply with {\"calls\":[{\"id\":\"c1\",\"tool\":\"local_shell\",\"args\":{\"cmd\":\"…\"}}],\"final\":null}."

const sandboxMsg = "[hermes-hands] your `%s` call ran in YOUR server sandbox, but %s. The operator's machine — their repo, files, git, build — is reachable only through the hands: put that work in a hands envelope {\"calls\":[...],\"final\":null}. Your own tools stay fine for server-side work (scratch computations, downloads, cron jobs)."

// Check is the smart policy (HERMES_HANDS_SANDBOX=smart) for one of Hermes'
// tool.started events: a hands tool called as if it were Hermes' own, or a
// server-side exec / file call aimed at the operator's workspace (Targets).
func Check(tool, preview, workspace, home string) Verdict {
	if v := handsTool(tool, preview); v.Steer {
		return v
	}
	if !SandboxTools[tool] {
		return Verdict{}
	}
	if aimed, why := Targets(tool, preview, workspace, home); aimed {
		return Verdict{Steer: true, Kind: "sandbox", Message: fmt.Sprintf(sandboxMsg, tool, why)}
	}
	return Verdict{}
}

// Strict is HERMES_HANDS_SANDBOX=steer: every server-side exec / file call is
// steered, and so is a hands tool called as Hermes' own.
func Strict(tool, preview string) Verdict {
	if v := handsTool(tool, preview); v.Steer {
		return v
	}
	if SandboxTools[tool] {
		return Verdict{Steer: true, Kind: "sandbox", Message: fmt.Sprintf(sandboxMsg, tool, "it reached for the operator's machine")}
	}
	return Verdict{}
}

func handsTool(tool, preview string) Verdict {
	if handsToolRe.MatchString(tool) || handsToolRe.MatchString(preview) {
		return Verdict{Steer: true, Kind: "hands-tool", Message: handsToolMsg}
	}
	return Verdict{}
}

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
