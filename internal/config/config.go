// Package config ports the configuration half of lib/util.sh: the two-tier
// config/secrets file load (hh_load_config), the placeholder sniffer
// (hh_looks_unset) and the transport-security check (hh_require_https).
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

// Config is the fully-resolved runtime configuration. Field groups differ in
// where they are read from, matching the bash sourcing order:
//
//   - The API tuning knobs and the state dir are cached by lib/*.sh at
//     source time, before hh_load_config runs, so the config file can never
//     influence them — they come from the process environment only.
//   - URL / key / profile / approve / deny / allow-http / verbose / the
//     instructions path are read live by the bash functions after
//     hh_load_config, so the config (and, conditionally, secrets) file wins
//     over the environment for those.
type Config struct {
	APIURL, APIKey, APIProfile            string
	ConnectTimeout, MaxTime, PollInterval time.Duration
	APIRetries                            int
	APIRunTimeout                         time.Duration
	Approve                               string // ask | auto | never (anything else behaves as ask)
	Deny                                  []string
	MaxRounds, RunTimeout, MaxOutput      int
	AllowHTTP, Verbose                    bool
	StateDir                              string
	ConfigPath, SecretsPath, InstrPath    string
}

// Load resolves configuration. It reads the config file (overriding the
// environment for the live-read keys) and then the secrets file — but the
// secrets file only when HERMES_API_URL or HERMES_API_KEY is still empty,
// exactly as hh_load_config does. It dials nothing.
func Load() (*Config, error) {
	proc := environMap(os.Environ())

	cfgPath := firstNonEmpty(proc["HERMES_HANDS_CONFIG"], xdgConfigHome(proc)+"/hermes-hands/config")
	secPath := firstNonEmpty(proc["HERMES_HANDS_SECRETS"], xdgConfigHome(proc)+"/hermes-hands/secrets")

	merged := cloneMap(proc)
	if b, err := os.ReadFile(cfgPath); err == nil {
		for k, v := range parseAssignments(b) {
			merged[k] = v
		}
	}
	if merged["HERMES_API_URL"] == "" || merged["HERMES_API_KEY"] == "" {
		if err := applyFallbackSecrets(merged, xdgConfigHome(proc)+"/hermes-hands", secPath); err != nil {
			return nil, err
		}
	}

	return &Config{
		APIURL:     merged["HERMES_API_URL"],
		APIKey:     merged["HERMES_API_KEY"],
		APIProfile: merged["HERMES_API_PROFILE"],
		Approve:    firstNonEmpty(merged["HERMES_HANDS_APPROVE"], "ask"),
		Deny:       splitDeny(merged["HERMES_HANDS_DENY"]),
		AllowHTTP:  merged["HERMES_HANDS_ALLOW_HTTP"] == "1",
		Verbose:    merged["HERMES_HANDS_VERBOSE"] != "",
		InstrPath:  firstNonEmpty(merged["HERMES_HANDS_INSTRUCTIONS"], xdgConfigHome(merged)+"/hermes-hands/instructions.md"),

		ConnectTimeout: secondsOr(proc["HERMES_API_CONNECT_TIMEOUT"], 5*time.Second),
		MaxTime:        secondsOr(proc["HERMES_API_MAX_TIME"], 30*time.Second),
		PollInterval:   secondsOr(proc["HERMES_API_POLL_INTERVAL"], 2*time.Second),
		APIRunTimeout:  secondsOr(proc["HERMES_API_RUN_TIMEOUT"], 600*time.Second),
		APIRetries:     intOr(proc["HERMES_API_RETRIES"], 3),
		MaxRounds:      intOr(proc["HERMES_HANDS_MAX_ROUNDS"], 8),
		RunTimeout:     intOr(proc["HERMES_HANDS_RUN_TIMEOUT"], 120),
		MaxOutput:      intOr(proc["HERMES_HANDS_MAX_OUTPUT"], 20000),
		StateDir:       firstNonEmpty(proc["HERMES_HANDS_STATE"], xdgStateHome(proc)+"/hermes-hands"),

		ConfigPath:  cfgPath,
		SecretsPath: secPath,
	}, nil
}

// applyFallbackSecrets fills still-empty HERMES_API_* keys, only when a URL or
// key is missing. The encrypted store (secrets.enc + keyseed, in confDir) is
// authoritative for the fallback when present; a missing keyseed or a decrypt
// failure is a hard error (never a silent fall-through). Only when there is no
// secrets.enc at all does the narrowed-parser plaintext `secrets` file apply,
// keeping pre-M10 installs working until they re-run setup.
func applyFallbackSecrets(merged map[string]string, confDir, plainPath string) error {
	encPath := confDir + "/secrets.enc"
	seedPath := confDir + "/keyseed"

	blob, err := os.ReadFile(encPath)
	if err != nil {
		if b, perr := os.ReadFile(plainPath); perr == nil {
			for k, v := range parseAssignments(b) {
				merged[k] = v
			}
		}
		return nil
	}

	warnIfLoose(encPath)
	seed, serr := os.ReadFile(seedPath)
	if serr != nil {
		return ErrSecretsUndecryptable
	}
	warnIfLoose(seedPath)

	payload, derr := decryptBlob(blob, seed)
	if derr != nil {
		return ErrSecretsUndecryptable
	}
	for k, v := range payload {
		if merged[k] == "" {
			merged[k] = v
		}
	}
	return nil
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

// xdgConfigHome / xdgStateHome use plain "/"-concatenation (not filepath.Join)
// so an empty $HOME yields "/.config" like bash, not a cleaned relative path.
func xdgConfigHome(env map[string]string) string {
	if v := env["XDG_CONFIG_HOME"]; v != "" {
		return v
	}
	return env["HOME"] + "/.config"
}

func xdgStateHome(env map[string]string) string {
	if v := env["XDG_STATE_HOME"]; v != "" {
		return v
	}
	return env["HOME"] + "/.local/state"
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
