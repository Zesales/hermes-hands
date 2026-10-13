// Package knowsystem keeps a deterministic picture of the machine hermes-hands
// runs on. It is probed ONCE per session start by the client — the brain is
// told the result instead of burning tool rounds rediscovering it.
//
// Single writer: hermes-hands. The brain only reads the injected brief; it
// never edits the file. The probe is deliberately fixed (a known tool list via
// exec.LookPath, a known repo-signal list), so it is deterministic, cheap, and
// identical every session — the point is that the brain can trust it.
//
// Freshness: every session start re-probes from scratch, so tools installed
// or removed between sessions are picked up automatically. A shell call that
// ends in 127 ("command not found") is recorded as learned-missing and
// survives into the next session's brief — until the next probe proves the
// tool exists, at which point the stale entry is dropped.
package knowsystem

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// toolProbe is the FIXED tool list checked with exec.LookPath. Fixed, not
// discovered: the brain sees the same list shape every session.
var toolProbe = []string{
	"bash", "sh", "git", "make", "docker", "docker-compose", "podman",
	"go", "node", "npm", "python3", "python", "cargo", "gcc", "java",
	"rg", "jq", "curl", "wget", "gh",
}

// repoSignals are the files that say how THIS repo wants its toolchain run —
// the operator's own tooling (Makefile targets, compose files, project
// manifests) outranks raw system binaries: if a Makefile exists, the brain
// should drive the repo through `make`, not hunt for individual binaries.
var repoSignals = []string{
	"Makefile", "makefile", "GNUmakefile", "Justfile", "justfile",
	"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
	"Dockerfile", "Containerfile",
	"go.mod", "package.json", "pyproject.toml", "requirements.txt",
	"Cargo.toml", "pom.xml", "build.gradle", "CMakeLists.txt",
}

// System is the probe result — also the JSON schema of known-system.json.
type System struct {
	ProbedAt    string            `json:"probed_at"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Kernel      string            `json:"kernel,omitempty"`
	Home        string            `json:"home,omitempty"`
	Cwd         string            `json:"cwd"`
	GitBranch   string            `json:"git_branch,omitempty"`
	RepoSignals []string          `json:"repo_signals,omitempty"`
	MakeTargets []string          `json:"make_targets,omitempty"`
	Tools       map[string]bool   `json:"tools"`
	Learned     map[string]string `json:"learned,omitempty"` // tool -> "missing"
}

// Probe collects everything deterministically: no LLM, no guessing, no
// round-trips. cwd is the repo root hermes-hands stands in; gitBranch is the
// caller's already-resolved branch name ("" outside a work tree).
func Probe(cwd, gitBranch string) *System {
	s := &System{
		ProbedAt:  time.Now().UTC().Format(time.RFC3339),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Home:      os.Getenv("HOME"),
		Cwd:       cwd,
		GitBranch: gitBranch,
		Tools:     make(map[string]bool, len(toolProbe)),
	}
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("uname", "-sr").Output(); err == nil {
			s.Kernel = strings.TrimSpace(string(out))
		}
	}
	seen := make(map[string]bool)
	for _, f := range repoSignals {
		if fi, err := os.Stat(cwd + "/" + f); err == nil && !fi.IsDir() && !seen[f] {
			seen[f] = true
			s.RepoSignals = append(s.RepoSignals, f)
		}
	}
	s.MakeTargets = makeTargets(cwd)
	for _, t := range toolProbe {
		_, err := exec.LookPath(t)
		s.Tools[t] = err == nil
	}
	return s
}

var makeTargetRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_.-]*):\s`)

