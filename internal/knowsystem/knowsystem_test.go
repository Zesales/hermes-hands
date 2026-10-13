package knowsystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
