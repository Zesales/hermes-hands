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

	"hermes-hands/internal/ttyio"
)

// lookPath is indirected so tests can force the plain (no-renderer) path.
var lookPath = exec.LookPath

// UI writes framed conversation output. Construct with New.
type UI struct {
	w   io.Writer
	tty bool

	cDim, cB, cR                   string
	cYou, cHermes, cOK, cBad, cAcc string

	mu      sync.Mutex    // serialises every write vs. the dock's repaint
	fd      int           // the terminal the UI writes to, for the dock's width; -1 = none
	working bool          // StartWorking … its stop: the dock is up (dock.go)
	held    int           // Hold()s open: a prompt owns the terminal, the dock is off
	up      int           // rows from the dock's top to the cursor: what wipe goes back up
	start   time.Time     // when the turn started working
	frame   int           // the working line's animation step
	paused  time.Duration // accumulated Hold() time this turn (approval waits) — reset by StartWorking

	activity      string      // what the spinner waits on ("#14 shell"); "" = plain "working"
	activitySince time.Time   // when that started — the spinner counts from here
	budget        string      // where the turn stands ("round 3/25 · calls 12/100"); "" = not shown
	streamOpen    bool        // Delta printed the "hermes" label; StreamDone closes it
	typeAhead     bool        // the operator can type during the turn (TypeAhead): the dock has its input line
	typing        string      // what they type (SetTyping); "" = the hint
	queue         []queuedMsg // what they queued this turn and the loop has not taken yet (Queue)
	nQueued       int         // how many they queued this turn — the last one's number
	approvals     string      // the turn's approval mode, in the dock's footer (SetApprovals)
}

// New builds a UI writing to stderr. Colour is on when stderr is a tty,
// NO_COLOR is empty, and TERM (default "dumb") is not "dumb".
func New(stderr *os.File) *UI {
	u := newUI(stderr, colorEnabled(stderr), ttyio.IsTerminal(stderr))
	if u.tty {
		u.fd = int(stderr.Fd())
	}
	return u
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
	u := &UI{w: w, tty: tty, fd: -1}
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
	u.sync(func() { fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, strings.Repeat("─", u.cols()), u.cR) })
}

// You ports ui_you: a bold cyan "you" label, then the message indented 3.
func (u *UI) You(msg string) {
	u.sync(func() {
		fmt.Fprintf(u.w, "%s%syou%s\n", u.cB, u.cYou, u.cR)
		io.WriteString(u.w, indentLines(msg, "   "))
	})
}

