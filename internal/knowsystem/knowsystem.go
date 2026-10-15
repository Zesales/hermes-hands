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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// toolProbe is the FIXED tool list checked with probeTools. Fixed, not
// discovered: the brain sees the same list shape every session.
var toolProbe = []string{
	"bash", "sh", "git", "make", "docker", "docker-compose", "podman",
	"go", "node", "npm", "python3", "python", "cargo", "gcc", "java",
	"rg", "jq", "curl", "wget", "gh",
}

// pkgManagers, in preference order: the first one found is named in the brief
// (installs stay the operator's call — the brain only needs to know the shape).
var pkgManagers = []string{"brew", "apt-get", "dnf", "pacman", "apk", "zypper"}

// gnuVariants are the g-prefixed GNU tools Homebrew's coreutils / gnu-sed /
// grep / findutils / gawk install on macOS next to the BSD originals.
var gnuVariants = []string{"gsed", "ggrep", "gdate", "gtimeout", "gfind", "gawk", "gstat", "greadlink"}

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
	if out, err := exec.Command("uname", "-sr").Output(); err == nil {
		s.Kernel = strings.TrimSpace(string(out))
	}
	s.WSL = os.Getenv("WSL_DISTRO_NAME") != "" || strings.Contains(strings.ToLower(s.Kernel), "microsoft")
	s.OSName = osName()
	s.Userland = "gnu"
	if runtime.GOOS == "darwin" {
		s.Userland = "bsd"
	}
	s.Bash = bashVersion()
	seen := make(map[string]bool)
	for _, f := range repoSignals {
		if fi, err := os.Stat(cwd + "/" + f); err == nil && !fi.IsDir() && !seen[f] {
			seen[f] = true
			s.RepoSignals = append(s.RepoSignals, f)
		}
	}
	s.MakeTargets, s.MakeHelp = makeTargets(cwd)
	names := append(append([]string{}, toolProbe...), pkgManagers...)
	if s.Userland == "bsd" {
		names = append(names, gnuVariants...)
	}
	found := probeTools(names)
	for _, t := range toolProbe {
		s.Tools[t] = found[t]
	}
	for _, pm := range pkgManagers {
		if found[pm] {
			s.PkgManager = pm
			break
		}
	}
	if s.Userland == "bsd" {
		for _, g := range gnuVariants {
			if found[g] {
				s.GNUTools = append(s.GNUTools, g)
			}
		}
	}
	s.Layout = layout(cwd)
	s.Guidance, s.MoreRules, s.MoreRulesN = guidance(cwd)
	return s
}

// probeTools reports which of names resolve in the hands' own environment: a
// non-interactive `bash --login`, exactly what internal/shell starts. So a
// PATH entry the login profile adds (~/.local/go/bin, a version manager …)
// counts — exec.LookPath on hermes-hands' own process PATH misses those and
// would tell the brain "go is not installed" while every hands command finds
// it. Without a working bash it falls back to exec.LookPath. Swappable in
// tests.
var probeTools = func(names []string) map[string]bool {
	found := make(map[string]bool, len(names))
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// PATH entries under /mnt are left out: on WSL they are the Windows PATH
	// (dozens of 9P-mounted directories), which makes every miss take ~0.1 s
	// and holds no Linux tool worth naming.
	script := `P=; IFS=:; for d in $PATH; do case $d in /mnt/*) ;; *) P=$P${P:+:}$d ;; esac; done; unset IFS; PATH=$P
for t in "$@"; do command -v -- "$t" >/dev/null 2>&1 && printf '%s\n' "$t"; done
exit 0` // a missing LAST name must not read as a failed probe (and trigger the fallback)
	cmd := exec.CommandContext(ctx, "bash", append([]string{"--login", "-c", script, "probe"}, names...)...)
	if out, err := cmd.Output(); err == nil {
		// a chatty profile may print to stdout too — only requested names count
		for _, line := range strings.Split(string(out), "\n") {
			if t := strings.TrimSpace(line); want[t] {
				found[t] = true
			}
		}
		return found
	}
	for _, n := range names {
		_, err := exec.LookPath(n)
		found[n] = err == nil
	}
	return found
}

