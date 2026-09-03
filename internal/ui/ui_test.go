package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestColorEnvGate(t *testing.T) {
	for _, tc := range []struct {
		term, noColor string
		want          bool
	}{
		{"xterm-256color", "", true},
		{"xterm-256color", "1", false},
		{"dumb", "", false},
		{"", "", false}, // TERM unset defaults to "dumb"
	} {
		t.Setenv("TERM", tc.term)
		t.Setenv("NO_COLOR", tc.noColor)
		if got := colorEnvOK(); got != tc.want {
			t.Errorf("colorEnvOK(TERM=%q NO_COLOR=%q) = %v, want %v", tc.term, tc.noColor, got, tc.want)
		}
	}
}

func TestColsClamp(t *testing.T) {
	u := newUI(io.Discard, false, false)
	for _, tc := range []struct {
		cols string
		want int
	}{
		{"10", 40},
		{"200", 120},
		{"", 96},
		{"88", 88},
	} {
		t.Setenv("COLUMNS", tc.cols)
		if got := u.cols(); got != tc.want {
			t.Errorf("cols(COLUMNS=%q) = %d, want %d", tc.cols, got, tc.want)
		}
	}
}

func TestRulePlain(t *testing.T) {
	t.Setenv("COLUMNS", "50")
	var b strings.Builder
	newUI(&b, false, false).Rule()
	if b.String() != strings.Repeat("─", 50)+"\n" {
		t.Errorf("Rule = %q", b.String())
	}
}

func TestYouIndentsEveryLine(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).You("hello\nworld")
	if b.String() != "you\n   hello\n   world\n" {
		t.Errorf("You = %q", b.String())
	}
}

func TestCallClipsPreviewAt72(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).Call("shell", strings.Repeat("x", 100), 0)
	want := "   ⟩ shell   " + strings.Repeat("x", 72) + "  exit 0\n"
	if b.String() != want {
		t.Errorf("Call =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestAnswerPlainPathIndents(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = old }()

	var b strings.Builder
	newUI(&b, false, false).Answer("line one\nline two")
	if b.String() != "hermes\n   line one\n   line two\n" {
		t.Errorf("Answer = %q", b.String())
	}
}

func TestBannerByteExact(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).Banner("0.1.0", "/home/x/repo", "hh-agent-99")
	want := "hermes-hands 0.1.0  ·  /home/x/repo\n" +
		"hermes-agent · session hh-agent-99\n" +
		"/help  /new  /sessions  /check  /exit\n\n"
	if b.String() != want {
		t.Errorf("Banner =\n%q\nwant\n%q", b.String(), want)
	}

	// no confirmed session id yet -> a "pending" note, no fabricated id
	var b2 strings.Builder
	newUI(&b2, false, false).Banner("0.1.0", "/r", "")
	if !strings.Contains(b2.String(), "hermes-agent · session (pending") {
		t.Errorf("empty hermesID = %q", b2.String())
	}
}

func TestHelpByteExact(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).Help("/home/x/repo")
	got := b.String()
	for _, want := range []string{
		"Type a message to your Hermes brain",
		"in /home/x/repo,",
		"/new", "/session <id>", "/sessions", "/fork", "/yolo", "/setup", "/check", "/help", "/exit",
		"Ctrl-C cancels the running turn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Help missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "one-shot") {
		t.Errorf("Help must not mention one-shot: %q", got)
	}
}

func TestColorCodesWhenEnabled(t *testing.T) {
	var b strings.Builder
	newUI(&b, true, true).Working()
	if !strings.Contains(b.String(), "\x1b[2m") || !strings.Contains(b.String(), "\x1b[35m") {
		t.Errorf("Working with colour = %q, want dim + accent codes", b.String())
	}
}

func TestWorkingShowsCancelHint(t *testing.T) {
	for _, color := range []bool{false, true} {
		var b strings.Builder
		newUI(&b, color, true).Working()
		if !strings.Contains(b.String(), "⋯ working") || !strings.Contains(b.String(), "Ctrl+C to cancel") {
			t.Errorf("Working(color=%v) = %q", color, b.String())
		}
	}
}

func TestSessionPromptHasNoControlRunes(t *testing.T) {
	// liner.Prompt rejects any prompt containing a control rune with
	// ErrInvalidPrompt, which silently kills the REPL on a colour terminal.
	for _, color := range []bool{false, true} {
		p := newUI(io.Discard, color, true).SessionPrompt("hh_20260902T160152_8fcc06")
		for _, r := range p {
			if r == 0x1b || (r < 0x20 && r != '\t') {
				t.Fatalf("SessionPrompt(color=%v) = %q contains control rune %U", color, p, r)
			}
		}
		if p != "hermes-hands - session 20260902T160152_8fcc06 > " {
			t.Errorf("SessionPrompt = %q", p)
		}
	}
}

func TestStartWorking_NonTTYStaticAndIdempotent(t *testing.T) {
	var b strings.Builder
	u := newUI(&b, false, false) // non-tty
	stop := u.StartWorking()
	stop()
	stop() // idempotent, must not panic
	if !strings.Contains(b.String(), "working") {
		t.Errorf("non-tty StartWorking should print the static line, got %q", b.String())
	}
}

func TestSyncClearsSpinnerLine(t *testing.T) {
	var b strings.Builder
	u := newUI(&b, true, true)
	u.mu.Lock()
	u.spinning = true
	u.mu.Unlock()
	u.Call("shell", "ls", 0)
	if !strings.HasPrefix(b.String(), "\r\x1b[K") {
		t.Errorf("Call while spinning must clear the line first, got %q", b.String())
	}
	if !strings.Contains(b.String(), "shell") {
		t.Errorf("Call content missing: %q", b.String())
	}
}
