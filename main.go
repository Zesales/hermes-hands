// Command hermes-hands is a terminal chat client for a central Hermes brain,
// spoken over Hermes' merged Runs API (POST /v1/runs + poll GET /v1/runs/{id}).
// Hermes holds the plan/memory; this binary drives a persistent local shell plus
// read_file / write_file / edit_file so Hermes can see and act on the repo you
// are standing in. Outbound only: nothing listens on your machine.
//
// This is the Go port of the original bash bundle. The wire contract and every
// user-facing string are kept byte-identical; see docs/go-port-plan.md.
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
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/peterh/liner"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/dispatch"
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

A session is one task. Continue it, or start a new one — there is no one-shot.

  hermes-hands                    continue this repo's latest session (main use)
  hermes-hands --session          same, explicitly
  hermes-hands --session <id>     open a specific session  (id from --session-list)
  hermes-hands --new              start a fresh session (new task)
  hermes-hands --rpc              JSON-lines session server for an editor/plugin

  hermes-hands --session-list     list sessions with id / turns / tokens
  hermes-hands sessions new       mint a session id (for --session / --rpc)
  hermes-hands setup              first-run config  (also: /setup in-session)
  hermes-hands check              preflight the API connection
  hermes-hands --version          print version

  --yolo   skip run/write approvals   (also: /yolo in-session)
