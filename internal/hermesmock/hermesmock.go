// Package hermesmock is an in-process stand-in for the Hermes Runs API, a Go
// port of test/mock_hermes.py. No network beyond the loopback httptest.Server,
// no model. Used by the internal/api, internal/loop and top-level integration
// tests so the suite needs no python3.
package hermesmock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	reCaps = regexp.MustCompile(`^/(?:p/[^/]+/)?v1/capabilities$`)
	reRuns = regexp.MustCompile(`^/(?:p/[^/]+/)?v1/runs$`)
	reRun  = regexp.MustCompile(`^/(?:p/[^/]+/)?v1/runs/([^/]+)$`)
	reSess = regexp.MustCompile(`^/(?:p/[^/]+/)?api/sessions/[^/]+$`)
	reEvts = regexp.MustCompile(`^/(?:p/[^/]+/)?v1/runs/([^/]+)/events$`)
	reCtrl = regexp.MustCompile(`^/(?:p/[^/]+/)?v1/runs/[^/]+/(?:steer|approval)$`)
)

type runRec struct {
	out, sid string
	round1   bool // the turn's first round (no results yet): the stream carries Hermes' own tool events
}

type server struct {
	mode   string
	stream bool // advertise run_events_sse and serve GET /v1/runs/{id}/events

	mu   sync.Mutex
	n    int
	runs map[string]runRec
}

// New starts a mock server for one of: plain, prose, badjson, delegate,
// shellstate, starved. Close it with (*httptest.Server).Close.
func New(mode string) *httptest.Server {
	s := &server{mode: mode, runs: map[string]runRec{}}
	return httptest.NewServer(s)
}

// NewStream is New with the SSE run-event stream on, like a live gateway:
// the output envelope arrives as 5-byte message.delta frames (so escapes
// fall across chunk boundaries), and mode "golden" adds Hermes' own tool
// events to its first round — a plain server call, one the sandbox guard
// steers back to the hands, and a server-tool approval request.
func NewStream(mode string) *httptest.Server {
	s := &server{mode: mode, stream: true, runs: map[string]runRec{}}
	return httptest.NewServer(s)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && reCaps.MatchString(r.URL.Path):
		caps := map[string]any{
			"model":   "hermes-agent",
			"runtime": map[string]any{"mode": "server_agent"},
		}
		if s.stream {
			caps["features"] = map[string]any{"run_events_sse": true}
		}
		writeJSON(w, 200, caps)
	case r.Method == http.MethodGet && s.stream && reEvts.MatchString(r.URL.Path):
		s.serveEvents(w, reEvts.FindStringSubmatch(r.URL.Path)[1])
	case r.Method == http.MethodPost && reCtrl.MatchString(r.URL.Path):
		writeJSON(w, 200, map[string]any{"ok": true})
	case r.Method == http.MethodGet && r.URL.Path == "/health": // unauthenticated, at the root like the gateway's
		writeJSON(w, 200, map[string]any{"status": "ok", "platform": "hermes-agent", "version": "0.22.2"})
	case r.Method == http.MethodGet && reRun.MatchString(r.URL.Path):
		id := reRun.FindStringSubmatch(r.URL.Path)[1]
		s.mu.Lock()
		rec, ok := s.runs[id]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]any{"error": "unknown run"})
			return
		}
		writeJSON(w, 200, map[string]any{
			"run_id": id, "status": "completed",
			"session_id": rec.sid, "output": rec.out,
		})
	case r.Method == http.MethodPost && reRuns.MatchString(r.URL.Path):
		s.handlePost(w, r)
	case r.Method == http.MethodPatch && reSess.MatchString(r.URL.Path):
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeJSON(w, 404, map[string]any{"error": "nf"})
	}
}

func (s *server) handlePost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input     string `json:"input"`
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	seenResults := strings.Contains(body.Input, `"results"`)
	gotFixup := strings.Contains(body.Input, `"error"`) && !seenResults
	postedSID := body.SessionID
	if postedSID == "" {
		postedSID = r.Header.Get("X-Hermes-Session-Id")
	}

	s.mu.Lock()
	s.n++
	rid := "run_" + strconv.Itoa(s.n)
	out := s.output(body.Input, seenResults, gotFixup)
	s.runs[rid] = runRec{out: out, sid: postedSID, round1: !seenResults}
	s.mu.Unlock()

	writeJSON(w, 200, map[string]any{"run_id": rid, "status": "started"})
}

