package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PhysicalCwd is the working directory with symlinks resolved — the repo
// root the hands work in.
func PhysicalCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if p, err := filepath.EvalSymlinks(wd); err == nil {
		return p
	}
	return wd
}

// gitBranch is the current branch name at root, or "" when root is not a git
// work tree (or git is missing). Used only to enrich the per-turn frame.
func gitBranch(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" { // detached
		return ""
	}
	return b
}

// pruneFiles deletes the files in dir older than maxAge, then dir itself once
// it is empty.
func pruneFiles(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && e.Type().IsRegular() && time.Since(fi.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails (and is meant to) while anything is left
}
