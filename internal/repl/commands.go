package repl

import (
	"context"
	"fmt"
	"strings"

	"hermes-hands/internal/app"
	"hermes-hands/internal/compact"
	"hermes-hands/internal/config"
)

// command is one slash command: its names and what it does with the rest of
// the input line.
type command struct {
	names []string
	run   func(r *repl, arg string)
}

// commands is every slash command; ui.Help lists them for the operator.
var commands = []command{
	{[]string{"/exit"}, func(r *repl, _ string) { r.quit = true }},
	{[]string{"/help", "/h", "/?"}, (*repl).help},
	{[]string{"/setup"}, (*repl).setup},
	{[]string{"/session"}, (*repl).session},
	{[]string{"/sessions"}, (*repl).sessions},
	{[]string{"/new"}, (*repl).newSession},
	{[]string{"/fork"}, (*repl).fork},
	{[]string{"/history", "/chat-history"}, (*repl).history},
	{[]string{"/out"}, (*repl).out},
	{[]string{"/approve"}, (*repl).approve},
	{[]string{"/yolo"}, (*repl).yolo},
	{[]string{"/debug"}, (*repl).debug},
	{[]string{"/check"}, (*repl).check},
	{[]string{"/config", "/hh-settings"}, (*repl).config},
	{[]string{"/compact", "/compress"}, (*repl).compact},
	{[]string{"/compact-aggressive"}, (*repl).compactAggressive},
	{[]string{"/context"}, (*repl).context},
}

// commandNamed finds a command by any of its names.
var commandNamed = func() map[string]command {
	m := map[string]command{}
	for _, c := range commands {
		for _, n := range c.names {
			m[n] = c
		}
	}
	return m
}()

func (r *repl) help(string) {
	r.a.UI.Help(r.a.RepoRoot)
	fmt.Fprintln(r.conv)
}

// setup re-runs the setup wizard and, when it configured something, rebuilds
// the app from the new configuration and reopens the session in it.
func (r *repl) setup(string) {
	if r.d.Setup() == 0 {
		if na, err := r.d.NewApp(); err == nil {
			r.a.Shell.Stop()
			r.a = na
			r.a.Bind(r.runner)
			r.wireApp()
			r.configured = r.a.Cfg.Configured()
			if _, err := r.a.Client.Server().Check(context.Background()); err != nil {
				fmt.Fprintf(r.conv, "BLOCKED: %v\n", err)
			}
			rec := r.rec
			if nr, err := r.a.Store.Resolve(r.smode, r.a.RepoRoot); err == nil {
				rec = nr
			}
			r.use(rec, true)
		}
	}
	fmt.Fprintln(r.conv)
}

// approveCycle is what /approve without an argument steps through.
var approveCycle = []string{"ask", "plan", "auto"}

// approveSays is what each mode means, as /approve reports it.
var approveSays = map[string]string{
	"ask":   "ask — shell / write / edit ask first",
	"plan":  "plan — Hermes looks and answers with a plan: reads run, shell asks, nothing is written",
	"auto":  "auto — shell / write / edit run unattended",
	"never": "never — shell / write / edit are refused, reads only",
}

// approve sets the hands' approvals: ask | plan | auto | never, or — with no
// argument — the next of ask → plan → auto. A runtime switch like /yolo:
// this session only, the next one opened starts from the config.
func (r *repl) approve(arg string) {
	mode := strings.ToLower(strings.TrimSpace(arg))
	switch mode {
	case "":
		mode = approveCycle[0]
		for i, m := range approveCycle {
			if m == r.a.Approvals() {
				mode = approveCycle[(i+1)%len(approveCycle)]
			}
		}
	case "yolo", "off":
		mode = "auto"
	}
	says, ok := approveSays[mode]
	if !ok {
		fmt.Fprintf(r.conv, "  /approve ask | plan | auto | never — no argument: the next of ask → plan → auto (now: %s)\n\n", r.a.Approvals())
		return
	}
	r.a.SetApprovals(mode)
	fmt.Fprintf(r.conv, "  approvals: %s (this session only)\n\n", says)
}

// yolo toggles approvals for the hands' shell / write / edit — a runtime
// switch: this session only, the next one opened starts from the config.
func (r *repl) yolo(string) {
	if r.a.Approvals() == "auto" {
		r.a.SetApprovals("ask")
		fmt.Fprintln(r.conv, "  approvals: ON — shell / write ask first")
	} else {
		r.a.SetApprovals("auto")
		fmt.Fprintln(r.conv, "  approvals: OFF (yolo) — shell / write run unattended (this session only)")
	}
	fmt.Fprintln(r.conv)
}

// debug switches the redacted wire trace: on | off | (nothing) = toggle.
func (r *repl) debug(arg string) {
	switch strings.ToLower(arg) {
	case "on", "true", "yes":
		r.debugOn = true
	case "off", "false", "no":
		r.debugOn = false
	default:
		r.debugOn = !r.debugOn
	}
	r.a.NoteDebug(r.debugOn)
	r.applyDebug()
	if r.debugOn {
		fmt.Fprintf(r.conv, "  debug: ON — redacted connection trace → %s\n", r.a.Store.DebugPath(r.rec))
		fmt.Fprintf(r.conv, "  (local only, 0600, deleted with the session after %d idle days)\n", r.a.Cfg.RetentionDays)
	} else {
		fmt.Fprintln(r.conv, "  debug: OFF")
	}
	fmt.Fprintln(r.conv)
}

func (r *repl) check(string) {
	if res, err := r.a.Client.Server().Check(context.Background()); err != nil {
		fmt.Fprintf(r.conv, "BLOCKED: %s\n", err)
	} else {
		r.configured = true
		fmt.Fprintln(r.conv, app.CheckLine(res))
	}
	fmt.Fprintln(r.conv)
}

// config is read-only on purpose: hermes-hands never writes settings back —
// the operator edits the file by hand (see the header this prints).
func (r *repl) config(string) {
	fmt.Fprint(r.conv, config.Describe(r.a.Cfg))
	fmt.Fprintln(r.conv)
}

// compact compacts the open session's chat now — hermes-hands' own
// compaction (internal/compact): every output so far as a line, readable
// with local_output; turns collapse only when the lines would not leave
// room. It takes no argument: a mistyped "/compact agressive" does nothing
// instead of something else.
func (r *repl) compact(arg string) {
	if arg != "" {
		fmt.Fprintln(r.conv, "  /compact takes no argument — /compact-aggressive also turns every earlier turn into question + answer.")
		fmt.Fprintln(r.conv)
		return
	}
	r.compactAs(compact.Now)
}

// compactAggressive is the deep one, for when /compact was not enough:
// every output as a line and every turn but the latest as question + answer
// (the files each one changed and read still named).
func (r *repl) compactAggressive(arg string) {
	if arg != "" {
		fmt.Fprintln(r.conv, "  /compact-aggressive takes no argument.")
		fmt.Fprintln(r.conv)
		return
	}
	r.compactAs(compact.Aggressive)
}

func (r *repl) compactAs(d compact.Depth) {
	ev, err := r.a.Keeper.Compact(d)
	if err != nil {
		fmt.Fprintf(r.conv, "  %v\n\n", err)
		return
	}
	r.a.UI.Show(r.runner.Record(ev), "")
	fmt.Fprintln(r.conv)
}

// context shows what Hermes reads now: the meter, the latest chat
// checkpoint and what came after it — "full" prints every message.
func (r *repl) context(arg string) {
	fmt.Fprint(r.conv, compact.Describe(r.a.Keeper.View(), arg == "full"))
	fmt.Fprintln(r.conv)
}
