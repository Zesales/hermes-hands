package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermes-hands/internal/app"
	"hermes-hands/internal/config"
)

func TestDoSetupPlaintext(t *testing.T) {
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()
	t.Setenv("HOME", t.TempDir()) // hermetic ~/.bashrc for offerBashrc

	dir := filepath.Join(t.TempDir(), "hermes-hands")
	in := bufio.NewReader(strings.NewReader("https://h.example.net\nsk-plainkey\nn\n"))
	if code := Run(in, dir, true); code != 0 {
		t.Fatalf("Run(--plaintext) = %d", code)
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
	if code := Run(in, dir, false); code != 0 {
		t.Fatalf("Run(default) = %d", code)
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

func TestDoSetupKeepsExistingConfig(t *testing.T) {
	stdinTTY = func() bool { return false }
	defer func() { stdinTTY = func() bool { return true } }()

	dir := filepath.Join(t.TempDir(), "hermes-hands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const mine = "HERMES_HANDS_MAX_ROUNDS=40\n"
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(strings.NewReader("https://h.example.net\nsk-enckey\n"))
	if code := Run(in, dir, false); code != 0 {
		t.Fatalf("Run = %d", code)
	}
	b, _ := os.ReadFile(cfg)
	got := config.Assignments(b)
	if !strings.HasPrefix(string(b), mine) || got["HERMES_HANDS_MAX_ROUNDS"] != "40" {
		t.Errorf("setup changed a value the operator set:\n%s", b)
	}
	if got["HERMES_HANDS_MAX_CALLS"] != "100" || got["HERMES_HANDS_APPROVE"] != "ask" {
		t.Errorf("setup should append the missing settings at their defaults:\n%s", b)
	}
	if did, _ := EnsureConfig(cfg); !strings.Contains(did, "sets every setting") {
		t.Errorf("a second run should change nothing, said %q", did)
	}

	// No config yet: setup creates it with every default set.
	dir2 := filepath.Join(t.TempDir(), "hermes-hands")
	in = bufio.NewReader(strings.NewReader("https://h.example.net\nsk-enckey\n"))
	if code := Run(in, dir2, false); code != 0 {
		t.Fatalf("Run = %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir2, "config")); string(b) != configBody() {
		t.Errorf("fresh config = %q, want the default body", b)
	}
}

func TestEnsureConfigReplacesCommentOnlyConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config")
	const old = "# hermes-hands config\n# HERMES_HANDS_APPROVE=ask\n"
	if err := os.WriteFile(cfg, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	did, err := EnsureConfig(cfg)
	if err != nil || !strings.HasPrefix(did, "replaced") {
		t.Fatalf("EnsureConfig = %q, %v", did, err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != configBody() {
		t.Errorf("config not replaced by the defaults: %q", b)
	}
	if b, _ := os.ReadFile(cfg + ".bak"); string(b) != old {
		t.Errorf("old config not kept as .bak: %q", b)
	}
	if got := config.Assignments([]byte(configBody())); len(got) != len(configDefaults) {
		t.Errorf("the fresh body sets %d keys, want all %d", len(got), len(configDefaults))
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
		code := Run(bufio.NewReader(strings.NewReader(in)), dir, false)
		if code != 1 {
			t.Errorf("Run(%q) = %d, want 1", in, code)
		}
		for _, f := range []string{"config", "secrets.enc", "keyseed", "secrets"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				t.Errorf("Run(%q) wrote %s despite empty input", in, f)
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

	ImportGuidance(bufio.NewReader(strings.NewReader("n\n")), dir)
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("declined import must write nothing")
	}
	ImportGuidance(bufio.NewReader(strings.NewReader("y\n")), dir)
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
	ImportGuidance(bufio.NewReader(strings.NewReader("\n")), dir)
	if b, _ := os.ReadFile(dst); string(b) != "hand-edited" {
		t.Error("an existing import was replaced without a yes")
	}
}
