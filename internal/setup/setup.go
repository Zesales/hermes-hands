// Package setup is the first-run wizard: the gateway URL and key, written
// to the hermes-hands home as config + a machine-bound secrets store, and
// the offer to import the operator's global agent rules.
package setup

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/peterh/liner"

	"github.com/Zesales/hermes-hands/internal/config"
	"github.com/Zesales/hermes-hands/internal/ttyio"
)

const setupConfigBody = "# hermes-hands config  —  edit by hand; `/config` shows what is in effect\n" +
	"# HERMES_API_PROFILE=coder             # optional /p/<profile>/ prefix\n" +
	"# HERMES_HANDS_APPROVE=ask              # ask | auto | never\n" +
	"# HERMES_HANDS_STREAM=auto              # auto | on | off\n" +
	"# HERMES_HANDS_RESPONSE_TIMEOUT=600     # seconds hermes-agent may go silent in a turn before it is cancelled (0 = no limit)\n" +
	"# HERMES_HANDS_WATCHDOG_INTERVAL=200    # seconds between server-side 'still working?' checks that push that limit back\n" +
	"# HERMES_HANDS_RETENTION_DAYS=14        # local sessions idle longer are deleted on start, debug trace included (0 = keep)\n" +
	"# HERMES_HANDS_DEBUG=1                  # start with /debug on: redacted connection trace per session\n" +
	"# HERMES_HANDS_SANDBOX=smart            # Hermes' own server terminal/file tools: smart | steer | show\n"

// Run prompts, then writes config + the secrets store. Default: a
// machine-bound secrets.enc + keyseed (nothing to add to the shell env).
// --plaintext: the pre-M10 0600 `secrets` file + the ~/.bashrc offer, for
// people who inject via env or a secrets manager.
func Run(in *bufio.Reader, dir string, plaintext bool) int {
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
		ImportGuidance(in, dir)
		return 0
	}

	if err := config.WriteEncryptedSecrets(dir, url, key); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s/config, %s/secrets.enc and %s/keyseed (machine-bound; nothing to add to your shell)\n", dir, dir, dir)
	ImportGuidance(in, dir)
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

// Home mirrors config.hermesHome for the setup path: the single
// self-contained data directory (HERMES_HANDS_HOME, else $HOME/hermes-hands).
func Home() string {
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