// output picks the run's `.output` string per mode, byte-for-byte matching
// test/mock_hermes.py — including the Python-style True/False in the delegate
// and shellstate finals.
func (s *server) output(input string, seenResults, gotFixup bool) string {
	switch s.mode {
	case "plain":
		return `{"calls": [], "final": "plain answer from the mock brain."}`
	case "prose":
		return "Just a plain-prose answer, no envelope at all."
	case "badjson":
		if !seenResults && !gotFixup {
			return "```json\n{\"calls\": [ {\"tool\": \"read_file\", }  ]  // oops\n```"
		}
		return `{"calls": [], "final": "recovered and answered."}`
	case "golden":
		if !seenResults {
			return `{"say": "Reading the README, then a shell check.", "calls": [{"id": "c1", "tool": "local_read", "args": {"path": "README.md"}}, {"id": "c2", "tool": "local_shell", "args": {"cmd": "printf 'one\\ntwo\\n'"}}], "final": null}`
		}
		return `{"calls": [], "final": "Read the README (\"demo readme\") and ran the shell.\nTwo lines — ä ✓."}`
	case "delegate":
		if !seenResults {
			return `{"calls": [{"tool": "read_file", "args": {"path": "README.md"}}, {"tool": "shell", "args": {"cmd": "git rev-parse --abbrev-ref HEAD"}}], "final": null}`
		}
		return "{\"calls\": [], \"final\": \"done. saw_results=" +
			pyBool(strings.Contains(input, `"exit_code": 0`) || strings.Contains(input, `"exit_code":0`)) + ".\"}"
	case "shellstate":
		if !seenResults {
			return `{"calls": [{"tool": "shell", "args": {"cmd": "mkdir -p sub && cd sub"}}, {"tool": "shell", "args": {"cmd": "pwd"}}], "final": null}`
		}
		return "{\"calls\": [], \"final\": \"cwd_persisted=" + pyBool(strings.Contains(input, "/sub")) + "\"}"
	case "starved":
		// A gateway that accepts our session_id (so the client believes
		// Threaded=true and never falls back to a local recap) yet does not
		// actually replay the turn's transcript to the model - the model sees
		// exactly the bytes in this POST's `input`, nothing more. Round 2 only
		// answers correctly if the operator's question rode along in that
		// literal payload; this is the failure mode observed against the real
		// gateway (a starved round 2 fell back to a generic repo description).
		if !seenResults {
			return `{"calls": [{"tool": "shell", "args": {"cmd": "git describe --tags"}}], "final": null}`
		}
		if strings.Contains(input, "what version is this, in one sentence") {
			return `{"calls": [], "final": "answered: saw the operator's question."}`
		}
		return `{"calls": [], "final": "generic repo description (never saw what was asked)."}`
	default:
		return `{"calls": [], "final": "?"}`
	}
}

// serveEvents streams one run as SSE: the golden mode's tool events on the
// first round, the output envelope in 5-byte deltas, then run.completed.
func (s *server) serveEvents(w http.ResponseWriter, id string) {
	s.mu.Lock()
	rec, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "unknown run"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	frame := func(v map[string]any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
	}
	if s.mode == "golden" && rec.round1 {
		frame(map[string]any{"event": "tool.started", "tool": "terminal", "preview": "echo probe"})
		frame(map[string]any{"event": "tool.started", "tool": "terminal", "preview": "make test"})
		frame(map[string]any{"event": "approval.request", "tool": "terminal", "request_id": "req-1", "command": "rm -rf /tmp/scratch"})
	}
	for out := rec.out; out != ""; {
		n := min(5, len(out))
		for n < len(out) && !utf8.RuneStart(out[n]) { // whole runes per frame, as a real gateway sends
			n++
		}
		frame(map[string]any{"event": "message.delta", "delta": out[:n]})
		out = out[n:]
	}
	frame(map[string]any{"event": "run.completed", "output": rec.out, "session_id": rec.sid, "usage": map[string]any{"total_tokens": 42}})
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
