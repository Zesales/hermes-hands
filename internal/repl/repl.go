// Package repl is the interactive terminal front end: it reads lines, runs a
// slash command (commands.go) or hands everything else to Hermes as a turn,
// and draws that turn through ui (front.go). On a terminal the box reads
// them (box.go) — from the start, a turn running or not.
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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
// input, and the state the slash commands change.
type repl struct {
	a          *app.App
	d          Deps
	smode      string // how the session was chosen: "new" or an id
	rec        *session.Record
	runner     *turn.Runner
	conv       io.Writer // the conversation, above the dock: what the commands print
	debugOn    bool      // /debug: the redacted wire trace into the session's debug.jsonl
	configured bool
	quit       bool // set by /exit

	box      *box          // the input on a terminal; nil: plain lines from stdin (plain)
	plain    *bufio.Reader // stdin, when there is no box
	turning  atomic.Bool   // a turn runs, in the background (box)
	done     chan turn.Result
	deferred []string // /commands typed during the turn that wait for its end
}

// liveCommands run at once when typed during a turn; every other /command
// waits for the turn's end.
var liveCommands = map[string]bool{"/approve": true, "/yolo": true, "/help": true, "/h": true, "/?": true,
	"/config": true, "/hh-settings": true, "/exit": true}

// Run is the REPL in session smode ("" or "new" = a fresh one, else an id).
func Run(smode string, d Deps) int {
	a, err := d.NewApp()
	if err != nil {
		fmt.Printf("BLOCKED: %v\n", err)
		return 1
	}
	r := &repl{a: a, d: d, smode: smode, configured: a.Cfg.Configured(), debugOn: a.Cfg.Debug, done: make(chan turn.Result, 1)}
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

	// One runner for the REPL's lifetime; /setup re-binds it to the rebuilt
	// app. Ctrl-C mid-turn cancels the turn, the shell child and the server run.
	r.runner = &turn.Runner{
		Paused:         func() time.Duration { return r.a.UI.Paused() },
		ServerApproval: func(tool, what string) string { return askServerApproval(r.a.Disp.Approver, tool, what) },
	}
	a.Bind(r.runner)
	r.runner.Use(log)
	defer r.runner.CancelOnInterrupt()()

	r.box = startBox(r.a.UI, r.interrupt, r.turning.Load, completeCommand)
	if r.box == nil {
		r.plain = bufio.NewReader(os.Stdin)
	}
	r.wireApp()
	r.refresh()
	if r.box == nil {
		r.plainLoop()
		return 0
	}
	defer r.box.close()
	r.boxLoop()
	return 0
}

// boxLoop takes what the box sends — between turns and during one — and
// each turn's end.
func (r *repl) boxLoop() {
	for !r.quit {
		select {
		case in := <-r.box.in:
			if in.eof {
				r.quit = true
				break
			}
			r.submit(in.text)
		case res := <-r.done:
			r.turning.Store(false)
			r.afterTurn(res)
		}
	}
	if r.turning.Load() { // /exit during a turn
		r.runner.Cancel()
		<-r.done
	}
}

// plainLoop reads lines from stdin, no terminal to type in: one at a time,
// each turn to its end before the next line.
func (r *repl) plainLoop() {
	for !r.quit {
		fmt.Fprint(os.Stderr, r.a.UI.SessionPrompt(r.rec.ID))
		line, err := r.plain.ReadString('\n')
		if err != nil && line == "" { // io.EOF (stdin closed)
			fmt.Println()
			return
		}
		r.handle(strings.TrimRight(line, "\r\n"))
	}
}

