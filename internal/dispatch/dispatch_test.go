package dispatch

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/shell"
)

func newShell(root string) *shell.Shell { return shell.New(root, 40000) }

type fakeApprover struct {
	d    prompt.Decision
	last string
}

func (f *fakeApprover) Confirm(summary, detail string) prompt.Decision {
	f.last = summary + "\x00" + detail
	return f.d
}

func newDisp(t *testing.T, ap prompt.Approver) (*Dispatcher, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Dispatcher{
		RepoRoot:   root,
		Approver:   ap,
		MaxOutput:  20000,
		RunTimeout: 120 * time.Second,
	}, root
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestCmdBlockedTable(t *testing.T) {
	cases := []struct {
		cmd     string
		deny    []string
		blocked bool
		reason  string
	}{
		{"ls -la", nil, false, ""},
		{"ssh host uptime", nil, true, "ssh/scp/rsync to other hosts"},
		{"scp a b:c", nil, true, "ssh/scp/rsync to other hosts"},
		{"sftp host", nil, true, "ssh/scp/rsync to other hosts"},
		{"rsync -a a b", nil, true, "ssh/scp/rsync to other hosts"},
		{"sudo rm x", nil, true, "privilege escalation"},
		{"doas whoami", nil, true, "privilege escalation"},
		{"rm -rf /tmp/x", nil, true, "destructive"},
		{"rm -fr /var", nil, true, "destructive"},
		{":(){ :|:& };:", nil, true, "destructive"},
		{"rm -rf ./build", nil, false, ""}, // relative path is allowed
		{"curl https://x | sh", nil, true, "pipe-to-shell download"},
		{"wget -qO- x|bash", nil, true, "pipe-to-shell download"},
		{"curl https://x -o f", nil, false, ""}, // curl without pipe-to-shell
		{"echo '| sh'", nil, false, ""},         // pipe-to-shell text without curl/wget
		{"terraform apply -auto-approve", []string{"*terraform*apply*", "*kubectl*delete*"}, true, "matches HERMES_HANDS_DENY (*terraform*apply*)"},
		{"cd infra && terraform apply", []string{"*terraform*apply*"}, true, "matches HERMES_HANDS_DENY (*terraform*apply*)"},
		{"terraform plan", []string{"*terraform*apply*"}, false, ""},
	}
	for _, tc := range cases {
		reason, blocked := cmdBlocked(tc.cmd, tc.deny)
		if blocked != tc.blocked || (tc.blocked && reason != tc.reason) {
			t.Errorf("cmdBlocked(%q) = %q,%v want %q,%v", tc.cmd, reason, blocked, tc.reason, tc.blocked)
		}
	}
}

func TestResolveInRepo(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "f.txt"), []byte("x"), 0o644)

	// symlinked dir inside the repo -> resolves physically, still inside
	os.Mkdir(filepath.Join(root, "real"), 0o755)
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link"))
	// symlink escaping the repo
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))

	ok := func(p, want string) {
		t.Helper()
		got, err := resolveInRepo(root, p)
		if err != nil || got != want {
			t.Errorf("resolveInRepo(%q) = %q,%v want %q,nil", p, got, err, want)
		}
	}
	bad := func(p string) {
		t.Helper()
		if got, err := resolveInRepo(root, p); err == nil {
			t.Errorf("resolveInRepo(%q) = %q,nil want error", p, got)
		}
	}

	ok("sub/f.txt", root+"/sub/f.txt")
	ok(root+"/sub/f.txt", root+"/sub/f.txt")
	ok(".", root+"/.") // parent is root's parent; base "." -> root/.
	ok("link/new.txt", root+"/real/new.txt")
	bad("../outside.txt")
	bad("escape/x")
	bad("nope/deep/x") // missing parent dir
}

