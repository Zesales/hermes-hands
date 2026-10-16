package knowsystem

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeRepoSignalsAndTargets(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(
		"VAR = x\n\nbuild:  ## build\n\tgo build .\n\ntest: ## tests\n\tgo test ./...\n\n.PHONY: build test\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644)

	s := Probe(dir, "main")
	if s.OS == "" || s.Arch == "" {
		t.Fatalf("probe missing os/arch: %+v", s)
	}
	if s.Cwd != dir || s.GitBranch != "main" {
		t.Fatalf("probe cwd/branch: %q %q", s.Cwd, s.GitBranch)
	}
	want := map[string]bool{"Makefile": false, "go.mod": false}
	for _, f := range s.RepoSignals {
		want[f] = true
	}
	for f, ok := range want {
		if !ok {
			t.Errorf("repo signal %s not found in %v", f, s.RepoSignals)
		}
	}
	targets := strings.Join(s.MakeTargets, " ")
	for _, want := range []string{"build", "test"} {
		if !strings.Contains(targets, want) {
			t.Errorf("make target %q missing from %v", want, s.MakeTargets)
		}
	}
	if strings.Contains(targets, "VAR") || strings.Contains(targets, "PHONY") {
		t.Errorf("make target scan picked up non-targets: %v", s.MakeTargets)
	}
	if len(s.Tools) == 0 {
		t.Fatal("no tools probed")
	}
}

func TestBriefCompact(t *testing.T) {
	s := Probe(t.TempDir(), "")
	b := s.Brief()
	if !strings.Contains(b, "Known system") {
		t.Fatalf("brief lacks header:\n%s", b)
	}
	if !strings.Contains(b, "tools on PATH:") {
		t.Fatalf("brief lacks tool list:\n%s", b)
	}
	if lines := strings.Count(b, "\n"); lines > 10 {
		t.Errorf("brief too long (%d lines) — injection must stay compact:\n%s", lines, b)
	}
}

func TestObserveShellLearned(t *testing.T) {
	s := Probe(t.TempDir(), "")
	out := "bash: line 1: frobnicate: command not found"
	if !s.ObserveShell(out, 127) {
		t.Fatal("127 not recorded")
	}
	if s.ObserveShell(out, 127) {
		t.Fatal("second identical 127 reported a change")
	}
	if s.ObserveShell("bash: line 1: nope: command not found", 2) {
		t.Fatal("non-127 exit recorded")
	}
	if s.ObserveShell("total 12\nfile.txt", 0) {
		t.Fatal("success recorded")
	}
	if !strings.Contains(s.Brief(), "frobnicate") {
		t.Errorf("brief does not surface learned-missing:\n%s", s.Brief())
	}
}

func TestMergeLearnedFreshness(t *testing.T) {
	// Freshness rule: a stale "missing" is dropped the moment the fresh probe
	// sees the tool on PATH; unknown tools stay learned.
	fresh := &System{Tools: map[string]bool{"git": true, "node": false}}
	old := &System{Learned: map[string]string{
		"git":        "missing", // installed since the old session — must drop
		"node":       "missing", // still absent — must stay
		"frobnicate": "missing", // absent from the fresh probe map — must stay
	}}
	fresh.MergeLearned(old)
	if fresh.Learned["git"] != "" {
		t.Errorf("stale missing for git not dropped despite probe: %+v", fresh.Learned)
	}
	if fresh.Learned["node"] != "missing" || fresh.Learned["frobnicate"] != "missing" {
		t.Errorf("learned facts lost: %+v", fresh.Learned)
	}
}

func TestMergeLearnedUnprobedToolInstalled(t *testing.T) {
	// A learned tool outside the fixed probe list must not stick forever: once
	// it is on PATH, the stale "missing" is dropped.
	orig := probeTools
	t.Cleanup(func() { probeTools = orig })
	probeTools = func(names []string) map[string]bool {
		found := map[string]bool{}
		for _, n := range names {
			found[n] = n == "terraform"
		}
		return found
	}
	fresh := &System{Tools: map[string]bool{"git": true}}
	old := &System{Learned: map[string]string{"terraform": "missing", "frobnicate": "missing"}}
	fresh.MergeLearned(old)
	if fresh.Learned["terraform"] != "" {
		t.Errorf("installed unprobed tool still learned missing: %+v", fresh.Learned)
	}
	if fresh.Learned["frobnicate"] != "missing" {
		t.Errorf("still-absent unprobed tool lost: %+v", fresh.Learned)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known-system.json")

	s := Probe(dir, "")
	s.ObserveShell("bash: line 1: frobnicate: command not found", 127)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("known-system.json perms: %+v err %v", fi, err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Learned["frobnicate"] != "missing" {
		t.Fatalf("round-trip lost learned fact: %+v", loaded.Learned)
	}
	fresh := Probe(dir, "")
	fresh.MergeLearned(loaded)
	if fresh.Learned["frobnicate"] != "missing" {
		t.Fatalf("learned fact not carried into next session: %+v", fresh.Learned)
	}
}

func TestProbeToolsUsesLoginShell(t *testing.T) {
	// what a hands command finds is what counts: bash itself must resolve, a
	// made-up name must not, and a profile's stdout chatter is ignored
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	got := probeTools([]string{"bash", "definitely-not-a-tool-xyz"})
	if !got["bash"] || got["definitely-not-a-tool-xyz"] {
		t.Errorf("probeTools = %v", got)
	}
	// a missing LAST name used to end the script with exit 1 -> the probe
	// silently fell back to the process PATH; it must stay in the login shell
	start := time.Now()
	for i := 0; i < 5; i++ {
		probeTools([]string{"bash", "definitely-not-a-tool-xyz"})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("5 probes took %v — the login-shell probe is falling back", d)
	}
}

func TestMakeTargetsWithHelp(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(
		"X := 1\nhelp:  ## this list\n\t@x\ntest: | $(C)  ## go test in the pinned container\n\tgo test\nall:\n\techo\n"), 0o644)
	names, help := makeTargets(dir)
	if strings.Join(names, " ") != "help test all" {
		t.Errorf("targets = %v", names)
	}
	if help["test"] != "go test in the pinned container" || help["help"] != "this list" || help["all"] != "" {
		t.Errorf("help = %v", help)
	}
	s := &System{MakeTargets: names, MakeHelp: help, Tools: map[string]bool{}}
	if !strings.Contains(s.Brief(), "  test — go test in the pinned container\n") {
		t.Errorf("brief does not carry make help:\n%s", s.Brief())
	}
}

func TestGuidanceIsRepoScoped(t *testing.T) {
	// the repo's rules only — root first, then the subdirectory's; nothing from
	// above the repo (global rules come from the setup import instead)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(parent, "CLAUDE.md"), []byte("above the repo — must not be read"), 0o644)
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("use make, not go directly"), 0o644)
	os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("use make, not go directly"), 0o644) // identical copy
	os.WriteFile(filepath.Join(repo, "sub", "AGENTS.md"), []byte(strings.Repeat("x", maxGuideFile+100)), 0o644)

	g, _, _ := guidance(filepath.Join(repo, "sub"))
	if len(g) != 2 || g[0].Path != filepath.Join(repo, "AGENTS.md") || g[1].Path != filepath.Join(repo, "sub", "AGENTS.md") {
		t.Fatalf("guidance = %+v", g)
	}
	if !strings.Contains(g[1].text, "cut by hermes-hands") {
		t.Error("an oversized file must be cut")
	}
	// outside a git work tree: only cwd's own files
	plain := t.TempDir()
	os.WriteFile(filepath.Join(plain, "AGENTS.md"), []byte("plain rules"), 0o644)
	if g, h, _ := guidance(plain); len(g) != 1 || g[0].text != "plain rules" || len(h) != 0 {
		t.Errorf("non-git guidance = %+v %+v", g, h)
	}
}

