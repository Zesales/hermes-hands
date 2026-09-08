// Package ui is the framed conversation output plus the REPL banner / help
// strings. Colour only on a tty (and not NO_COLOR / TERM=dumb); every line
// goes to the writer it is handed (stderr in the REPL). No alt-screen.
package ui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

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

	mu       sync.Mutex // serialises every write vs. the spinner goroutine
	spinning bool
	paused   time.Duration // accumulated Hold() time this turn (approval waits) — reset by StartWorking
}

// New builds a UI writing to stderr. Colour is on when stderr is a tty,
// NO_COLOR is empty, and TERM (default "dumb") is not "dumb".
func New(stderr *os.File) *UI {
	return newUI(stderr, colorEnabled(stderr), ttyio.IsTerminal(stderr))
}

func colorEnabled(stderr *os.File) bool {
	return ttyio.IsTerminal(stderr) && colorEnvOK()
}

// colorEnvOK is the environment half of the colour gate: NO_COLOR empty and
// TERM (default "dumb") not "dumb".
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

// sync runs f as the sole writer, wiping the spinner's line first (if one is
// running) so f's output doesn't collide with it.
func (u *UI) sync(f func()) {
	u.mu.Lock()
	if u.spinning {
		fmt.Fprint(u.w, "\r\x1b[K")
	}
	f()
	u.mu.Unlock()
}

// StartWorking shows an animated "…working" line until the returned stop is
// called (idempotent, safe from any goroutine). Non-tty: the old static line.
func (u *UI) StartWorking() (stop func()) {
	if !u.tty {
		u.Working() // the plain static line; no goroutine, no repaint
		return func() {}
	}
	u.mu.Lock()
	u.spinning = true
	u.paused = 0 // fresh turn: no approval waits counted against it yet
	u.mu.Unlock()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		frames := []string{".  ", ".. ", "...", " ..", "  .", "   "}
		tk := time.NewTicker(220 * time.Millisecond)
		defer tk.Stop()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			case <-tk.C:
				u.mu.Lock()
				el := int((time.Since(start) - u.paused).Seconds())
				if u.spinning {
					fmt.Fprintf(u.w, "\r%s   %s%sworking%s%s  ·  %ds  ·  Ctrl+C to cancel%s\x1b[K",
						u.cDim, u.cAcc, frames[i%len(frames)], u.cR, u.cDim, el, u.cR)
				}
				u.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			u.mu.Lock()
			u.spinning = false
			fmt.Fprint(u.w, "\r\x1b[K")
			u.mu.Unlock()
		})
	}
}

// Hold suspends the spinner's repaint (wiping its current line) until the
// returned resume runs. Wrap it around a foreground prompt that owns the line
// — e.g. the approval y/n/a/q gate — so the spinner can't overwrite it. The
// held interval is excluded from the elapsed counter (both the live spinner
// and the turn's final "worked for Xs", via Paused()): waiting on the
// operator's y/n/a/q answer is not hermes-agent taking time to work.
func (u *UI) Hold() (resume func()) {
	u.mu.Lock()
	was := u.spinning
	u.spinning = false
	heldSince := time.Now()
	if was {
		fmt.Fprint(u.w, "\r\x1b[K")
	}
	u.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			u.paused += time.Since(heldSince)
			u.spinning = was
			u.mu.Unlock()
		})
	}
}

// Paused reports how long the current turn has spent held (approval prompts)
// so far. Subtract it from a wall-clock turn duration to get actual working
// time, e.g. before TurnDone.
func (u *UI) Paused() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.paused
}

// Rule ports ui_rule: a full-width dim horizontal rule.
func (u *UI) Rule() {
	u.sync(func() { fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, strings.Repeat("─", u.cols()), u.cR) })
}

// You ports ui_you: a bold cyan "you" label, then the message indented 3.
func (u *UI) You(msg string) {
	u.sync(func() {
		fmt.Fprintf(u.w, "%s%syou%s\n", u.cB, u.cYou, u.cR)
		io.WriteString(u.w, indentLines(msg, "   "))
	})
}

// Working is the pre-animation static line — non-tty and tests use it.
func (u *UI) Working() {
	u.sync(func() {
		fmt.Fprintf(u.w, "%s   %s⋯ working%s%s  ·  Ctrl+C to cancel%s\n",
			u.cDim, u.cAcc, u.cR, u.cDim, u.cR)
	})
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
	u.sync(func() {
		fmt.Fprintf(u.w, "%s   ⟩ %-7s%s %s%s%s  %sexit %d%s\n",
			u.cDim, tool, u.cR, u.cDim, preview, u.cR, ec, exit, u.cR)
	})
}

// Answer prints a bold green "hermes" label, then the answer indented 3. On a
// tty it renders the markdown through `glow` or `bat` when either is installed;
// otherwise the text is left exactly as Hermes sent it (raw markdown reads fine
// in a terminal, and `fmt` mangled code blocks / lists so it was dropped).
func (u *UI) Answer(text string) {
	w := strconv.Itoa(u.cols() - 3)
	var rendered string
	switch {
	case u.tty && have("glow"):
		rendered = pipe("glow", []string{"-w", w, "-"}, text+"\n")
	case u.tty && have("bat"):
		rendered = pipe("bat", []string{"-pp", "-l", "md", "--color=always"}, text+"\n")
	default:
		rendered = text + "\n"
	}
	u.sync(func() {
		fmt.Fprintf(u.w, "%s%shermes%s\n", u.cB, u.cHermes, u.cR)
		io.WriteString(u.w, indentLines(strings.TrimSuffix(rendered, "\n"), "   "))
	})
}

