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
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/peterh/liner"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/debuglog"
	"github.com/Zesales/hermes-hands/internal/dispatch"
	"github.com/Zesales/hermes-hands/internal/guard"
	"github.com/Zesales/hermes-hands/internal/knowsystem"
	"github.com/Zesales/hermes-hands/internal/loop"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/redact"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/shell"
	"github.com/Zesales/hermes-hands/internal/ttyio"
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
Hermes holds the plan/memory; it drives a persistent local shell plus
read_file / write_file / edit_file to see and act on the repo you're in.

A session is one task. There is no one-shot, and no auto-resume: a bare run
starts a fresh session — resume an old one by its id only (plugins keep the id).

  hermes-hands                    start a fresh session here (main use)
  hermes-hands --session <id>     open a specific session  (id from --session-list)
  hermes-hands --new              start a fresh session  (same as a bare run)
  hermes-hands --rpc              JSON-lines session server for an editor/plugin

  hermes-hands --session-list     list sessions with id / turns / tokens / idle
  hermes-hands sessions new       mint a session id (for --session / --rpc)
  hermes-hands setup              first-run config  (also: /setup in-session)
  hermes-hands guidance           (re-)import your global agent rules (~/.claude/CLAUDE.md, …)
  hermes-hands check              preflight the API connection
  hermes-hands --version          print version

  --yolo   skip run/write approvals   (also: /yolo in-session)
