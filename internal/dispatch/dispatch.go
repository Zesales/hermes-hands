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
	"strconv"
	"strings"
	"time"

	"hermes-hands/internal/knowsystem"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/shell"
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
	// Rules hands out the rules files of a subdirectory with the first call
	// into it (nil: none). The loop calls NextRound before each batch.
	Rules *knowsystem.ScopedRules
	// Recall reads the stored output of an earlier call #ref of the open
	// session — what local_output serves (nil: the tool is unavailable).
	Recall func(ref int) (string, error)
}

// NextRound tells the rules hand-out that a new batch of calls begins.
func (d *Dispatcher) NextRound() {
	if d != nil {
		d.Rules.NextRound()
	}
}

var timeoutRe = regexp.MustCompile(`^[0-9]+$`)

// The hands' tools. Named local_* so the brain never mixes them up with its
// OWN server-side read_file / write_file / terminal; the old names stay as
// aliases.
const (
	Shell  = "local_shell"
	Read   = "local_read"
	Write  = "local_write"
	Edit   = "local_edit"
	Output = "local_output"
)

// Canonical maps a tool name or any alias to its local_* name ("" when it is
// no hands tool).
func Canonical(tool string) string {
	switch tool {
	case Shell, "shell", "run", "bash", "exec", "terminal":
		return Shell
	case Read, "read_file", "read", "cat":
		return Read
	case Write, "write_file", "write":
		return Write
	case Edit, "edit_file", "edit":
		return Edit
	case Output, "output", "recall":
		return Output
	}
	return ""
}

// Dispatch runs one tool call. tool may be an alias. A nil error means the turn
// continues (bash `return 0`); ErrAbortTurn means abort (bash `return 3`).
func (d *Dispatcher) Dispatch(tool string, rawArgs json.RawMessage) (Result, error) {
	args := parseArgs(rawArgs)

	switch Canonical(tool) {
	case Shell:
		return d.doShell(args)
	case Read:
		return d.doRead(args), nil
	case Write:
		return d.doWrite(args)
	case Edit:
		return d.doEdit(args)
	case Output:
		return d.doOutput(args), nil
	default:
		return Result{Exit: 2, Out: "unknown tool '" + tool + "' (the hands have: local_shell, local_read, local_write, local_edit, local_output)"}, nil
	}
}

// doOutput serves local_output: a slice of the stored output of an earlier
// call — the full copy hermes-hands keeps when it cut a round's results or
// elided an old one in a compacted transcript. It reads the hands' own
// record, which already passed the scrubber, so it asks no approval and runs
// nothing. offset / limit are bytes; limit defaults to (and is capped at)
// the per-result cap.
func (d *Dispatcher) doOutput(args map[string]json.RawMessage) Result {
	if d.Recall == nil {
		return Result{Exit: 2, Out: "local_output: no session output store here"}
	}
	ref, err := strconv.Atoi(strings.TrimPrefix(argStr(args, "ref", "id"), "#"))
	if err != nil || ref <= 0 {
		return Result{Exit: 2, Out: `local_output: missing 'ref' — the #ref of the call whose output to read, e.g. {"ref":14}`}
	}
	out, err := d.Recall(ref)
	if err != nil {
		return Result{Exit: 1, Out: fmt.Sprintf("local_output: no stored output for #%d", ref)}
	}
	offset, _ := strconv.Atoi(argStr(args, "offset"))
	offset = min(max(offset, 0), len(out))
	limit, _ := strconv.Atoi(argStr(args, "limit"))
	if limit <= 0 || (d.MaxOutput > 0 && limit > d.MaxOutput) {
		limit = d.MaxOutput
	}
	end := len(out)
	if limit > 0 {
		end = min(offset+limit, len(out))
	}
	head := fmt.Sprintf("[#%d: bytes %d–%d of %d]\n", ref, offset, end, len(out))
	if end < len(out) {
		return Result{Out: head + out[offset:end] + fmt.Sprintf("\n[… %d more bytes — offset %d reads on]", len(out)-end, end)}
	}
	return Result{Out: head + out[offset:end]}
}

