// Command hermes-hands is a terminal chat client for a central Hermes brain,
// spoken over Hermes' Runs API (POST /v1/runs, its SSE events, or a poll of
// GET /v1/runs/{id}). Hermes holds the plan and the memory; this binary is
// its hands — a persistent local shell (local_shell) plus local_read /
// local_write / local_edit — in the repo you are standing in. Outbound only:
// nothing listens on your machine.
//
// The tool-call envelope Hermes replies with is the same shape split-runtime
// (NousResearch/hermes-agent#63966) expects, so the local dispatcher does not
// change when that lands — only the transport. Everything lives under
// internal/; internal/cli is where a run starts.
package main

import (
	"os"

	"hermes-hands/internal/cli"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "0.0.0-dev"
	commit  = ""
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Build{Version: version, Commit: commit, Instructions: builtinInstructions}))
}