// bashVersion is $BASH_VERSION of a `bash --login` — the hands' shell (macOS
// ships 3.2 as /bin/bash; Homebrew's bash 5 wins when it is first on PATH).
var bashVersion = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "--login", "-c", `printf '\n__bash %s\n' "$BASH_VERSION"`).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "__bash "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// osName is the human OS release: "macOS 15.1" (sw_vers), or the Linux
// distribution's PRETTY_NAME from /etc/os-release.
func osName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
		return "macOS"
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return parseOSRelease(string(b))
}

// parseOSRelease returns PRETTY_NAME (else NAME + VERSION_ID) of an
// /etc/os-release file.
func parseOSRelease(content string) string {
	kv := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}
	if kv["PRETTY_NAME"] != "" {
		return kv["PRETTY_NAME"]
	}
	return strings.TrimSpace(kv["NAME"] + " " + kv["VERSION_ID"])
}

var (
	makeTargetRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_.-]*):(\s|$)`)
	makeHelpRe   = regexp.MustCompile(`##\s*(.+)$`)
)

// makeTargets reads target names — and their `## help` comments — out of the
// Makefile textually: no `make -p` execution (that would run a shell command
// at probe time and can be slow or side-effecting). The help tells the brain
// what a target does (e.g. that `make test` runs in a pinned container), so it
// drives the repo through its own tooling instead of guessing.
func makeTargets(cwd string) ([]string, map[string]string) {
	var out []string
	help := map[string]string{}
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
			if h := makeHelpRe.FindStringSubmatch(line); h != nil {
				help[m[1]] = strings.TrimSpace(h[1])
			}
			if len(out) >= 24 {
				break
			}
		}
		if len(out) > 0 {
			break
		}
	}
	if len(help) == 0 {
		help = nil
	}
	return out, help
}

// layout is a one-line map of the tracked tree (git ls-files): root files,
// then each top-level directory with its subdirectories and file count —
// enough for the brain to know where things live before it adds a file.
// "" outside a git work tree.
func layout(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "ls-files").Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	var rootFiles []string
	count := map[string]int{}
	subs := map[string]map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(f, "/", 3)
		if len(parts) == 1 {
			rootFiles = append(rootFiles, f)
			continue
		}
		top := parts[0]
		count[top]++
		if len(parts) == 3 && top != "vendor" && top != "node_modules" {
			if subs[top] == nil {
				subs[top] = map[string]bool{}
			}
			subs[top][parts[1]] = true
		}
	}
	var dirs []string
	for d := range count {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var segs []string
	if len(rootFiles) > 0 {
		segs = append(segs, joinCapped(rootFiles, 14))
	}
	for _, d := range dirs {
		seg := d + "/"
		if len(subs[d]) > 0 {
			var names []string
			for n := range subs[d] {
				names = append(names, n)
			}
			sort.Strings(names)
			seg = d + "/{" + strings.Join(capList(names, 12), ",") + "}"
		}
		segs = append(segs, fmt.Sprintf("%s (%d)", seg, count[d]))
	}
	return strings.Join(segs, " | ")
}

// guidanceNames are a repo's rules files, in the order they are read in each
// directory: the shared AGENTS.md, Claude Code's CLAUDE.md (or .claude/
// CLAUDE.md), then CLAUDE.local.md — the operator's personal rules for the
// repo, usually gitignored, which is why the files are read from disk.
var guidanceNames = []string{"AGENTS.md", "CLAUDE.md", ".claude/CLAUDE.md", "CLAUDE.local.md"}

const (
	maxGuideFile  = 8 * 1024
	maxGuideTotal = 16 * 1024
	maxNested     = 20 // deeper rules files named in the instructions
	maxImports    = 10 // `@path` imports inlined
	maxImportHops = 5  // as Claude Code: an import's imports, 5 deep
)

