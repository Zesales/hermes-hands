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
	newUI(&b, false, false).Banner("0.1.0", "/home/x/repo", "hh_20260902T101112_abc123")
	want := "hermes-hands 0.1.0  ·  /home/x/repo\n" +
		"session 20260902T101112_abc123  ·  /help  /new  /sessions  /exit\n\n"
	if b.String() != want {
		t.Errorf("Banner =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestHelpByteExact(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).Help()
	want := "  type a message to talk to Hermes; it drives shell/read/write here.\n" +
		"    /new       fresh session in this directory\n" +
		"    /sessions  list local sessions\n" +
		"    /check     re-test the API\n" +
		"    /exit      quit  (Ctrl-D also)\n" +
		"  one-shot for scripts:  hermes-hands \"question\"\n"
	if b.String() != want {
		t.Errorf("Help =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestColorCodesWhenEnabled(t *testing.T) {
	var b strings.Builder
	newUI(&b, true, true).Working()
	if !strings.Contains(b.String(), "\x1b[2m") || !strings.Contains(b.String(), "\x1b[35m") {
		t.Errorf("Working with colour = %q, want dim + accent codes", b.String())
	}
}

func TestYouPromptHasNoControlRunes(t *testing.T) {
	// liner.Prompt rejects any prompt containing a control rune with
	// ErrInvalidPrompt, which silently kills the REPL on a colour terminal.
	for _, color := range []bool{false, true} {
		p := newUI(io.Discard, color, true).YouPrompt()
		for _, r := range p {
			if r == 0x1b || (r < 0x20 && r != '\t') {
				t.Fatalf("YouPrompt(color=%v) = %q contains control rune %U", color, p, r)
			}
		}
	}
}