// gitRepo makes a git work tree with the given files ("" content: the path is
// written but stays untracked); returns its root.
func gitRepo(t *testing.T, files map[string]string, tracked ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for f, body := range files {
		os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(repo, f), []byte(body), 0o644)
	}
	if len(tracked) > 0 {
		if out, err := exec.Command("git", append([]string{"-C", repo, "add", "--"}, tracked...)...).CombinedOutput(); err != nil {
			t.Fatalf("git add: %v %s", err, out)
		}
	}
	return repo
}

func TestGuidanceLocalAndDotClaude(t *testing.T) {
	// CLAUDE.local.md is personal and gitignored — read from disk all the
	// same, after CLAUDE.md; .claude/CLAUDE.md counts like CLAUDE.md
	repo := gitRepo(t, map[string]string{
		".gitignore":            "CLAUDE.local.md\n",
		"CLAUDE.md":             "team rules",
		"CLAUDE.local.md":       "my rules",
		"sub/.claude/CLAUDE.md": "sub rules",
	}, ".gitignore", "CLAUDE.md", "sub/.claude/CLAUDE.md")
	g, h, _ := guidance(filepath.Join(repo, "sub"))
	var got []string
	for _, f := range g {
		got = append(got, f.text)
	}
	if strings.Join(got, "|") != "team rules|my rules|sub rules" || len(h) != 0 {
		t.Fatalf("guidance = %v, hints %+v", got, h)
	}
	block := (&System{Guidance: g}).GuidanceBlock()
	if !strings.Contains(block, "personal rules for this repo, "+filepath.Join(repo, "CLAUDE.local.md")+" (not committed") {
		t.Errorf("CLAUDE.local.md not labelled as personal:\n%s", block)
	}
}

