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
	"fmt"
	"os"
	"runtime/debug"
)

// Injected at build time via -ldflags "-X main.version=... -X main.commit=...".
// The Makefile fills these from VERSION and `git rev-parse --short HEAD`.
var (
	version = "0.0.0-dev"
	commit  = ""
)

// usageText is the verbatim body of the bash `usage()` heredoc
// (bin/hermes-hands). It must stay byte-identical.
const usageText = `hermes-hands - terminal chat with a central Hermes brain over its Runs API.
Hermes holds the plan/memory; it drives a persistent local shell plus
read_file / write_file / edit_file to see and act on the repo you're in.

  hermes-hands                    open a session in the current repo (main use)
  hermes-hands -c                 resume this repo's last session
  hermes-hands --session <id>     open a specific session
  hermes-hands sessions           list local sessions
  hermes-hands setup              interactive first-run config
  hermes-hands check              preflight the API connection
  hermes-hands --version          print version

  hermes-hands "message"          one-shot (scripting); also: … | hermes-hands -
  --new   force a fresh session      --yolo   skip run/write approvals
`

// versionString mirrors bash hh_version: "hermes-hands <ver>" plus " (<sha>)"
// only when a build sha is known. When no ldflags were passed (plain
// `go build` / `go install`), fall back to the module build info.
func versionString() string {
	ver, sha := version, commit
	if ver == "0.0.0-dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			if v := info.Main.Version; v != "" && v != "(devel)" {
				ver = v
			}
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && sha == "" && len(s.Value) >= 7 {
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

func main() {
	// M0 skeleton: only --help / --version are wired. The full CLI surface
	// (REPL, one-shot, stdin, setup, check, sessions) lands in M7.
	for _, a := range os.Args[1:] {
		switch a {
		case "-h", "--help":
			fmt.Print(usageText)
			return
		case "-v", "--version", "version":
			fmt.Println(versionString())
			return
		}
	}
	fmt.Fprintln(os.Stderr, "hermes-hands: this build is an M0 skeleton - only --help and --version are wired yet")
	os.Exit(2)
}
