package main

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Zesales/hermes-hands/internal/app"
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