func TestGuidanceNamesNestedRules(t *testing.T) {
	// rules deeper in the tree are named with the directory they govern —
	// shallow first; vendored, ignored and deleted ones are not
	repo := gitRepo(t, map[string]string{
		".gitignore":                   "ignored/\n",
		"AGENTS.md":                    "root rules",
		"services/api/AGENTS.md":       "api rules",
		"services/api/CLAUDE.md":       "api claude rules",
		"web/CLAUDE.md":                "new, untracked, not ignored",
		"tools/.claude/CLAUDE.md":      "tools rules",
		"a/b/c/AGENTS.md":              "deep rules",
		"vendor/x/AGENTS.md":           "someone else's",
		"web/node_modules/p/CLAUDE.md": "someone else's",
		"ignored/AGENTS.md":            "ignored",
		"gone/AGENTS.md":               "deleted after commit",
		"docs/MY_AGENTS.md":            "not a rules file",
	}, ".gitignore", "AGENTS.md", "services", "tools", "a", "vendor", "gone", "docs")
	os.Remove(filepath.Join(repo, "gone", "AGENTS.md"))

	g, h, more := guidance(repo)
	if len(g) != 1 || g[0].text != "root rules" {
		t.Fatalf("inlined = %+v", g)
	}
	var got []string
	for _, x := range h {
		got = append(got, x.Path+" -> "+x.Scope)
	}
	want := "tools/.claude/CLAUDE.md -> tools/|web/CLAUDE.md -> web/|services/api/AGENTS.md -> services/api/|services/api/CLAUDE.md -> services/api/|a/b/c/AGENTS.md -> a/b/c/"
	if strings.Join(got, "|") != want || more != 0 {
		t.Fatalf("hints =\n%s\nwant\n%s (more %d)", strings.Join(got, "|"), want, more)
	}
	block := (&System{Guidance: g, MoreRules: h}).GuidanceBlock()
	if !strings.Contains(block, "- `services/api/AGENTS.md` — governs `services/api/`") || !strings.Contains(block, "held back until you have seen them") {
		t.Errorf("block:\n%s", block)
	}
	// from a subdirectory: only what lies below it, relative to it
	if _, h, _ := guidance(filepath.Join(repo, "services")); len(h) != 2 || h[0].Path != filepath.Join("api", "AGENTS.md") || h[0].Scope != "api/" {
		t.Errorf("hints from services/ = %+v", h)
	}
}

func TestGuidanceNestedCap(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < maxNested+3; i++ {
		files[fmt.Sprintf("m%02d/AGENTS.md", i)] = fmt.Sprintf("rules %d", i)
	}
	repo := gitRepo(t, files)
	_, h, more := guidance(repo)
	if len(h) != maxNested || more != 3 || h[0].Path != filepath.Join("m00", "AGENTS.md") {
		t.Fatalf("%d hints, %d more: %+v", len(h), more, h)
	}
	if block := (&System{MoreRules: h, MoreRulesN: more}).GuidanceBlock(); !strings.Contains(block, "- … 3 more — `git ls-files") {
		t.Errorf("block:\n%s", block)
	}
}