// guidance reads the REPO's rules fresh at every session start — each repo has
// its own: the guidanceNames from the repo's top level down to cwd (a
// subdirectory's rules refine the root's, so they come after), then the files
// those import with Claude Code's `@path` — an import is part of the file
// that names it, so it is inlined too, but after every rules file proper (the
// budget goes to those first) and only from inside the repo. Outside a git
// work tree only cwd's own files count. Nothing above the repo is read — the
// operator's global rules are imported once at setup (see UseGlobal).
// Identical copies are read once; each file and the total are capped.
//
// The rules files deeper in the tree are named (hints, with the directory
// each governs); more counts the ones past maxNested.
func guidance(cwd string) (files []GuideFile, hints []RuleHint, more int) {
	cwd = filepath.Clean(cwd)
	top := cwd
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		if t := filepath.Clean(strings.TrimSpace(string(out))); t != "" && (t == cwd || strings.HasPrefix(cwd, t+string(filepath.Separator))) {
			top = t
		}
	}
	dirs := []string{top}
	if rel, err := filepath.Rel(top, cwd); err == nil && rel != "." {
		d := top
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			d = filepath.Join(d, part)
			dirs = append(dirs, d)
		}
	}
	type imp struct {
		from, path string
		hop        int
	}
	var queue []imp
	seen := map[string]bool{} // contents inlined
	read := map[string]bool{} // paths read — an import of one adds nothing
	total := 0
	for _, dir := range dirs {
		for _, name := range guidanceNames {
			p := filepath.Join(dir, name)
			b, err := os.ReadFile(p)
			if err != nil || len(bytes.TrimSpace(b)) == 0 {
				continue
			}
			read[p] = true
			if seen[string(b)] {
				continue
			}
			seen[string(b)] = true
			files = append(files, capGuide(p, b, &total))
			for _, ip := range importsOf(p, b) {
				queue = append(queue, imp{p, ip, 1})
			}
		}
	}
	for n := 0; len(queue) > 0 && n < maxImports; {
		im := queue[0]
		queue = queue[1:]
		if read[im.path] || !inside(top, im.path) {
			continue
		}
		read[im.path] = true
		b, err := os.ReadFile(im.path)
		if err != nil || len(bytes.TrimSpace(b)) == 0 || seen[string(b)] {
			continue // unreadable, empty, or a copy of an inlined file
		}
		seen[string(b)] = true
		g := capGuide(im.path, b, &total)
		g.ImportedBy = im.from
		files = append(files, g)
		n++
		if im.hop < maxImportHops {
			for _, ip := range importsOf(im.path, b) {
				queue = append(queue, imp{im.path, ip, im.hop + 1})
			}
		}
	}
	hints, more = nestedRules(cwd)
	return files, hints, more
}

// inside reports whether p — symlinks resolved — lies in dir (physical).
func inside(dir, p string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	return strings.HasPrefix(real, dir+string(filepath.Separator))
}

var (
	importRe   = regexp.MustCompile(`(?:^|\s)@(\S+)`)
	codeSpanRe = regexp.MustCompile("`[^`]*`")
)

// importsOf finds Claude Code's `@path` imports in a rules file, as absolute
// paths: an `@` that starts a word, outside code blocks and code spans,
// naming a file relative to the importing file (or `~/…`, or absolute). Only
// names that resolve to an existing file count — `@someone` or `@types/node`
// do not.
func importsOf(path string, b []byte) []string {
	var out []string
	fence := ""
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			continue
		}
		for _, m := range importRe.FindAllStringSubmatch(codeSpanRe.ReplaceAllString(line, " "), -1) {
			p := strings.TrimRight(m[1], ".,;:!?)]}'\"")
			if p == "" || strings.Contains(p, "://") {
				continue
			}
			switch {
			case strings.HasPrefix(p, "~/"):
				home := os.Getenv("HOME")
				if home == "" {
					continue
				}
				p = filepath.Join(home, p[2:])
			case !filepath.IsAbs(p):
				p = filepath.Join(filepath.Dir(path), p)
			}
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				out = append(out, filepath.Clean(p))
			}
		}
	}
	return out
}

