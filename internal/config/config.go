// Package config resolves runtime configuration: the config-file / secrets-store
// load, the placeholder sniffer (LooksUnset) and the transport-security check
// (RequireHTTPS). It dials nothing.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Warnf mirrors bash `hh_warn`. Overridable in tests.
var Warnf = func(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "hermes-hands: WARNING: "+format+"\n", a...)
}

// Config is the fully-resolved runtime configuration.
//
// Everything lives inside one self-contained directory, Home
// (HERMES_HANDS_HOME, else $HOME/hermes-hands): config, the secrets store,
// the session index and an optional instructions override — no scattered XDG
// layout. Each path still has its own override (HERMES_HANDS_CONFIG /
// _SECRETS / _STATE / _INSTRUCTIONS) which wins over the Home-derived default.
//
// The API tuning knobs come from the process environment only; URL / key /
// profile / approve / stream / deny / allow-http / verbose / the watchdog
// knobs are read after the config file so the file (and, conditionally, the
// secrets store) wins over the environment for those.
type Config struct {
	APIURL, APIKey, APIProfile            string
	ConnectTimeout, MaxTime, PollInterval time.Duration
	APIRetries                            int
	APIRunTimeout                         time.Duration
	Approve                               string // ask | auto | never (anything else behaves as ask)
	Stream                                string // HERMES_HANDS_STREAM: "" / auto | on | off
	Deny                                  []string
	MaxRounds, RunTimeout, MaxOutput      int

	// ResponseTimeout is how long hermes-agent may go silent within one turn
	// before the REPL's watchdog cancels it (HERMES_HANDS_RESPONSE_TIMEOUT,
	// 0 = no limit). WatchdogInterval is how often, in the meantime, the
	// watchdog probes the server run out-of-band so a legitimately long
	// "thinking" phase keeps pushing that deadline back
	// (HERMES_HANDS_WATCHDOG_INTERVAL). Both hand-tunable in the config file.
	ResponseTimeout                    time.Duration
	WatchdogInterval                   time.Duration
	AllowHTTP, Verbose                 bool
	StateDir                           string
	Home                               string // HERMES_HANDS_HOME (else $HOME/hermes-hands)
	ConfigPath, SecretsPath, InstrPath string

	// SecretsSource records where the URL/key ultimately came from, for the
	// read-only /config view: "secrets.enc" | "secrets" (plaintext fallback) |
	// "" (nothing on disk contributed — env / config file, or unset).
	SecretsSource string
}

// Load resolves configuration. It reads the config file (overriding the
// environment for the live-read keys) and then the secrets file — but the
// secrets file only when HERMES_API_URL or HERMES_API_KEY is still empty,
// exactly as hh_load_config does. It dials nothing.
func Load() (*Config, error) {
	proc := environMap(os.Environ())

	home := hermesHome(proc)
	cfgPath := firstNonEmpty(proc["HERMES_HANDS_CONFIG"], home+"/config")
	secPath := firstNonEmpty(proc["HERMES_HANDS_SECRETS"], home+"/secrets")

	merged := cloneMap(proc)
	if b, err := os.ReadFile(cfgPath); err == nil {
		for k, v := range parseAssignments(b) {
			merged[k] = v
		}
	}
	// A placeholder URL/key (from the shipped example, or a stale ~/.bashrc line
	// exporting the old placeholder `secrets`) counts as unset: blank it so the
	// secrets-store fallback runs and `setup` isn't a dead end.
	for _, k := range []string{"HERMES_API_URL", "HERMES_API_KEY"} {
		if LooksUnset(merged[k]) {
			merged[k] = ""
		}
	}
	secSrc := ""
	if merged["HERMES_API_URL"] == "" || merged["HERMES_API_KEY"] == "" {
		src, err := applyFallbackSecrets(merged, home, secPath)
		if err != nil {
			return nil, err
		}
		secSrc = src
	}

	return &Config{
		APIURL:     merged["HERMES_API_URL"],
		APIKey:     merged["HERMES_API_KEY"],
		APIProfile: merged["HERMES_API_PROFILE"],
		Approve:    firstNonEmpty(merged["HERMES_HANDS_APPROVE"], "ask"),
		Stream:     merged["HERMES_HANDS_STREAM"],
		Deny:       splitDeny(merged["HERMES_HANDS_DENY"]),

		// hand-tunable in the config file (like APPROVE / STREAM), not an
		// env-only tuning knob — an operator adjusts these by editing config.
		ResponseTimeout:  secondsOr(merged["HERMES_HANDS_RESPONSE_TIMEOUT"], 600*time.Second),
		WatchdogInterval: secondsOr(merged["HERMES_HANDS_WATCHDOG_INTERVAL"], 200*time.Second),
		AllowHTTP:        merged["HERMES_HANDS_ALLOW_HTTP"] == "1",
		Verbose:          merged["HERMES_HANDS_VERBOSE"] != "",
		InstrPath:        firstNonEmpty(merged["HERMES_HANDS_INSTRUCTIONS"], home+"/instructions.md"),

		ConnectTimeout: secondsOr(proc["HERMES_API_CONNECT_TIMEOUT"], 5*time.Second),
		MaxTime:        secondsOr(proc["HERMES_API_MAX_TIME"], 30*time.Second),
		PollInterval:   secondsOr(proc["HERMES_API_POLL_INTERVAL"], 2*time.Second),
		APIRunTimeout:  secondsOr(proc["HERMES_API_RUN_TIMEOUT"], 600*time.Second),
		APIRetries:     intOr(proc["HERMES_API_RETRIES"], 3),
		MaxRounds:      intOr(proc["HERMES_HANDS_MAX_ROUNDS"], 8),
		RunTimeout:     intOr(proc["HERMES_HANDS_RUN_TIMEOUT"], 120),
		MaxOutput:      intOr(proc["HERMES_HANDS_MAX_OUTPUT"], 20000),
		StateDir:       firstNonEmpty(proc["HERMES_HANDS_STATE"], home),

		Home:          home,
		ConfigPath:    cfgPath,
		SecretsPath:   secPath,
		SecretsSource: secSrc,
	}, nil
}