`

// parsed is the outcome of the argv scan.
type parsed struct {
	action string // "" | help | version | check | sessions | sessionnew | setup
	errMsg string // non-empty => fatal (unknown option / stray argument)
	smode  string // "" (== new) | new | continue | <id>
	rpc    bool
	yolo   bool
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
		case "sessions", "--list", "--session-list":
			if a == "sessions" && i+1 < len(args) && args[i+1] == "new" {
				p.action = "sessionnew"
			} else {
				p.action = "sessions"
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
		os.Exit(runSessions())
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

// finalStreamer turns the raw SSE token stream (which carries the JSON
// envelope `{"calls":...,"final":"..."}`) into a live preview of just the
// `final` answer text. On a tool-call round (non-empty "calls") it emits
// nothing — the tool lines speak for it.
type finalStreamer struct {
	emit    func(string) // e.g. ui.Delta
	raw     strings.Builder
	started bool // found the opening quote of the final value
	done    bool // hit its closing quote, or it's a calls-round
	pos     int  // index in raw where unparsed value bytes begin
}

func (f *finalStreamer) reset() { *f = finalStreamer{emit: f.emit} }

func (f *finalStreamer) feed(chunk string) {
	if f.done {
		return
	}
	f.raw.WriteString(chunk)
	s := f.raw.String()

	if !f.started {
		// a real tool-call round -> never a final to stream
		if i := strings.Index(s, `"calls"`); i >= 0 {
			after := strings.TrimLeft(s[i+7:], " :\t\r\n")
			if strings.HasPrefix(after, "[") {
				rest := strings.TrimLeft(after[1:], " \t\r\n")
				if rest == "" {
					return // the chunk ended at "[" — can't tell an empty array yet
				}
				if !strings.HasPrefix(rest, "]") {
					f.done = true
					return
				}
			}
		}
		k := strings.Index(s, `"final"`)
		if k < 0 {
			return
		}
		q := strings.IndexByte(s[k+7:], '"') // opening quote of the value
		if q < 0 {
			return
		}
		f.started = true
		f.pos = k + 7 + q + 1
	}

	// walk from f.pos, emitting decoded chars until an unescaped closing quote
	var out strings.Builder
	i := f.pos
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case '"':
				out.WriteByte('"')
			case '\\':
				out.WriteByte('\\')
			case '/':
				out.WriteByte('/')
			default:
				out.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		if c == '"' { // end of the final string
			f.done = true
			i++
			break
		}
		out.WriteByte(c)
		i++
	}
	f.pos = i
	if out.Len() > 0 && f.emit != nil {
		f.emit(out.String())
	}
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

// --- shared logging ---

func warnf(f string, a ...any) { fmt.Fprintf(os.Stderr, "hermes-hands: WARNING: "+f+"\n", a...) }
func logf(f string, a ...any)  { fmt.Fprintf(os.Stderr, "hermes-hands: "+f+"\n", a...) }

// --- app wiring ---

type app struct {
	cfg      *config.Config
	repoRoot string
	ui       *ui.UI
	client   *api.Client
	store    *session.Store
	shell    *shell.Shell
	disp     *dispatch.Dispatcher
	loop     *loop.Loop
}

// openSession makes rec the open session: loads its event log, continues the
// call numbering, replays the last turns into the terminal when show is set,
// and prepares the first turn when the server lost the conversation.
func openSession(a *app, rec *session.Record, show bool) *sessionLog {
	evlog, evs := openLog(a.store, a.ui, rec)
	a.loop.Ref = session.LastRef(evs)
	if show && len(evs) > 0 {
		evlog.replay(evs, replayTurns)
	}
	prepareResume(a, rec, evs)
	return evlog
}

// prepareResume: hermes-agent holds the conversation the model sees; when it
// still has this session there is nothing to do. When it lost it (404 / empty
// — a Hermes update, a retention purge), the first turn carries a recap of
// the local event log, so the brain continues instead of starting blind.
// Nothing is ever synced from the server into the local log.
func prepareResume(a *app, rec *session.Record, evs []session.Event) {
	a.loop.ResumeNote = ""
	if rec.HermesSessionID == "" || rec.Turns == 0 {
		return // nothing has been said in this session yet
	}
	if idle := session.IdleFor(rec, time.Now()); idle > session.StaleAfter {
		logf("session %s: idle %s — model prompt caches (Bifrost/hermes-agent/Claude-style) likely expired; the first turn re-reads the full conversation (token cost)", rec.ID, idle.Round(time.Minute))
	}
	if si, err := a.client.SessionInfo(context.Background(), rec.HermesSessionID); err == nil && si.Messages > 0 {
		return
	}
	if r := session.Recap(evs); r != "" {
		a.loop.ResumeNote = r
		logf("session %s: hermes-agent no longer has this conversation — the first turn carries a recap of the local history", rec.ID)
	}
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

func newApp() (*app, error) {
	cfg := loadCfgOrExit()
	repoRoot := physicalCwd()
	u := ui.New(os.Stderr)

	// known system: probe the machine once per session so the brain is told
	// what exists instead of rediscovering it with tool rounds (internal/
	// knowsystem). The previous session's learned-missing facts are merged in,
	// stale ones dropped by the fresh probe; the record lives in the home dir.
	knownPath := cfg.Home + "/known-system.json"
	known := knowsystem.Probe(repoRoot, gitBranch(repoRoot))
	known.UseGlobal(filepath.Join(cfg.Home, globalGuidanceFile)) // the rules imported at setup
	if old, err := knowsystem.Load(knownPath); err == nil {
		known.MergeLearned(old)
	} else if !os.IsNotExist(err) {
		warnf("known-system: %v", err)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		warnf("known-system: home: %v", err)
	}
	if err := known.Save(knownPath); err != nil {
		warnf("known-system: save: %v", err)
	}

	var vlogf func(string, ...any)
	if cfg.Verbose {
		vlogf = func(f string, a ...any) { u.DimLine("hermes-hands: " + fmt.Sprintf(f, a...)) }
	}

	client := &api.Client{
		HTTP:         api.NewHTTPClient(cfg.ConnectTimeout, cfg.MaxTime),
		BaseURL:      cfg.APIURL,
		Key:          cfg.APIKey,
		Profile:      cfg.APIProfile,
		SecretsPath:  cfg.SecretsPath,
		AllowHTTP:    cfg.AllowHTTP,
		Retries:      cfg.APIRetries,
		PollInterval: cfg.PollInterval,
		RunTimeout:   cfg.APIRunTimeout,
		Instructions: buildInstructions(resolveInstructions(cfg.InstrPath), known),
		StreamMode:   cfg.Stream,
		Warnf:        warnf,
		Log:          logf,
		Vlogf:        vlogf,
	}
	client.Guard = sandboxGuard(cfg.Sandbox, repoRoot, os.Getenv("HOME"))

	store, err := session.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	store.Warnf = warnf
	// Retention: sessions idle past the window go — events, tool output and
	// debug trace with them — so nothing typed by mistake lingers forever.
	if cfg.RetentionDays > 0 {
		maxAge := time.Duration(cfg.RetentionDays) * 24 * time.Hour
		if gone := store.Prune(maxAge); len(gone) > 0 {
			logf("retention: removed %d local session(s) idle more than %d days", len(gone), cfg.RetentionDays)
		}
		pruneFiles(cfg.Home+"/verbose", maxAge) // the dropped /reasoning logs
	}

	sh := shell.New(repoRoot, 2*cfg.MaxOutput)
	disp := &dispatch.Dispatcher{
		RepoRoot:   repoRoot,
		Shell:      sh,
		Approver:   &prompt.TTYApprover{Mode: cfg.Approve, Warnf: warnf},
		Deny:       cfg.Deny,
		MaxOutput:  cfg.MaxOutput,
		RunTimeout: time.Duration(cfg.RunTimeout) * time.Second,
	}
	lp := &loop.Loop{
		API: client, Dispatch: disp,
		MaxRounds: cfg.MaxRounds, Scrub: redact.Scrub,
		RepoRoot: repoRoot, GitBranch: func() string { return gitBranch(repoRoot) },
		Vlogf: vlogf, Warnf: warnf,
		OnResult: func(tool, out string, exit int) {
			if known.ObserveShell(out, exit) { // learned 127: persist immediately
				if err := known.Save(knownPath); err != nil {
					warnf("known-system: save: %v", err)
				}
			}
		},
	}
	return &app{cfg, repoRoot, u, client, store, sh, disp, lp}, nil
}

// sandboxGuard is the policy for Hermes' own tool calls in a hands run
// (HERMES_HANDS_SANDBOX): smart steers only what is aimed at the operator's
// machine or calls a hands tool as Hermes' own (guard.Check), steer every
// server-side exec / file call (guard.Strict), show never steers.
func sandboxGuard(mode, workspace, home string) func(tool, preview string) guard.Verdict {
	switch mode {
	case guard.Steer:
		return guard.Strict
	case guard.Show:
		return func(string, string) guard.Verdict { return guard.Verdict{} }
	}
	return func(tool, preview string) guard.Verdict { return guard.Check(tool, preview, workspace, home) }
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
	fmt.Fprintf(out, "\n  ⚠  hermes wants to run %s on its SERVER sandbox (not your machine):\n      %s\n", tool, what)
	line, err := ta.AskLine("  [o]nce  [s]ession  [n]o > ")
	if err != nil {
		return "deny"
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "y", "once":
		return "once"
	case "s", "session":
		return "session"
	}
	return "deny"
}

// pruneFiles deletes the files in dir older than maxAge, then dir itself once
// it is empty.
func pruneFiles(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && e.Type().IsRegular() && time.Since(fi.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails (and is meant to) while anything is left
}

// buildInstructions is everything Hermes is told up front, in one stable
// block (stable = cacheable): the protocol and behaviour (instructions.md),
// the known-system brief, then the operator's AGENTS.md / CLAUDE.md rules.
func buildInstructions(base string, known *knowsystem.System) string {
	s := base + "\n\n" + known.Brief()
	if g := known.GuidanceBlock(); g != "" {
		s += "\n\n" + g
	}
	return s
}

// resolveInstructions is the instructions ladder:
// $HERMES_HANDS_INSTRUCTIONS | ~/hermes-hands/instructions.md (first readable
// & non-empty) else the embedded share/instructions.md.
func resolveInstructions(path string) string {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimRight(string(b), "\n"); s != "" {
			return s
		}
	}
	return strings.TrimRight(builtinInstructions, "\n")
}

func physicalCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	if p, err := filepath.EvalSymlinks(wd); err == nil {
		return p
	}
	return wd
}

// gitBranch is the current branch name at root, or "" when root is not a git
// work tree (or git is missing). Used only to enrich the per-turn frame.
func gitBranch(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" { // detached
		return ""
	}
	return b
}

// --- REPL ---

// notConfigured reports whether there is no usable URL+key yet — nothing worth
// preflighting. Network is not touched.
func notConfigured(cfg *config.Config) bool {
	return config.LooksUnset(cfg.APIURL) || config.LooksUnset(cfg.APIKey)
}

func runREPL(smode string) int {
	a, err := newApp()
	if err != nil {
		fmt.Printf("BLOCKED: %v\n", err)
		return 1
	}
	defer func() { a.shell.Stop() }()

	configured := !notConfigured(a.cfg)
	if configured {
		if _, err := a.client.Check(context.Background()); err != nil {
			fmt.Printf("BLOCKED: %v\n", err)
			fmt.Fprintln(os.Stderr, "(fix the URL/key: `hermes-hands setup`, or /setup in-session)")
			return 1
		}
	}

	rec, err := a.store.Resolve(smode, a.repoRoot)
	if err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		return 1
	}

	agentSID := "" // only real once a turn has confirmed it
	if rec.Turns > 0 {
		agentSID = rec.HermesSessionID
	}
	a.ui.Banner(bareVersion(), a.repoRoot, agentSID)
	if !configured {
		fmt.Fprintln(os.Stderr, "hermes-hands is not configured — type /setup")
	}
	// a reopened session shows its last turns exactly as they ran
	evlog := openSession(a, rec, true)

	ln := liner.NewLiner()
	defer func() { ln.Close() }()
	ln.SetCtrlCAborts(true)

	var (
		turnMu     sync.Mutex
		cancelTurn context.CancelFunc
		runID      string          // in-flight Hermes run, for POST /v1/runs/{id}/stop
		streamBuf  strings.Builder // this turn's live-previewed answer text (dedupes the final render)
		stopWork   = func() {}     // stops this turn's animated "working" line (set per turn)
	)
	// The first streamed chunk means output is flowing — retire the spinner.
	fs := &finalStreamer{emit: func(s string) { stopWork(); streamBuf.WriteString(s); a.ui.Delta(s) }}

	// Silence watchdog: cancel a turn if hermes-agent goes quiet past
	// HERMES_HANDS_RESPONSE_TIMEOUT, but keep pushing that deadline back while
	// there are signs of life (streamed text, tool rounds, fresh sub-runs) or a
	// periodic out-of-band probe still finds the server run running.
	wd := &watchdog{timeout: a.cfg.ResponseTimeout, interval: a.cfg.WatchdogInterval}

	// abortTurn is what Ctrl-C does: cancel the turn context, kill the shell
	// child, stop the server run. Shared by the SIGINT handler and the watchdog.
	abortTurn := func() {
		turnMu.Lock()
		rid := runID
		if cancelTurn != nil {
			cancelTurn()
			a.shell.Kill()
		}
		turnMu.Unlock()
		if rid != "" { // stop the server run too, not just the local wait
			go a.client.StopRun(context.Background(), rid)
		}
	}
	wd.onFire = abortTurn
	wd.check = func(ctx context.Context) bool {
		turnMu.Lock()
		rid := runID
		turnMu.Unlock()
		if rid == "" {
			return true // between rounds: local dispatch is the holdup, not the brain
		}
		st, err := a.client.RunState(ctx, rid)
		if err != nil {
			return false // can't confirm it's alive — let the deadline run down
		}
		switch st {
		case "completed", "failed", "cancelled":
			return false
		default: // "", queued, started, running, stopping, or anything unknown
			return true
		}
	}

	// /debug: a redacted wire trace of the connection into the open session's
	// debug.jsonl (HERMES_HANDS_DEBUG=1 starts with it on). Re-pointed on every
	// session switch; deleted with its session by the retention window.
	debugOn := a.cfg.Debug
	applyDebug := func() {
		if debugOn {
			a.client.Trace = debuglog.New(a.store.DebugPath(rec), a.cfg.APIKey).Write
		} else {
			a.client.Trace = nil
		}
	}

	// Per-app wiring that must be redone when /setup swaps `a`:
	//  - the approval gate reads its answer through liner (one terminal owner;
	//    a separate /dev/tty reader deadlocks against liner's input goroutine),
	//  - OnRunStart records the run id so Ctrl-C / the watchdog can stop the
	//    server run; every run/delta/tool event bumps the watchdog.
	wireApp := func() {
		if ta, ok := a.disp.Approver.(*prompt.TTYApprover); ok {
			ta.Out = os.Stderr
			ta.AskLine = func(q string) (string, error) { return ln.Prompt(q) }
			ta.Pause = a.ui.Hold
		}
		a.client.OnRunStart = func(id string) {
			turnMu.Lock()
			runID = id
			turnMu.Unlock()
			// each round is a fresh envelope: a calls-round must not keep the
			// preview muted for the final round that follows it
			fs.reset()
			wd.bump()
		}
		a.client.OnDelta = func(s string) { wd.bump(); fs.feed(s) } // previews the `final` text; any token = progress
		a.client.OnEvent = func(string) { wd.bump() }               // any SSE frame is a sign of life
		a.client.OnApproval = func(tool, what string) string { return askServerApproval(a.disp.Approver, tool, what) }
		a.client.OnHermesTool = func(tool, preview, action string) {
			ev := evlog.add(session.Event{Kind: session.KindHermesTool, Tool: tool, Text: preview, Action: action})
			evlog.show(ev, "")
		}
		// every hands call: shown (and stored) BEFORE it runs, then its result
		a.loop.Emit = func(ev session.Event) {
			wd.bump() // a call starting or finishing = progress
			ev = evlog.add(ev)
			evlog.show(ev, ev.Out)
			if ev.Kind == session.KindCall {
				a.ui.SetActivity(fmt.Sprintf("#%d %s", ev.Ref, ev.Tool))
			} else {
				a.ui.SetActivity("")
			}
		}
		applyDebug()
	}
	wireApp()

	// SIGINT during a turn -> cancel the turn context, kill the shell child,
	// and stop the server run. At the prompt, liner turns Ctrl-C into
	// ErrPromptAborted itself (raw mode, no signal).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			abortTurn()
		}
	}()

	for {
		input, err := ln.Prompt(a.ui.SessionPrompt(rec.ID))
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
			a.ui.Help(a.repoRoot)
			fmt.Fprintln(os.Stderr)
			continue
		case "/setup":
			if code := doSetup(bufio.NewReader(os.Stdin), hermesHome(), false); code == 0 {
				if na, e := newApp(); e == nil {
					a.shell.Stop()
					a = na
					wireApp()
					wd.timeout = a.cfg.ResponseTimeout
					wd.interval = a.cfg.WatchdogInterval
					configured = !notConfigured(a.cfg)
					if _, e2 := a.client.Check(context.Background()); e2 != nil {
						fmt.Fprintf(os.Stderr, "BLOCKED: %v\n", e2)
					}
					if nr, e3 := a.store.Resolve(smode, a.repoRoot); e3 == nil {
						rec = nr
					}
					evlog = openSession(a, rec, true)
					applyDebug()
				}
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/session":
			if rest == "" { // no id -> show the current session's detail
				fresh, _ := a.store.Resolve(rec.ID, a.repoRoot)
				if fresh == nil {
					fresh = rec
				}
				fmt.Fprint(os.Stderr, session.FormatDetail(fresh))
				if si, e := a.client.SessionInfo(context.Background(), fresh.HermesSessionID); e == nil {
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
			nr, e := a.store.Resolve(rest, a.repoRoot)
			if e != nil {
				fmt.Fprintf(os.Stderr, "  %v\n\n", e)
				continue
			}
			rec = nr
			a.ui.NewSessionNote(rec.ID)
			evlog = openSession(a, rec, true)
			applyDebug()
			continue
		case "/history", "/chat-history":
			// /history = this session, every turn; /history <n> = its last n
			// turns; /history <id> = another session's (read-only — it never
			// switches the REPL to it)
			target, n := evlog, 0
			if rest != "" {
				if k, e := strconv.Atoi(rest); e == nil && k > 0 {
					n = k
				} else if other, e := a.store.Load(rest); e == nil {
					target = &sessionLog{store: a.store, ui: a.ui, rec: other}
				} else {
					fmt.Fprintf(os.Stderr, "  no such session: %s\n\n", rest)
					continue
				}
			}
			evs, e := a.store.LoadEvents(target.rec)
			if e != nil {
				warnf("history: %v", e)
				continue
			}
			target.replay(evs, n)
			fmt.Fprintln(os.Stderr)
			continue
		case "/out":
			evlog.showOutput(rest)
			continue
		case "/yolo":
			if ta, ok := a.disp.Approver.(*prompt.TTYApprover); ok {
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
				fmt.Fprintf(os.Stderr, "  debug: ON — redacted connection trace → %s\n", a.store.DebugPath(rec))
				fmt.Fprintf(os.Stderr, "  (local only, 0600, deleted with the session after %d idle days)\n", a.cfg.RetentionDays)
			} else {
				fmt.Fprintln(os.Stderr, "  debug: OFF")
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/check":
			if res, e := a.client.Check(context.Background()); e != nil {
				fmt.Fprintf(os.Stderr, "BLOCKED: %s\n", e)
			} else {
				configured = true
				fmt.Fprintf(os.Stderr, "API OK: %s @ %s\n", res.Model, res.Base)
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
			newID, e := a.client.Fork(context.Background(), rec.HermesSessionID)
			if e != nil {
				fmt.Fprintf(os.Stderr, "  BLOCKED: %v\n\n", e)
				continue
			}
			nr, _ := a.store.Resolve("new", a.repoRoot)
			nr.HermesSessionID = newID
			_, _ = a.store.SetTitleLocal(nr, "fork of "+rec.ID)
			rec = nr
			evlog = openSession(a, rec, false)
			applyDebug()
			fmt.Fprintf(os.Stderr, "  forked → hermes-agent session %s (local %s)\n\n", newID, rec.ID)
			continue
		case "/new":
			nr, e := a.store.Resolve("new", a.repoRoot)
			if e != nil {
				fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", e)
				continue
			}
			rec = nr
			a.ui.NewSessionNote(rec.ID)
			evlog = openSession(a, rec, false)
			applyDebug()
			continue
		case "/sessions":
			recs, _ := a.store.List()
			fmt.Fprint(os.Stderr, session.FormatList(recs))
			if ss, e := a.client.ListSessions(context.Background()); e == nil && len(ss) > 0 {
				fmt.Fprint(os.Stderr, "\n"+formatSrvList(ss))
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/config", "/hh-settings":
			// Read-only on purpose: hermes-hands never writes settings back —
			// edit the file by hand (see the header this prints).
			fmt.Fprint(os.Stderr, formatConfig(a.cfg))
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
		evlog.beginTurn()
		evlog.show(evlog.add(session.Event{Kind: session.KindUser, Text: redact.Scrub(input)}), "")
		turnStart := time.Now()
		stopWork = a.ui.StartWorking()

		streamBuf.Reset()
		fs.reset()
		ctx, cancel := context.WithCancel(context.Background())
		turnMu.Lock()
		cancelTurn = cancel
		runID = "" // no in-flight run yet this turn (the watchdog probe / Ctrl-C read this)
		turnMu.Unlock()

		wdStop := make(chan struct{})
		go wd.guard(wdStop)

		out := a.loop.Run(ctx, input, rec, func(runID, sid string, tok int) { _ = a.store.BumpTurn(rec, runID, sid, tok) })

		close(wdStop)
		timedOut := wd.disarm() // barrier: authoritative "did the watchdog fire?"
		stopWork()
		a.ui.SetActivity("")
		streamed := a.ui.StreamDone()
		turnMu.Lock()
		cancelTurn = nil
		turnMu.Unlock()
		interrupted := ctx.Err() != nil
		cancel()
		// Exclude time spent waiting on the operator's y/n/a/q answer at an
		// approval prompt — that is the operator deciding, not hermes-agent
		// working, and shouldn't inflate "worked for Xs".
		turnDur := time.Since(turnStart) - a.ui.Paused()

		end := session.Event{Kind: session.KindTurnEnd, Ms: turnDur.Milliseconds()}
		switch {
		case timedOut:
			end.Outcome, end.Limit = "timeout", int(a.cfg.ResponseTimeout/time.Second)
		case interrupted:
			end.Outcome = "interrupted"
		default:
			if out.OK {
				if changed, _ := a.store.SetTitleLocal(rec, input); changed && rec.HermesSessionID != "" {
					a.client.SetTitle(context.Background(), rec.HermesSessionID, rec.Title)
				}
			}
			ans := evlog.add(session.Event{Kind: session.KindAnswer, Text: redact.Scrub(out.Answer), OK: session.Bool(out.OK)})
			// The streamed preview already showed exactly this answer, in
			// the same shape — don't print it twice.
			if !streamed || strings.TrimSpace(streamBuf.String()) != strings.TrimSpace(out.Answer) {
				evlog.show(ans, "")
			}
		}
		evlog.show(evlog.add(end), "")
	}
	return 0
}

// --- subcommands ---

func runCheck() int {
	cfg := loadCfgOrExit()
	res, err := checkClient(cfg).Check(context.Background())
	if err != nil {
		fmt.Printf("BLOCKED: %s\n", err)
		return 1
	}
	fmt.Printf("API OK: %s @ %s\n", res.Model, res.Base)
	return 0
}

func checkClient(cfg *config.Config) *api.Client {
	return &api.Client{
		HTTP:         api.NewHTTPClient(cfg.ConnectTimeout, cfg.MaxTime),
		BaseURL:      cfg.APIURL,
		Key:          cfg.APIKey,
		Profile:      cfg.APIProfile,
		SecretsPath:  cfg.SecretsPath,
		AllowHTTP:    cfg.AllowHTTP,
		Retries:      cfg.APIRetries,
		PollInterval: cfg.PollInterval,
		RunTimeout:   cfg.APIRunTimeout,
		Warnf:        warnf,
		Log:          logf,
	}
}

func runSessions() int {
	cfg := loadCfgOrExit()
	store, err := session.Open(cfg.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 0
	}
	recs, _ := store.List()
	fmt.Print(session.FormatList(recs))
	if !notConfigured(cfg) {
		if ss, e := checkClient(cfg).ListSessions(context.Background()); e == nil && len(ss) > 0 {
			fmt.Print("\n" + formatSrvList(ss))
		}
	}
	return 0
}

// formatSrvList renders the server-side session list (from GET /api/sessions).
func formatSrvList(ss []api.SrvSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hermes-agent sessions (%d):\n", len(ss))
	fmt.Fprintf(&b, "%-40s  %5s  %7s  %s\n", "SESSION", "MSGS", "TOKENS", "TITLE")
	for _, s := range ss {
		id := s.ID
		if len(id) > 40 {
			id = id[:39] + "…"
		}
		tok := "-"
		if s.Tokens > 0 {
			tok = strconv.Itoa(s.Tokens)
		}
		title := s.Title
		if s.Parent != "" {
			title += "  (from " + s.Parent + ")"
		}
		fmt.Fprintf(&b, "%-40s  %5d  %7s  %s\n", id, s.Messages, tok, title)
	}
	return b.String()
}

// formatConfig renders the read-only `/config` view: the files hermes-hands
// reads, and the effective value of every hand-tunable knob with its env-var
// name. It never offers to write anything — the operator edits the config file
// directly, so a fat-fingered `/config KEY=value` can't wedge the install.
func formatConfig(cfg *config.Config) string {
	none := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	secs := func(d time.Duration) string {
		if d <= 0 {
			return "off"
		}
		return strconv.Itoa(int(d/time.Second)) + "s"
	}
	instr := "(built-in default)"
	if b, err := os.ReadFile(cfg.InstrPath); err == nil && strings.TrimSpace(string(b)) != "" {
		instr = cfg.InstrPath
	}
	var secrets string
	switch cfg.SecretsSource {
	case "secrets.enc":
		secrets = filepath.Join(cfg.Home, "secrets.enc") + "  (encrypted, machine-bound)"
	case "secrets":
		secrets = cfg.SecretsPath + "  (plaintext fallback — run `setup` to encrypt)"
	default:
		if cfg.APIKey != "" {
			secrets = "(from the environment / config file — no secrets file)"
		} else {
			secrets = "(none — run `hermes-hands setup`)"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  home         : %s\n", cfg.Home)
	fmt.Fprintf(&b, "  config file  : %s\n", cfg.ConfigPath)
	fmt.Fprintf(&b, "  secrets      : %s\n", secrets)
	fmt.Fprintf(&b, "  instructions : %s\n", instr)
	fmt.Fprintf(&b, "  sessions     : %s\n", filepath.Join(cfg.StateDir, "sessions"))
	b.WriteString("\n  effective settings — edit the config file by hand; hermes-hands never writes these:\n")
	for _, r := range [][2]string{
		{"HERMES_API_URL", none(cfg.APIURL)},
		{"HERMES_API_PROFILE", none(cfg.APIProfile)},
		{"HERMES_HANDS_APPROVE", cfg.Approve},
		{"HERMES_HANDS_STREAM", none(cfg.Stream)},
		{"HERMES_HANDS_RESPONSE_TIMEOUT", secs(cfg.ResponseTimeout)},
		{"HERMES_HANDS_WATCHDOG_INTERVAL", secs(cfg.WatchdogInterval)},
		{"HERMES_HANDS_RETENTION_DAYS", retention(cfg.RetentionDays)},
		{"HERMES_HANDS_DEBUG", onOff(cfg.Debug)},
		{"HERMES_HANDS_SANDBOX", cfg.Sandbox},
		{"HERMES_HANDS_MAX_ROUNDS", strconv.Itoa(cfg.MaxRounds)},
		{"HERMES_HANDS_RUN_TIMEOUT", strconv.Itoa(cfg.RunTimeout) + "s"},
		{"HERMES_API_RUN_TIMEOUT", secs(cfg.APIRunTimeout)},
		{"HERMES_HANDS_MAX_OUTPUT", strconv.Itoa(cfg.MaxOutput)},
		{"HERMES_HANDS_DENY", none(strings.Join(cfg.Deny, " | "))},
	} {
		fmt.Fprintf(&b, "    %-30s %s\n", r[0], r[1])
	}
	return b.String()
}

// runEventsDump is the hidden `_events` command: start a run and copy its raw
// SSE event stream to stdout, verbatim, so the wire shape can be inspected.
func runEventsDump(input string) int {
	if input == "" {
		input = "count from one to five"
	}
	cfg := loadCfgOrExit()
	if notConfigured(cfg) {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	cl := checkClient(cfg)
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
	if notConfigured(cfg) {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	body, err := checkClient(cfg).RawGet(context.Background(), path)
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
	if notConfigured(cfg) {
		fmt.Fprintln(os.Stderr, "not configured")
		return 1
	}
	cl := checkClient(cfg)
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
	rec, err := store.Resolve("new", physicalCwd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Println(rec.ID)
	return 0
}

func retention(days int) string {
	if days <= 0 {
		return "off (keep every session)"
	}
	return strconv.Itoa(days) + " days"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
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

// globalGuidanceFile (in the hermes-hands home) holds the operator's global
// agent rules, imported once at setup — stored knowledge every session puts in
// front of the repo's own AGENTS.md / CLAUDE.md, which are read fresh at each
// session start.
const globalGuidanceFile = "guidance.md"

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
	dst := filepath.Join(dir, globalGuidanceFile)
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
