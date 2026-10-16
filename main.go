// Command hermes-hands is a terminal chat client for a central Hermes brain,
// spoken over Hermes' merged Runs API (POST /v1/runs + poll GET /v1/runs/{id}).
// Hermes holds the plan/memory; this binary drives a persistent local shell plus
// read_file / write_file / edit_file so Hermes can see and act on the repo you
// are standing in. Outbound only: nothing listens on your machine.
//
// The tool-call envelope Hermes replies with is the same shape split-runtime
// (NousResearch/hermes-agent#63966) expects, so the local dispatcher does not
// change when that lands — only the transport.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/peterh/liner"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/app"
	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/redact"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ttyio"
	"github.com/Zesales/hermes-hands/internal/turn"
	"github.com/Zesales/hermes-hands/internal/ui"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "0.0.0-dev"
	commit  = ""
)

// usageText is what `hermes-hands -h` prints. Everything past first-run config
// happens inside the session with /commands; the shell surface stays small.
const usageText = `hermes-hands - terminal chat with a central Hermes brain over its Runs API.
Hermes holds the plan/memory; it drives its hands here — local_shell (a
persistent bash), local_read / local_write / local_edit — in the repo you're in.

A session is one task. There is no one-shot, and no auto-resume: a bare run
starts a fresh session — resume an old one by its id only (plugins keep the id).

  hermes-hands                    start a fresh session here (main use)
  hermes-hands --session <id>     open a specific session  (id from --session-list)
  hermes-hands --new              start a fresh session  (same as a bare run)
  hermes-hands --rpc              JSON-lines session server for an editor/plugin

  hermes-hands --session-list     list this machine's sessions: id / turns / tokens / idle
  hermes-hands --session-list-verbose
                                  … each with its hermes-agent session (local vs. server)
  hermes-hands sessions new       mint a session id (for --session / --rpc)
  hermes-hands setup              first-run config  (also: /setup in-session)
  hermes-hands guidance           (re-)import your global agent rules (~/.claude/CLAUDE.md, …)
  hermes-hands check              preflight the API connection
  hermes-hands --version          print version

  --yolo   skip run/write approvals   (also: /yolo in-session)
`

// parsed is the outcome of the argv scan.
type parsed struct {
	action  string // "" | help | version | check | sessions | sessionnew | setup
	errMsg  string // non-empty => fatal (unknown option / stray argument)
	smode   string // "" (== new) | new | continue | <id>
	rpc     bool
	yolo    bool
	verbose bool // sessions: --session-list-verbose / sessions -v
}

// parseArgs scans argv, first match winning; the eager subcommands return
// immediately. There is no positional message: every mode works on a session
// (see runREPL / runRPC).
func parseArgs(args []string) parsed {
	p := parsed{}
	// Hidden dev command: `hermes-hands _events "<prompt>"` starts a run and
	// dumps its raw /v1/runs/{id}/events SSE stream. Not in usage / help.
	if len(args) >= 1 && args[0] == "_events" {
		p.action = "events"
		p.smode = strings.Join(args[1:], " ")
		return p
	}
	if len(args) >= 2 && args[0] == "_raw" {
		p.action = "raw"
		p.smode = args[1] // an API path, e.g. /v1/capabilities
		return p
	}
	if len(args) >= 2 && args[0] == "_run" {
		p.action = "run"
		p.smode = args[1] // a raw JSON body for POST /v1/runs
		return p
	}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-h", "--help":
			p.action = "help"
			return p
		case "-v", "--version", "version":
			p.action = "version"
			return p
		case "check", "--check":
			p.action = "check"
			return p
		case "sessions", "--list", "--session-list", "--session-list-verbose":
			next := ""
			if i+1 < len(args) {
				next = args[i+1]
			}
			switch {
			case a == "sessions" && next == "new":
				p.action = "sessionnew"
			default:
				p.action = "sessions"
				p.verbose = a == "--session-list-verbose" || next == "-v" || next == "--verbose"
			}
			return p
		case "setup":
			p.action = "setup"
			return p
		case "guidance":
			p.action = "guidance"
			return p
		case "--rpc":
			p.rpc = true
		case "--new":
			p.smode = "new"
		case "--session":
			// resume is explicit by id: "--session <id>" opens that one; a bare
			// "--session" has nothing to point at any more (no per-dir pointer).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				p.smode = args[i]
			} else {
				p.errMsg = "--session needs a session id (ids from --session-list)"
			}
		case "--yolo":
			p.yolo = true
		default:
			if strings.HasPrefix(a, "-") {
				p.errMsg = "unknown option: " + a
			} else {
				p.errMsg = "unexpected argument: " + a + " (there is no one-shot; start a session)"
			}
			return p
		}
	}
	return p
}

