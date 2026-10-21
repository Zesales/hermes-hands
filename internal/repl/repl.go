// Package repl is the interactive terminal front end: it reads lines, runs a
// slash command (commands.go) or hands everything else to Hermes as a turn,
// and draws that turn through ui (front.go).
package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/peterh/liner"

	"hermes-hands/internal/app"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/redact"
	"hermes-hands/internal/session"
	"hermes-hands/internal/turn"
)

// Deps is what the REPL needs from outside: how to build the app (again,
// after /setup), the setup wizard, and the version for the banner.
type Deps struct {
	NewApp  func() (*app.App, error)
	Setup   func() int // the setup wizard; 0 = configured
	Version string
}

// repl is one running REPL: the app, the open session, its turn runner, the
// line reader, and the state the slash commands change.
type repl struct {
	a          *app.App
	d          Deps
	smode      string // how the session was chosen: "new" or an id
	rec        *session.Record
	runner     *turn.Runner
	ln         *liner.State
	debugOn    bool // /debug: the redacted wire trace into the session's debug.jsonl
	configured bool
	quit       bool // set by /exit

	ta      *typeahead // reads what the operator types while a turn runs; nil between turns
	prefill string     // what the next prompt starts with: typed during a turn, not sent
}

// Run is the REPL in session smode ("" or "new" = a fresh one, else an id).
func Run(smode string, d Deps) int {
	a, err := d.NewApp()
	if err != nil {
		fmt.Printf("BLOCKED: %v\n", err)
		return 1
	}
	r := &repl{a: a, d: d, smode: smode, configured: a.Cfg.Configured(), debugOn: a.Cfg.Debug}
	defer func() { r.a.Shell.Stop() }()

	if r.configured {
		res, err := a.Client.Server().Check(context.Background())
		if err != nil {
			fmt.Printf("BLOCKED: %v\n", err)
			fmt.Fprintln(os.Stderr, "(fix the URL/key: `hermes-hands setup`, or /setup in-session)")
			return 1
		}
		if w := app.CompatWarning(res.Version); w != "" {
			app.Warnf("%s", w)
		}
	}

	if r.rec, err = a.Store.Resolve(smode, a.RepoRoot); err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		return 1
	}
	agentSID := "" // only real once a turn has confirmed it
	if r.rec.Turns > 0 {
		agentSID = r.rec.HermesSessionID
	}
	a.UI.Banner(d.Version, a.RepoRoot, agentSID)
	if !r.configured {
		fmt.Fprintln(os.Stderr, "hermes-hands is not configured — type /setup")
	}
	// a reopened session shows its last turns exactly as they ran
	log := a.OpenSession(r.rec, true)

	r.ln = liner.NewLiner()
	defer func() { r.ln.Close() }()
	r.ln.SetCtrlCAborts(true)

	// One runner for the REPL's lifetime; /setup re-binds it to the rebuilt
	// app. Ctrl-C mid-turn cancels the turn, the shell child and the server run.
	r.runner = &turn.Runner{
		Paused:         func() time.Duration { return r.a.UI.Paused() },
		ServerApproval: func(tool, what string) string { return askServerApproval(r.a.Disp.Approver, tool, what) },
	}
	a.Bind(r.runner)
	r.runner.Use(log)
	defer r.runner.CancelOnInterrupt()()
	r.wireApp()

	for !r.quit {
		input, err := r.prompt()
		if err == liner.ErrPromptAborted { // Ctrl-C clears the line, never quits
			fmt.Fprintln(os.Stderr, "  (use /exit to quit)")
			continue
		}
		if err != nil { // io.EOF (Ctrl-D), or an unexpected liner error
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
			}
			fmt.Println()
			break
		}
		r.handle(input)
	}
	return 0
}

// handle runs one input line: a slash command, or a turn.
func (r *repl) handle(input string) {
	name, arg := splitCmd(input)
	if name == "" {
		return
	}
	if c, ok := commandNamed[name]; ok {
		c.run(r, arg)
		return
	}
	// An unknown /command never goes to Hermes as a task: a typo or a
	// dropped alias (/quit) would otherwise cost a whole run.
	if isSlashCmd(name) {
		fmt.Fprintf(os.Stderr, "  unknown command %s — /help lists the commands, /exit leaves\n\n", name)
		return
	}
	if !r.configured {
		fmt.Fprintln(os.Stderr, "  not configured — type /setup first")
		fmt.Fprintln(os.Stderr)
		return
	}
	r.ln.AppendHistory(input)
	// Everything a turn prints goes through the session's event log
	// (stored, scrubbed) — so reopening the session replays it 1:1. While it
	// runs, what the operator types goes to the turn's next round.
	r.ta = startTypeahead(r.a.UI, r.a.Loop.Inbox)
	res := r.runner.Run(input, &front{ui: r.a.UI})
	typed, cmds := r.ta.finish()
	r.ta = nil
	r.afterTurn(res, typed, cmds)
}

// afterTurn handles what the operator typed while the turn ran and the turn
// did not take: the /commands typed meanwhile run now; what was queued but
// not sent becomes the next turn — unless the turn was cancelled or timed
// out: then it waits in the prompt, with the line still being typed, to
// send or change.
func (r *repl) afterTurn(res turn.Result, typed string, cmds []string) {
	for _, c := range cmds {
		if !r.quit {
			r.handle(c)
		}
	}
	left := r.a.Loop.Inbox.Take()
	if len(left) > 0 && res.End.Outcome == "" && !r.quit {
		r.prefill = strings.TrimSpace(r.prefill + " " + typed)
		r.handle(strings.Join(left, "\n\n"))
		return
	}
	r.prefill = strings.TrimSpace(strings.Join(append(left, r.prefill, typed), " "))
}

// prompt reads the next line — starting with what was typed during the
// last turn and not sent, if anything.
func (r *repl) prompt() (string, error) {
	p := r.a.UI.SessionPrompt(r.rec.ID)
	if text := r.prefill; text != "" {
		r.prefill = ""
		return r.ln.PromptWithSuggestion(p, text, -1)
	}
	return r.ln.Prompt(p)
}

// wireApp is the per-app wiring, redone when /setup swaps the app: the
// approval gate reads its answer through liner (one terminal owner; a
// separate /dev/tty reader deadlocks against liner's input goroutine), and an
// answered prompt — on screen already — is recorded, so a replay shows it.
func (r *repl) wireApp() {
	if ta, ok := r.a.Disp.Approver.(*prompt.TTYApprover); ok {
		ta.Out = os.Stderr
		ta.AskLine = func(q string) (string, error) { return r.ln.Prompt(q) }
		ta.Pause = func() (resume func()) { // the prompt owns the line and the keys
			resumeUI, resumeTA := r.a.UI.Hold(), r.ta.hold()
			return func() { resumeTA(); resumeUI() }
		}
		ta.OnAnswer = func(summary, detail, answer string, waited time.Duration) {
			r.runner.Record(session.Event{Kind: session.KindApproval, Text: redact.Scrub(summary),
				Detail: clipLines(redact.Scrub(detail), maxApprovalDetail), Action: answer, Ms: waited.Milliseconds()})
		}
	}
	r.applyDebug()
}

// use makes rec the open session; show replays its last turns.
func (r *repl) use(rec *session.Record, show bool) {
	r.rec = rec
	r.runner.Use(r.a.OpenSession(rec, show)) // drops /yolo, /debug of the session before
	r.debugOn = r.a.Cfg.Debug
	r.applyDebug()
}

func (r *repl) applyDebug() { r.a.SetDebug(r.rec, r.debugOn) }