func (d *Dispatcher) doShell(args map[string]json.RawMessage) (Result, error) {
	cmd := argStr(args, "cmd", "command", "input")
	if cmd == "" {
		return Result{Exit: 2, Out: "shell: missing 'cmd'"}, nil
	}
	if reason, blocked := cmdBlocked(cmd, d.Deny); blocked {
		return Result{Exit: 126, Out: "(blocked by worker policy: " + reason + ")"}, nil
	}

	switch d.Approver.Confirm("local_shell: "+cmd, "") {
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
	if d.Rules != nil {
		rules, _ := d.Rules.Take(d.shellPaths(cmd)...)
		res.Ctx = joinCtx(res.Ctx, rules)
	}
	return res, nil
}

// shellPaths are the places a shell command touched, as far as can be told
// from outside: the shell's directory after it ran (a `cd` persists) and every
// word of the command that names an existing path — relative to that
// directory or to the repo root; a path still to be created counts by its
// directory. A guess, which is why a shell call only brings rules along and is
// never held back.
func (d *Dispatcher) shellPaths(cmd string) []string {
	cwd := d.RepoRoot
	if out, exit := d.Shell.Run("pwd -P", 5*time.Second); exit == 0 && filepath.IsAbs(strings.TrimSpace(out)) {
		cwd = strings.TrimSpace(out)
	}
	paths := []string{cwd}
	for _, w := range shellWordRe.FindAllString(cmd, -1) {
		if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$~") {
			continue
		}
		if i := strings.IndexAny(w, "*?[{"); i >= 0 {
			w = w[:i] // a glob: its literal prefix
		}
		if w == "" {
			continue
		}
		for _, base := range []string{cwd, d.RepoRoot} {
			p := w
			if !filepath.IsAbs(p) {
				p = filepath.Join(base, p)
			}
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
				break
			}
			if dir := filepath.Dir(p); dir != base {
				if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
					paths = append(paths, dir)
					break
				}
			}
		}
	}
	return paths
}

var shellWordRe = regexp.MustCompile("[^\\s;|&<>()'\"`=]+")

func joinCtx(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}

// heldBack is the answer to a local_write / local_edit into a directory whose
// rules the brain had not seen when it wrote the call: nothing is changed and
// the operator is not asked; the rules come along, the brain sends the call
// again (or a corrected one).
func (d *Dispatcher) heldBack(abs, p string) (Result, bool) {
	rules, fresh := d.Rules.Take(abs)
	if !fresh {
		return Result{}, false
	}
	where := "they are in this result's context"
	if rules == "" {
		where = "they came with an earlier result of this batch"
	}
	return Result{Exit: 1, Ctx: rules, Out: "held back, nothing changed: '" + p + "' lies in a directory with its own rules," +
		" which you had not seen when you wrote this call — " + where + "." +
		" Check the change against them, then send the call again."}, true
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
	d.Rules.Saw(abs) // reading a rules file itself: it is with the brain now
	rules, _ := d.Rules.Take(abs)
	// bash: `_hh_cap "$(timeout ... cat -- "$abs" 2>&1)"` — the cat error text
	// lands in the output and _hh_cap masks the exit to 0.
	if fi, statErr := os.Stat(abs); statErr == nil && fi.IsDir() {
		return Result{Exit: 0, Out: "cat: " + abs + ": Is a directory", Ctx: rules}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Result{Exit: 0, Out: "cat: " + abs + ": " + catErr(err), Ctx: rules}
	}
	return Result{Exit: 0, Out: capBytes(string(b), d.MaxOutput), Ctx: rules}
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
	if res, held := d.heldBack(abs, p); held {
		return res, nil
	}
	if d.Planning() {
		return Result{Exit: 125, Out: notWritten}, nil
	}

	var diff string
	if fi, err := os.Stat(abs); err == nil && fi.Mode().IsRegular() {
		diff = unifiedDiff(p, abs, content)
	} else {
		diff = fmt.Sprintf("(new file, %d lines)", strings.Count(content, "\n"))
	}

	switch d.Approver.Confirm("local_write: "+p, diff) {
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
	if res, held := d.heldBack(abs, p); held {
		return res, nil
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
	if d.Planning() { // after the match check: a plan's edit that would not apply is worth knowing
		return Result{Exit: 125, Out: notWritten}, nil
	}
	updated := strings.Replace(content, old, newS, 1)
	diff := unifiedDiff(p, abs, updated)

	switch d.Approver.Confirm("local_edit: "+p, diff) {
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
