package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/app"
	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/setup"
)

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

// newApp builds the app for this directory from the configuration.
func (b Build) newApp() (*app.App, error) { return app.New(loadCfgOrExit(), b.Instructions) }

func (b Build) runCheck() int {
	cfg := loadCfgOrExit()
	res, err := app.CheckClient(cfg).Server().Check(context.Background())
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
	rid, sid, err := cl.Runs().RawRun(context.Background(), input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "run_id=%s session_id=%s\n--- /v1/runs/%s/events ---\n", rid, sid, rid)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := cl.Runs().RawEvents(ctx, rid, os.Stdout); err != nil {
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
	body, err := app.CheckClient(cfg).Server().RawGet(context.Background(), path)
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
	rid, sid, err := cl.Runs().RawRunBody(context.Background(), []byte(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "run_id=%s session_id=%s\n--- events ---\n", rid, sid)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := cl.Runs().RawEvents(ctx, rid, os.Stdout); err != nil {
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

// runSetup is `hermes-hands setup [--plaintext]`: the wizard, then a check.
func (b Build) runSetup(args []string) int {
	plaintext := false
	for _, a := range args {
		if a == "--plaintext" {
			plaintext = true
		}
	}
	if code := setup.Run(bufio.NewReader(os.Stdin), setup.Home(), plaintext); code != 0 {
		return code
	}
	// setup then runs check directly.
	_ = b.runCheck()
	return 0
}
