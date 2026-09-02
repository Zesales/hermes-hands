package main

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Zesales/hermes-hands/internal/config"
)

func TestParseArgsTable(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want parsed
	}{
		{"none", nil, parsed{smode: "new"}},
		{"help short", []string{"-h"}, parsed{smode: "new", action: "help"}},
		{"help long", []string{"--help"}, parsed{smode: "new", action: "help"}},
		{"version -v", []string{"-v"}, parsed{smode: "new", action: "version"}},
		{"version word", []string{"version"}, parsed{smode: "new", action: "version"}},
		{"check", []string{"check"}, parsed{smode: "new", action: "check"}},
		{"check flag", []string{"--check"}, parsed{smode: "new", action: "check"}},
		{"sessions", []string{"sessions"}, parsed{smode: "new", action: "sessions"}},
		{"list flag", []string{"--list"}, parsed{smode: "new", action: "sessions"}},
		{"setup", []string{"setup"}, parsed{smode: "new", action: "setup"}},
		{"continue", []string{"-c"}, parsed{smode: "continue"}},
		{"continue long", []string{"--continue"}, parsed{smode: "continue"}},
		{"new explicit", []string{"--new"}, parsed{smode: "new"}},
		{"session id", []string{"--session", "hh_abc"}, parsed{smode: "hh_abc"}},
		{"session missing id", []string{"--session"}, parsed{smode: "new", errMsg: "--session needs an id"}},
		{"yolo", []string{"--yolo"}, parsed{smode: "new", yolo: true}},
		{"stdin dash", []string{"-"}, parsed{smode: "new", stdin: true}},
		{"oneshot words", []string{"why", "is", "ci", "red"}, parsed{smode: "new", oneshot: []string{"why", "is", "ci", "red"}}},
		{"double dash rest", []string{"--", "check", "--help"}, parsed{smode: "new", oneshot: []string{"check", "--help"}}},
		{"unknown option", []string{"--bogus"}, parsed{smode: "new", errMsg: "unknown option: --bogus"}},
		{"eager check wins mid-parse", []string{"foo", "check"}, parsed{smode: "new", action: "check", oneshot: []string{"foo"}}},
		{"continue then oneshot", []string{"-c", "and", "fix"}, parsed{smode: "continue", oneshot: []string{"and", "fix"}}},
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

func TestUsageTextByteExact(t *testing.T) {
	// The last line must not carry an extra trailing blank line (matches
	// `cat <<'EOF'`).
	if !strings.HasSuffix(usageText, "  --new   force a fresh session      --yolo   skip run/write approvals\n") {
		t.Errorf("usage text tail is wrong:\n%q", usageText[len(usageText)-90:])
	}
	if !strings.HasPrefix(usageText, "hermes-hands - terminal chat with a central Hermes brain over its Runs API.\n") {
		t.Errorf("usage text head is wrong")
	}
	if strings.Contains(usageText, "\n\n\n") {
		t.Errorf("usage text has a triple newline")
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

func TestNotConfigured(t *testing.T) {
	for _, tc := range []struct {
		url, key string
		want     bool
	}{
		{"", "", true},
		{"https://h.example.net", "", true},
		{"", "sk-real", true},
		{"https://h.example.net", "<REPLACE_ME>", true},
		{"https://h.example.net", "sk-real", false},
	} {
		got := notConfigured(&config.Config{APIURL: tc.url, APIKey: tc.key})
		if got != tc.want {
			t.Errorf("notConfigured(%q,%q) = %v, want %v", tc.url, tc.key, got, tc.want)
		}
	}
}

// blankConfigEnv points config.Load at an empty dir and clears the URL/key so
// the process looks brand-new.
func blankConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HERMES_API_URL", "HERMES_API_KEY", "HERMES_HANDS_CONFIG", "HERMES_HANDS_SECRETS",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestEnsureConfiguredNonTTYBlocks(t *testing.T) {
	blankConfigEnv(t)
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()

	code, ok := ensureConfigured()
	if ok || code != 1 {
		t.Errorf("ensureConfigured() with no config, non-TTY = (%d, %v), want (1, false)", code, ok)
	}
}

func TestEnsureConfiguredProceedsWhenSet(t *testing.T) {
	blankConfigEnv(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hermes-hands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "secrets"),
		"export HERMES_API_URL=\"https://h.example.net\"\nexport HERMES_API_KEY='sk-real'\n")

	code, ok := ensureConfigured()
	if !ok || code != 0 {
		t.Errorf("ensureConfigured() with a real secrets file = (%d, %v), want (0, true)", code, ok)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
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
