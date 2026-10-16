package knowsystem

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

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
