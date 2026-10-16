// Package knowsystem keeps a deterministic picture of the machine hermes-hands
// runs on. It is probed ONCE per session start by the client — the brain is
// told the result instead of burning tool rounds rediscovering it.
//
// Single writer: hermes-hands. The brain only reads the injected brief; it
// never edits the file. The probe is deliberately fixed (a known tool list, a
// known repo-signal list), so it is deterministic, cheap, and identical every
// session — the point is that the brain can trust it.
//
// Two layers: the HOST (OS / WSL, kernel, which tools resolve — probed in the
// same `bash --login` the hands' shell runs, so the brief matches what a hands
// command actually finds; whether docker is there as the isolated way to get a
// tool the host lacks) and, optionally, the REPO the session stands in (git
// branch, manifests, make targets with their `##` help, a layout map, and the
// repo's own rules: AGENTS.md / CLAUDE.md / CLAUDE.local.md on the way down to
// cwd and their `@path` imports inlined, deeper ones named — ScopedRules hands
// those out at the first call into their directory). The operator's global
// rules are imported once at setup into the hermes-hands home (UseGlobal).
//
// Freshness: every session start re-probes from scratch, so tools installed
// or removed between sessions are picked up automatically. A shell call that
// ends in 127 ("command not found") is recorded as learned-missing and
// survives into the next session's brief — until a probe finds the tool, at
// which point the stale entry is dropped.
package knowsystem

import ()

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
	MakeHelp    map[string]string `json:"make_help,omitempty"` // target -> its `## …` help text
	Layout      string            `json:"layout,omitempty"`    // compact map of the tracked tree
	WSL         bool              `json:"wsl,omitempty"`
	OSName      string            `json:"os_name,omitempty"`     // "macOS 15.1", "Ubuntu 24.04.1 LTS"
	Bash        string            `json:"bash,omitempty"`        // $BASH_VERSION of the hands' login shell
	Userland    string            `json:"userland,omitempty"`    // "gnu" | "bsd"
	PkgManager  string            `json:"pkg_manager,omitempty"` // brew | apt-get | dnf | pacman | apk | zypper
	GNUTools    []string          `json:"gnu_tools,omitempty"`   // macOS: GNU variants installed (gsed, ggrep, …)
	Tools       map[string]bool   `json:"tools"`
	Learned     map[string]string `json:"learned,omitempty"`        // tool -> "missing"
	Guidance    []GuideFile       `json:"guidance,omitempty"`       // AGENTS.md / CLAUDE.md read for the brief
	MoreRules   []RuleHint        `json:"more_rules,omitempty"`     // deeper rules files, named, not inlined
	MoreRulesN  int               `json:"more_rules_cut,omitempty"` // deeper rules files past maxNested
}

// GuideFile is one operator rules file: the global rules imported at setup
// (Global), a repo's AGENTS.md / CLAUDE.md, or a file one of those imports with
// `@path` (ImportedBy). Its text goes into the instructions, never into
// known-system.json.
type GuideFile struct {
	Path       string `json:"path"`
	Bytes      int    `json:"bytes"`
	Global     bool   `json:"global,omitempty"`
	ImportedBy string `json:"imported_by,omitempty"`
	text       string // as inlined (capped)
	raw        string // as read
}

// RuleHint is a rules file deeper in the tree than cwd: it governs its own
// subdirectory (Scope), so the instructions only name it, and ScopedRules
// hands it to the brain with the first call into that directory.
type RuleHint struct {
	Path  string `json:"path"`  // relative to cwd
	Scope string `json:"scope"` // the subdirectory it governs ("services/api/")
}
