// Package guard decides what to do about one of Hermes' OWN server-side tool
// calls (terminal, execute_code, read_file, …) in a hands run. A hands
// session's work is the operator's project, on the operator's machine — only
// the hands reach it. Its toolchain and services belong there too: a model
// that meets a missing tool tends to rebuild the project in its own sandbox
// (install the toolchain, clone the code, look around the server's
// containers), which is work on the wrong machine.
//
// The default (steer, Strict) steers every server-side exec / file /
// delegation call back to the hands. smart (Check) steers only what is
// clearly meant for the operator's project. Deterministic, no model:
//
//   - it echoes a hands envelope through a tool ({"calls": …})
//   - it names the operator's workspace or home path
//   - it runs make, or a repo-local git command (status, diff, commit, …)
//   - it names a file or directory that exists in the operator's repo
//   - it sets up an environment: installs a toolchain or dependencies, pipes
//     a download into a shell, clones code
//   - it explores the server: its containers, its network, the docker socket
//
// Separately, a hands tool (local_shell, …) that Hermes tries to call as one
// of its own — via its tool_call / tool_search deferral mechanism, say — is
// steered in every mode but show, and so is the hands envelope passed to
// tool_call (its preview "Unnamed call"). So is a placeholder call of any tool
// (Placeholder: "noop", "Mcp Placeholder", a delegate_task of "noop check"),
// made only to have called something — right after a steer, or to "check the
// connection". Hermes' other tools (web, memory, skills, cron) are never
// steered for real work.
package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Modes for HERMES_HANDS_SANDBOX.
const (
	Steer = "steer" // steer every server-side exec / file / delegation call (default)
	Smart = "smart" // steer only calls meant for the operator's project
	Show  = "show"  // never steer — only show them
)

// SandboxTools are Hermes' OWN tools that act on the API server's sandbox — a
// shell and filesystem that are NOT the operator's machine. They stay usable
// for server-side work; Check decides per call. `process` is the pre-v0.22
// name of process_manage, still accepted by hermes-agent. delegate_task
// counts: its sub-agents work in that sandbox too and never reach the hands.
var SandboxTools = map[string]bool{
	"terminal": true, "process_manage": true, "process": true,
	"execute_code": true, "read_file": true, "write_file": true, "patch": true, "search_files": true,
	"delegate_task": true,
}

// Verdict is the guard's answer for one of Hermes' tool.started events. Kind
// groups steers so a run is steered only a few times per kind of mistake.
type Verdict struct {
	Steer   bool
	Kind    string // "sandbox" | "setup" | "hands-tool" | "placeholder"
	Message string // the steer text sent to the run
}

// handsToolRe matches the hands' tool names (and their display forms, as in
// Hermes' tool_call preview "Local Shell").
var handsToolRe = regexp.MustCompile(`(?i)\blocal[ _-]?(shell|read|write|edit)\b`)

const handsToolMsg = "[hermes-hands] `local_shell` / `local_read` / `local_write` / `local_edit` are not tools of yours — not via tool_call, not via tool_search, under no spelling: their \"not a known tool name\" error is final, a retry only burns the run. They are names you put in your reply envelope, and the hands run them on the operator's machine. No tool call answers this note: end this run with your reply {\"calls\":[{\"id\":\"c1\",\"tool\":\"local_shell\",\"args\":{\"cmd\":\"…\"}}],\"final\":null}."

// localWork is what every steer tells Hermes: where the work belongs, and
// what to do when a tool is missing there.
const localWork = "Everything for the operator's project — code, files, git, build, tests, its toolchain, its dependencies, its services — is on THEIR machine, reachable only through the hands: put it in a hands envelope {\"calls\":[...],\"final\":null}. A tool missing there: use the project's own way (make targets, docker, scripts), or end with a final that tells the operator what is missing and the exact command to install it (e.g. `sudo apt install …`) — never set it up in your sandbox. No tool call answers this note: your next reply is the envelope."

const sandboxMsg = "[hermes-hands] your `%s` call ran in YOUR server sandbox, but %s. " + localWork

const strictMsg = "[hermes-hands] your `%s` call ran in YOUR server sandbox — in a hands session your own exec, file and delegation tools are not for the work. " + localWork

// Check is the smart policy (HERMES_HANDS_SANDBOX=smart) for one of Hermes'
// tool.started events: a hands tool called as if it were Hermes' own, or a
// server-side exec / file call aimed at the operator's workspace (Targets).
func Check(tool, preview, workspace, home string) Verdict {
	if v := handsTool(tool, preview); v.Steer {
		return v
	}
	if v := placeholder(tool, preview); v.Steer {
		return v
	}
	if !SandboxTools[tool] {
		return Verdict{}
	}
	if aimed, why := Targets(tool, preview, workspace, home); aimed {
		return Verdict{Steer: true, Kind: "sandbox", Message: fmt.Sprintf(sandboxMsg, tool, why)}
	}
	if why := Setup(preview); why != "" {
		return Verdict{Steer: true, Kind: "setup", Message: fmt.Sprintf(sandboxMsg, tool, why)}
	}
	return Verdict{}
}

