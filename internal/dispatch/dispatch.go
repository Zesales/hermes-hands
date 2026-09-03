// Package dispatch ports the router half of lib/dispatch.sh: it takes one tool
// call, applies the denylist / repo jail / approval gate, runs it (shell via
// internal/shell, file tools via stdlib), and returns {exit, output, context}.
// It is the piece that does NOT change when Hermes split-runtime lands — only
// the transport in front of it does.
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/shell"
)

// Result is HH_TOOL_EXIT / HH_TOOL_OUT / HH_TOOL_CTX.
type Result struct {
	Exit int
	Out  string
	Ctx  string
}

// ErrAbortTurn is returned when the operator answers [q] at an approval
// (bash `return 3`).
var ErrAbortTurn = errors.New("hermes-hands: turn aborted by operator")

// Dispatcher routes one tool call.
type Dispatcher struct {
	RepoRoot   string
	Shell      *shell.Shell
	Approver   prompt.Approver
	Deny       []string
	MaxOutput  int           // read_file byte cap (HERMES_HANDS_MAX_OUTPUT)
	RunTimeout time.Duration // default per shell command (HERMES_HANDS_RUN_TIMEOUT)
}

var timeoutRe = regexp.MustCompile(`^[0-9]+$`)

// Dispatch runs one tool call. tool may be an alias. A nil error means the turn
// continues (bash `return 0`); ErrAbortTurn means abort (bash `return 3`).
func (d *Dispatcher) Dispatch(tool string, rawArgs json.RawMessage) (Result, error) {
	args := parseArgs(rawArgs)

	switch tool {
	case "shell", "run", "bash", "exec", "terminal":
		return d.doShell(args)
	case "read_file", "read", "cat":
		return d.doRead(args), nil
	case "write_file", "write":
		return d.doWrite(args)
	case "edit_file", "edit":
		return d.doEdit(args)
	default:
		return Result{Exit: 2, Out: "unknown tool '" + tool + "' (have: shell, read_file, write_file, edit_file)"}, nil
	}
}

func (d *Dispatcher) doShell(args map[string]json.RawMessage) (Result, error) {
	cmd := argStr(args, "cmd", "command", "input")
	if cmd == "" {
		return Result{Exit: 2, Out: "shell: missing 'cmd'"}, nil
	}
	if reason, blocked := cmdBlocked(cmd, d.Deny); blocked {
		return Result{Exit: 126, Out: "(blocked by worker policy: " + reason + ")"}, nil
	}
	if runtime.GOOS == "windows" {
		return Result{Exit: 126, Out: "(blocked by worker policy: shell is unsupported on windows - use WSL)"}, nil
	}

	switch d.Approver.Confirm("shell: "+cmd, "") {
	case prompt.AbortTurn:
		return Result{}, ErrAbortTurn
	case prompt.Deny:
		return Result{Exit: 125, Out: "(declined by operator)"}, nil
	}

	timeout := d.RunTimeout
	if t := argStr(args, "timeout"); timeoutRe.MatchString(t) {
		n, _ := strconv.Atoi(t)
		timeout = time.Duration(n) * time.Second
	}

	out, exit := d.Shell.Run(cmd, timeout)
	res := Result{Exit: exit, Out: out}
	if exit != 0 {
		res.Ctx = d.failCtx(cmd)
	}
	return res, nil
}

// failCtx ports the auto-context attached after a non-zero shell exit: cwd +
// `git status -s` (capped in-shell at 1200 bytes), then `make` targets when the
// failed command contained " make ". Both run in the same persistent shell,
// 15s each, output only.
func (d *Dispatcher) failCtx(cmd string) string {
	ctx, _ := d.Shell.Run(`echo "cwd: $(pwd)"; git status -s 2>/dev/null | head -c 1200`, 15*time.Second)
	if strings.Contains(" "+cmd+" ", " make ") {
		targets, _ := d.Shell.Run(
			`make -pRrq : 2>/dev/null | awk -F: "/^[a-zA-Z0-9][^\$#/\t=]*:/{print \$1}" | sort -u`,
			15*time.Second)
		ctx += "\n--- make targets ---\n" + targets
	}
	return ctx
}