// nestedRules names the rules files deeper in cwd's tree (git ls-files: tracked,
// or new and not ignored), each with the subdirectory it governs. Shallow ones
// first (they govern more), at most maxNested; cut counts the rest. Vendored
// code (vendor/, node_modules/) carries someone else's rules and is skipped;
// files above or beside cwd are outside the hands' directory and not listed.
func nestedRules(cwd string) (hints []RuleHint, cut int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "ls-files", "-z", "--cached", "--others", "--exclude-standard",
		"--", "*AGENTS.md", "*CLAUDE.md", "*CLAUDE.local.md").Output()
	if err != nil {
		return nil, 0
	}
	var found []string
	seen := map[string]bool{}
	scope := map[string]string{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" || seen[f] { // an unmerged file is listed once per stage
			continue
		}
		seen[f] = true
		dir, base := path.Split(f)
		if base != "AGENTS.md" && base != "CLAUDE.md" && base != "CLAUDE.local.md" {
			continue
		}
		if base == "CLAUDE.md" && (dir == ".claude/" || strings.HasSuffix(dir, "/.claude/")) {
			dir = strings.TrimSuffix(dir, ".claude/")
		}
		if dir == "" || vendored(dir) {
			continue // cwd's own (inlined), or vendored
		}
		if fi, err := os.Stat(filepath.Join(cwd, f)); err != nil || !fi.Mode().IsRegular() {
			continue // deleted in the work tree
		}
		scope[f] = dir
		found = append(found, f)
	}
	sort.Slice(found, func(i, j int) bool {
		if di, dj := strings.Count(scope[found[i]], "/"), strings.Count(scope[found[j]], "/"); di != dj {
			return di < dj
		}
		return found[i] < found[j]
	})
	if len(found) > maxNested {
		cut = len(found) - maxNested
		found = found[:maxNested]
	}
	for _, f := range found {
		hints = append(hints, RuleHint{Path: filepath.FromSlash(f), Scope: scope[f]})
	}
	return hints, cut
}

// vendored reports whether a slash-separated relative directory lies in
// someone else's code (vendor/, node_modules/) or in .git.
func vendored(dir string) bool {
	for _, part := range strings.Split(dir, "/") {
		if part == "vendor" || part == "node_modules" || part == ".git" {
			return true
		}
	}
	return false
}

// ScopedRules hands the brain the rules files that govern a subdirectory of
// cwd — an AGENTS.md / CLAUDE.md / .claude/CLAUDE.md / CLAUDE.local.md below
// it, which the instructions only name — with the first call that touches
// that directory, the way Claude Code loads a nested CLAUDE.md once it reads a
// file there. Looked up on disk at call time, so a rules file written during
// the session counts too. Each file is handed out once; the ones inlined in
// the instructions never. A nil *ScopedRules hands out nothing.
type ScopedRules struct {
	root  string
	round int
	given map[string]int  // path -> round it reached the brain (-1: in the instructions)
	texts map[string]bool // contents already with the brain
}

// Scoped starts the hand-out for this session: rooted at cwd, with every file
// the instructions inline already counted as seen.
func (s *System) Scoped() *ScopedRules {
	r := &ScopedRules{root: filepath.Clean(s.Cwd), given: map[string]int{}, texts: map[string]bool{}}
	for _, g := range s.Guidance {
		r.given[filepath.Clean(g.Path)] = -1
		r.texts[g.raw] = true
	}
	return r
}

// NextRound marks the start of a new batch of calls: rules handed out in an
// earlier round have been seen by the brain when it wrote this batch.
func (r *ScopedRules) NextRound() {
	if r != nil {
		r.round++
	}
}

// Saw records that the brain gets path's full text right now (a local_read of
// the rules file itself), so it is not handed out again.
func (r *ScopedRules) Saw(path string) {
	if r == nil {
		return
	}
	path = filepath.Clean(path)
	if _, ok := r.given[path]; !ok && r.governs(path) {
		r.given[path] = r.round
		if b, err := os.ReadFile(path); err == nil {
			r.texts[string(b)] = true
		}
	}
}