// Banner is the REPL header: name + version + cwd, then the hermes-agent-side
// session id (the local worker id lives in the prompt, every line), then the
// command list. hermesID is "" until the first turn confirms it.
func (u *UI) Banner(version, cwd, hermesID string) {
	agent := hermesID + " "
	if hermesID == "" {
		agent = "(pending — confirmed on the first turn) "
	}
	fmt.Fprintf(u.w, "%s%shermes-hands%s %s%s  ·  %s%s\n",
		u.cB, u.cHermes, u.cR, u.cDim, version, cwd, u.cR)
	fmt.Fprintf(u.w, "%s%shermes-agent%s%s · session %s%s\n",
		u.cB, u.cAcc, u.cR, u.cDim, strings.TrimRight(agent, " "), u.cR)
	fmt.Fprintf(u.w, "%s/help  /new  /sessions  /check  /exit%s\n\n", u.cDim, u.cR)
}

// SessionPrompt is the REPL input prompt: "hermes-hands - session <id> > ". It
// MUST stay free of ANSI escapes — liner.Prompt rejects any prompt containing a
// control rune (ErrInvalidPrompt), which would make the REPL exit right after
// the banner on a colour-capable terminal.
func (u *UI) SessionPrompt(id string) string {
	return "hermes-hands - session " + strings.TrimPrefix(id, "hh_") + " > "
}

// NewSessionNote is the `/new` line: "— new session <id> —" (dim), then a
// blank line, with any leading "hh_" stripped from the id.
func (u *UI) NewSessionNote(id string) {
	u.sync(func() {
		fmt.Fprintf(u.w, "%s— new session %s —%s\n\n", u.cDim, strings.TrimPrefix(id, "hh_"), u.cR)
	})
}

// TurnDone prints the one-line turn footer: how long the turn ran (from the
// operator's message to the answer / cancel) and how it ended.
//
//	outcome ""          -> "— worked for 12s —"
//	outcome "interrupted" -> "— interrupted after 8s —"  (Ctrl-C)
//	outcome "timeout"     -> "— timeout after 603s — no reply from hermes-agent
//	                          (HERMES_HANDS_RESPONSE_TIMEOUT=600) —"
func (u *UI) TurnDone(d time.Duration, outcome string, limitSec int) {
	var msg string
	switch outcome {
	case "interrupted":
		msg = "interrupted after " + humanDur(d)
	case "timeout":
		msg = fmt.Sprintf("timeout after %s — no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=%ds)", humanDur(d), limitSec)
	default:
		msg = "worked for " + humanDur(d)
	}
	u.sync(func() { fmt.Fprintf(u.w, "%s— %s —%s\n\n", u.cDim, msg, u.cR) })
}

// humanDur is a compact turn duration: "8s" under a minute, "3m07s" over.
func humanDur(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return strconv.Itoa(s) + "s"
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// DimLine writes one dim line (used for HERMES_HANDS_VERBOSE chatter, matching
// hh_vlog's dim wrapping).
func (u *UI) DimLine(msg string) {
	fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, msg, u.cR)
}

// Delta streams one answer-text chunk during a turn (dim, no newline) — a live
// preview; the clean final still renders via Answer afterwards.
func (u *UI) Delta(s string) {
	u.sync(func() {
		if u.cDim != "" {
			fmt.Fprintf(u.w, "%s%s%s", u.cDim, s, u.cR)
		} else {
			fmt.Fprint(u.w, s)
		}
	})
}

// TurnDone and NewSessionNote also coordinate with the spinner.

// Help is the REPL `/help` block: what typing does, then the slash commands.
func (u *UI) Help(cwd string) {
	fmt.Fprintf(u.w, "%s  Type a message to your Hermes brain. It works this directory through\n", u.cDim)
	fmt.Fprintf(u.w, "  you — shell, read_file, write_file, edit_file in %s,\n", cwd)
	io.WriteString(u.w, "  each with your approval.\n")
	io.WriteString(u.w, "    /new          start a fresh session in this directory\n")
	io.WriteString(u.w, "    /session      show this session's detail (turns, tokens, splits)\n")
	io.WriteString(u.w, "    /session <id> switch to another session  (ids from /sessions)\n")
	io.WriteString(u.w, "    /sessions     list this machine's + hermes-agent's sessions\n")
	io.WriteString(u.w, "    /fork         branch this session on the server, switch to it\n")
	io.WriteString(u.w, "    /yolo         toggle approvals for shell / write / edit\n")
	io.WriteString(u.w, "    /setup        (re)configure the gateway URL + key\n")
	io.WriteString(u.w, "    /config       config file path + effective settings (read-only; edit by hand)\n")
	io.WriteString(u.w, "    /check        re-test the gateway connection\n")
	io.WriteString(u.w, "    /help         show this\n")
	fmt.Fprintf(u.w, "    /exit         quit  (Ctrl-D too; Ctrl-C cancels the running turn)%s\n", u.cR)
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