// applyFallbackSecrets fills still-empty HERMES_API_* keys, only when a URL or
// key is missing. The encrypted store (secrets.enc + keyseed, in homeDir) is
// authoritative for the fallback when present; a missing keyseed or a decrypt
// failure is a hard error (never a silent fall-through). Only when there is no
// secrets.enc at all does the narrowed-parser plaintext `secrets` file apply,
// keeping old installs working until they re-run setup. The returned string
// records which store contributed ("secrets.enc" | "secrets" | "").
func applyFallbackSecrets(merged map[string]string, homeDir, plainPath string) (string, error) {
	encPath := homeDir + "/secrets.enc"
	seedPath := homeDir + "/keyseed"

	blob, err := os.ReadFile(encPath)
	if err != nil {
		if b, perr := os.ReadFile(plainPath); perr == nil {
			applied := false
			for k, v := range parseAssignments(b) {
				merged[k] = v
				applied = true
			}
			if applied {
				return "secrets", nil
			}
		}
		return "", nil
	}

	warnIfLoose(encPath)
	seed, serr := os.ReadFile(seedPath)
	if serr != nil {
		return "", ErrSecretsUndecryptable
	}
	warnIfLoose(seedPath)

	payload, derr := decryptBlob(blob, seed)
	if derr != nil {
		return "", ErrSecretsUndecryptable
	}
	for k, v := range payload {
		if merged[k] == "" {
			merged[k] = v
		}
	}
	return "secrets.enc", nil
}

// LooksUnset ports hh_looks_unset: empty, or carrying an obvious placeholder
// marker, or shaped like <...>.
func LooksUnset(v string) bool {
	if v == "" {
		return true
	}
	for _, m := range []string{"REPLACE", "CHANGE", "PLACEHOLDER", "placeholder", "EXAMPLE"} {
		if strings.Contains(v, m) {
			return true
		}
	}
	return strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")
}

// RequireHTTPS ports hh_require_https. https is always fine; plain http is fine
// only against loopback and only with allowHTTP (with a warning); anything else
// is an error whose text matches the bash `hh_die` message (minus the
// "BLOCKED: " prefix the caller adds).
func RequireHTTPS(rawURL, name string, allowHTTP bool) error {
	switch {
	case strings.HasPrefix(rawURL, "https://"):
		return nil
	case strings.HasPrefix(rawURL, "http://127.0.0.1"),
		strings.HasPrefix(rawURL, "http://localhost"),
		strings.HasPrefix(rawURL, "http://0.0.0.0"),
		strings.HasPrefix(rawURL, "http://[::1]"):
		if allowHTTP {
			Warnf("%s is plain http on loopback (tests only)", name)
			return nil
		}
		return fmt.Errorf("%s is plain http on loopback; set HERMES_HANDS_ALLOW_HTTP=1 only for local tests.", name)
	case strings.HasPrefix(rawURL, "http://"):
		return fmt.Errorf("%s must be https:// - the bearer token may not cross the network unencrypted.", name)
	default:
		return fmt.Errorf("%s is not a http(s) URL: %s", name, rawURL)
	}
}

func environMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func cloneMap(m map[string]string) map[string]string {
	n := make(map[string]string, len(m))
	for k, v := range m {
		n[k] = v
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// hermesHome is the app's single self-contained data directory:
// HERMES_HANDS_HOME, else $HOME/hermes-hands. Plain "/"-concatenation (not
// filepath.Join) so an empty $HOME yields "/hermes-hands", not a cleaned
// relative path.
func hermesHome(env map[string]string) string {
	if v := env["HERMES_HANDS_HOME"]; v != "" {
		return v
	}
	return env["HOME"] + "/hermes-hands"
}

func splitDeny(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, g := range strings.Split(s, "|") {
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

func secondsOr(s string, def time.Duration) time.Duration {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second))
	}
	return def
}

func intOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