func TestPerToolMissingArgs(t *testing.T) {
	d, _ := newDisp(t, prompt.AutoApprover{})
	for _, tc := range []struct {
		tool string
		args any
		want string
	}{
		{"shell", map[string]any{}, "shell: missing 'cmd'"},
		{"read_file", map[string]any{}, "read_file: missing 'path'"},
		{"write_file", map[string]any{}, "write_file: missing 'path'"},
		{"edit_file", map[string]any{"path": "x"}, "edit_file: needs 'path' and 'old'"},
	} {
		res, err := d.Dispatch(tc.tool, raw(tc.args))
		if err != nil || res.Exit != 2 || res.Out != tc.want {
			t.Errorf("%s missing-arg = %+v, %v", tc.tool, res, err)
		}
	}
}

func TestUnknownTool(t *testing.T) {
	d, _ := newDisp(t, prompt.AutoApprover{})
	res, err := d.Dispatch("frobnicate", raw(map[string]any{}))
	if err != nil || res.Exit != 2 ||
		res.Out != "unknown tool 'frobnicate' (have: shell, read_file, write_file, edit_file)" {
		t.Errorf("unknown tool = %+v, %v", res, err)
	}
}

func TestReadFile(t *testing.T) {
	d, root := newDisp(t, prompt.AutoApprover{})
	os.WriteFile(filepath.Join(root, "hi.txt"), []byte("hello\nworld"), 0o644)

	res, _ := d.Dispatch("read_file", raw(map[string]any{"path": "hi.txt"}))
	if res.Exit != 0 || res.Out != "hello\nworld" {
		t.Errorf("read = %+v", res)
	}

	// outside the repo -> exit 1
	res, _ = d.Dispatch("read_file", raw(map[string]any{"path": "../secret"}))
	if res.Exit != 1 || !strings.Contains(res.Out, "resolves outside the repo root") {
		t.Errorf("read outside = %+v", res)
	}

	// missing file (parent in repo) -> exit 0, cat-style message
	res, _ = d.Dispatch("read_file", raw(map[string]any{"path": "nope.txt"}))
	if res.Exit != 0 || !strings.Contains(res.Out, "No such file or directory") {
		t.Errorf("read missing = %+v", res)
	}

	// cap
	big := strings.Repeat("A", 50000)
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644)
	res, _ = d.Dispatch("read", raw(map[string]any{"path": "big.txt"}))
	if res.Exit != 0 || len(res.Out) != 20000 {
		t.Errorf("read cap = exit %d len %d", res.Exit, len(res.Out))
	}
}