func (d *Dispatcher) doRead(args map[string]json.RawMessage) Result {
	p := argStr(args, "path", "file")
	if p == "" {
		return Result{Exit: 2, Out: "read_file: missing 'path'"}
	}
	abs, err := resolveInRepo(d.RepoRoot, p)
	if err != nil {
		return Result{Exit: 1, Out: fmt.Sprintf("refused: '%s' resolves outside the repo root (%s)", p, d.RepoRoot)}
	}
	// bash: `_hh_cap "$(timeout ... cat -- "$abs" 2>&1)"` — the cat error text
	// lands in the output and _hh_cap masks the exit to 0.
	if fi, statErr := os.Stat(abs); statErr == nil && fi.IsDir() {
		return Result{Exit: 0, Out: "cat: " + abs + ": Is a directory"}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Exit: 0, Out: "cat: " + abs + ": " + catErr(err)}
	}
	return Result{Exit: 0, Out: capBytes(string(b), d.MaxOutput)}
}

func (d *Dispatcher) doWrite(args map[string]json.RawMessage) (Result, error) {
	p := argStr(args, "path", "file")
	content := argStr(args, "content", "text")
	if p == "" {
		return Result{Exit: 2, Out: "write_file: missing 'path'"}, nil
	}
	abs, err := resolveInRepo(d.RepoRoot, p)
	if err != nil {
		return Result{Exit: 1, Out: "refused: '" + p + "' outside repo root"}, nil
	}

	var diff string
	if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
		diff = unifiedDiff(p, abs, content)
	} else {
		diff = fmt.Sprintf("(new file, %d lines)", strings.Count(content, "\n"))
	}

	switch d.Approver.Confirm("write_file: "+p, diff) {
	case prompt.AbortTurn:
		return Result{}, ErrAbortTurn
	case prompt.Deny:
		return Result{Exit: 125, Out: "(declined by operator)"}, nil
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Result{Exit: 1, Out: "write failed: " + p}, nil
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return Result{Exit: 1, Out: "write failed: " + p}, nil
	}
	n := int64(len(content))
	if fi, err := os.Stat(abs); err == nil {
		n = fi.Size()
	}
	return Result{Exit: 0, Out: fmt.Sprintf("wrote %s (%d bytes)", p, n)}, nil
}

func (d *Dispatcher) doEdit(args map[string]json.RawMessage) (Result, error) {
	p := argStr(args, "path", "file")
	old := argStr(args, "old", "find")
	newS := argStr(args, "new", "replace")
	if p == "" || old == "" {
		return Result{Exit: 2, Out: "edit_file: needs 'path' and 'old'"}, nil
	}
	abs, err := resolveInRepo(d.RepoRoot, p)
	if err != nil {
		return Result{Exit: 1, Out: "refused: '" + p + "' outside repo root"}, nil
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return Result{Exit: 1, Out: "edit_file: no such file: " + p}, nil
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Exit: 1, Out: "edit_file: no such file: " + p}, nil
	}
	content := string(b)

	// Literal, exactly-once replace: drop the old regex-substitution
	// `awk sub()` regex + `&` interpretation.
	if n := strings.Count(content, old); n != 1 {
		return Result{Exit: 1, Out: fmt.Sprintf("edit_file: 'old' matched %d times in %s (need exactly 1)", n, p)}, nil
	}
	updated := strings.Replace(content, old, newS, 1)
	diff := unifiedDiff(p, abs, updated)

	switch d.Approver.Confirm("edit_file: "+p, diff) {
	case prompt.AbortTurn:
		return Result{}, ErrAbortTurn
	case prompt.Deny:
		return Result{Exit: 125, Out: "(declined by operator)"}, nil
	}

	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return Result{Exit: 1, Out: "edit failed: " + p}, nil
	}
	return Result{Exit: 0, Out: "edited " + p}, nil
}

// --- helpers ---

func parseArgs(raw json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return m
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

// argStr mirrors `jq -r '.a // .b // ""'`: the first key that is present and
// not null/false, as a string (a non-string value yields its raw form, as
// `jq -r` would print it).
func argStr(args map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := args[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(string(raw))
		if s == "null" || s == "false" {
			continue
		}
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return str
		}
		return s
	}
	return ""
}

func capBytes(s string, n int) string {
	if n >= 0 && len(s) > n {
		return s[:n]
	}
	return s
}

func catErr(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied"
	default:
		return err.Error()
	}
}

// unifiedDiff shells to `diff -u` exactly as bash did (labels a/<p>, b/<p>;
// new content on stdin), capped at 4000 bytes. Absent `diff`, the preview is
// empty — the write still proceeds. This preview never leaves the machine.
func unifiedDiff(pathLabel, oldPath, newContent string) string {
	cmd := exec.Command("diff", "-u", "--label", "a/"+pathLabel, oldPath, "--label", "b/"+pathLabel, "-")
	cmd.Stdin = strings.NewReader(newContent)
	out, _ := cmd.Output() // diff exits 1 when files differ; ignore it and stderr
	return capBytes(string(out), 4000)
}