func main() {
	p := parseArgs(os.Args[1:])
	if p.yolo {
		// Exported before config.Load so nested config parsing sees it too.
		_ = os.Setenv("HERMES_HANDS_APPROVE", "auto")
	}

	switch p.action {
	case "help":
		fmt.Print(usageText)
		return
	case "version":
		fmt.Println(versionString())
		return
	case "check":
		os.Exit(runCheck())
	case "sessions":
		os.Exit(runSessions(p.verbose))
	case "sessionnew":
		os.Exit(runSessionNew())
	case "setup":
		os.Exit(runSetup())
	case "guidance":
		offerGuidanceImport(bufio.NewReader(os.Stdin), hermesHome())
		os.Exit(0)
	case "events":
		os.Exit(runEventsDump(p.smode))
	case "raw":
		os.Exit(runRawGet(p.smode))
	case "run":
		os.Exit(runRawRun(p.smode))
	}
	if p.errMsg != "" {
		fmt.Fprintf(os.Stderr, "hermes-hands: %s\n", p.errMsg)
		os.Exit(1)
	}

	// Every mode works ON a session. A bare REPL run starts a fresh one (a
	// session is one task); resuming is always EXPLICIT by id (`--session <id>`,
	// `/session <id>`, rpc `use`) — the way editor plugins work: they keep the
	// id from the `ready` event. `--rpc` defaults to a fresh session and the
	// plugin switches to its stored one with {"type":"use"}.
	if p.rpc {
		os.Exit(runRPC(orElse(p.smode, "new")))
	}
	os.Exit(runREPL(orElse(p.smode, "new")))
}

func orElse(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// splitCmd separates a REPL line into its first word (the /command) and the
// rest. A plain message returns (firstWord, rest) too; only the switch's
// /command cases act on it, everything else is sent as-is.
func splitCmd(s string) (cmd, rest string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

var slashCmdRe = regexp.MustCompile(`^/[A-Za-z?][A-Za-z0-9_-]*$`)

// isSlashCmd reports whether the first word looks like a REPL command
// ("/quit"), as opposed to a message that starts with a path ("/etc/hosts …").
func isSlashCmd(cmd string) bool { return slashCmdRe.MatchString(cmd) }

// --- version strings ---

func bareVersion() string {
	if version != "0.0.0-dev" {
		return version
	}
	// go install <module>@vX.Y.Z sets a real tag here; a plain local `go build`
	// sets a "v0.0.0-<pseudo>" string we would rather not show.
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
			return v
		}
	}
	return version
}

func versionString() string {
	ver, sha := bareVersion(), commit
	if sha == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					sha = s.Value[:7]
				}
			}
		}
	}
	if sha != "" {
		return fmt.Sprintf("hermes-hands %s (%s)", ver, sha)
	}
	return "hermes-hands " + ver
}

// --- shared helpers ---

