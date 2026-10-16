package knowsystem

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

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
