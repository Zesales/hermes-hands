package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Configured reports whether there is a usable URL + key — anything worth
// preflighting. Network is not touched.
func (c *Config) Configured() bool { return !LooksUnset(c.APIURL) && !LooksUnset(c.APIKey) }

// Describe renders the read-only `/config` view: the files hermes-hands
// reads, and the effective value of every hand-tunable knob with its env-var
// name. It never offers to write anything — the operator edits the config file
// directly, so a fat-fingered `/config KEY=value` can't wedge the install.
func Describe(cfg *Config) string {
	none := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	secs := func(d time.Duration) string {
		if d <= 0 {
			return "off"
		}
		return strconv.Itoa(int(d/time.Second)) + "s"
	}
	instr := "(built-in default)"
	if b, err := os.ReadFile(cfg.InstrPath); err == nil && strings.TrimSpace(string(b)) != "" {
		instr = cfg.InstrPath
	}
	var secrets string
	switch cfg.SecretsSource {
	case "secrets.enc":
		secrets = filepath.Join(cfg.Home, "secrets.enc") + "  (encrypted, machine-bound)"
	case "secrets":
		secrets = cfg.SecretsPath + "  (plaintext fallback — run `setup` to encrypt)"
	default:
		if cfg.APIKey != "" {
			secrets = "(from the environment / config file — no secrets file)"
		} else {
			secrets = "(none — run `hermes-hands setup`)"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  home         : %s\n", cfg.Home)
	fmt.Fprintf(&b, "  config file  : %s\n", cfg.ConfigPath)
	fmt.Fprintf(&b, "  secrets      : %s\n", secrets)
	fmt.Fprintf(&b, "  instructions : %s\n", instr)
	fmt.Fprintf(&b, "  sessions     : %s\n", filepath.Join(cfg.StateDir, "sessions"))
	b.WriteString("\n  effective settings — edit the config file by hand; hermes-hands never writes these:\n")
	for _, r := range [][2]string{
		{"HERMES_API_URL", none(cfg.APIURL)},
		{"HERMES_API_PROFILE", none(cfg.APIProfile)},
		{"HERMES_HANDS_APPROVE", cfg.Approve},
		{"HERMES_HANDS_STREAM", none(cfg.Stream)},
		{"HERMES_HANDS_RESPONSE_TIMEOUT", secs(cfg.ResponseTimeout)},
		{"HERMES_HANDS_WATCHDOG_INTERVAL", secs(cfg.WatchdogInterval)},
		{"HERMES_HANDS_RETENTION_DAYS", retention(cfg.RetentionDays)},
		{"HERMES_HANDS_DEBUG", onOff(cfg.Debug)},
		{"HERMES_HANDS_SANDBOX", cfg.Sandbox},
		{"HERMES_HANDS_MAX_ROUNDS", strconv.Itoa(cfg.MaxRounds)},
		{"HERMES_HANDS_RUN_TIMEOUT", strconv.Itoa(cfg.RunTimeout) + "s"},
		{"HERMES_API_RUN_TIMEOUT", secs(cfg.APIRunTimeout)},
		{"HERMES_HANDS_MAX_OUTPUT", strconv.Itoa(cfg.MaxOutput)},
		{"HERMES_HANDS_DENY", none(strings.Join(cfg.Deny, " | "))},
	} {
		fmt.Fprintf(&b, "    %-30s %s\n", r[0], r[1])
	}
	return b.String()
}

func retention(days int) string {
	if days <= 0 {
		return "off (keep every session)"
	}
	return strconv.Itoa(days) + " days"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
