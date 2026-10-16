package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zesales/hermes-hands/internal/app"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

func TestParseArgsTable(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want parsed
	}{
		{"none", nil, parsed{}},
		{"help short", []string{"-h"}, parsed{action: "help"}},
		{"help long", []string{"--help"}, parsed{action: "help"}},
		{"version -v", []string{"-v"}, parsed{action: "version"}},
		{"version word", []string{"version"}, parsed{action: "version"}},
		{"check", []string{"check"}, parsed{action: "check"}},
		{"check flag", []string{"--check"}, parsed{action: "check"}},
		{"sessions", []string{"sessions"}, parsed{action: "sessions"}},
		{"list flag", []string{"--list"}, parsed{action: "sessions"}},
		{"setup", []string{"setup"}, parsed{action: "setup"}},
		{"session-list flag", []string{"--session-list"}, parsed{action: "sessions"}},
		{"-c is gone", []string{"-c"}, parsed{errMsg: "unknown option: -c"}},
		{"--continue is gone", []string{"--continue"}, parsed{errMsg: "unknown option: --continue"}},
		{"new explicit", []string{"--new"}, parsed{smode: "new"}},
		{"rpc", []string{"--rpc"}, parsed{rpc: true}},
		{"session bare needs an id", []string{"--session"}, parsed{errMsg: "--session needs a session id (ids from --session-list)"}},
		{"session id", []string{"--session", "hh_abc"}, parsed{smode: "hh_abc"}},
		{"session then flag needs an id", []string{"--session", "--yolo"}, parsed{errMsg: "--session needs a session id (ids from --session-list)", yolo: true}},
		{"sessions new", []string{"sessions", "new"}, parsed{action: "sessionnew"}},
		{"sessions list", []string{"sessions"}, parsed{action: "sessions"}},
		{"session-list verbose", []string{"--session-list-verbose"}, parsed{action: "sessions", verbose: true}},
		{"sessions -v", []string{"sessions", "-v"}, parsed{action: "sessions", verbose: true}},
		{"session-list --verbose", []string{"--session-list", "--verbose"}, parsed{action: "sessions", verbose: true}},
		{"yolo", []string{"--yolo"}, parsed{yolo: true}},
		{"unknown option", []string{"--bogus"}, parsed{errMsg: "unknown option: --bogus"}},
		{"stray argument", []string{"why is ci red"}, parsed{errMsg: "unexpected argument: why is ci red (there is no one-shot; start a session)"}},
		{"positional before subcommand still errors", []string{"foo", "check"}, parsed{errMsg: "unexpected argument: foo (there is no one-shot; start a session)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseArgs(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestUsageTextShape(t *testing.T) {
	if !strings.HasPrefix(usageText, "hermes-hands - terminal chat with a central Hermes brain over its Runs API.\n") {
		t.Errorf("usage text head is wrong")
	}
	if !strings.HasSuffix(usageText, "\n") || strings.HasSuffix(usageText, "\n\n") {
		t.Errorf("usage text must end with exactly one newline")
	}
	if strings.Contains(usageText, "\n\n\n") {
		t.Errorf("usage text has a triple newline")
	}
	for _, want := range []string{"--rpc", "sessions new", "/setup in-session", "/yolo in-session"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text missing %q", want)
		}
	}
	if strings.Contains(usageText, "hermes-hands -c") {
		t.Errorf("usage text still documents the removed -c flag")
	}
}

func TestVersionString(t *testing.T) {
	// With the build-time default and no ldflags, the git sha (if any) is
	// appended in parentheses; without it, just the bare version.
	got := versionString()
	if !strings.HasPrefix(got, "hermes-hands ") {
		t.Errorf("versionString = %q", got)
	}
	if strings.Contains(got, "(") && !strings.HasSuffix(got, ")") {
		t.Errorf("unbalanced sha parens: %q", got)
	}
}

func TestDoSetupPlaintext(t *testing.T) {
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()
	t.Setenv("HOME", t.TempDir()) // hermetic ~/.bashrc for offerBashrc

	dir := filepath.Join(t.TempDir(), "hermes-hands")
	in := bufio.NewReader(strings.NewReader("https://h.example.net\nsk-plainkey\nn\n"))
	if code := doSetup(in, dir, true); code != 0 {
		t.Fatalf("doSetup(--plaintext) = %d", code)
	}

	sec := filepath.Join(dir, "secrets")
	fi, err := os.Stat(sec)
	if err != nil {
		t.Fatalf("secrets not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets perm = %o, want 600", fi.Mode().Perm())
	}
	body, _ := os.ReadFile(sec)
	if !strings.Contains(string(body), "export HERMES_API_URL=https://h.example.net") ||
		!strings.Contains(string(body), "export HERMES_API_KEY=sk-plainkey") {
		t.Errorf("secrets body = %q", body)
	}
	for _, gone := range []string{"secrets.enc", "keyseed"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("--plaintext must not write %s", gone)
		}
	}
}