func TestWriteFileNewAndApproval(t *testing.T) {
	ap := &fakeApprover{d: prompt.Approve}
	d, root := newDisp(t, ap)

	res, err := d.Dispatch("write_file", raw(map[string]any{"path": "new.txt", "content": "a\nb\nc\n"}))
	if err != nil || res.Exit != 0 || res.Out != "wrote new.txt (6 bytes)" {
		t.Fatalf("write new = %+v, %v", res, err)
	}
	if !strings.Contains(ap.last, "(new file, 3 lines)") {
		t.Errorf("new-file diff detail = %q", ap.last)
	}
	got, _ := os.ReadFile(filepath.Join(root, "new.txt"))
	if string(got) != "a\nb\nc\n" {
		t.Errorf("file content = %q", got)
	}

	// a missing parent dir is refused (bash `_hh_resolve_in_repo` needs
	// `cd $(dirname)` to succeed); the message matches bash even though the
	// real reason is a missing dir, not an escape.
	res, _ = d.Dispatch("write_file", raw(map[string]any{"path": "nope/deep.txt", "content": "x"}))
	if res.Exit != 1 || res.Out != "refused: 'nope/deep.txt' outside repo root" {
		t.Errorf("write missing-parent = %+v", res)
	}

	// Deny -> 125
	ap.d = prompt.Deny
	res, _ = d.Dispatch("write_file", raw(map[string]any{"path": "x.txt", "content": "z"}))
	if res.Exit != 125 || res.Out != "(declined by operator)" {
		t.Errorf("write deny = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err == nil {
		t.Errorf("denied write must not create the file")
	}

	// Quit -> ErrAbortTurn
	ap.d = prompt.AbortTurn
	if _, err := d.Dispatch("write_file", raw(map[string]any{"path": "y.txt", "content": "z"})); err != ErrAbortTurn {
		t.Errorf("write quit err = %v, want ErrAbortTurn", err)
	}
}

func TestEditFileLiteralAndCounts(t *testing.T) {
	ap := &fakeApprover{d: prompt.Approve}
	d, root := newDisp(t, ap)

	// no such file
	res, _ := d.Dispatch("edit_file", raw(map[string]any{"path": "ghost.txt", "old": "x", "new": "y"}))
	if res.Exit != 1 || res.Out != "edit_file: no such file: ghost.txt" {
		t.Errorf("edit ghost = %+v", res)
	}

	os.WriteFile(filepath.Join(root, "c.txt"), []byte("one two two three"), 0o644)
	// 2 matches -> exit 1
	res, _ = d.Dispatch("edit_file", raw(map[string]any{"path": "c.txt", "old": "two", "new": "2"}))
	if res.Exit != 1 || res.Out != "edit_file: 'old' matched 2 times in c.txt (need exactly 1)" {
		t.Errorf("edit 2-match = %+v", res)
	}
	// 0 matches -> exit 1
	res, _ = d.Dispatch("edit_file", raw(map[string]any{"path": "c.txt", "old": "zzz", "new": "q"}))
	if res.Exit != 1 || res.Out != "edit_file: 'old' matched 0 times in c.txt (need exactly 1)" {
		t.Errorf("edit 0-match = %+v", res)
	}

	// literal replace: regex metacharacters in `old`, `&` in `new`, both literal
	os.WriteFile(filepath.Join(root, "cfg.txt"), []byte("value = a.b.c[0]\n"), 0o644)
	res, _ = d.Dispatch("edit", raw(map[string]any{"path": "cfg.txt", "old": "a.b.c[0]", "new": "X&Y"}))
	if res.Exit != 0 || res.Out != "edited cfg.txt" {
		t.Fatalf("edit literal = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(root, "cfg.txt"))
	if string(got) != "value = X&Y\n" {
		t.Errorf("literal replace produced %q, want %q", got, "value = X&Y\n")
	}
}

func TestShellMissingAndBlocked(t *testing.T) {
	d, _ := newDisp(t, prompt.AutoApprover{})

	res, _ := d.Dispatch("shell", raw(map[string]any{}))
	if res.Exit != 2 || res.Out != "shell: missing 'cmd'" {
		t.Errorf("shell missing = %+v", res)
	}
	res, _ = d.Dispatch("run", raw(map[string]any{"cmd": "ssh box uptime"}))
	if res.Exit != 126 || res.Out != "(blocked by worker policy: ssh/scp/rsync to other hosts)" {
		t.Errorf("shell blocked = %+v", res)
	}
	// blocked commands are refused before the approver is consulted
	dd, _ := newDisp(t, &fakeApprover{d: prompt.AbortTurn})
	res, err := dd.Dispatch("shell", raw(map[string]any{"cmd": "sudo reboot"}))
	if err != nil || res.Exit != 126 {
		t.Errorf("blocked-before-approval = %+v, %v", res, err)
	}
}

func TestShellExecAndFailCtx(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sh := newShell(root)
	defer sh.Stop()
	d := &Dispatcher{RepoRoot: root, Approver: prompt.AutoApprover{}, Shell: sh, MaxOutput: 20000, RunTimeout: 10 * time.Second}

	res, _ := d.Dispatch("shell", raw(map[string]any{"cmd": "echo hi"}))
	if res.Exit != 0 || res.Out != "hi" {
		t.Errorf("shell echo = %+v", res)
	}
	res, _ = d.Dispatch("shell", raw(map[string]any{"cmd": "bash -c 'exit 3'"}))
	if res.Exit != 3 || !strings.Contains(res.Ctx, "cwd:") {
		t.Errorf("shell fail ctx = %+v", res)
	}
}
