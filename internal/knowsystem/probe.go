package knowsystem

import (
	"context"
	"os"
	"os/exec"
	"runtime"
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
