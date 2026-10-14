// Package debuglog writes the /debug wire trace: one JSON object per line
// (0600, in the session's directory), for debugging the connection to
// hermes-agent — which request went out, what came back, which SSE frames
// arrived, what the sandbox guard did.
//
// Nothing reaches the disk unredacted: every string value is scrubbed with
// redact.Scrub (bearer / authorization / api-key / password / token shapes,
// AWS / OpenAI / GitHub keys, PEM headers) after the literal secrets handed to
// New (the configured API key) are blanked. Headers are never traced at all.
// What no pattern can recognise — a bare password typed into a message — is
// bounded by the session retention window, which deletes the trace with its
// session. The trace never leaves the machine.
package debuglog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Zesales/hermes-hands/internal/redact"
)

// maxField caps one traced string; a debug trace needs the shape of a frame,
// not megabytes of tool output.
const maxField = 16 * 1024

// Log is one session's trace file. The zero value is unusable — use New.
type Log struct {
	mu      sync.Mutex
	path    string
	secrets []string
}

// New traces into path. secrets are literal values (the API key) blanked
// wherever they appear, before pattern redaction.
func New(path string, secrets ...string) *Log {
	l := &Log{path: path}
	for _, s := range secrets {
		if len(s) >= 6 { // a too-short "secret" would blank ordinary text
			l.secrets = append(l.secrets, s)
		}
	}
	return l
}

// Path is the trace file.
func (l *Log) Path() string { return l.path }

// Write appends one redacted event. Best-effort: a trace must never break a
// turn, so errors are swallowed.
func (l *Log) Write(kind string, fields map[string]any) {
	ev := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind}
	for k, v := range fields {
		ev[k] = l.clean(v)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if os.MkdirAll(filepath.Dir(l.path), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// clean redacts strings (recursively through maps and slices).
func (l *Log) clean(v any) any {
	switch t := v.(type) {
	case string:
		return l.scrub(t)
	case []byte:
		return l.scrub(string(t))
	case error:
		return l.scrub(t.Error())
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = l.clean(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = l.clean(x)
		}
		return out
	default:
		return v
	}
}

func (l *Log) scrub(s string) string {
	for _, sec := range l.secrets {
		s = strings.ReplaceAll(s, sec, "«redacted»")
	}
	s = redact.Scrub(s)
	if len(s) > maxField {
		s = s[:maxField] + "…[truncated]"
	}
	return s
}
