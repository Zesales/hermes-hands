package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// resolveInRepo ports _hh_resolve_in_repo: make the path absolute (relative to
// repoRoot), resolve its PARENT physically (symlinks resolved, like
// `cd $(dirname) && pwd -P`) and re-append the literal basename, then require
// the result to be repoRoot itself or strictly inside it.
func resolveInRepo(repoRoot, p string) (string, error) {
	abs := p
	if !filepath.IsAbs(p) {
		abs = repoRoot + "/" + p
	}
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)

	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err // parent missing / unreadable -> bash `cd` fails
	}
	if fi, err := os.Stat(realParent); err != nil || !fi.IsDir() {
		return "", errors.New("parent is not a directory")
	}

	resolved := realParent + "/" + base
	if resolved == repoRoot || strings.HasPrefix(resolved, repoRoot+"/") {
		return resolved, nil
	}
	return "", errors.New("resolves outside the repo root")
}
