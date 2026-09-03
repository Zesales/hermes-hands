package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clearEnv blanks every variable Load consults so a test starts from a known
// state regardless of the caller's environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HERMES_HANDS_CONFIG", "HERMES_HANDS_SECRETS", "HERMES_HANDS_STATE",
		"HERMES_HANDS_INSTRUCTIONS", "HERMES_API_URL", "HERMES_API_KEY",
		"HERMES_API_PROFILE", "HERMES_HANDS_APPROVE", "HERMES_HANDS_DENY",
		"HERMES_HANDS_ALLOW_HTTP", "HERMES_HANDS_VERBOSE",
		"HERMES_API_CONNECT_TIMEOUT", "HERMES_API_MAX_TIME", "HERMES_API_POLL_INTERVAL",
		"HERMES_API_RUN_TIMEOUT", "HERMES_API_RETRIES", "HERMES_HANDS_MAX_ROUNDS",
		"HERMES_HANDS_RUN_TIMEOUT", "HERMES_HANDS_MAX_OUTPUT", "HERMES_HANDS_STREAM",
		"HERMES_HANDS_RESPONSE_TIMEOUT", "HERMES_HANDS_WATCHDOG_INTERVAL",
		"HERMES_HANDS_HOME",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("HOME", t.TempDir())
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Approve != "ask" {
		t.Errorf("Approve = %q, want ask", c.Approve)
	}
	if c.MaxRounds != 8 || c.RunTimeout != 120 || c.MaxOutput != 20000 || c.APIRetries != 3 {
		t.Errorf("int defaults wrong: %+v", c)
	}
	if c.ConnectTimeout != 5*time.Second || c.MaxTime != 30*time.Second ||
		c.PollInterval != 2*time.Second || c.APIRunTimeout != 600*time.Second {
		t.Errorf("duration defaults wrong: %+v", c)
	}
	if c.AllowHTTP || c.Verbose || c.Deny != nil {
		t.Errorf("bool/slice defaults wrong: %+v", c)
	}
	if c.ResponseTimeout != 600*time.Second || c.WatchdogInterval != 200*time.Second {
		t.Errorf("watchdog defaults wrong: ResponseTimeout=%v WatchdogInterval=%v", c.ResponseTimeout, c.WatchdogInterval)
	}
	if c.Home == "" || c.ConfigPath != c.Home+"/config" {
		t.Errorf("Home/ConfigPath wrong: Home=%q ConfigPath=%q", c.Home, c.ConfigPath)
	}
	if c.StateDir != c.Home {
		t.Errorf("StateDir default = %q, want Home %q", c.StateDir, c.Home)
	}
}

func TestWatchdogKnobsFromConfigFile(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HERMES_HANDS_HOME", home)
	mustWrite(t, filepath.Join(home, "config"),
		"HERMES_HANDS_RESPONSE_TIMEOUT=120\nHERMES_HANDS_WATCHDOG_INTERVAL=30\n")
	t.Setenv("HERMES_API_URL", "https://x")
	t.Setenv("HERMES_API_KEY", "k")

	c, _ := Load()
	if c.ResponseTimeout != 120*time.Second {
		t.Errorf("ResponseTimeout = %v, want 120s (config file wins)", c.ResponseTimeout)
	}
	if c.WatchdogInterval != 30*time.Second {
		t.Errorf("WatchdogInterval = %v, want 30s", c.WatchdogInterval)
	}

	// 0 disables the ceiling.
	mustWrite(t, filepath.Join(home, "config"), "HERMES_HANDS_RESPONSE_TIMEOUT=0\n")
	c, _ = Load()
	if c.ResponseTimeout != 0 {
		t.Errorf("ResponseTimeout = %v, want 0 (disabled)", c.ResponseTimeout)
	}
}

func TestConfigFileOverridesEnvForLiveKeys(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HERMES_HANDS_HOME", home)
	mustWrite(t, filepath.Join(home, "config"),
		"HERMES_API_PROFILE=fromfile\nHERMES_HANDS_APPROVE=never\n")
	t.Setenv("HERMES_API_PROFILE", "fromenv")
	t.Setenv("HERMES_API_URL", "https://x") // non-empty so secrets file is not consulted
	t.Setenv("HERMES_API_KEY", "k")

	c, _ := Load()
	if c.APIProfile != "fromfile" {
		t.Errorf("APIProfile = %q, want fromfile (config file wins)", c.APIProfile)
	}
	if c.Approve != "never" {
		t.Errorf("Approve = %q, want never", c.Approve)
	}
}

