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

	"hermes-hands/internal/config"
	"hermes-hands/internal/ttyio"
)

// Run prompts, then writes the secrets store and makes sure the config sets
// every setting (EnsureConfig): a re-run (a new key, a new machine) keeps the
// operator's values. Default: a machine-bound secrets.enc + keyseed (nothing
// to add to the shell env).
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

	did, err := EnsureConfig(dir + "/config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Println(did)

	if plaintext {
		body := "# hermes-hands secrets - chmod 600, do not commit\n" +
			"export HERMES_API_URL=" + shellQuoteQ(url) + "\n" +
			"export HERMES_API_KEY=" + shellQuoteQ(key) + "\n"
		if err := os.WriteFile(dir+"/secrets", []byte(body), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
			return 1
		}
		fmt.Printf("wrote %s/secrets\n", dir)
		offerBashrc(in)
		ImportGuidance(in, dir)
		return 0
	}

	if err := config.WriteEncryptedSecrets(dir, url, key); err != nil {
		fmt.Fprintf(os.Stderr, "hermes-hands: %v\n", err)
		return 1
	}
	fmt.Printf("wrote %s/secrets.enc and %s/keyseed (machine-bound; nothing to add to your shell)\n", dir, dir)
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
