package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hermes-hands/internal/api"
	"hermes-hands/internal/config"
	"hermes-hands/internal/debuglog"
	"hermes-hands/internal/dispatch"
	"hermes-hands/internal/knowsystem"
	"hermes-hands/internal/loop"
	"hermes-hands/internal/prompt"
	"hermes-hands/internal/redact"
	"hermes-hands/internal/session"
	"hermes-hands/internal/shell"
	"hermes-hands/internal/turn"
	"hermes-hands/internal/ui"
)

// App is one hermes-hands instance, built from the configuration: the UI,
// the API client, the session store, the hands' shell and dispatcher, and
// the delegation loop. The REPL, --rpc and the subcommands all start here.
type App struct {
	Cfg      *config.Config
	RepoRoot string
	UI       *ui.UI
	Client   *api.Client
	Store    *session.Store
	Shell    *shell.Shell
	Disp     *dispatch.Dispatcher
	Loop     *loop.Loop
}

// New builds the app for cfg in the current directory. builtin is the
// embedded share/instructions.md — the fallback when no instructions file
// overrides it.
func New(cfg *config.Config, builtin string) (*App, error) {
	repoRoot := PhysicalCwd()
	u := ui.New(os.Stderr)
	known, knownPath := probeKnown(cfg, repoRoot)

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
		Instructions: buildInstructions(resolveInstructions(cfg.InstrPath, builtin), known),
		StreamMode:   cfg.Stream,
		Warnf:        Warnf,
		Log:          Logf,
		Vlogf:        vlogf,
	}
	client.Guard = sandboxGuard(cfg.Sandbox, repoRoot, os.Getenv("HOME"))

	store, err := openStore(cfg)
	if err != nil {
		return nil, err
	}

	sh := shell.New(repoRoot, 2*cfg.MaxOutput)
	disp := &dispatch.Dispatcher{
		RepoRoot:   repoRoot,
		Shell:      sh,
		Approver:   &prompt.TTYApprover{Mode: cfg.Approve, Warnf: Warnf},
		Deny:       cfg.Deny,
		MaxOutput:  cfg.MaxOutput,
		RunTimeout: time.Duration(cfg.RunTimeout) * time.Second,
		Rules:      known.Scoped(), // a subdirectory's rules, at the first call into it
	}
	lp := &loop.Loop{
		API: client.Runs(), Dispatch: disp,
		MaxRounds: cfg.MaxRounds, Scrub: redact.Scrub,
		RepoRoot: repoRoot, GitBranch: func() string { return gitBranch(repoRoot) },
		Vlogf: vlogf, Warnf: Warnf,
		OnResult: func(tool, out string, exit int) {
			if known.ObserveShell(out, exit) { // learned 127: persist immediately
				if err := known.Save(knownPath); err != nil {
					Warnf("known-system: save: %v", err)
				}
			}
		},
	}
	return &App{Cfg: cfg, RepoRoot: repoRoot, UI: u, Client: client, Store: store, Shell: sh, Disp: disp, Loop: lp}, nil
}

// probeKnown probes the machine once per session, so the brain is told what
// exists instead of rediscovering it with tool rounds (internal/knowsystem).
// The previous session's learned-missing facts are merged in, stale ones
// dropped by the fresh probe; the record lives in the home dir.
func probeKnown(cfg *config.Config, repoRoot string) (known *knowsystem.System, path string) {
	path = cfg.Home + "/known-system.json"
	known = knowsystem.Probe(repoRoot, gitBranch(repoRoot))
	known.UseGlobal(filepath.Join(cfg.Home, GlobalGuidanceFile)) // the rules imported at setup
	if old, err := knowsystem.Load(path); err == nil {
		known.MergeLearned(old)
	} else if !os.IsNotExist(err) {
		Warnf("known-system: %v", err)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		Warnf("known-system: home: %v", err)
	}
	if err := known.Save(path); err != nil {
		Warnf("known-system: save: %v", err)
	}
	return known, path
}

// openStore opens the session store and applies the retention window:
// sessions idle past it go — events, tool output and debug trace with them —
// so nothing typed by mistake lingers forever.
func openStore(cfg *config.Config) (*session.Store, error) {
	store, err := session.Open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	store.Warnf = Warnf
	if cfg.RetentionDays > 0 {
		maxAge := time.Duration(cfg.RetentionDays) * 24 * time.Hour
		if gone := store.Prune(maxAge); len(gone) > 0 {
			Logf("retention: removed %d local session(s) idle more than %d days", len(gone), cfg.RetentionDays)
		}
		pruneFiles(cfg.Home+"/verbose", maxAge) // the dropped /reasoning logs
	}
	return store, nil
}

// Bind points the turn runner at a's parts — again after /setup rebuilt a.
func (a *App) Bind(r *turn.Runner) {
	r.Client, r.Loop, r.Store, r.Shell = a.Client, a.Loop, a.Store, a.Shell
	r.Timeout, r.Interval = a.Cfg.ResponseTimeout, a.Cfg.WatchdogInterval
}

// SetDebug turns the redacted wire trace of the connection on or off, into
// rec's debug.jsonl (HERMES_HANDS_DEBUG=1 starts with it on). Re-pointed on
// every session switch; deleted with its session by the retention window.
func (a *App) SetDebug(rec *session.Record, on bool) {
	if on {
		a.Client.Trace = debuglog.New(a.Store.DebugPath(rec), a.Cfg.APIKey).Write
	} else {
		a.Client.Trace = nil
	}
}

// CheckClient is an API client for cfg alone — what the subcommands that
// need no session (check, sessions, the dev commands) talk through.
func CheckClient(cfg *config.Config) *api.Client {
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
		Warnf:        Warnf,
		Log:          Logf,
	}
}