// plainAsk is the approval prompt with no box: the question, a line from stdin.
func (r *repl) plainAsk(q string) (string, error) {
	fmt.Fprint(os.Stderr, q)
	line, err := r.plain.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// submit takes a line from the box. During a turn a message is queued for
// its next round and a /command runs now when it is safe to (liveCommands),
// else when the turn is done.
func (r *repl) submit(text string) {
	if !r.turning.Load() {
		r.handle(text)
		return
	}
	name, arg := splitCmd(text)
	c, known := commandNamed[name]
	switch {
	case known && liveCommands[name]:
		c.run(r, arg)
		r.refresh()
	case known:
		r.deferred = append(r.deferred, text)
		r.a.UI.Note(name + " runs when the turn is done — commands that change the session wait for it")
	case isSlashCmd(name):
		r.handle(text) // unknown: says so
	default:
		r.a.Loop.Inbox.Push(text)
		r.a.UI.Queue(text)
	}
}

// handle runs one input line between turns: a slash command, or a turn.
func (r *repl) handle(input string) {
	name, arg := splitCmd(input)
	if name == "" {
		return
	}
	if c, ok := commandNamed[name]; ok {
		r.command(c, name, arg)
		return
	}
	// An unknown /command never goes to Hermes as a task: a typo or a
	// dropped alias (/quit) would otherwise cost a whole run.
	if isSlashCmd(name) {
		fmt.Fprintf(r.conv, "  unknown command %s — /help lists the commands, /exit leaves\n\n", name)
		return
	}
	if !r.configured {
		fmt.Fprintln(r.conv, "  not configured — type /setup first")
		fmt.Fprintln(r.conv)
		return
	}
	r.startTurn(input)
}

// command runs a /command between turns. /setup takes the terminal: the
// dock steps aside, the box lets go of the keys, and when the wizard built
// a new app the box moves to its UI.
func (r *repl) command(c command, name, arg string) {
	if name != "/setup" {
		c.run(r, arg)
		r.refresh()
		return
	}
	u := r.a.UI
	resumeUI := u.Hold()
	resumeBox := func() {}
	if r.box != nil {
		resumeBox = r.box.suspend()
	}
	c.run(r, arg)
	resumeBox()
	if r.a.UI == u {
		resumeUI()
	} else if r.box != nil {
		r.box.use(r.a.UI)
	}
	r.refresh()
}

// startTurn hands input to Hermes. Everything a turn prints goes through the
// session's event log (stored, scrubbed) — so reopening the session replays
// it 1:1. With the box the turn runs in the background: what the operator
// types meanwhile goes to the turn's next round, /approve and the like act
// at once.
func (r *repl) startTurn(input string) {
	if r.box == nil {
		r.afterTurn(r.runner.Run(input, &front{ui: r.a.UI}))
		return
	}
	r.turning.Store(true)
	r.refresh()
	fe, b := &front{ui: r.a.UI}, r.box
	go func() {
		defer func() { // a crash must not leave the terminal without its echo
			if p := recover(); p != nil {
				b.release()
				panic(p)
			}
		}()
		r.done <- r.runner.Run(input, fe)
	}()
}

// afterTurn handles what the operator typed while the turn ran and the turn
// did not take: the /commands typed meanwhile run now; what was queued but
// not sent becomes the next turn — unless the turn was cancelled or timed
// out: then it waits in the input line, to send or change.
func (r *repl) afterTurn(res turn.Result) {
	r.a.UI.ClearQueue()
	cmds := r.deferred
	r.deferred = nil
	for _, c := range cmds {
		if !r.quit {
			r.handle(c)
		}
	}
	left := r.a.Loop.Inbox.Take()
	switch {
	case len(left) > 0 && res.End.Outcome == "" && !r.quit:
		r.handle(strings.Join(left, "\n\n"))
		return
	case len(left) > 0 && r.box != nil:
		r.box.prepend(strings.Join(left, " "))
	}
	r.refresh()
}

// interrupt is the box's Ctrl-C: it cancels the running turn, if one runs.
func (r *repl) interrupt() bool {
	if !r.turning.Load() {
		return false
	}
	r.runner.Cancel()
	return true
}

// refresh brings the dock's footer and the box up to the REPL's state.
func (r *repl) refresh() {
	what := "session " + strings.TrimPrefix(r.rec.ID, "hh_")
	if r.turning.Load() {
		what = "Enter queues it for the next round"
	}
	r.a.UI.SetFooter("⏵ approvals " + r.a.Approvals() + "  ·  " + what)
	if r.box != nil {
		r.box.redraw()
	}
}

// completeCommand is every /command name a Tab can complete prefix to.
func completeCommand(prefix string) []string {
	var names []string
	for n := range commandNamed {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// wireApp is the per-app wiring, redone when /setup swaps the app: the
// approval gate reads its answer through the box (one terminal owner) — or
// stdin with no box — and an answered prompt, on screen already, is
// recorded, so a replay shows it.
func (r *repl) wireApp() {
	r.conv = r.a.UI.Writer()
	if ta, ok := r.a.Disp.Approver.(*prompt.TTYApprover); ok {
		ta.Out = r.conv
		if r.box != nil {
			ta.AskLine, ta.Pause = r.box.Ask, r.a.UI.Await
		} else {
			ta.AskLine, ta.Pause = r.plainAsk, r.a.UI.Hold
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
