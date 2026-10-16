package knowsystem

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const briefHeader = "## Known system (probed by hermes-hands at session start — trust it; do not spend tool rounds rediscovering these facts)"

// Brief renders the compact block appended to the instructions. Deliberately
// short: ~8 lines. The full record lives in known-system.json and can be read
// with one shell call when the brain needs the JSON or the make targets.
func (s *System) Brief() string {
	var b strings.Builder
	b.WriteString(briefHeader + "\n")

	host := s.OS + " " + s.Arch
	if s.OSName != "" {
		host = s.OSName + " — " + host
	}
	if s.OS == "darwin" && s.Arch == "arm64" {
		host += " (Apple Silicon)"
	}
	if s.WSL {
		host += " (WSL — a Linux userland on a Windows host; Windows drives under /mnt/<letter>)"
	}
	if s.Kernel != "" {
		host += ", kernel " + s.Kernel
	}
	line := "host: " + host
	if s.Home != "" {
		line += " | HOME " + s.Home
	}
	b.WriteString(line + "\n")
	if s.Bash != "" {
		sh := "shell: bash " + s.Bash + " (the hands' `bash --login`)"
		if strings.HasPrefix(s.Bash, "3.") {
			sh += " — bash 3: no mapfile/readarray, no associative arrays, no ${x,,} / ${x^^}, no `&>>`"
		}
		b.WriteString(sh + "\n")
	}
	if s.Userland == "bsd" {
		u := "userland: BSD (macOS) — `sed -i ''` (not `sed -i`), no `grep -P`, `date -v` (not `date -d`), `stat -f` (not `stat -c`), `sed`/`awk` without GNU extensions"
		if len(s.GNUTools) > 0 {
			u += "; GNU variants installed: " + strings.Join(s.GNUTools, " ")
		} else {
			u += "; no GNU variants (gsed, ggrep, …) installed"
		}
		b.WriteString(u + "\n")
	}
	if s.PkgManager != "" {
		b.WriteString("package manager: " + s.PkgManager + "\n")
	}

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
	if s.Layout != "" {
		b.WriteString("layout (tracked files): " + s.Layout + "\n")
	}
	if len(s.MakeTargets) > 0 {
		if len(s.MakeHelp) == 0 {
			b.WriteString("make targets: " + strings.Join(s.MakeTargets, " ") + "\n")
		} else {
			b.WriteString("make targets (`make <target>`):\n")
			for _, t := range s.MakeTargets {
				if h := s.MakeHelp[t]; h != "" {
					b.WriteString("  " + t + " — " + clip(h, 90) + "\n")
				} else {
					b.WriteString("  " + t + "\n")
				}
			}
		}
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
		b.WriteString("learned missing: " + strings.Join(learned, " ") + " (exit 127 in an earlier session; dropped once found on PATH)\n")
	}

	b.WriteString("Prefer the repo's own tooling (make targets, compose) over raw binaries.")
	if s.Tools["docker"] {
		b.WriteString(" docker is available: a tool the host lacks can run in a throwaway `docker run --rm` container (isolated, nothing installed on the host).")
	}
	b.WriteString(" Installs on the host are the operator's call.")
	return b.String()
}

// GuidanceBlock renders the operator's rules for the instructions — the
// imported global rules first, then the repo's AGENTS.md / CLAUDE.md and their
// imports, then the deeper rules files by name ("" when there are none). They
// bind the brain the same way they bind any agent working this workspace.
func (s *System) GuidanceBlock() string {
	if len(s.Guidance) == 0 && len(s.MoreRules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Operator rules (binding: follow them; where they name a tool, a pipeline or a limit, that is how the work is done here)\n")
	for _, g := range s.Guidance {
		head := "the repo's " + g.Path + " (read at session start)"
		switch {
		case g.Global:
			head = "global rules, imported by the operator at setup (" + g.Path + ")"
		case g.ImportedBy != "":
			head = "the repo's " + g.Path + " (imported by " + g.ImportedBy + " with `@`, so part of its rules)"
		case filepath.Base(g.Path) == "CLAUDE.local.md":
			head = "the operator's personal rules for this repo, " + g.Path + " (not committed; read at session start)"
		}
		b.WriteString("\n### " + head + "\n\n" + strings.TrimSpace(g.text) + "\n")
	}
	if len(s.MoreRules) > 0 || s.MoreRulesN > 0 {
		b.WriteString("\n### rules for subdirectories — binding there, in addition to the rules above. Not inlined: your first call into such a directory brings its rules along in the result's `context`, and a local_write / local_edit there is held back until you have seen them — `local_read` them first to save that round\n\n")
		for _, h := range s.MoreRules {
			b.WriteString("- `" + h.Path + "` — governs `" + h.Scope + "`\n")
		}
		if s.MoreRulesN > 0 {
			b.WriteString(fmt.Sprintf("- … %d more — `git ls-files '*AGENTS.md' '*CLAUDE.md'` lists them\n", s.MoreRulesN))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// capList keeps the first max entries and sums up the rest as "+N".
func capList(list []string, max int) []string {
	if len(list) <= max {
		return list
	}
	return append(list[:max:max], fmt.Sprintf("+%d", len(list)-max))
}

func joinCapped(list []string, max int) string {
	if len(list) <= max {
		return strings.Join(list, " ")
	}
	return strings.Join(list[:max], " ") + " …(+" + strings.Join(list[max:], " ") + ")"
}