// makeTargets reads target names out of the Makefile textually — no `make -p`
// execution (that would run a shell command at probe time and can be slow or
// side-effecting). Good enough to tell the brain the repo is make-driven.
func makeTargets(cwd string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		b, err := os.ReadFile(cwd + "/" + name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ".") {
				continue
			}
			m := makeTargetRe.FindStringSubmatch(line)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, m[1])
			if len(out) >= 24 {
				return out
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

var notFoundRe = regexp.MustCompile(`: ([A-Za-z0-9_][A-Za-z0-9_.+/-]*): command not found`)

// ObserveShell watches tool results for the one signal that matters at run
// time: a 127 "command not found". The offending command is taken from bash's
// own message (not from guessing the first token of the call — a compound
// command names the failing part in its error). Returns true when the learned
// set changed, i.e. the caller should persist.
func (s *System) ObserveShell(output string, exit int) bool {
	if exit != 127 {
		return false
	}
	m := notFoundRe.FindStringSubmatch(output)
	if m == nil || m[1] == "" {
		return false
	}
	if s.Learned == nil {
		s.Learned = make(map[string]string)
	}
	if s.Learned[m[1]] == "missing" {
		return false
	}
	s.Learned[m[1]] = "missing"
	return true
}

// MergeLearned folds the previous session's learned facts into this fresh
// probe — but a stale "missing" is dropped the moment the new probe sees the
// tool on PATH. Freshness always wins over memory.
func (s *System) MergeLearned(old *System) {
	if old == nil || old.Learned == nil {
		return
	}
	if s.Learned == nil {
		s.Learned = make(map[string]string)
	}
	for tool, state := range old.Learned {
		if state != "missing" {
			continue
		}
		if s.Tools[tool] {
			continue // probe says it exists now — stale fact dropped
		}
		s.Learned[tool] = state
	}
	if len(s.Learned) == 0 {
		s.Learned = nil
	}
}

// Load reads a previous known-system.json. A missing/corrupt file is not an
// error condition worth breaking a session for — the caller decides.
func Load(path string) (*System, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s System
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the record (0600: it is a local fact file inside the operator's
// hermes-hands home, next to secrets — keep the same posture).
func (s *System) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

const briefHeader = "## Known system (probed by hermes-hands at session start — trust it; do not spend tool rounds rediscovering these facts)"

// Brief renders the compact block appended to the instructions. Deliberately
// short: ~8 lines. The full record lives in known-system.json and can be read
// with one shell call when the brain needs the JSON or the make targets.
func (s *System) Brief() string {
	var b strings.Builder
	b.WriteString(briefHeader + "\n")

	host := s.OS + " " + s.Arch
	if s.Kernel != "" {
		host += ", kernel " + s.Kernel
	}
	line := "host: " + host
	if s.Home != "" {
		line += " | HOME " + s.Home
	}
	b.WriteString(line + "\n")

	cwd := "cwd: " + s.Cwd
	if s.GitBranch != "" {
		cwd += " (git branch: " + s.GitBranch + ")"
	}
	b.WriteString(cwd + "\n")

	if len(s.RepoSignals) > 0 {
		b.WriteString("repo signals: " + strings.Join(s.RepoSignals, ", ") + "\n")
	} else {
		b.WriteString("repo signals: none (no Makefile / compose / manifest in cwd)\n")
	}
	if len(s.MakeTargets) > 0 {
		b.WriteString("make targets: " + strings.Join(s.MakeTargets, " ") + "\n")
	}

	var have, missing []string
	for tool, ok := range s.Tools {
		if ok {
			have = append(have, tool)
		} else {
			missing = append(missing, tool)
		}
	}
	sort.Strings(have)
	sort.Strings(missing)
	b.WriteString("tools on PATH: " + joinCapped(have, 20) + "\n")
	if len(missing) > 0 {
		b.WriteString("tools NOT on PATH: " + joinCapped(missing, 20) + "\n")
	}

	var learned []string
	for tool := range s.Learned {
		learned = append(learned, tool)
	}
	sort.Strings(learned)
	if len(learned) > 0 {
		b.WriteString("learned-missing this session: " + strings.Join(learned, " ") + " (127 observed; re-probed next session)\n")
	}

	b.WriteString("Prefer the repo's own tooling (make targets, compose) over raw binaries; installs are the operator's call.")
	return b.String()
}

func joinCapped(list []string, max int) string {
	if len(list) <= max {
		return strings.Join(list, " ")
	}
	return strings.Join(list[:max], " ") + " …(+" + strings.Join(list[max:], " ") + ")"
}
