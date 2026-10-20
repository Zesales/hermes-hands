package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"hermes-hands/internal/config"
)

// configDefaults is every setting a config file carries, at its built-in
// default: a fresh config sets all of them — the file shows what is in effect
// — and a later setup appends the ones an older config lacks.
var configDefaults = []struct{ key, val, note string }{
	{"HERMES_HANDS_APPROVE", "ask", "ask | plan | auto | never — --yolo, /yolo and /approve switch it per run"},
	{"HERMES_HANDS_STREAM", "auto", "auto | on | off"},
	{"HERMES_HANDS_SANDBOX", "steer", "Hermes' own server terminal/file tools: steer (back to the hands) | smart | show"},
	{"HERMES_HANDS_MAX_ROUNDS", strconv.Itoa(config.DefaultMaxRounds), "requests to Hermes per turn (0 = no limit)"},
	{"HERMES_HANDS_MAX_CALLS", strconv.Itoa(config.DefaultMaxCalls), "hands calls per turn (0 = no limit)"},
	{"HERMES_HANDS_MAX_CALLS_PER_ROUND", strconv.Itoa(config.DefaultMaxCallsPerRound), "calls per envelope — a bigger one is not run (0 = no limit)"},
	{"HERMES_HANDS_RESPONSE_TIMEOUT", "600", "seconds hermes-agent may go silent in a turn before it is cancelled (0 = no limit)"},
	{"HERMES_HANDS_WATCHDOG_INTERVAL", "200", "seconds between server-side 'still working?' checks that push that limit back"},
	{"HERMES_HANDS_RUN_TIMEOUT", "120", "seconds per local command"},
	{"HERMES_HANDS_MAX_OUTPUT", "20000", "bytes kept per tool result (local_shell keeps 2x)"},
	{"HERMES_HANDS_MAX_ROUND_OUTPUT", strconv.Itoa(config.DefaultMaxRoundOutput), "bytes of one round's results together; a bigger round is cut, the full output stays local (0 = no limit)"},
	{"HERMES_HANDS_CONTEXT_WINDOW", strconv.Itoa(config.DefaultContextWindow), "the model's context window in tokens (0 = unknown: no automatic compaction)"},
	{"HERMES_HANDS_COMPACT_AT", strconv.Itoa(config.DefaultCompactAt), "compact before a request past this % of the window — keep it below hermes-agent's compression.threshold (0 = only /compact)"},
	{"HERMES_HANDS_RETENTION_DAYS", "14", "local sessions idle longer are deleted on start, debug trace included (0 = keep)"},
	{"HERMES_HANDS_DEBUG", "0", "1 = start with /debug on: redacted connection trace per session"},
}

const configHeader = "# hermes-hands config — your defaults for every session; `/config` shows what is in effect.\n" +
	"# Layers, weakest first: built-in default < this file < environment < flag (--yolo)\n" +
	"# < runtime switch (/yolo, /debug, rpc). Runtime switches are never saved: every\n" +
	"# session you open or resume starts from here again. `hermes-hands setup --config`\n" +
	"# adds settings a newer version brings, at their defaults, and never changes yours.\n"

// configLine is one `KEY=value   # note` line.
func configLine(key, val, note string) string {
	return fmt.Sprintf("%-38s # %s\n", key+"="+val, note)
}

// configBody is a fresh config: the header, every default, and the optional
// keys as commented examples.
func configBody() string {
	var b strings.Builder
	b.WriteString(configHeader)
	for _, d := range configDefaults {
		b.WriteString(configLine(d.key, d.val, d.note))
	}
	b.WriteString(configLine("# HERMES_API_PROFILE", "coder", "optional /p/<profile>/ prefix"))
	b.WriteString(configLine("# HERMES_HANDS_DENY", "'*git push*|*rm -r*'", "extra denied local_shell commands, |-separated globs"))
	return b.String()
}

// EnsureConfig makes sure path is a config that sets every setting, and says
// what it did. No file: a fresh one. A file that sets nothing (only comments —
// an old template): replaced by a fresh one, the old kept as <path>.bak. A
// file that sets something: only the keys it lacks are appended, at their
// defaults — a value the operator set is never changed.
func EnsureConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "wrote " + path + " (defaults)", writeNew(path, configBody())
	case err != nil:
		return "", err
	}
	have := config.Assignments(data)
	if len(have) == 0 {
		bak := path + ".bak"
		if err := os.WriteFile(bak, data, 0o600); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(configBody()), 0o644); err != nil {
			return "", err
		}
		return "replaced " + path + " — it set nothing; now every default is in it (the old one is " + bak + ")", nil
	}
	var add strings.Builder
	var keys []string
	for _, d := range configDefaults {
		if _, ok := have[d.key]; !ok {
			add.WriteString(configLine(d.key, d.val, d.note))
			keys = append(keys, d.key)
		}
	}
	if len(keys) == 0 {
		return "kept " + path + " (it sets every setting)", nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return "", err
	}
	_, werr := fmt.Fprintf(f, "\n# added by hermes-hands setup %s — new settings, at their defaults\n%s",
		time.Now().Format("2006-01-02"), add.String())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return "kept " + path + " — added " + strings.Join(keys, ", ") + " at their defaults", werr
}

// writeNew creates path with body; O_EXCL: an existing file is never truncated.
func writeNew(path, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
