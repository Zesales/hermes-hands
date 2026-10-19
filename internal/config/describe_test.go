package config

import (
	"strings"
	"testing"
	"time"
)

func TestFormatConfig(t *testing.T) {
	cfg := &Config{
		Home: "/app/hh", ConfigPath: "/app/hh/config", SecretsPath: "/app/hh/secrets",
		SecretsSource: "secrets.enc",
		InstrPath:     "/nope/instructions.md", StateDir: "/app/hh",
		APIURL: "https://hermes-api.example.net", APIKey: "sk-should-not-appear",
		Approve: "ask", Stream: "auto",
		ResponseTimeout: 600 * time.Second, WatchdogInterval: 200 * time.Second,
		APIRunTimeout: 600 * time.Second, MaxRounds: 8, RunTimeout: 120, MaxOutput: 20000,
		Sources: map[string]string{"HERMES_HANDS_APPROVE": "env"},
	}
	out := Describe(cfg)

	for _, want := range []string{
		"home         : /app/hh",
		"/app/hh/config",
		"/app/hh/secrets.enc  (encrypted, machine-bound)",
		"sessions     : /app/hh/sessions",
		"(built-in default)",
		"default < config < env < flag < runtime",
		"HERMES_HANDS_MAX_CALLS", "(default)", "(env)",
		"HERMES_HANDS_RESPONSE_TIMEOUT", "600s",
		"HERMES_HANDS_WATCHDOG_INTERVAL", "200s",
		"https://hermes-api.example.net",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatConfig missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-should-not-appear") {
		t.Errorf("formatConfig leaked the API key:\n%s", out)
	}

	// plaintext fallback names the file + nudges to encrypt
	cfg.SecretsSource = "secrets"
	if o := Describe(cfg); !strings.Contains(o, "/app/hh/secrets  (plaintext fallback") {
		t.Errorf("plaintext secrets line wrong:\n%s", o)
	}
	// nothing on disk, key from env
	cfg.SecretsSource = ""
	if o := Describe(cfg); !strings.Contains(o, "no secrets file") {
		t.Errorf("env-source secrets line wrong:\n%s", o)
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
		got := !(&Config{APIURL: tc.url, APIKey: tc.key}).Configured()
		if got != tc.want {
			t.Errorf("!Configured(%q,%q) = %v, want %v", tc.url, tc.key, got, tc.want)
		}
	}
}