func TestDoSetupEncryptedDefault(t *testing.T) {
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()

	dir := filepath.Join(t.TempDir(), "hermes-hands")
	in := bufio.NewReader(strings.NewReader("https://h.example.net\nsk-enckey\n"))
	if code := doSetup(in, dir, false); code != 0 {
		t.Fatalf("doSetup(default) = %d", code)
	}

	for _, f := range []string{"config", "secrets.enc", "keyseed"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
		if f != "config" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %o, want 600", f, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets")); err == nil {
		t.Errorf("default setup must not write the plaintext secrets file")
	}
}

func TestDoSetupRejectsEmpty(t *testing.T) {
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()

	for _, in := range []string{
		"\n\n",                      // empty URL, empty key
		"https://h.example.net\n\n", // URL, empty key
		"\nsk-real\n",               // empty URL, key
	} {
		dir := filepath.Join(t.TempDir(), "hermes-hands")
		code := doSetup(bufio.NewReader(strings.NewReader(in)), dir, false)
		if code != 1 {
			t.Errorf("doSetup(%q) = %d, want 1", in, code)
		}
		for _, f := range []string{"config", "secrets.enc", "keyseed", "secrets"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				t.Errorf("doSetup(%q) wrote %s despite empty input", in, f)
			}
		}
	}
}

func TestShellQuoteQRoundTrips(t *testing.T) {
	for _, v := range []string{
		"https://hermes-api.example.net",
		"sk-abcDEF123._-",
		"has space",
		"quote'inside",
		"dollar$and&amp",
		"tab\tnewline\n",
		"",
	} {
		q := shellQuoteQ(v)
		if q == "" {
			t.Errorf("shellQuoteQ(%q) produced empty output", v)
		}
		// A bare (unquoted) result must have no unescaped shell metacharacters.
		if !strings.HasPrefix(q, "'") && !strings.HasPrefix(q, "$'") {
			for _, meta := range []string{" ", "\t", "'", "$", "&", "\n"} {
				if strings.Contains(v, meta) && strings.Contains(q, meta) && !strings.Contains(q, "\\"+meta) {
					t.Errorf("shellQuoteQ(%q) = %q leaves %q unescaped", v, q, meta)
				}
			}
		}
	}
}

func TestSplitCmd(t *testing.T) {
	for _, tc := range []struct{ in, cmd, rest string }{
		{"", "", ""},
		{"  ", "", ""},
		{"/help", "/help", ""},
		{"/session hh_abc123", "/session", "hh_abc123"},
		{"/session   hh_abc  ", "/session", "hh_abc"},
		{"hello world", "hello", "world"},
		{"just-one-word", "just-one-word", ""},
	} {
		c, r := splitCmd(tc.in)
		if c != tc.cmd || r != tc.rest {
			t.Errorf("splitCmd(%q) = (%q,%q), want (%q,%q)", tc.in, c, r, tc.cmd, tc.rest)
		}
	}
}