// showOutput prints the stored full output of call #ref (`/out <ref>`).
func showOutput(u *ui.UI, st *session.Store, rec *session.Record, arg string) {
	ref, err := strconv.Atoi(arg)
	if err != nil || ref <= 0 {
		u.Note("usage: /out <n> — n is the #number of a call")
		u.Line("")
		return
	}
	out, err := st.LoadOutput(rec, ref)
	if err != nil {
		u.Note(fmt.Sprintf("no stored output for #%d in this session", ref))
		u.Line("")
		return
	}
	u.OutputBlock(ref, out)
	u.Line("")
}

// loadCfgOrExit resolves configuration, or prints "hermes-hands: <err>" and
// exits 1 (e.g. an undecryptable secrets.enc — never a panic).
func loadCfgOrExit() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

// askServerApproval puts an approval request of Hermes' OWN server-side tool
// to the operator. It is a separate trust domain from the hands' approvals,
// so their [a]ll latch never answers it. o = once, s = for this Hermes
// session, anything else = deny. Without an interactive prompt (approvals
// auto / never) it is a deny — a server command nobody looked at never runs.
func askServerApproval(ap prompt.Approver, tool, what string) string {
	ta, ok := ap.(*prompt.TTYApprover)
	if !ok || ta.Mode != "ask" || ta.AskLine == nil {
		return "deny"
	}
	if ta.Pause != nil {
		defer ta.Pause()()
	}
	out := ta.Out
	if out == nil {
		out = os.Stderr
	}
	if tool == "" {
		tool = "a tool"
	}
	summary := "hermes wants to run " + tool + " on its SERVER sandbox (not your machine):"
	asked := time.Now()
	fmt.Fprintf(out, "\n  ⚠  %s\n      %s\n", summary, what)
	line, err := ta.AskLine("  [o]nce  [s]ession  [n]o > ")
	answer := "deny"
	if err == nil {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "o", "y", "once":
			answer = "once"
		case "s", "session":
			answer = "session"
		}
	}
	if ta.OnAnswer != nil {
		ta.OnAnswer(summary, what, answer, time.Since(asked))
	}
	return answer
}

// maxApprovalDetail caps the diff an approval event keeps for its replay.
const maxApprovalDetail = 200

// clipLines keeps the first n lines of s and says how many it dropped.
func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
}

// --- REPL ---

// newApp builds the app for this directory from the configuration.
func newApp() (*app.App, error) { return app.New(loadCfgOrExit(), builtinInstructions) }