func TestSecretsFileOnlyWhenURLorKeyEmpty(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("HERMES_HANDS_HOME", home)
	mustWrite(t, filepath.Join(home, "secrets"),
		"export HERMES_API_URL=\"https://from-secrets\"\nexport HERMES_API_KEY='sekret'\n")

	// URL + KEY already present -> secrets file ignored.
	t.Setenv("HERMES_API_URL", "https://from-env")
	t.Setenv("HERMES_API_KEY", "envkey")
	c, _ := Load()
	if c.APIURL != "https://from-env" || c.APIKey != "envkey" {
		t.Fatalf("secrets file should have been skipped, got %q / %q", c.APIURL, c.APIKey)
	}

	// KEY missing -> secrets file applied (both keys from it).
	t.Setenv("HERMES_API_KEY", "")
	c, _ = Load()
	if c.APIURL != "https://from-secrets" || c.APIKey != "sekret" {
		t.Fatalf("secrets file not applied: %q / %q", c.APIURL, c.APIKey)
	}
	if c.SecretsSource != "secrets" {
		t.Errorf("SecretsSource = %q, want secrets (plaintext fallback)", c.SecretsSource)
	}

	// A placeholder key counts as missing -> secrets file applied.
	t.Setenv("HERMES_API_URL", "https://from-env")
	t.Setenv("HERMES_API_KEY", "<REPLACE_ME>")
	c, _ = Load()
	if c.APIKey != "sekret" {
		t.Fatalf("placeholder key should trigger the secrets fallback, got %q", c.APIKey)
	}
}

func TestStateDir(t *testing.T) {
	clearEnv(t)
	t.Setenv("HERMES_HANDS_HOME", "/app/hh")
	if c, _ := Load(); c.StateDir != "/app/hh" {
		t.Errorf("StateDir default = %q, want the home dir /app/hh", c.StateDir)
	}
	t.Setenv("HERMES_HANDS_STATE", "/literal/dir")
	if c, _ := Load(); c.StateDir != "/literal/dir" {
		t.Errorf("HERMES_HANDS_STATE must be used literally, got %q", c.StateDir)
	}
}

func TestHomeAndPathOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("HERMES_HANDS_HOME", "/app/hh")
	c, _ := Load()
	if c.Home != "/app/hh" || c.ConfigPath != "/app/hh/config" ||
		c.SecretsPath != "/app/hh/secrets" || c.InstrPath != "/app/hh/instructions.md" {
		t.Fatalf("home-derived paths wrong: %+v", c)
	}
	// per-path overrides still win over the home default
	t.Setenv("HERMES_HANDS_CONFIG", "/etc/hh.conf")
	t.Setenv("HERMES_HANDS_INSTRUCTIONS", "/etc/hh.md")
	c, _ = Load()
	if c.ConfigPath != "/etc/hh.conf" || c.InstrPath != "/etc/hh.md" {
		t.Errorf("per-path override ignored: ConfigPath=%q InstrPath=%q", c.ConfigPath, c.InstrPath)
	}
}

func TestTuningKnobsComeFromProcessEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("HERMES_HANDS_MAX_ROUNDS", "3")
	t.Setenv("HERMES_API_POLL_INTERVAL", "1")
	c, _ := Load()
	if c.MaxRounds != 3 {
		t.Errorf("MaxRounds = %d, want 3", c.MaxRounds)
	}
	if c.PollInterval != 1*time.Second {
		t.Errorf("PollInterval = %v, want 1s", c.PollInterval)
	}
}

func TestLooksUnset(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"REPLACE_me", true},
		{"needs CHANGE", true},
		{"a PLACEHOLDER b", true},
		{"lower placeholder", true},
		{"see EXAMPLE.net", true},
		{"<your-key>", true},
		{"<>", true},
		{"https://hermes.EXAMPLE.net", true},  // matches *EXAMPLE* (case-sensitive)
		{"https://hermes.example.net", false}, // lowercase "example" does NOT match
		{"https://hermes.real.net", false},
		{"sk-realkey", false},
		{"<half", false},
		{"half>", false},
	} {
		if got := LooksUnset(tc.in); got != tc.want {
			t.Errorf("LooksUnset(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRequireHTTPS(t *testing.T) {
	var warned int
	old := Warnf
	Warnf = func(string, ...any) { warned++ }
	defer func() { Warnf = old }()

	for _, tc := range []struct {
		url      string
		allow    bool
		wantErr  bool
		wantWarn bool
	}{
		{"https://hermes.net", false, false, false},
		{"http://127.0.0.1:8971", true, false, true},
		{"http://localhost/x", false, true, false},
		{"http://example.net", false, true, false},
		{"ftp://example.net", false, true, false},
	} {
		warned = 0
		err := RequireHTTPS(tc.url, "HERMES_API_URL", tc.allow)
		if (err != nil) != tc.wantErr {
			t.Errorf("RequireHTTPS(%q, allow=%v) err = %v, wantErr %v", tc.url, tc.allow, err, tc.wantErr)
		}
		if (warned > 0) != tc.wantWarn {
			t.Errorf("RequireHTTPS(%q) warned=%d, wantWarn %v", tc.url, warned, tc.wantWarn)
		}
	}

	if err := RequireHTTPS("http://evil.net", "HERMES_API_URL", false); err == nil ||
		err.Error() != "HERMES_API_URL must be https:// - the bearer token may not cross the network unencrypted." {
		t.Errorf("http-other-host message = %v", err)
	}
	if err := RequireHTTPS("ftp://x", "HERMES_API_URL", false); err == nil ||
		err.Error() != "HERMES_API_URL is not a http(s) URL: ftp://x" {
		t.Errorf("non-http message = %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