// Take renders the rules files governing the given absolute paths that the
// brain has not seen yet, for a tool result's context ("" when there are
// none), and marks them seen. fresh reports whether any of the governing
// files reached the brain only in this round — a write composed before it saw
// them has to wait (see dispatch).
func (r *ScopedRules) Take(paths ...string) (text string, fresh bool) {
	if r == nil {
		return "", false
	}
	var b strings.Builder
	total := 0
	for _, p := range paths {
		for _, f := range r.governing(p) {
			if at, ok := r.given[f]; ok {
				fresh = fresh || at == r.round
				continue
			}
			r.given[f] = r.round
			raw, err := os.ReadFile(f)
			if err != nil || len(bytes.TrimSpace(raw)) == 0 || r.texts[string(raw)] {
				continue
			}
			r.texts[string(raw)] = true
			fresh = true
			rel, _ := filepath.Rel(r.root, f)
			scope := filepath.ToSlash(filepath.Dir(rel))
			scope = strings.TrimSuffix(scope, "/.claude") + "/"
			g := capGuide(rel, raw, &total)
			b.WriteString("### " + rel + " — governs " + scope + "\n\n" + strings.TrimSpace(g.text) + "\n\n")
		}
	}
	if b.Len() == 0 {
		return "", fresh
	}
	return "[hermes-hands: the rules for the directory this call touched — binding there, in addition to the operator rules in your instructions; shown once]\n\n" +
		strings.TrimRight(b.String(), "\n"), fresh
}

// governing lists the rules files on disk in the directories from just below
// root down to p's own (p itself when it is a directory), shallow first.
// Nothing at or above root (the instructions carry those), nothing vendored.
func (r *ScopedRules) governing(p string) []string {
	rel, err := filepath.Rel(r.root, filepath.Clean(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		parts = parts[:len(parts)-1] // a file (or one still to be written): its directory
	}
	var out []string
	d := r.root
	for _, part := range parts {
		if vendored(part) {
			break
		}
		d = filepath.Join(d, part)
		for _, name := range guidanceNames {
			f := filepath.Join(d, name)
			if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() {
				out = append(out, f)
			}
		}
	}
	return out
}

// governs reports whether path is one of the rules files below root.
func (r *ScopedRules) governs(path string) bool {
	for _, f := range r.governing(path) {
		if f == path {
			return true
		}
	}
	return false
}

// capGuide wraps one rules file, cut to maxGuideFile, and not inlined at all
// once the running total would pass maxGuideTotal.
func capGuide(path string, b []byte, total *int) GuideFile {
	text := string(b)
	if len(text) > maxGuideFile {
		text = text[:maxGuideFile] + "\n[… cut by hermes-hands — read the file for the rest]"
	}
	if *total+len(text) > maxGuideTotal {
		return GuideFile{Path: path, Bytes: len(b), raw: string(b), text: "[not inlined — over the guidance budget; read the file]"}
	}
	*total += len(text)
	return GuideFile{Path: path, Bytes: len(b), raw: string(b), text: text}
}

// UseGlobal puts the operator's global rules — the file `hermes-hands setup`
// imported from the home directory (GlobalGuidancePath) — in front of the
// repo's. A missing or empty file means none were imported.
func (s *System) UseGlobal(path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return
	}
	total := 0
	g := capGuide(path, b, &total)
	if len(b) > maxGuideFile { // the global file is one import of several sources — allow the full budget
		g.text = string(b)
		if len(g.text) > maxGuideTotal {
			g.text = g.text[:maxGuideTotal] + "\n[… cut by hermes-hands — read the file for the rest]"
		}
	}
	g.Global = true
	s.Guidance = append([]GuideFile{g}, s.Guidance...)
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
// probe — but a stale "missing" is dropped the moment the tool is on PATH
// again. Freshness always wins over memory. Tools outside the fixed probe list
// get their own probe here; otherwise a 127 learned once would stick forever,
// even after the tool was installed.
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
		probed, inProbe := s.Tools[tool]
		if probed {
			continue // probe says it exists now — stale fact dropped
		}
		if !inProbe && probeTools([]string{tool})[tool] {
			continue // installed since — stale fact dropped
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
