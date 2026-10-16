package knowsystem

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

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

// LineOverlap is the share of a's distinct non-empty lines that also appear
// as a whole line in b: both sides are cut with strings.TrimSpace, so
// indentation, CRLF and blank lines never matter. 0 when a has no non-empty
// line. A line repeated in a counts once, so a file that repeats itself does
// not inflate the share. A short line of a that merely sits INSIDE a longer
// line of b is not a restatement and does not count.
//
// It measures near-duplicates, not identity — meant for the global rules
// import, to warn (from 0.8) when one imported file mostly restates another,
// e.g. an older copy of the same rules. Not wired in yet.
func LineOverlap(a, b string) float64 {
	want := map[string]bool{}
	for _, l := range strings.Split(a, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			want[l] = true
		}
	}
	if len(want) == 0 {
		return 0
	}
	have := map[string]bool{}
	for _, l := range strings.Split(b, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			have[l] = true
		}
	}
	hits := 0
	for l := range want {
		if have[l] {
			hits++
		}
	}
	return float64(hits) / float64(len(want))
}