func runREPL(smode string) int {
	a, err := newApp()
	if err != nil {
		fmt.Printf("BLOCKED: %v\n", err)
		return 1
	}
	defer func() { a.Shell.Stop() }()

	configured := a.Cfg.Configured()
	if configured {
		res, err := a.Client.Check(context.Background())
		if err != nil {
			fmt.Printf("BLOCKED: %v\n", err)
			fmt.Fprintln(os.Stderr, "(fix the URL/key: `hermes-hands setup`, or /setup in-session)")
			return 1
		}
		if w := app.CompatWarning(res.Version); w != "" {
			app.Warnf("%s", w)
		}
	}

	rec, err := a.Store.Resolve(smode, a.RepoRoot)
	if err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		return 1
	}

	agentSID := "" // only real once a turn has confirmed it
	if rec.Turns > 0 {
		agentSID = rec.HermesSessionID
	}
	a.UI.Banner(bareVersion(), a.RepoRoot, agentSID)
	if !configured {
		fmt.Fprintln(os.Stderr, "hermes-hands is not configured — type /setup")
	}
	// a reopened session shows its last turns exactly as they ran
	evlog := a.OpenSession(rec, true)

	ln := liner.NewLiner()
	defer func() { ln.Close() }()
	ln.SetCtrlCAborts(true)

	// One runner for the REPL's lifetime; /setup re-binds it to the rebuilt
	// app. Ctrl-C mid-turn cancels the turn, the shell child and the server run.
	runner := &turn.Runner{
		Paused:         func() time.Duration { return a.UI.Paused() },
		ServerApproval: func(tool, what string) string { return askServerApproval(a.Disp.Approver, tool, what) },
	}
	a.Bind(runner)
	runner.Use(evlog)
	defer runner.CancelOnInterrupt()()

	// /debug: a redacted wire trace of the connection into the open session's
	// debug.jsonl (HERMES_HANDS_DEBUG=1 starts with it on). Re-pointed on every
	// session switch; deleted with its session by the retention window.
	debugOn := a.Cfg.Debug
	applyDebug := func() { a.SetDebug(rec, debugOn) }

	// Per-app wiring that must be redone when /setup swaps `a`: the approval
	// gate reads its answer through liner (one terminal owner; a separate
	// /dev/tty reader deadlocks against liner's input goroutine), and an
	// answered prompt — on screen already — is recorded, so a replay shows it.
	wireApp := func() {
		if ta, ok := a.Disp.Approver.(*prompt.TTYApprover); ok {
			ta.Out = os.Stderr
			ta.AskLine = func(q string) (string, error) { return ln.Prompt(q) }
			ta.Pause = a.UI.Hold
			ta.OnAnswer = func(summary, detail, answer string, waited time.Duration) {
				runner.Record(session.Event{Kind: session.KindApproval, Text: redact.Scrub(summary),
					Detail: clipLines(redact.Scrub(detail), maxApprovalDetail), Action: answer, Ms: waited.Milliseconds()})
			}
		}
		applyDebug()
	}
	wireApp()

	for {
		input, err := ln.Prompt(a.UI.SessionPrompt(rec.ID))
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

		cmd, rest := splitCmd(input)
		switch cmd {
		case "":
			continue
		case "/exit":
			return 0
		case "/help", "/h", "/?":
			a.UI.Help(a.RepoRoot)
			fmt.Fprintln(os.Stderr)
			continue
		case "/setup":
			if code := doSetup(bufio.NewReader(os.Stdin), hermesHome(), false); code == 0 {
				if na, e := newApp(); e == nil {
					a.Shell.Stop()
					a = na
					a.Bind(runner)
					wireApp()
					configured = a.Cfg.Configured()
					if _, e2 := a.Client.Check(context.Background()); e2 != nil {
						fmt.Fprintf(os.Stderr, "BLOCKED: %v\n", e2)
					}
					if nr, e3 := a.Store.Resolve(smode, a.RepoRoot); e3 == nil {
						rec = nr
					}
					runner.Use(a.OpenSession(rec, true))
					applyDebug()
				}
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/session":
			if rest == "" { // no id -> show the current session's detail
				fresh, _ := a.Store.Resolve(rec.ID, a.RepoRoot)
				if fresh == nil {
					fresh = rec
				}
				fmt.Fprint(os.Stderr, session.FormatDetail(fresh))
				if si, e := a.Client.SessionInfo(context.Background(), fresh.HermesSessionID); e == nil {
					fmt.Fprintln(os.Stderr, "  — from hermes-agent —")
					if si.Messages > 0 {
						fmt.Fprintf(os.Stderr, "  messages             : %d\n", si.Messages)
					}
					if si.Tokens > 0 {
						fmt.Fprintf(os.Stderr, "  context tokens       : %d\n", si.Tokens)
					}
					if si.Parent != "" {
						fmt.Fprintf(os.Stderr, "  parent (pre-compact) : %s\n", si.Parent)
					}
					if si.Model != "" {
						fmt.Fprintf(os.Stderr, "  model                : %s\n", si.Model)
					}
					if si.Ended {
						fmt.Fprintln(os.Stderr, "  ended                : yes")
					}
				}
				fmt.Fprintln(os.Stderr)
				continue
			}
			nr, e := a.Store.Resolve(rest, a.RepoRoot)
			if e != nil {
				fmt.Fprintf(os.Stderr, "  %v\n\n", e)
				continue
			}
			rec = nr
			a.UI.NewSessionNote(rec.ID)
			runner.Use(a.OpenSession(rec, true))
			applyDebug()
			continue
		case "/history", "/chat-history":
			// /history = this session, every turn; /history <n> = its last n
			// turns; /history <id> = another session's (read-only — it never
			// switches the REPL to it)
			target, n := rec, 0
			if rest != "" {
				if k, e := strconv.Atoi(rest); e == nil && k > 0 {
					n = k
				} else if other, e := a.Store.Load(rest); e == nil {
					target = other
				} else {
					fmt.Fprintf(os.Stderr, "  no such session: %s\n\n", rest)
					continue
				}
			}
			evs, e := a.Store.LoadEvents(target)
			if e != nil {
				app.Warnf("history: %v", e)
				continue
			}
			a.UI.Replay(evs, n, target.ID, app.StoredOutput(a.Store, target))
			fmt.Fprintln(os.Stderr)
			continue
		case "/out":
			showOutput(a.UI, a.Store, rec, rest)
			continue
		case "/yolo":
			if ta, ok := a.Disp.Approver.(*prompt.TTYApprover); ok {
				if ta.Mode == "auto" {
					ta.Mode = "ask"
					fmt.Fprintln(os.Stderr, "  approvals: ON — shell / write ask first")
				} else {
					ta.Mode = "auto"
					fmt.Fprintln(os.Stderr, "  approvals: OFF (yolo) — shell / write run unattended")
				}
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/debug":
			switch strings.ToLower(rest) {
			case "on", "true", "yes":
				debugOn = true
			case "off", "false", "no":
				debugOn = false
			default:
				debugOn = !debugOn
			}
			applyDebug()
			if debugOn {
				fmt.Fprintf(os.Stderr, "  debug: ON — redacted connection trace → %s\n", a.Store.DebugPath(rec))
				fmt.Fprintf(os.Stderr, "  (local only, 0600, deleted with the session after %d idle days)\n", a.Cfg.RetentionDays)
			} else {
				fmt.Fprintln(os.Stderr, "  debug: OFF")
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/check":
			if res, e := a.Client.Check(context.Background()); e != nil {
				fmt.Fprintf(os.Stderr, "BLOCKED: %s\n", e)
			} else {
				configured = true
				fmt.Fprintln(os.Stderr, app.CheckLine(res))
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/compact", "/compress":
			// This gateway exposes no REST compaction endpoint (`/compress` is
			// an internal chat command). Hermes compacts on its own; start a
			// new session when a task is done.
			fmt.Fprintln(os.Stderr, "  this Hermes doesn't expose compaction over the API —")
			fmt.Fprintln(os.Stderr, "  it compacts automatically; use /new for a new task.")
			fmt.Fprintln(os.Stderr)
			continue
		case "/fork":
			if rec.HermesSessionID == "" {
				fmt.Fprintln(os.Stderr, "  no hermes-agent session yet — run a turn first")
				fmt.Fprintln(os.Stderr)
				continue
			}
			newID, e := a.Client.Fork(context.Background(), rec.HermesSessionID)
			if e != nil {
				fmt.Fprintf(os.Stderr, "  BLOCKED: %v\n\n", e)
				continue
			}
			nr, _ := a.Store.Resolve("new", a.RepoRoot)
			nr.HermesSessionID = newID
			_, _ = a.Store.SetTitleLocal(nr, "fork of "+rec.ID)
			rec = nr
			runner.Use(a.OpenSession(rec, false))
			applyDebug()
			fmt.Fprintf(os.Stderr, "  forked → hermes-agent session %s (local %s)\n\n", newID, rec.ID)
			continue
		case "/new":
			nr, e := a.Store.Resolve("new", a.RepoRoot)
			if e != nil {
				fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", e)
				continue
			}
			rec = nr
			a.UI.NewSessionNote(rec.ID)
			runner.Use(a.OpenSession(rec, false))
			applyDebug()
			continue
		case "/sessions":
			fmt.Fprint(os.Stderr, app.SessionList(a.Store, a.Client, rest == "-v" || rest == "--verbose"))
			fmt.Fprintln(os.Stderr)
			continue
		case "/config", "/hh-settings":
			// Read-only on purpose: hermes-hands never writes settings back —
			// edit the file by hand (see the header this prints).
			fmt.Fprint(os.Stderr, config.Describe(a.Cfg))
			fmt.Fprintln(os.Stderr)
			continue
		}
		// An unknown /command never goes to Hermes as a task: a typo or a
		// dropped alias (/quit) would otherwise cost a whole run.
		if isSlashCmd(cmd) {
			fmt.Fprintf(os.Stderr, "  unknown command %s — /help lists the commands, /exit leaves\n\n", cmd)
			continue
		}

		if !configured {
			fmt.Fprintln(os.Stderr, "  not configured — type /setup first")
			fmt.Fprintln(os.Stderr)
			continue
		}

		ln.AppendHistory(input)
		// Everything a turn prints goes through the session's event log
		// (stored, scrubbed) — so reopening the session replays it 1:1.
		runner.Run(input, &replFront{ui: a.UI})
	}
	return 0
}

// --- subcommands ---

func runCheck() int {
	cfg := loadCfgOrExit()
	res, err := app.CheckClient(cfg).Check(context.Background())
	if err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		return 1
	}
	fmt.Println(app.CheckLine(res))
	return 0
}

func runSessions(verbose bool) int {
	cfg := loadCfgOrExit()
	store, err := session.Open(cfg.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 0
	}
	var client *api.Client
	if verbose && cfg.Configured() {
		client = app.CheckClient(cfg)
	}
	fmt.Print(app.SessionList(store, client, verbose))
	return 0
}

// runEventsDump is the hidden `_events` command: start a run and copy its raw
// SSE event stream to stdout, verbatim, so the wire shape can be inspected.
func runEventsDump(input string) int {
	if input == "" {
		input = "count from one to five"
	}
	cfg := loadCfgOrExit()
	if !cfg.Configured() {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	cl := app.CheckClient(cfg)
	rid, sid, err := cl.RawRun(context.Background(), input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "run_id=%s session_id=%s\n--- /v1/runs/%s/events ---\n", rid, sid, rid)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := cl.RawEvents(ctx, rid, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "\n[events: %v]\n", err)
	}
	return 0
}

// runRawGet is the hidden `_raw <path>` command: GET an API path with the
// resolved key and print the body, for inspecting response schemas.
func runRawGet(path string) int {
	cfg := loadCfgOrExit()
	if !cfg.Configured() {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	body, err := app.CheckClient(cfg).RawGet(context.Background(), path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Println(body)
	return 0
}

// runRawRun is the hidden `_run '<json>'` command: POST /v1/runs with a raw
// body and stream the events — for probing run params (toolsets, etc.).
func runRawRun(body string) int {
	cfg := loadCfgOrExit()
	if !cfg.Configured() {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	cl := app.CheckClient(cfg)
	rid, sid, err := cl.RawRunBody(context.Background(), []byte(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "run_id=%s session_id=%s\n--- events ---\n", rid, sid)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := cl.RawEvents(ctx, rid, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "\n[events: %v]\n", err)
	}
	return 0
}

// runSessionNew mints one session rooted at the cwd and prints its id on
// stdout — a plugin captures it once and then drives it with --session <id> or
// --rpc, instead of spawning a new session per call.
func runSessionNew() int {
	cfg := loadCfgOrExit()
	store, err := session.Open(cfg.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	rec, err := store.Resolve("new", app.PhysicalCwd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Println(rec.ID)
	return 0
}

// --- setup ---

const setupConfigBody = "# hermes-hands config  —  edit by hand; `/config` shows what is in effect\n" +
	"# HERMES_API_PROFILE=coder             # optional /p/<profile>/ prefix\n" +
	"# HERMES_HANDS_APPROVE=ask              # ask | auto | never\n" +
	"# HERMES_HANDS_STREAM=auto              # auto | on | off\n" +
	"# HERMES_HANDS_RESPONSE_TIMEOUT=600     # seconds hermes-agent may go silent in a turn before it is cancelled (0 = no limit)\n" +
	"# HERMES_HANDS_WATCHDOG_INTERVAL=200    # seconds between server-side 'still working?' checks that push that limit back\n" +
	"# HERMES_HANDS_RETENTION_DAYS=14        # local sessions idle longer are deleted on start, debug trace included (0 = keep)\n" +
	"# HERMES_HANDS_DEBUG=1                  # start with /debug on: redacted connection trace per session\n" +
	"# HERMES_HANDS_SANDBOX=smart            # Hermes' own server terminal/file tools: smart | steer | show\n"

func runSetup() int {
	plaintext := false
	for _, a := range os.Args[1:] {
		if a == "--plaintext" {
			plaintext = true
		}
	}
	if code := doSetup(bufio.NewReader(os.Stdin), hermesHome(), plaintext); code != 0 {
		return code
	}
	// setup then runs check directly.
	_ = runCheck()
	return 0
}

// doSetup prompts, then writes config + the secrets store. Default: a
// machine-bound secrets.enc + keyseed (nothing to add to the shell env).
// --plaintext: the pre-M10 0600 `secrets` file + the ~/.bashrc offer, for
// people who inject via env or a secrets manager.
func doSetup(in *bufio.Reader, dir string, plaintext bool) int {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	_ = os.Chmod(dir, 0o700)

	url := promptLine(in, "Hermes API base URL (https://…): ")
	key := promptSecret(in, "Hermes API_SERVER_KEY: ")
	if url == "" || key == "" {
		fmt.Fprintln(os.Stderr, "hermes-hands: setup needs both a URL and a key — nothing written.")
		return 1
	}

	if err := os.WriteFile(dir+"/config", []byte(setupConfigBody), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}

	if plaintext {
		body := "# hermes-hands secrets - chmod 600, do not commit\n" +
			"export HERMES_API_URL=" + shellQuoteQ(url) + "\n" +
			"export HERMES_API_KEY=" + shellQuoteQ(key) + "\n"
		if err := os.WriteFile(dir+"/secrets", []byte(body), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
			return 1
		}
		fmt.Printf("wrote %s/config and %s/secrets\n", dir, dir)
		offerBashrc(in)
		offerGuidanceImport(in, dir)
		return 0
	}

	if err := config.WriteEncryptedSecrets(dir, url, key); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s/config, %s/secrets.enc and %s/keyseed (machine-bound; nothing to add to your shell)\n", dir, dir, dir)
	offerGuidanceImport(in, dir)
	return 0
}

// globalGuidanceSources are the home-directory rules files other agents read:
// the import candidates, in this order.
var globalGuidanceSources = []string{".claude/CLAUDE.md", ".codex/AGENTS.md", "AGENTS.md", "CLAUDE.md", ".config/AGENTS.md"}

// offerGuidanceImport offers to snapshot the global agent rules found in the
// home directory into <dir>/guidance.md (0600). A snapshot, not a link: edit
// it freely; `hermes-hands guidance` re-imports. Asks before replacing an
// existing import; identical sources are imported once.
func offerGuidanceImport(in *bufio.Reader, dir string) {
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	type src struct {
		path string
		body string
	}
	var found []src
	seen := map[string]bool{}
	for _, rel := range globalGuidanceSources {
		p := filepath.Join(home, rel)
		b, err := os.ReadFile(p)
		body := strings.TrimSpace(string(b))
		if err != nil || body == "" || seen[body] {
			continue
		}
		seen[body] = true
		found = append(found, src{p, body})
	}
	if len(found) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "\nGlobal agent rules in your home directory:")
	for _, f := range found {
		fmt.Fprintf(os.Stderr, "  %s  (%d bytes)\n", f.path, len(f.body))
	}
	dst := filepath.Join(dir, app.GlobalGuidanceFile)
	q := "Import them as your global rules for Hermes (→ " + dst + ")? [y/N] "
	if _, err := os.Stat(dst); err == nil {
		q = "Replace " + dst + " with a fresh import of them? [y/N] "
	}
	if !strings.EqualFold(promptLine(in, q), "y") {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- hermes-hands global rules, imported %s from:\n", time.Now().Format("2006-01-02"))
	for _, f := range found {
		fmt.Fprintf(&b, "     %s\n", f.path)
	}
	b.WriteString("     A snapshot — edit freely; `hermes-hands guidance` re-imports. -->\n")
	for _, f := range found {
		fmt.Fprintf(&b, "\n## from %s\n\n%s\n", f.path, f.body)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	if err := os.WriteFile(dst, []byte(b.String()), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	fmt.Printf("wrote %s — every session now starts with these rules\n", dst)
}

func offerBashrc(in *bufio.Reader) {
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	rc := home + "/.bashrc"
	if b, err := os.ReadFile(rc); err == nil && strings.Contains(string(b), "hermes-hands/secrets") {
		return
	}
	if !strings.EqualFold(promptLine(in, "Add the secrets-sourcing line to ~/.bashrc? [y/N] "), "y") {
		return
	}
	line := `hh_secrets="${HERMES_HANDS_HOME:-$HOME/hermes-hands}/secrets"; [ -f "$hh_secrets" ] && . "$hh_secrets"`
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# hermes-hands\n%s\n", line); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return
	}
	fmt.Println("added.")
}

// hermesHome mirrors config.hermesHome for the setup path: the single
// self-contained data directory (HERMES_HANDS_HOME, else $HOME/hermes-hands).
func hermesHome() string {
	if v := os.Getenv("HERMES_HANDS_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/hermes-hands"
}

func promptLine(in *bufio.Reader, msg string) string {
	fmt.Fprint(os.Stderr, msg)
	line, _ := in.ReadString('\n')
	return strings.Trim(line, " \t\r\n")
}

// stdinTTY reports whether stdin is an interactive terminal. A package var so
// setup tests can force the plain-read path.
var stdinTTY = func() bool { return ttyio.IsTerminal(os.Stdin) }

func promptSecret(in *bufio.Reader, msg string) string {
	if stdinTTY() {
		ln := liner.NewLiner()
		defer ln.Close()
		if s, err := ln.PasswordPrompt(msg); err == nil {
			fmt.Fprintln(os.Stderr)
			return strings.Trim(s, " \t\r\n")
		}
	}
	return promptLine(in, msg)
}

// shellQuoteQ mimics bash `printf %q`: bare when safe, backslash-escaped for
// shell metacharacters, $'...' for control characters. The narrowed config
// parser reads all three forms back.
func shellQuoteQ(s string) string {
	if s == "" {
		return "''"
	}
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./:@%^=+,-"
	simple, ctrl := true, false
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			ctrl, simple = true, false
		case !strings.ContainsRune(safe, r):
			simple = false
		}
	}
	if simple {
		return s
	}
	var b strings.Builder
	if ctrl {
		b.WriteString("$'")
		for _, r := range s {
			switch r {
			case '\n':
				b.WriteString(`\n`)
			case '\t':
				b.WriteString(`\t`)
			case '\r':
				b.WriteString(`\r`)
			case '\\':
				b.WriteString(`\\`)
			case '\'':
				b.WriteString(`\'`)
			default:
				if r < 0x20 || r == 0x7f {
					fmt.Fprintf(&b, `\x%02x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteString("'")
		return b.String()
	}
	const special = " !\"#$&'()*;<>?[\\]^`{|}~"
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
