package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCallStartShowsRefToolAndCommand(t *testing.T) {
	var b strings.Builder
	u := newUI(&b, false, false)
	head, more := Summarize("shell", json.RawMessage(`{"cmd":"cd x\nmake a\nmake b\nmake c\nmake d","timeout":600}`))
	u.CallStart(14, "shell", head, more)
	want := " #14 shell  cd x   (timeout 600s)\n" +
		"            make a\n" +
		"            make b\n" +
		"            … +2 lines\n"
	if b.String() != want {
		t.Errorf("CallStart =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestSummarizeFileTools(t *testing.T) {
	if h, _ := Summarize("read_file", json.RawMessage(`{"path":"internal/ui/ui.go"}`)); h != "internal/ui/ui.go" {
		t.Errorf("read_file head = %q", h)
	}
	if h, _ := Summarize("write_file", json.RawMessage(`{"path":"a.txt","content":"1\n2\n3"}`)); h != "a.txt   (3 lines)" {
		t.Errorf("write_file head = %q", h)
	}
}

func TestCallDoneExcerptSuccess(t *testing.T) {
	var b strings.Builder
	out := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10"
	newUI(&b, false, false).CallDone(3, "shell", 0, 72*time.Second, out)
	want := " #3 ✓ exit 0 · 1m12s · 10 lines\n" +
		"     │ l1\n" +
		"     │ l2\n" +
		"     │ … 5 lines — /out 3\n" +
		"     │ l8\n" +
		"     │ l9\n" +
		"     │ l10\n"
	if b.String() != want {
		t.Errorf("CallDone =\n%s\nwant\n%s", b.String(), want)
	}
}

func TestCallDoneExcerptFailureKeepsErrors(t *testing.T) {
	var b strings.Builder
	lines := []string{"compiling", "main.go:3: undefined: foo", "a", "b", "c", "d", "e", "f", "FAIL pkg"}
	newUI(&b, false, false).CallDone(4, "shell", 1, 0, strings.Join(lines, "\n"))
	got := b.String()
	for _, want := range []string{"#4 ✗ exit 1 · 9 lines", "│ main.go:3: undefined: foo", "│ FAIL pkg", "… 1 line — /out 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "│ compiling") {
		t.Errorf("a plain early line must stay hidden:\n%s", got)
	}
}

func TestCallDoneReadFileHasNoExcerpt(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).CallDone(5, "read_file", 0, 0, "package x\n\nfunc a() {}\n")
	if b.String() != " #5 ✓ exit 0 · 3 lines\n" {
		t.Errorf("read_file CallDone = %q", b.String())
	}
}

func TestCallDoneStripsANSIAndNamesSpecialExits(t *testing.T) {
	var b strings.Builder
	newUI(&b, false, false).CallDone(6, "shell", 124, 0, "\x1b[31mred\x1b[0m\r\n")
	if b.String() != " #6 ✗ exit 124 (timed out / shell reset) · 1 line\n     │ red\n" {
		t.Errorf("CallDone = %q", b.String())
	}
}

func TestHermesToolLines(t *testing.T) {
	var b strings.Builder
	u := newUI(&b, false, false)
	u.HermesTool("web_search", "go release", "")
	u.HermesTool("terminal", "make test", "steered")
	u.HermesTool("tool_call", "Local Shell", "repeat")
	u.HermesTool("tool_call", "Local Edit", "stopped")
	got := b.String()
	if !strings.Contains(got, "· hermes web_search  go release") ||
		!strings.Contains(got, "⚠ hermes-sandbox terminal  make test") ||
		!strings.Contains(got, "steered back to the hands") ||
		!strings.Contains(got, "⚠ hermes-sandbox tool_call  Local Shell\n     ↳ again — this run was steered for it already; not again") ||
		!strings.Contains(got, "⚠ hermes-sandbox tool_call  Local Edit\n     ↳ run stopped — hermes-hands asks once more, with a correction") {
		t.Errorf("HermesTool =\n%s", got)
	}
}

func TestDeltaStreamsUnderAnswerLabel(t *testing.T) {
	var b strings.Builder
	u := newUI(&b, false, false)
	u.Delta("line one\nli")
	u.Delta("ne two")
	if !u.StreamDone() || u.StreamDone() {
		t.Error("StreamDone must report the open stream exactly once")
	}
	// identical to a blank line + Answer's plain rendering of the same text
	if b.String() != "\nhermes\n   line one\n   line two\n" {
		t.Errorf("streamed = %q", b.String())
	}
}
