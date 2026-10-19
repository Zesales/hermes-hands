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

// The operator's budget per turn (internal/loop): requests to Hermes, hands
// calls, and calls per envelope. Hermes sees it from the start and a countdown
// once it gets close; 0 = no limit.
const (
	DefaultMaxRounds        = 25
	DefaultMaxCalls         = 100
	DefaultMaxCallsPerRound = 20
)

// The context (internal/compact): the model's window in tokens, the share of
// it in percent a request may reach before hermes-hands compacts first — kept
// below hermes-agent's own compression.threshold (default 0.5), so the
// lossless compaction comes first — and the bytes of one round's results
// together. 0 = unknown / off / no limit.
const (
	DefaultContextWindow  = 131072
	DefaultCompactAt      = 45
	DefaultMaxRoundOutput = 60000
)

// flags holds command-line overrides (SetFlag) — the strongest layer Load
// resolves.
var flags = map[string]string{}

// SetFlag makes a command-line flag (--yolo → HERMES_HANDS_APPROVE=auto) win
// over the environment and the config file for every later Load.
func SetFlag(key, value string) { flags[key] = value }

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
// Every setting resolves through layers, weakest first: the built-in
// default < the config file (the operator's defaults for every session) < the
// environment (a non-empty value) < a command-line flag. Runtime switches
// (/yolo, /debug, rpc) sit on top in the running process only and are never
// saved (internal/app). The secrets store fills the URL / key only while both
// layers left one unset. Sources says which layer each key came from.
type Config struct {
	APIURL, APIKey, APIProfile            string
	ConnectTimeout, MaxTime, PollInterval time.Duration
	APIRetries                            int
	APIRunTimeout                         time.Duration
	Approve                               string // ask | auto | never (anything else behaves as ask)
	Stream                                string // HERMES_HANDS_STREAM: "" / auto | on | off
	Deny                                  []string
	MaxRounds, RunTimeout, MaxOutput      int
	MaxCalls, MaxCallsPerRound            int // the turn's call budget (MaxRounds: its requests)
	MaxRoundOutput                        int // bytes of one round's results together (0 = no limit)
	ContextWindow, CompactAt              int // the model's window in tokens; compact before a request past this percent of it

	// ResponseTimeout is how long hermes-agent may go silent within one turn
	// before the REPL's watchdog cancels it (HERMES_HANDS_RESPONSE_TIMEOUT,
	// 0 = no limit). WatchdogInterval is how often, in the meantime, the
	// watchdog probes the server run out-of-band so a legitimately long
	// "thinking" phase keeps pushing that deadline back
	// (HERMES_HANDS_WATCHDOG_INTERVAL). Both hand-tunable in the config file.
	ResponseTimeout    time.Duration
	WatchdogInterval   time.Duration
	AllowHTTP, Verbose bool
	Debug              bool // HERMES_HANDS_DEBUG / /debug: redacted wire trace into the session's debug.jsonl

	// Sandbox is how Hermes' own server-side exec / file tools are treated in a
	// hands run (HERMES_HANDS_SANDBOX): smart (default — steer only calls aimed
	// at the operator's machine) | steer (every one) | show (never steer).
	Sandbox string

	// RetentionDays: local sessions idle longer than this are deleted on start
	// — events, tool output and debug trace with them (HERMES_HANDS_RETENTION_DAYS,
	// default 14; 0 keeps everything). Bounds how long anything typed by
	// mistake (a password) can linger on disk.
	RetentionDays                      int
	StateDir                           string
	Home                               string // HERMES_HANDS_HOME (else $HOME/hermes-hands)
	ConfigPath, SecretsPath, InstrPath string

	// SecretsSource records where the URL/key ultimately came from, for the
	// read-only /config view: "secrets.enc" | "secrets" (plaintext fallback) |
	// "" (nothing on disk contributed — env / config file, or unset).
	SecretsSource string

	// Sources maps a key to the layer it came from: "config" | "env" |
	// "flag" | "runtime" | "secrets.enc" | "secrets"; a key not in it is the
	// built-in default. The /config view shows it per row.
	Sources map[string]string
}