// Added is what the operator wrote while the turn ran, as it goes out with
// the next round: a dim "you (added)" label — live with the number it had in
// the dock's queue, which it leaves now — the message indented 3.
func (u *UI) Added(msg string) {
	u.sync(func() {
		label := " (added)"
		if len(u.queue) > 0 {
			label = fmt.Sprintf(" (added · %d)", u.queue[0].n)
			u.queue = u.queue[1:]
		}
		fmt.Fprintf(u.w, "%s%syou%s%s%s%s\n", u.cB, u.cYou, u.cR, u.cDim, label, u.cR)
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
// operator's message to the answer / cancel, approval waits left out) and how
// it ended. A turn whose hands ran calls splits that time into Hermes' share
// and the hands' (local) share, and names the approval wait it left out:
//
//	outcome ""          -> "— worked for 12s —"
//	                       "— worked for 1m06s · hermes 58s · local 8s · waited on you 1m48s —"
//	outcome "interrupted" -> "— interrupted after 8s —"  (Ctrl-C)
//	outcome "timeout"     -> "— timeout after 603s — no reply from hermes-agent
//	                          (HERMES_HANDS_RESPONSE_TIMEOUT=600) —"
func (u *UI) TurnDone(d time.Duration, outcome string, limitSec int, local, wait time.Duration, rounds, calls int) {
	var msg string
	switch outcome {
	case "interrupted":
		msg = "interrupted after " + humanDur(d)
	case "timeout":
		msg = fmt.Sprintf("timeout after %s — no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=%ds)", humanDur(d), limitSec)
	default:
		msg = "worked for " + humanDur(d)
		if local > 0 {
			msg += " · hermes " + shortDur(max(d-local, 0)) + " · local " + shortDur(local)
		}
		if wait >= time.Second {
			msg += " · waited on you " + humanDur(wait)
		}
	}
	if rounds > 0 { // what the turn took against the budget; absent in old logs
		msg += " · " + plural(rounds, "round") + " · " + plural(calls, "call")
	}
	u.sync(func() { fmt.Fprintf(u.w, "%s— %s —%s\n\n", u.cDim, msg, u.cR) })
}

// shortDur is humanDur that says "<1s" for a share below half a second
// instead of a misleading "0s".
func shortDur(d time.Duration) string {
	if d > 0 && d < 500*time.Millisecond {
		return "<1s"
	}
	return humanDur(d)
}

// humanDur is a compact turn duration: "8s" under a minute, "3m07s" over.
func humanDur(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return strconv.Itoa(s) + "s"
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// Line writes one plain line (spinner-safe); Line("") is a blank line.
func (u *UI) Line(s string) {
	u.sync(func() { fmt.Fprintln(u.w, s) })
}

// DimLine writes one dim line (used for HERMES_HANDS_VERBOSE chatter, matching
// hh_vlog's dim wrapping).
func (u *UI) DimLine(msg string) {
	u.sync(func() { fmt.Fprintf(u.w, "%s%s%s\n", u.cDim, msg, u.cR) })
}

// Delta streams one chunk of the answer as it arrives, after a blank line and
// under the same "hermes" label and indent Answer uses — so a streamed answer
// and the same answer replayed from the event log (a blank line, then Answer)
// look alike. StreamDone ends it.
func (u *UI) Delta(s string) {
	u.sync(func() {
		if !u.streamOpen {
			u.streamOpen = true
			fmt.Fprintf(u.w, "\n%s%shermes%s\n   ", u.cB, u.cHermes, u.cR)
		}
		io.WriteString(u.w, strings.ReplaceAll(s, "\n", "\n   "))
	})
}

// StreamDone closes a streamed answer (a final newline) and reports whether
// one was open.
func (u *UI) StreamDone() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.streamOpen {
		return false
	}
	u.streamOpen = false
	fmt.Fprintln(u.w)
	return true
}

// TurnDone and NewSessionNote also coordinate with the spinner.

// Help is the REPL `/help` block: what typing does, then the slash commands.
func (u *UI) Help(cwd string) {
	fmt.Fprintf(u.w, "%s  Type a message to Hermes, your assistant: it answers, and works this directory\n", u.cDim)
	fmt.Fprintf(u.w, "  through you — local_shell, local_read, local_write, local_edit in %s,\n", cwd)
	io.WriteString(u.w, "  each with your approval. @path attaches a file — an image, or a text file's content.\n")
	io.WriteString(u.w, "    /new          start a fresh session in this directory\n")
	io.WriteString(u.w, "    /session      show this session's detail (turns, tokens, splits)\n")
	io.WriteString(u.w, "    /session <id> switch to another session  (ids from /sessions)\n")
	io.WriteString(u.w, "    /sessions     list this machine's sessions  (/sessions -v: each with its hermes-agent session)\n")
	io.WriteString(u.w, "    /history      replay this session (/history <n> = last n turns, /history <id> = another)\n")
	io.WriteString(u.w, "    /out <n>      full output of call #n\n")
	io.WriteString(u.w, "    /fork         branch this session on the server, switch to it\n")
	io.WriteString(u.w, "    /compact      compact this session's chat now (a chat checkpoint; outputs stay readable)\n")
	io.WriteString(u.w, "    /compact-aggressive  also every earlier turn as question + answer — when /compact was not enough\n")
	io.WriteString(u.w, "    /context      what Hermes reads now (/context full = every message)\n")
	io.WriteString(u.w, "    /approve      approvals ask → plan → auto (or /approve ask|plan|auto|never)\n")
	io.WriteString(u.w, "    /yolo         toggle approvals for shell / write / edit\n")
	io.WriteString(u.w, "    /debug        toggle the redacted connection trace (session debug.jsonl)\n")
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