`

// parsed is the outcome of the argv scan.
type parsed struct {
	action string // "" | help | version | check | sessions | sessionnew | setup
	errMsg string // non-empty => fatal (unknown option / stray argument)
	smode  string // "" (== continue) | new | continue | <id>
	rpc    bool
	yolo   bool
}

// parseArgs scans argv, first match winning; the eager subcommands return
// immediately. There is no positional message: every mode works on a session
// (see runREPL / runRPC).
func parseArgs(args []string) parsed {
	p := parsed{}
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
		case "--rpc":
			p.rpc = true
		case "-c", "--continue":
			p.smode = "continue"
		case "--new":
			p.smode = "new"
		case "--session":
			// optional id: "--session" alone == continue the latest;
			// "--session <id>" opens that one.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				p.smode = args[i]
			} else {
				p.smode = "continue"
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
		// Exported before config.Load so nested config parsing sees it too
		// (plan §2 #17).
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
	}
	if p.errMsg != "" {
		fmt.Fprintf(os.Stderr, "hermes-hands: %s\n", p.errMsg)
		os.Exit(1)
	}

	// Every mode works ON a session: a bare run (or --session with no id)
	// continues this repo's latest, --new starts one, --session <id> opens a
	// specific one, --rpc lets a caller hold one. No throwaway session per
	// invocation, so the central Hermes isn't fragmented.
	if p.rpc {
		os.Exit(runRPC(orElse(p.smode, "continue")))
	}
	os.Exit(runREPL(orElse(p.smode, "continue")))
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

// --- version strings (bash hh_version + HH_VERSION) ---

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

// --- shared logging (bash hh_warn / hh_log) ---

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
		Instructions: resolveInstructions(cfg.InstrPath),
		Warnf:        warnf,
		Log:          logf,
		Vlogf:        vlogf,
	}

	store, err := session.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	store.Warnf = warnf

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
		API: client, Dispatch: disp, UI: u,
		MaxRounds: cfg.MaxRounds, Scrub: redact.Scrub,
		RepoRoot: repoRoot, GitBranch: func() string { return gitBranch(repoRoot) },
		Vlogf: vlogf, Warnf: warnf,
	}
	return &app{cfg, repoRoot, u, client, store, sh, disp, lp}, nil
}

// resolveInstructions ports the hh_api_ask instructions ladder:
// $HERMES_HANDS_INSTRUCTIONS | ~/.config/hermes-hands/instructions.md (first
// readable & non-empty) else the embedded share/instructions.md.
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

	ln := liner.NewLiner()
	defer func() { ln.Close() }()
	ln.SetCtrlCAborts(true)

	// SIGINT during a turn -> cancel the turn context + kill the shell child,
	// then fall back to the prompt. At the prompt, liner turns Ctrl-C into
	// ErrPromptAborted itself (raw mode, no signal).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	var (
		turnMu     sync.Mutex
		cancelTurn context.CancelFunc
	)
	go func() {
		for range sigCh {
			turnMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
				a.shell.Kill()
			}
			turnMu.Unlock()
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
		case "/exit", "/quit", "/q":
			return 0
		case "/help", "/h", "/?":
			a.ui.Help(a.repoRoot)
			fmt.Fprintln(os.Stderr)
			continue
		case "/setup":
			if code := doSetup(bufio.NewReader(os.Stdin), xdgConfigHome()+"/hermes-hands", false); code == 0 {
				if na, e := newApp(); e == nil {
					a.shell.Stop()
					a = na
					configured = !notConfigured(a.cfg)
					if _, e2 := a.client.Check(context.Background()); e2 != nil {
						fmt.Fprintf(os.Stderr, "BLOCKED: %v\n", e2)
					}
					if nr, e3 := a.store.Resolve(smode, a.repoRoot); e3 == nil {
						rec = nr
					}
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
		case "/check":
			if res, e := a.client.Check(context.Background()); e != nil {
				fmt.Fprintf(os.Stderr, "BLOCKED: %s\n", e)
			} else {
				configured = true
				fmt.Fprintf(os.Stderr, "API OK: %s @ %s\n", res.Model, res.Base)
			}
			fmt.Fprintln(os.Stderr)
			continue
		case "/new":
			nr, e := a.store.Resolve("new", a.repoRoot)
			if e != nil {
				fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", e)
				continue
			}
			rec = nr
			a.ui.NewSessionNote(rec.ID)
			continue
		case "/sessions":
			recs, _ := a.store.List()
			fmt.Fprint(os.Stderr, session.FormatList(recs))
			fmt.Fprintln(os.Stderr)
			continue
		}

		if !configured {
			fmt.Fprintln(os.Stderr, "  not configured — type /setup first")
			fmt.Fprintln(os.Stderr)
			continue
		}

		ln.AppendHistory(input)
		fmt.Fprintln(os.Stderr)
		a.ui.Rule()
		a.ui.You(input)
		a.ui.Working()

		ctx, cancel := context.WithCancel(context.Background())
		turnMu.Lock()
		cancelTurn = cancel
		turnMu.Unlock()

		out := a.loop.Run(ctx, input, rec, func(runID, sid string, tok int) { _ = a.store.BumpTurn(rec, runID, sid, tok) })

		turnMu.Lock()
		cancelTurn = nil
		turnMu.Unlock()
		interrupted := ctx.Err() != nil
		cancel()

		if interrupted {
			a.ui.InterruptedNote()
			continue
		}
		if out.OK {
			if changed, _ := a.store.SetTitleLocal(rec, input); changed && rec.HermesSessionID != "" {
				a.client.SetTitle(context.Background(), rec.HermesSessionID, rec.Title)
			}
		}
		fmt.Fprintln(os.Stderr)
		a.ui.Answer(out.Answer)
		fmt.Fprintln(os.Stderr)
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

// --- setup (bash hh_setup) ---

const setupConfigBody = "# hermes-hands config\n" +
	"# HERMES_API_PROFILE=coder   # optional /p/<profile>/ prefix\n" +
	"# HERMES_HANDS_APPROVE=ask    # ask | auto | never\n"

func runSetup() int {
	plaintext := false
	for _, a := range os.Args[1:] {
		if a == "--plaintext" {
			plaintext = true
		}
	}
	if code := doSetup(bufio.NewReader(os.Stdin), xdgConfigHome()+"/hermes-hands", plaintext); code != 0 {
		return code
	}
	// bash re-execs `"$_self" check`; the Go port calls check directly
	// (plan §2 #21 — $_self is unset in a released bundle).
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
		return 0
	}

	if err := config.WriteEncryptedSecrets(dir, url, key); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s/config, %s/secrets.enc and %s/keyseed (machine-bound; nothing to add to your shell)\n", dir, dir, dir)
	return 0
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
	line := `[ -f "$HOME/.config/hermes-hands/secrets" ] && . "$HOME/.config/hermes-hands/secrets"`
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

func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.config"
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