// The environment work a model reaches for in its own sandbox when a tool is
// missing on the operator's machine.
var (
	installRe = regexp.MustCompile(`(?i)\b(apt-get|apt|apk|yum|dnf|pacman|zypper|brew|snap|choco)\s+(-\S+\s+)*(install|add)\b|` +
		`\b(pip3?|pipx|uv|npm|pnpm|yarn|bun|cargo|gem|go|composer)\s+(-\S+\s+)*(install|add|get|i)\b|` +
		`\b(curl|wget)\b[^|\n]*\|\s*(sudo\s+)?(ba|z)?sh\b|` +
		`go\.dev/dl|golang\.org/dl|nodejs\.org/dist|sh\.rustup\.rs|get\.docker\.com`)
	cloneRe   = regexp.MustCompile(`(?i)\b(git\s+clone|gh\s+repo\s+clone)\b`)
	exploreRe = regexp.MustCompile(`(?i)\bdocker(-compose)?\s+(ps|inspect|network|exec|logs|images|container|compose|info)\b|` +
		`\b(kubectl|nmap|netstat|arp)\b|\bss\s+-|\bip\s+(a|addr|route|neigh)\b|` +
		`host\.docker\.internal|docker\.sock`)
)

// Setup reports why a server-side call is environment work that belongs on
// the operator's machine ("" when it is none): a toolchain or dependency
// installed in the sandbox, code cloned into it, the server's containers or
// network explored.
func Setup(preview string) string {
	switch {
	case installRe.MatchString(preview):
		return "it sets up a toolchain or dependencies there — the operator's project and its environment are on their machine"
	case cloneRe.MatchString(preview):
		return "it clones code into your sandbox — the operator's project is on their machine already"
	case exploreRe.MatchString(preview):
		return "it explores the server's containers / network — neither the operator's machine nor your task"
	}
	return ""
}

// Strict is HERMES_HANDS_SANDBOX=steer: every server-side exec / file /
// delegation call is steered, and so is a hands tool called as Hermes' own or
// a placeholder call.
func Strict(tool, preview string) Verdict {
	if v := handsTool(tool, preview); v.Steer {
		return v
	}
	if v := placeholder(tool, preview); v.Steer {
		return v
	}
	if SandboxTools[tool] {
		return Verdict{Steer: true, Kind: "sandbox", Message: fmt.Sprintf(strictMsg, tool)}
	}
	return Verdict{}
}

// envelopeCallRe is hermes-agent's preview of a tool_call batch whose calls
// have no `name` — seen live as "Unnamed call +2": the hands envelope passed
// to tool_call, whose own parameter is `calls` too, its entries carrying
// `tool` where tool_call wants `name`.
var envelopeCallRe = regexp.MustCompile(`^Unnamed call( \+\d+)?$`)

const envelopeCallMsg = "[hermes-hands] your `tool_call` carried a hands envelope — its calls have no `name`, so it fails as \"Unnamed call\" every time. The envelope's `calls` are not tool_call's `calls`: the envelope is your reply text, and the hands run it on the operator's machine. No tool call answers this note: end this run with your reply {\"calls\":[{\"id\":\"c1\",\"tool\":\"local_shell\",\"args\":{\"cmd\":\"…\"}}],\"final\":null}."

func handsTool(tool, preview string) Verdict {
	if handsToolRe.MatchString(tool) || handsToolRe.MatchString(preview) {
		return Verdict{Steer: true, Kind: "hands-tool", Message: handsToolMsg}
	}
	if tool == "tool_call" && envelopeCallRe.MatchString(strings.TrimSpace(preview)) {
		return Verdict{Steer: true, Kind: "hands-tool", Message: envelopeCallMsg}
	}
	return Verdict{}
}

const placeholderMsg = "[hermes-hands] your `%s` call is a placeholder — no tool call is needed: not to check the connection (the hands are connected whenever you run; this run exists because they asked), not to answer a note. Your reply text is the envelope {\"calls\":[...],\"final\":null}: to check anything, make your first real call in it — its result is the check."

func placeholder(tool, preview string) Verdict {
	if Placeholder(preview) {
		return Verdict{Steer: true, Kind: "placeholder", Message: fmt.Sprintf(placeholderMsg, tool)}
	}
	return Verdict{}
}

// What a do-nothing call is made of, as seen live: tool_call "Noop
// Placeholder" / "Mcp Placeholder", terminal "echo placeholder",
// delegate_task "1 tasks: noop check".
var (
	placeholderMarks = map[string]bool{"noop": true, "nop": true, "placeholder": true, "dummy": true}
	placeholderFill  = map[string]bool{"check": true, "test": true, "ping": true, "mcp": true, "echo": true,
		"true": true, "task": true, "tasks": true, "call": true, "tool": true}
)

// Placeholder reports whether a tool.started preview is nothing but a
// placeholder: a mark (noop, placeholder, dummy) among filler words and
// numbers only. A real task that merely mentions one ("research no-op
// patterns") is not.
func Placeholder(preview string) bool {
	p := strings.NewReplacer("no-op", "noop", "no_op", "noop").Replace(strings.ToLower(preview))
	mark := false
	for _, w := range strings.FieldsFunc(p, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch {
		case placeholderMarks[w]:
			mark = true
		case placeholderFill[w], strings.Trim(w, "0123456789") == "":
		default:
			return false
		}
	}
	return mark
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
