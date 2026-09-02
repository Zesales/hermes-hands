// Package ui ports lib/ui.sh plus the REPL banner/help strings from
// bin/hermes-hands. Colour only on a tty (and not NO_COLOR / TERM=dumb); every
// line goes to the writer it is handed (stderr in the REPL). No alt-screen.
package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Zesales/hermes-hands/internal/ttyio"
)

// lookPath is indirected so tests can force the plain (no-renderer) path.
var lookPath = exec.LookPath

// UI writes framed conversation output. Construct with New.
type UI struct {
	w   io.Writer
	tty bool

	cDim, cB, cR                   string
	cYou, cHermes, cOK, cBad, cAcc string
}

// New builds a UI writing to stderr, matching lib/ui.sh's colour gate:
// stderr is a tty, NO_COLOR is empty, and TERM (default "dumb") is not "dumb".
func New(stderr *os.File) *UI {
	return newUI(stderr, colorEnabled(stderr), ttyio.IsTerminal(stderr))
}

func colorEnabled(stderr *os.File) bool {
	return ttyio.IsTerminal(stderr) && colorEnvOK()
}

// colorEnvOK is the environment half of lib/ui.sh's colour gate: NO_COLOR empty
// and TERM (default "dumb") not "dumb".
func colorEnvOK() bool {
	return os.Getenv("NO_COLOR") == "" && envOr("TERM", "dumb") != "dumb"
}

func newUI(w io.Writer, color, tty bool) *UI {
	u := &UI{w: w, tty: tty}
	if color {
		u.cDim, u.cB, u.cR = "\x1b[2m", "\x1b[1m", "\x1b[0m"
		u.cYou, u.cHermes = "\x1b[36m", "\x1b[32m"
		u.cOK, u.cBad, u.cAcc = "\x1b[32m", "\x1b[31m", "\x1b[35m"
	}
	return u
}

// cols ports ui_cols: COLUMNS if set and numeric, else 96, clamped to [40,120].
func (u *UI) cols() int {
	c := 96
	if v := strings.TrimSpace(os.Getenv("COLUMNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c = n
		}
	}
	if c > 120 {
		c = 120
	}
	if c < 40 {
		c = 40
	}
	return c
}

// Rule ports ui_rule: a full-width dim horizontal rule.
func (u *UI) Rule() {
	fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, strings.Repeat("─", u.cols()), u.cR)
}

// You ports ui_you: a bold cyan "you" label, then the message indented 3.
func (u *UI) You(msg string) {
	fmt.Fprintf(u.w, "%s%syou%s\n", u.cB, u.cYou, u.cR)
	io.WriteString(u.w, indentLines(msg, "   "))
}

// Working ports ui_working.
func (u *UI) Working() {
	fmt.Fprintf(u.w, "%s   %s⋯ working%s\n", u.cDim, u.cAcc, u.cR)
}

// Call ports ui_call: a per-tool progress line. The exit code is green when 0,
// red otherwise; the preview is clipped to 72 bytes.
func (u *UI) Call(tool, preview string, exit int) {
	ec := u.cOK
	if exit != 0 {
		ec = u.cBad
	}
	if len(preview) > 72 {
		preview = preview[:72]
	}
	fmt.Fprintf(u.w, "%s   ⟩ %-7s%s %s%s%s  %sexit %d%s\n",
		u.cDim, tool, u.cR, u.cDim, preview, u.cR, ec, exit, u.cR)
}

// Answer ports ui_answer: a bold green "hermes" label, then the answer rendered
// through glow / bat (only on a tty) or fmt if present, else raw — every line
// indented 3.
func (u *UI) Answer(text string) {
	fmt.Fprintf(u.w, "%s%shermes%s\n", u.cB, u.cHermes, u.cR)
	w := strconv.Itoa(u.cols() - 3)
	var rendered string
	switch {
	case u.tty && have("glow"):
		rendered = pipe("glow", []string{"-w", w, "-"}, text+"\n")
	case u.tty && have("bat"):
		rendered = pipe("bat", []string{"-pp", "-l", "md", "--color=always"}, text+"\n")
	case have("fmt"):
		rendered = pipe("fmt", []string{"-s", "-w", w}, text+"\n")
	default:
		rendered = text + "\n"
	}
	io.WriteString(u.w, indentLines(strings.TrimSuffix(rendered, "\n"), "   "))
}

// Banner ports the two REPL header lines (bin/hermes-hands:141-142). The
// session id is shown with any leading "hh_" stripped.
func (u *UI) Banner(version, cwd, sessionID string) {
	fmt.Fprintf(u.w, "%s%shermes-hands%s %s%s  %s·%s  %s\n",
		u.cB, u.cHermes, u.cR, u.cDim, version, u.cDim, u.cR, cwd)
	fmt.Fprintf(u.w, "%ssession %s  ·  /help  /new  /sessions  /exit%s\n\n",
		u.cDim, strings.TrimPrefix(sessionID, "hh_"), u.cR)
}

// YouPrompt is the REPL input prompt. It MUST stay free of ANSI escapes:
// liner.Prompt rejects any prompt containing a control rune (ErrInvalidPrompt),
// which would make the REPL exit immediately on a colour-capable terminal.
func (u *UI) YouPrompt() string {
	return "you ❯ "
}

// NewSessionNote ports the `/new` line: "— new session <id> —" (dim), then a
// blank line, with any leading "hh_" stripped from the id.
func (u *UI) NewSessionNote(id string) {
	fmt.Fprintf(u.w, "%s— new session %s —%s\n\n", u.cDim, strings.TrimPrefix(id, "hh_"), u.cR)
}

// InterruptedNote marks a turn cancelled by Ctrl-C (no bash equivalent — bash
// dies on SIGINT; the Go REPL returns to the prompt).
func (u *UI) InterruptedNote() {
	fmt.Fprintf(u.w, "%s— interrupted —%s\n\n", u.cDim, u.cR)
}

// DimLine writes one dim line (used for HERMES_HANDS_VERBOSE chatter, matching
// hh_vlog's dim wrapping).
func (u *UI) DimLine(msg string) {
	fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, msg, u.cR)
}

// Help ports the REPL `_help` block (bin/hermes-hands:145-152).
func (u *UI) Help() {
	fmt.Fprintf(u.w, "%s  type a message to talk to Hermes; it drives shell/read/write here.\n", u.cDim)
	io.WriteString(u.w, "    /new       fresh session in this directory\n")
	io.WriteString(u.w, "    /sessions  list local sessions\n")
	io.WriteString(u.w, "    /check     re-test the API\n")
	io.WriteString(u.w, "    /exit      quit  (Ctrl-D also)\n")
	fmt.Fprintf(u.w, "  one-shot for scripts:  hermes-hands \"question\"%s\n", u.cR)
}

func have(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func pipe(name string, args []string, input string) string {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return input
	}
	return string(out)
}

// indentLines prefixes every line (mirrors `sed 's/^/PREFIX/'` after a
// `printf '%s\n'`), always ending with a newline.
func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