func TestIsSlashCmd(t *testing.T) {
	for in, want := range map[string]bool{
		"/quit": true, "/q": true, "/?": true, "/chat-history": true,
		"/etc/hosts": false, "/home/x/repo": false, "hello": false, "/": false, "": false,
	} {
		if got := isSlashCmd(in); got != want {
			t.Errorf("isSlashCmd(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRPCReqUnmarshal(t *testing.T) {
	var r rpcReq
	if err := json.Unmarshal([]byte(`{"id":7,"type":"turn","text":"hi"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Type != "turn" || r.Text != "hi" || string(r.ID) != "7" {
		t.Errorf("rpcReq = %+v", r)
	}
}

func TestOfferGuidanceImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("no ssh to the LAN\n"), 0o644)
	os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("pin versions exactly\n"), 0o644)
	os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("no ssh to the LAN"), 0o644) // same rules again
	dir := filepath.Join(t.TempDir(), "hermes-hands")
	dst := filepath.Join(dir, app.GlobalGuidanceFile)

	offerGuidanceImport(bufio.NewReader(strings.NewReader("n\n")), dir)
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("declined import must write nothing")
	}
	offerGuidanceImport(bufio.NewReader(strings.NewReader("y\n")), dir)
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Count(got, "no ssh to the LAN") != 1 || !strings.Contains(got, "pin versions exactly") ||
		!strings.Contains(got, "## from "+filepath.Join(home, ".claude", "CLAUDE.md")) {
		t.Errorf("guidance.md =\n%s", got)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Errorf("guidance.md perms = %v", fi.Mode().Perm())
	}
	// an existing import is only replaced on an explicit yes
	os.WriteFile(dst, []byte("hand-edited"), 0o600)
	offerGuidanceImport(bufio.NewReader(strings.NewReader("\n")), dir)
	if b, _ := os.ReadFile(dst); string(b) != "hand-edited" {
		t.Error("an existing import was replaced without a yes")
	}
}

func TestAskServerApproval(t *testing.T) {
	ask := func(mode, answer string, err error) string {
		var out strings.Builder
		ta := &prompt.TTYApprover{Mode: mode, Out: &out, AskLine: func(string) (string, error) { return answer, err }}
		return askServerApproval(ta, "terminal", "rm -rf /tmp/build")
	}
	for _, tc := range []struct {
		mode, answer string
		err          error
		want         string
	}{
		{"ask", "o", nil, "once"},
		{"ask", "s", nil, "session"},
		{"ask", "n", nil, "deny"},
		{"ask", "", nil, "deny"},
		{"ask", "o", io.EOF, "deny"}, // Ctrl-C / EOF at the prompt
		{"auto", "o", nil, "deny"},   // yolo never approves a server command unseen
		{"never", "o", nil, "deny"},
	} {
		if got := ask(tc.mode, tc.answer, tc.err); got != tc.want {
			t.Errorf("mode %s answer %q -> %q, want %q", tc.mode, tc.answer, got, tc.want)
		}
	}
	// the hands' [a]ll latch is another trust domain: it must not answer this
	ta := &prompt.TTYApprover{Mode: "ask", AskLine: func(string) (string, error) { return "a", nil }}
	if askServerApproval(ta, "terminal", "x") != "deny" {
		t.Error("'a' (the hands' all-latch) must not approve a server command")
	}
}

// A replayed approval prompt reads exactly as the live one did — the prompt
// text plus the answer the operator typed (echoed by the line reader).
func TestApprovalReplayMatchesLivePrompt(t *testing.T) {
	render := func(f func(u *ui.UI)) string {
		tmp, err := os.CreateTemp(t.TempDir(), "ui")
		if err != nil {
			t.Fatal(err)
		}
		f(ui.New(tmp))
		b, _ := os.ReadFile(tmp.Name())
		tmp.Close()
		return string(b)
	}
	for _, tc := range []struct {
		name, typed, answer string
		ask                 func(ta *prompt.TTYApprover)
	}{
		{"hands", "a", "all", func(ta *prompt.TTYApprover) { ta.Confirm("local_shell: git log", "") }},
		{"hands diff", "n", "no", func(ta *prompt.TTYApprover) { ta.Confirm("local_write: x.go", "+line 1\n+line 2") }},
		{"server", "s", "session", func(ta *prompt.TTYApprover) { askServerApproval(ta, "terminal", "rm -rf /tmp/x") }},
	} {
		var live strings.Builder
		var ev session.Event
		ta := &prompt.TTYApprover{Mode: "ask", Out: &live}
		ta.AskLine = func(q string) (string, error) { live.WriteString(q + tc.typed + "\n"); return tc.typed, nil }
		ta.OnAnswer = func(summary, detail, answer string, _ time.Duration) {
			ev = session.Event{Kind: session.KindApproval, Text: summary, Detail: detail, Action: answer}
		}
		tc.ask(ta)
		if ev.Action != tc.answer {
			t.Errorf("%s: recorded answer %q, want %q", tc.name, ev.Action, tc.answer)
		}
		if got := render(func(u *ui.UI) { u.Approval(ev.Text, ev.Detail, ev.Action) }); got != live.String() {
			t.Errorf("%s: replay\n%q\nlive\n%q", tc.name, got, live.String())
		}
	}
}
