package cli

import "strings"

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
// (see repl.Run / rpc.Run).
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

func orElse(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
