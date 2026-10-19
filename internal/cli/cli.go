// Package cli is hermes-hands' command line: it reads the arguments, then
// runs the REPL, --rpc, or one of the subcommands (check, sessions, setup,
// guidance, version and the hidden dev commands).
package cli

import (
	"bufio"
	"fmt"
	"os"

	"hermes-hands/internal/config"
	"hermes-hands/internal/repl"
	"hermes-hands/internal/rpc"
	"hermes-hands/internal/setup"
)

// Build is what main knows at build time: the version and commit (set via
// -ldflags) and the embedded share/instructions.md.
type Build struct {
	Version, Commit string
	Instructions    string
}

// Main runs hermes-hands with args (os.Args[1:]) and returns the exit code.
func Main(args []string, b Build) int {
	p := parseArgs(args)
	if p.yolo {
		// A flag: wins over the environment and the config file. Also
		// exported, so a hermes-hands started from the hands' shell sees it.
		config.SetFlag("HERMES_HANDS_APPROVE", "auto")
		_ = os.Setenv("HERMES_HANDS_APPROVE", "auto")
	}

	switch p.action {
	case "help":
		fmt.Print(usageText)
		return 0
	case "version":
		fmt.Println(b.versionString())
		return 0
	case "check":
		return b.runCheck()
	case "sessions":
		return runSessions(p.verbose)
	case "sessionnew":
		return runSessionNew()
	case "setup":
		return b.runSetup(args)
	case "guidance":
		setup.ImportGuidance(bufio.NewReader(os.Stdin), setup.Home())
		return 0
	case "events":
		return runEventsDump(p.smode)
	case "raw":
		return runRawGet(p.smode)
	case "run":
		return runRawRun(p.smode)
	}
	if p.errMsg != "" {
		fmt.Fprintf(os.Stderr, "hermes-hands: %s\n", p.errMsg)
		return 1
	}

	// Every mode works ON a session. A bare REPL run starts a fresh one (a
	// session is one task); resuming is always EXPLICIT by id (`--session <id>`,
	// `/session <id>`, rpc `use`) — the way editor plugins work: they keep the
	// id from the `ready` event. `--rpc` defaults to a fresh session and the
	// plugin switches to its stored one with {"type":"use"}.
	if p.rpc {
		return rpc.Run(orElse(p.smode, "new"), rpc.Deps{NewApp: b.newApp, Version: b.bareVersion()})
	}
	return repl.Run(orElse(p.smode, "new"), repl.Deps{
		NewApp:  b.newApp,
		Setup:   func() int { return setup.Run(bufio.NewReader(os.Stdin), setup.Home(), false) },
		Version: b.bareVersion(),
	})
}