func TestGuidanceImports(t *testing.T) {
	// Claude Code's `@path` imports are part of the file that names them:
	// inlined after the rules files proper, resolved relative to the importing
	// file, followed into their own imports. Not imports: one of a file inlined
	// anyway, a code span, a fenced block, an @mention, an e-mail address; a
	// file outside the repo is not inlined
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.WriteFile(filepath.Join(home, "team.md"), []byte("team-wide rules"), 0o644)
	repo := gitRepo(t, map[string]string{
		"AGENTS.md": "shared rules",
		"CLAUDE.md": "@AGENTS.md\n\nSee @docs/style.md. Ask @someone, mail ops@docs/x.md.\n" +
			"Not `@docs/span.md`, and personal: @~/team.md\n```\n@docs/fenced.md\n```\n",
		"docs/style.md":  "style — see @more.md",
		"docs/more.md":   "more style",
		"docs/span.md":   "span",
		"docs/fenced.md": "fenced",
		"docs/x.md":      "x",
	})
	g, _, _ := guidance(repo)
	var got []string
	for _, f := range g {
		rel, _ := filepath.Rel(repo, f.Path)
		by := ""
		if f.ImportedBy != "" {
			by, _ = filepath.Rel(repo, f.ImportedBy)
		}
		got = append(got, rel+"<"+by)
	}
	want := "AGENTS.md<|CLAUDE.md<|" + filepath.Join("docs", "style.md") + "<CLAUDE.md|" +
		filepath.Join("docs", "more.md") + "<" + filepath.Join("docs", "style.md")
	if strings.Join(got, "|") != want {
		t.Fatalf("inlined =\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	block := (&System{Guidance: g}).GuidanceBlock()
	if !strings.Contains(block, "### the repo's "+filepath.Join(repo, "docs", "style.md")+" (imported by "+filepath.Join(repo, "CLAUDE.md")+" with `@`") ||
		strings.Contains(block, "team-wide") {
		t.Errorf("block:\n%s", block)
	}
}

func TestScopedRulesHandOut(t *testing.T) {
	// a subdirectory's rules reach the brain with the first call into it —
	// shallow first, once, never the ones the instructions inline, never
	// vendored ones; a rules file written mid-session counts too
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for f, body := range map[string]string{
		"CLAUDE.md":                      "root rules — and @services/db/AGENTS.md",
		"services/AGENTS.md":             "svc rules",
		"services/api/CLAUDE.md":         "api rules",
		"services/api/.claude/CLAUDE.md": "svc rules", // a copy: handed out once
		"services/db/AGENTS.md":          "db rules",  // imported by the root CLAUDE.md
		"services/web/AGENTS.md":         "web rules",
		"vendor/x/AGENTS.md":             "someone else's",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(root, f), []byte(body), 0o644)
	}
	g, _, _ := guidance(root)
	r := (&System{Cwd: root, Guidance: g}).Scoped()
	at := func(rel string) string { return filepath.Join(root, rel) }

	if text, _ := r.Take(at("main.go"), at("vendor/x/a.go")); text != "" {
		t.Errorf("root / vendored: %q", text)
	}
	text, fresh := r.Take(at("services/api/new.go")) // not written yet: its directory counts
	si, ai := strings.Index(text, "svc rules"), strings.Index(text, "api rules")
	if !fresh || si < 0 || ai < si || strings.Count(text, "svc rules") != 1 ||
		!strings.Contains(text, "### "+filepath.Join("services", "AGENTS.md")+" — governs services/") ||
		!strings.Contains(text, "governs services/api/") {
		t.Fatalf("take = %v %q", fresh, text)
	}
	if text, fresh := r.Take(at("services/api/x.go")); text != "" || !fresh {
		t.Errorf("same round again = %v %q", fresh, text)
	}
	r.NextRound()
	if text, fresh := r.Take(at("services/api/x.go")); text != "" || fresh {
		t.Errorf("next round = %v %q", fresh, text)
	}
	if text, _ := r.Take(at("services/db/q.sql")); text != "" {
		t.Errorf("inlined by import, handed out again: %q", text)
	}
	r.Saw(at("services/web/AGENTS.md")) // read in full by the brain
	if text, fresh := r.Take(at("services/web/x")); text != "" || !fresh {
		t.Errorf("after Saw = %v %q", fresh, text)
	}
	os.WriteFile(at("services/api/AGENTS.md"), []byte("written mid-session"), 0o644)
	if text, _ := r.Take(at("services/api")); !strings.Contains(text, "written mid-session") {
		t.Errorf("new rules file: %q", text)
	}
	var none *ScopedRules
	none.NextRound()
	none.Saw(at("x"))
	if text, fresh := none.Take(at("services/api/x.go")); text != "" || fresh {
		t.Error("a nil hand-out hands out nothing")
	}
}

func TestUseGlobalComesFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guidance.md")
	os.WriteFile(path, []byte("no ssh to the LAN"), 0o600)
	s := &System{Guidance: []GuideFile{{Path: "/r/AGENTS.md", text: "use make"}}}
	s.UseGlobal(path)
	s.UseGlobal(filepath.Join(t.TempDir(), "missing.md")) // none imported: no-op
	if len(s.Guidance) != 2 || !s.Guidance[0].Global {
		t.Fatalf("guidance = %+v", s.Guidance)
	}
	block := s.GuidanceBlock()
	gi, ri := strings.Index(block, "no ssh to the LAN"), strings.Index(block, "use make")
	if gi < 0 || ri < 0 || gi > ri || !strings.Contains(block, "imported by the operator at setup") || !strings.Contains(block, "binding") {
		t.Errorf("guidance block:\n%s", block)
	}
	if (&System{}).GuidanceBlock() != "" {
		t.Error("no rules, no block")
	}
}

func TestLayoutFromGitLsFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, f := range []string{"main.go", "internal/api/a.go", "internal/ui/u.go", "docs/x.md"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if got := layout(dir); got != "main.go | docs/ (1) | internal/{api,ui} (2)" {
		t.Errorf("layout = %q", got)
	}
	if layout(t.TempDir()) != "" {
		t.Error("no git work tree, no layout")
	}
}

func TestParseOSRelease(t *testing.T) {
	if got := parseOSRelease("NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"); got != "Ubuntu 24.04.1 LTS" {
		t.Errorf("PRETTY_NAME = %q", got)
	}
	if got := parseOSRelease("NAME=Alpine Linux\nVERSION_ID=3.20.3\n"); got != "Alpine Linux 3.20.3" {
		t.Errorf("NAME+VERSION_ID = %q", got)
	}
}

func TestBriefMacOS(t *testing.T) {
	// a macOS host: release + Apple Silicon, the system bash 3.2 caveats, the
	// BSD userland traps, the installed GNU variants, the package manager
	s := &System{OS: "darwin", Arch: "arm64", OSName: "macOS 15.1", Kernel: "Darwin 24.1.0",
		Bash: "3.2.57(1)-release", Userland: "bsd", GNUTools: []string{"gsed"}, PkgManager: "brew",
		Tools: map[string]bool{"bash": true}}
	b := s.Brief()
	for _, want := range []string{
		"host: macOS 15.1 — darwin arm64 (Apple Silicon), kernel Darwin 24.1.0",
		"shell: bash 3.2.57(1)-release", "no mapfile",
		"userland: BSD (macOS)", "`sed -i ''`", "GNU variants installed: gsed",
		"package manager: brew",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	linux := (&System{OS: "linux", Arch: "amd64", Userland: "gnu", Bash: "5.2.21(1)-release", Tools: map[string]bool{}}).Brief()
	if strings.Contains(linux, "userland") || strings.Contains(linux, "no mapfile") {
		t.Errorf("a GNU/bash-5 host needs no caveats:\n%s", linux)
	}
}

func TestLineOverlapNearDuplicateRules(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"a has no non-empty line", "  \n\t\n\n", "keep the rules short\n", 0},
		{"both empty", "", "", 0},
		{"identical", "use make, not raw go\nrun make ci before a PR\n", "use make, not raw go\nrun make ci before a PR\n", 1},
		{"a is a subset of b", "use make, not raw go\n", "global rules\n\nuse make, not raw go\nno pushes from the hands\n", 1},
		{"half of a is in b", "use make, not raw go\ncommit per repo\n", "use make, not raw go\nother line\n", 0.5},
		{"disjoint", "alpha\nbeta\n", "gamma\ndelta\n", 0},
		{"whitespace and blank lines ignored", "\t use make \n\n   commit per repo  \n", "use make\n   COMMIT   \ncommit per repo\n\n", 1},
		{"a repeated line counts once", "use make\nuse make\ncommit per repo\n", "use make\n", 0.5},
		{"crlf lines", "use make\r\ncommit per repo\r\n", "use make\ncommit per repo\n", 1},
		{"only inside a longer line is not a restatement", "make\n", "always run make ci before a PR\n", 0},
		{"warn threshold at 0.8", "l1\nl2\nl3\nl4\nl5\n", "l1\nl2\nl3\nl4\nelsewhere\n", 0.8},
	}
	for _, c := range cases {
		if got := LineOverlap(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: LineOverlap = %v, want %v", c.name, got, c.want)
		}
	}
}