// Load resolves configuration through the layers (see Config): the config
// file, then the environment, then the flags — and the secrets store only when
// HERMES_API_URL or HERMES_API_KEY is still empty afterwards. It dials nothing.
func Load() (*Config, error) {
	proc := environMap(os.Environ())

	home := hermesHome(proc)
	cfgPath := firstNonEmpty(proc["HERMES_HANDS_CONFIG"], home+"/config")
	secPath := firstNonEmpty(proc["HERMES_HANDS_SECRETS"], home+"/secrets")

	merged, src := layered(proc, cfgPath)
	// A placeholder URL/key (from the shipped example, or a stale ~/.bashrc line
	// exporting the old placeholder `secrets`) counts as unset: blank it so the
	// secrets-store fallback runs and `setup` isn't a dead end.
	for _, k := range []string{"HERMES_API_URL", "HERMES_API_KEY"} {
		if LooksUnset(merged[k]) {
			merged[k] = ""
			delete(src, k)
		}
	}
	secSrc := ""
	if merged["HERMES_API_URL"] == "" || merged["HERMES_API_KEY"] == "" {
		before := map[string]string{"HERMES_API_URL": merged["HERMES_API_URL"], "HERMES_API_KEY": merged["HERMES_API_KEY"]}
		s, err := applyFallbackSecrets(merged, home, secPath)
		if err != nil {
			return nil, err
		}
		secSrc = s
		for k, was := range before {
			if merged[k] != was {
				src[k] = secSrc
			}
		}
	}

	return &Config{
		APIURL:     merged["HERMES_API_URL"],
		APIKey:     merged["HERMES_API_KEY"],
		APIProfile: merged["HERMES_API_PROFILE"],
		Approve:    firstNonEmpty(merged["HERMES_HANDS_APPROVE"], "ask"),
		Stream:     merged["HERMES_HANDS_STREAM"],
		Deny:       splitDeny(merged["HERMES_HANDS_DENY"]),

		ResponseTimeout:  secondsOr(merged["HERMES_HANDS_RESPONSE_TIMEOUT"], 600*time.Second),
		WatchdogInterval: secondsOr(merged["HERMES_HANDS_WATCHDOG_INTERVAL"], 200*time.Second),
		AllowHTTP:        merged["HERMES_HANDS_ALLOW_HTTP"] == "1",
		Verbose:          switchOn(merged["HERMES_HANDS_VERBOSE"]),
		Debug:            switchOn(merged["HERMES_HANDS_DEBUG"]),
		Sandbox:          sandboxMode(merged["HERMES_HANDS_SANDBOX"]),
		RetentionDays:    intOr(merged["HERMES_HANDS_RETENTION_DAYS"], 14),
		InstrPath:        firstNonEmpty(merged["HERMES_HANDS_INSTRUCTIONS"], home+"/instructions.md"),

		ConnectTimeout:   secondsOr(merged["HERMES_API_CONNECT_TIMEOUT"], 5*time.Second),
		MaxTime:          secondsOr(merged["HERMES_API_MAX_TIME"], 30*time.Second),
		PollInterval:     secondsOr(merged["HERMES_API_POLL_INTERVAL"], 2*time.Second),
		APIRunTimeout:    secondsOr(merged["HERMES_API_RUN_TIMEOUT"], 600*time.Second),
		APIRetries:       intOr(merged["HERMES_API_RETRIES"], 3),
		MaxRounds:        intOr(merged["HERMES_HANDS_MAX_ROUNDS"], DefaultMaxRounds),
		MaxCalls:         intOr(merged["HERMES_HANDS_MAX_CALLS"], DefaultMaxCalls),
		MaxCallsPerRound: intOr(merged["HERMES_HANDS_MAX_CALLS_PER_ROUND"], DefaultMaxCallsPerRound),
		RunTimeout:       intOr(merged["HERMES_HANDS_RUN_TIMEOUT"], 120),
		MaxOutput:        intOr(merged["HERMES_HANDS_MAX_OUTPUT"], 20000),
		MaxRoundOutput:   intOr(merged["HERMES_HANDS_MAX_ROUND_OUTPUT"], DefaultMaxRoundOutput),
		ContextWindow:    intOr(merged["HERMES_HANDS_CONTEXT_WINDOW"], DefaultContextWindow),
		CompactAt:        min(intOr(merged["HERMES_HANDS_COMPACT_AT"], DefaultCompactAt), 95),
		StateDir:         firstNonEmpty(merged["HERMES_HANDS_STATE"], home),

		Home:          home,
		ConfigPath:    cfgPath,
		SecretsPath:   secPath,
		SecretsSource: secSrc,
		Sources:       src,
	}, nil
}

// layered merges the config file, the environment and the flags, weakest
// first, and records which layer each key came from. An empty environment
// variable counts as unset (it never blanks a config value), and so does a
// placeholder URL / key there — a stale `export` must not hide the real one.
func layered(proc map[string]string, cfgPath string) (merged, src map[string]string) {
	merged, src = map[string]string{}, map[string]string{}
	if b, err := os.ReadFile(cfgPath); err == nil {
		for k, v := range parseAssignments(b) {
			merged[k], src[k] = v, "config"
		}
	}
	for k, v := range proc {
		if v == "" || (k == "HERMES_API_URL" || k == "HERMES_API_KEY") && LooksUnset(v) {
			continue
		}
		merged[k], src[k] = v, "env"
	}
	for k, v := range flags {
		merged[k], src[k] = v, "flag"
	}
	return merged, src
}

// Assignments is the narrowed config parser for callers outside the package
// (setup reads which keys a config already sets).
func Assignments(data []byte) map[string]string { return parseAssignments(data) }

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

// sandboxMode normalises HERMES_HANDS_SANDBOX: smart | steer | show, anything
// else (unset included) is smart.
func sandboxMode(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "steer", "show":
		return v
	}
	return "smart"
}

// switchOn reads an on/off setting: empty, 0, false, off and no are off;
// anything else is on.
func switchOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

func intOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
