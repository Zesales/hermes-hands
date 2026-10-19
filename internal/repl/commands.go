package repl

import (
	"context"
	"fmt"
	"os"
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
	{[]string{"/yolo"}, (*repl).yolo},
	{[]string{"/debug"}, (*repl).debug},
	{[]string{"/check"}, (*repl).check},
	{[]string{"/config", "/hh-settings"}, (*repl).config},
	{[]string{"/compact", "/compress"}, (*repl).compact},
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
	fmt.Fprintln(os.Stderr)
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
				fmt.Fprintf(os.Stderr, "BLOCKED: %v\n", err)
			}
			rec := r.rec
			if nr, err := r.a.Store.Resolve(r.smode, r.a.RepoRoot); err == nil {
				rec = nr
			}
			r.use(rec, true)
		}
	}
	fmt.Fprintln(os.Stderr)
}

// yolo toggles approvals for the hands' shell / write / edit — a runtime
// switch: this session only, the next one opened starts from the config.
func (r *repl) yolo(string) {
	if r.a.Approvals() == "auto" {
		r.a.SetApprovals("ask")
		fmt.Fprintln(os.Stderr, "  approvals: ON — shell / write ask first")
	} else {
		r.a.SetApprovals("auto")
		fmt.Fprintln(os.Stderr, "  approvals: OFF (yolo) — shell / write run unattended (this session only)")
	}
	fmt.Fprintln(os.Stderr)
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
		fmt.Fprintf(os.Stderr, "  debug: ON — redacted connection trace → %s\n", r.a.Store.DebugPath(r.rec))
		fmt.Fprintf(os.Stderr, "  (local only, 0600, deleted with the session after %d idle days)\n", r.a.Cfg.RetentionDays)
	} else {
		fmt.Fprintln(os.Stderr, "  debug: OFF")
	}
	fmt.Fprintln(os.Stderr)
}

func (r *repl) check(string) {
	if res, err := r.a.Client.Server().Check(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "BLOCKED: %s\n", err)
	} else {
		r.configured = true
		fmt.Fprintln(os.Stderr, app.CheckLine(res))
	}
	fmt.Fprintln(os.Stderr)
}

// config is read-only on purpose: hermes-hands never writes settings back —
// the operator edits the file by hand (see the header this prints).
func (r *repl) config(string) {
	fmt.Fprint(os.Stderr, config.Describe(r.a.Cfg))
	fmt.Fprintln(os.Stderr)
}

// compact compacts the open session's transcript now — hermes-hands' own
// compaction (internal/compact): from here on it sends the transcript with
// every run, old outputs elided but readable with local_output.
func (r *repl) compact(arg string) {
	if arg != "" {
		fmt.Fprintln(os.Stderr, "  /compact takes no focus — nothing is summarised away, every output stays readable.")
	}
	ev, err := r.a.Keeper.Compact(compact.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n\n", err)
		return
	}
	ev = r.runner.Record(ev)
	fmt.Fprintf(os.Stderr, "  %s\n\n", ev.Text)
}
