//go:build unix

package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
}

func TestCwdPersistsAcrossCalls(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := New(root, 40000)
	defer sh.Stop()

	if out, code := sh.Run("cd sub && pwd", 10*time.Second); code != 0 || !strings.HasSuffix(out, "/sub") {
		t.Fatalf("cd sub && pwd = %q, %d", out, code)
	}
	if out, code := sh.Run("pwd", 10*time.Second); code != 0 || !strings.HasSuffix(out, "/sub") {
		t.Errorf("second pwd = %q, %d — cwd did not persist", out, code)
	}
}

func TestExportedVarPersists(t *testing.T) {
	requireBash(t)
	sh := New(t.TempDir(), 40000)
	defer sh.Stop()

	sh.Run("export HH_TEST_VAR=bar123", 10*time.Second)
	if out, code := sh.Run("echo $HH_TEST_VAR", 10*time.Second); code != 0 || out != "bar123" {
		t.Errorf("echo $HH_TEST_VAR = %q, %d", out, code)
	}
}

func TestBashrcAliasAndFunction(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	rc := "alias hhgreet='echo hello-alias'\nhhfunc() { echo hello-func; }\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	sh := New(t.TempDir(), 40000)
	defer sh.Stop()

	if out, code := sh.Run("hhgreet", 10*time.Second); code != 0 || out != "hello-alias" {
		t.Errorf("alias from ~/.bashrc = %q, %d", out, code)
	}
	if out, code := sh.Run("hhfunc", 10*time.Second); code != 0 || out != "hello-func" {
		t.Errorf("function from ~/.bashrc = %q, %d", out, code)
	}
}

func TestExitCodePropagates(t *testing.T) {
	requireBash(t)
	sh := New(t.TempDir(), 40000)
	defer sh.Stop()

	if out, code := sh.Run("bash -c 'exit 7'", 10*time.Second); code != 7 {
		t.Errorf("exit-7 command = %q, code %d, want 7", out, code)
	}
	if _, code := sh.Run("true", 10*time.Second); code != 0 {
		t.Errorf("true = code %d, want 0", code)
	}
}

func TestKillingTheShellRecyclesIt(t *testing.T) {
	requireBash(t)
	sh := New(t.TempDir(), 40000)
	defer sh.Stop()

	// `exit` ends the login shell before the mark printf runs -> EOF -> 124.
	if _, code := sh.Run("exit", 10*time.Second); code != 124 {
		t.Errorf("bare `exit` = code %d, want 124", code)
	}
	// The next call must transparently get a fresh shell.
	if out, code := sh.Run("echo alive", 10*time.Second); code != 0 || out != "alive" {
		t.Errorf("after recycle, echo alive = %q, %d", out, code)
	}
}

func TestIdleTimeoutRecyclesAndLosesState(t *testing.T) {
	requireBash(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := New(root, 40000)
	defer sh.Stop()

	sh.Run("cd sub", 10*time.Second)

	out, code := sh.Run("sleep 5", 1*time.Second)
	if code != 124 {
		t.Errorf("sleep 5 @ 1s = code %d, want 124", code)
	}
	if !strings.Contains(out, "[timed out after 1s - shell was reset]") {
		t.Errorf("timeout text missing: %q", out)
	}

	// Fresh shell was re-primed and re-cd'd to the repo root: /sub is gone.
	if out, code := sh.Run("pwd", 10*time.Second); code != 0 || strings.HasSuffix(out, "/sub") {
		t.Errorf("after timeout, pwd = %q — state should have been lost", out)
	}
}

func TestOutputCappedButExitStillParsed(t *testing.T) {
	requireBash(t)
	sh := New(t.TempDir(), 200) // tiny cap
	defer sh.Stop()

	out, code := sh.Run(`for i in $(seq 1 400); do echo 01234567890123456789; done; echo TAIL_MARKER`, 15*time.Second)
	if code != 0 {
		t.Errorf("code = %d, want 0 (mark must still be found past the cap)", code)
	}
	if len(out) > 4000 {
		t.Errorf("output not capped: %d bytes", len(out))
	}
	if strings.Contains(out, "TAIL_MARKER") {
		t.Errorf("output past the cap should be dropped, but TAIL_MARKER is present")
	}
}
