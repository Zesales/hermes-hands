package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hermes-hands/internal/hermesmock"

	"hermes-hands/internal/guard"
)

func testClient(baseURL string) *Client {
	return &Client{
		HTTP:         NewHTTPClient(2*time.Second, 5*time.Second),
		BaseURL:      baseURL,
		Key:          "testkey",
		AllowHTTP:    true,
		Retries:      1,
		PollInterval: time.Millisecond,
		RunTimeout:   5 * time.Second,
		retryPause:   time.Millisecond,
		now:          func() time.Time { return time.Unix(1_700_000_000, 0) },
		rnd:          func() int { return 12345 },
	}
}

func TestAsk_PlainCompleted(t *testing.T) {
	srv := hermesmock.New("plain")
	defer srv.Close()
	c := testClient(srv.URL)

	res, err := c.Runs().Ask(context.Background(), "hi", "hh-abc-ts", "hermes-hands:key")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if res.State != "completed" {
		t.Errorf("State = %q, want completed", res.State)
	}
	if !strings.Contains(res.Text, "plain answer from the mock brain") {
		t.Errorf("Text = %q", res.Text)
	}
	if res.RunID != "run_1" {
		t.Errorf("RunID = %q", res.RunID)
	}
	if !res.Threaded {
		t.Errorf("Threaded = false, want true (no drop)")
	}
	if res.SessionID != "hh-abc-ts" {
		t.Errorf("SessionID = %q, want the echoed posted sid", res.SessionID)
	}
}

func TestCheck_OK(t *testing.T) {
	srv := hermesmock.New("plain")
	defer srv.Close()
	c := testClient(srv.URL)

	cr, err := c.Server().Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if cr.Model != "hermes-agent" {
		t.Errorf("Model = %q, want hermes-agent", cr.Model)
	}
	if cr.Base != srv.URL {
		t.Errorf("Base = %q, want %q", cr.Base, srv.URL)
	}
	if cr.Version != "0.22.2" {
		t.Errorf("Version = %q, want the /health version", cr.Version)
	}
}

func TestAsk_401Fatal(t *testing.T) {
	var posts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			posts++
			mu.Unlock()
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Runs().Ask(context.Background(), "hi", "hh-x", "k")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401 at /v1/runs - HERMES_API_KEY rejected.") {
		t.Fatalf("err = %v, want the 401 message", err)
	}
	if posts != 1 {
		t.Errorf("posts = %d, want 1 (401 must not retry)", posts)
	}
}

// AskWith sends the transcript as conversation_history; the run's usage
// gives the prompt, and a polled run cannot say how many model calls it took.
func TestAskWith_SendsTheHistory(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/runs"):
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"run_id":"run_1","status":"queued"}`))
		case strings.HasSuffix(r.URL.Path, "/v1/runs/run_1"):
			_, _ = w.Write([]byte(`{"status":"completed","output":"ok","session_id":"s1","usage":{"input_tokens":51234,"output_tokens":20,"total_tokens":51254}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	hist := []Message{{Role: "user", Content: "[operator] hi"}, {Role: "assistant", Content: "hello"}}
	res, err := c.Runs().AskWith(context.Background(), "next", "s1", "", hist)
	if err != nil {
		t.Fatal(err)
	}
	var got []Message
	if json.Unmarshal(body["conversation_history"], &got) != nil || len(got) != 2 || got[1].Content != "hello" {
		t.Errorf("conversation_history = %s", body["conversation_history"])
	}
	if res.Prompt != 51234 || res.Tokens != 51254 || res.HermesTools != -1 {
		t.Errorf("res = %+v", res)
	}
	body = nil
	if _, err := c.Runs().Ask(context.Background(), "plain", "s1", ""); err != nil {
		t.Fatal(err)
	}
	if _, sent := body["conversation_history"]; sent {
		t.Error("Ask must not send a history")
	}
}

func TestAsk_SessionDropRetry(t *testing.T) {
	type postInfo struct {
		hasSID bool
		hdrID  string
		hdrKey string
	}
	var mu sync.Mutex
	var seen []postInfo

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/runs"):
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			_, hasSID := body["session_id"]
			mu.Lock()
			seen = append(seen, postInfo{
				hasSID: hasSID,
				hdrID:  r.Header.Get("X-Hermes-Session-Id"),
				hdrKey: r.Header.Get("X-Hermes-Session-Key"),
			})
			n := len(seen)
			mu.Unlock()
			if hasSID {
				w.WriteHeader(422)
				return
			}
			writeTestJSON(w, 200, `{"run_id":"run_`+itoa(n)+`","status":"started"}`)
		case r.Method == http.MethodGet:
			writeTestJSON(w, 200, `{"status":"completed","output":"{\"calls\":[],\"final\":\"ok\"}","session_id":""}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.Warnf = func(string, ...any) {} // silence the expected warning

	res, err := c.Runs().Ask(context.Background(), "hi", "hh-sess-id", "hermes-hands:sesskey")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("POST count = %d, want 2 (one rejected + one retried)", len(seen))
	}
	if !seen[0].hasSID {
		t.Errorf("POST#1 should have carried body session_id")
	}
	if seen[1].hasSID {
		t.Errorf("POST#2 must omit body session_id after the drop")
	}
	for i, p := range seen {
		if p.hdrID != "hh-sess-id" || p.hdrKey != "hermes-hands:sesskey" {
			t.Errorf("POST#%d headers = %q/%q, want the session headers retained on both", i+1, p.hdrID, p.hdrKey)
		}
	}
	if res.Threaded {
		t.Errorf("Threaded = true, want false after a drop")
	}
}

func TestAsk_RetryThenUnreachable(t *testing.T) {
	var posts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(503)
	}))
	defer srv.Close()

	c := testClient(srv.URL) // Retries: 1 -> 2 attempts
	_, err := c.Runs().Ask(context.Background(), "hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "POST /v1/runs unreachable") {
		t.Fatalf("err = %v, want unreachable", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 2 {
		t.Errorf("posts = %d, want 2 (Retries=1 -> 2 attempts)", posts)
	}
}

func TestAsk_PollFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeTestJSON(w, 200, `{"run_id":"run_1","status":"started"}`)
			return
		}
		writeTestJSON(w, 200, `{"status":"failed","output":"boom"}`)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Runs().Ask(context.Background(), "hi", "", "")
	if err == nil || err.Error() != "run run_1 failed: boom" {
		t.Fatalf("err = %v, want 'run run_1 failed: boom'", err)
	}
}

// hermes-agent marks a run the gateway restart cut off "interrupted" — a
// terminal status, not one to poll on until the watchdog
func TestAsk_PollInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeTestJSON(w, 200, `{"run_id":"run_1","status":"started"}`)
			return
		}
		writeTestJSON(w, 200, `{"status":"interrupted","error":"The gateway restarted before this run settled."}`)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	_, err := c.Runs().Ask(context.Background(), "hi", "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "run run_1 interrupted: ") {
		t.Fatalf("err = %v, want the interrupted run reported", err)
	}
}

func TestSetTitle_SwallowsError(t *testing.T) {
	var got struct {
		mu     sync.Mutex
		path   string
		body   string
		called bool
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			got.mu.Lock()
			got.called, got.path, got.body = true, r.URL.Path, string(b)
			got.mu.Unlock()
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	c.Sessions().SetTitle(context.Background(), "hh-abc", "my <title> & more") // must not panic

	got.mu.Lock()
	defer got.mu.Unlock()
	if !got.called {
		t.Fatal("PATCH was not sent")
	}
	if !strings.HasSuffix(got.path, "/api/sessions/hh-abc") {
		t.Errorf("PATCH path = %q", got.path)
	}
	if got.body != `{"title":"my <title> & more"}` {
		t.Errorf("PATCH body = %q, want unescaped title JSON", got.body)
	}
}

func TestBaseAndTitleBase(t *testing.T) {
	c := &Client{BaseURL: "https://h.example.net/"}
	if c.base() != "https://h.example.net" {
		t.Errorf("base trims exactly one slash: %q", c.base())
	}
	c.Profile = "coder"
	if c.base() != "https://h.example.net/p/coder" {
		t.Errorf("profile suffix: %q", c.base())
	}
	c2 := &Client{BaseURL: "https://h.example.net/v1"}
	if c2.titleBase() != "https://h.example.net" {
		t.Errorf("titleBase strips /v1: %q", c2.titleBase())
	}
}

func writeTestJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func itoa(n int) string {
	return string(rune('0' + n))
}

func TestStopRun_FiresPOST(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(202)
	}))
	defer srv.Close()
	testClient(srv.URL).Runs().Stop(context.Background(), "run_9")
	if got != "POST /v1/runs/run_9/stop" {
		t.Errorf("StopRun hit %q", got)
	}
	// empty run id is a no-op
	got = ""
	testClient(srv.URL).Runs().Stop(context.Background(), "")
	if got != "" {
		t.Errorf("StopRun('') should not call, hit %q", got)
	}
}

func TestRunState(t *testing.T) {
	var status string
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/run_7" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if code != 200 {
			w.WriteHeader(code)
			return
		}
		writeTestJSON(w, 200, status)
	}))
	defer srv.Close()
	c := testClient(srv.URL)

	status = `{"status":"running"}`
	if st, err := c.Runs().State(context.Background(), "run_7"); err != nil || st != "running" {
		t.Errorf("RunState running = %q, %v", st, err)
	}
	status = `{"status":"completed","output":"hi"}`
	if st, err := c.Runs().State(context.Background(), "run_7"); err != nil || st != "completed" {
		t.Errorf("RunState completed = %q, %v", st, err)
	}
	status = `{"output":"hi"}` // no status field
	if st, err := c.Runs().State(context.Background(), "run_7"); err != nil || st != "" {
		t.Errorf("RunState missing = %q, %v (want empty, nil)", st, err)
	}
	code = 404
	if st, err := c.Runs().State(context.Background(), "run_7"); err == nil || st != "" {
		t.Errorf("RunState 404 = %q, %v (want error)", st, err)
	}
	// empty run id -> error, no request
	if _, err := c.Runs().State(context.Background(), ""); err == nil {
		t.Error("RunState('') should error")
	}
}

func TestSessionInfo_LenientParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/hh-x" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"object":"hermes.session","session":{"id":"hh-x","title":"a task",
		  "message_count":42,"parent_session_id":"hh-old","model":"qwen",
		  "input_tokens":600,"cache_read_tokens":7500,"started_at":1788400000,"ended_at":"2026-09-03"}}`)
	}))
	defer srv.Close()
	si, err := testClient(srv.URL).Sessions().Info(context.Background(), "hh-x")
	if err != nil {
		t.Fatal(err)
	}
	if si.Title != "a task" || si.Messages != 42 || si.Parent != "hh-old" ||
		si.Model != "qwen" || si.Tokens != 8100 || !si.Ended || si.Created == "" {
		t.Errorf("SrvSession = %+v", si)
	}
}

func TestListSessions_ArrayAndWrapped(t *testing.T) {
	for _, body := range []string{
		`[{"session_id":"hh-1","title":"one","messages":3},{"session_id":"hh-2"}]`,
		`{"sessions":[{"session_id":"hh-1","title":"one","messages":3},{"session_id":"hh-2"}]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		ss, err := testClient(srv.URL).Sessions().List(context.Background())
		srv.Close()
		if err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if len(ss) != 2 || ss[0].ID != "hh-1" || ss[0].Title != "one" || ss[0].Messages != 3 || ss[1].ID != "hh-2" {
			t.Errorf("body %s -> %+v", body, ss)
		}
	}
}

func TestParseCaps(t *testing.T) {
	c := parseCaps([]byte(`{"model":"m","features":{"run_events_sse":true,"run_stop":true},"session":{"session_compress":false}}`))
	if c.Model != "m" || !c.Has("run_events_sse") || !c.Has("run_stop") || c.Has("session_compress") || c.Has("nope") {
		t.Errorf("caps = %+v", c)
	}
}

func TestStreamRun_RealShape(t *testing.T) {
	// exactly the frames a live gateway emits
	body := "" +
		`data: {"event": "message.delta", "delta": "Hi"}` + "\n\n" +
		`data: {"event": "message.delta", "delta": " there"}` + "\n\n" +
		`data: {"event": "reasoning.available", "text": "thinking"}` + "\n\n" +
		`data: {"event": "run.completed", "output": "Hi there.", "usage": {"input_tokens": 100, "output_tokens": 5, "total_tokens": 105}, "session_id": "sess-9"}` + "\n\n" +
		": stream closed\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/run_x/events" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	var deltas []string
	c.OnDelta = func(s string) { deltas = append(deltas, s) }
	var events []string
	c.OnEvent = func(e string) { events = append(events, e) }
	res, err := c.streamRun(context.Background(), srv.URL, "run_x", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hi there." || res.SessionID != "sess-9" || res.Tokens != 105 || !res.Threaded {
		t.Errorf("res = %+v", res)
	}
	if strings.Join(deltas, "") != "Hi there" {
		t.Errorf("deltas = %q", deltas)
	}
	// every frame is a sign of life — including ones without a case
	if strings.Join(events, ",") != "message.delta,message.delta,reasoning.available,run.completed" {
		t.Errorf("events = %q", events)
	}
}

func TestStreamRun_SandboxGuard(t *testing.T) {
	// Hermes reaching for its own sandbox tools is steered back to the hands
	// (each call, up to steersPerKind a run), its approval requests are
	// denied, and thinking-side tools pass untouched.
	body := "" +
		`data: {"event": "tool.started", "tool": "web_search", "preview": "go 1.26 release"}` + "\n\n" +
		`data: {"event": "tool.started", "tool": "terminal", "preview": "make test"}` + "\n\n" +
		`data: {"event": "tool.started", "tool": "read_file", "preview": "Makefile"}` + "\n\n" +
		`data: {"event": "approval.request", "request_id": "ap-1", "command": "rm -rf /tmp/x"}` + "\n\n" +
		`data: {"event": "run.completed", "output": "{\"calls\":[],\"final\":\"ok\"}"}` + "\n\n"
	var steers, approvals []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/runs/run_g/events":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, body)
		case "/v1/runs/run_g/steer":
			steers = append(steers, string(b))
			io.WriteString(w, `{"accepted":true}`)
		case "/v1/runs/run_g/approval":
			approvals = append(approvals, string(b))
			io.WriteString(w, `{"resolved":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	var seen []string
	c.OnHermesTool = func(tool, preview, action string) { seen = append(seen, tool+"/"+action) }
	if _, err := c.streamRun(context.Background(), srv.URL, "run_g", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, " ") != "web_search/ terminal/steered read_file/steered approval/denied" {
		t.Errorf("hermes tools = %q", seen)
	}
	if len(steers) != 2 || !strings.Contains(steers[0], "`terminal`") || !strings.Contains(steers[1], "`read_file`") ||
		!strings.Contains(steers[0], `"input"`) {
		t.Errorf("steers = %q, want one per sandbox call, each naming its tool", steers)
	}
	if len(approvals) != 1 || !strings.Contains(approvals[0], `"choice":"deny"`) || !strings.Contains(approvals[0], `"request_id":"ap-1"`) {
		t.Errorf("approvals = %q", approvals)
	}
}

func TestStreamRun_SteersRepeatsUpToCapThenStops(t *testing.T) {
	// seen live: steered once, a model kept calling tool_call "Local Shell"
	// — each repeat is steered again up to the cap; the next one stops the
	// run, the rest of it is only waited out, and the result says why.
	// When the server refuses the stop, a repeat is only reported.
	for _, tc := range []struct {
		name    string
		stopOK  bool
		end     string
		actions string // past the steersPerKind steers
	}{
		{"stopped", true, `{"event": "run.cancelled"}`, "stopped"},
		{"stopped, ends empty", true, `{"event": "run.completed", "output": ""}`, "stopped"},
		{"stop refused", false, `{"event": "run.completed", "output": "{\"calls\":[],\"final\":\"ok\"}"}`, "repeat repeat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body strings.Builder
			for range steersPerKind + 2 {
				body.WriteString(`data: {"event": "tool.started", "tool": "tool_call", "preview": "Local Shell"}` + "\n\n")
			}
			body.WriteString("data: " + tc.end + "\n\n")
			var steers []string
			stops := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				switch r.URL.Path {
				case "/v1/runs/run_r/events":
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, body.String())
				case "/v1/runs/run_r/steer":
					steers = append(steers, string(b))
					io.WriteString(w, `{"accepted":true}`)
				case "/v1/runs/run_r/stop":
					stops++
					if !tc.stopOK {
						w.WriteHeader(500)
					}
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()
			c := testClient(srv.URL)
			var seen []string
			c.OnHermesTool = func(tool, preview, action string) { seen = append(seen, action) }
			res, err := c.streamRun(context.Background(), srv.URL, "run_r", true)
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Repeat("steered ", steersPerKind) + tc.actions; strings.Join(seen, " ") != want {
				t.Errorf("actions = %q, want %q", seen, want)
			}
			if len(steers) != steersPerKind || !strings.Contains(steers[0], "under no spelling") {
				t.Errorf("steers = %q, want %d hands-tool steers", steers, steersPerKind)
			}
			if tc.stopOK {
				if stops != 1 || res.State != "stopped" || res.Stopped != "`tool_call` Local Shell" || res.Text != "" {
					t.Errorf("stops = %d, res = %+v, want one stop and a stopped result naming the call", stops, res)
				}
			} else if res.State != "completed" || res.Stopped != "" || stops != 2 {
				t.Errorf("stops = %d, res = %+v, want a stop asked for each repeat and the run's own reply", stops, res)
			}
		})
	}
}

func TestTraceSeesRunAndFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event": "run.completed", "output": "done"}`+"\n\n")
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	var kinds []string
	c.Trace = func(kind string, f map[string]any) { kinds = append(kinds, kind) }
	if _, err := c.streamRun(context.Background(), srv.URL, "r", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "http,sse" {
		t.Errorf("trace kinds = %q", kinds)
	}
}

func TestStreamRun_NoCompletedFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `data: {"event":"message.delta","delta":"partial"}`+"\n\n")
	}))
	defer srv.Close()
	if _, err := testClient(srv.URL).streamRun(context.Background(), srv.URL, "r", true); err == nil {
		t.Error("stream without run.completed must error so Ask falls back to poll")
	}
}

func TestWantStream(t *testing.T) {
	c := &Client{}
	if c.wantStream() {
		t.Error("auto + no caps -> no stream")
	}
	c.Caps = Caps{Features: map[string]bool{"run_events_sse": true}}
	if !c.wantStream() {
		t.Error("auto + caps -> stream")
	}
	c.StreamMode = "off"
	if c.wantStream() {
		t.Error("off -> no stream even with caps")
	}
	c.StreamMode, c.Caps = "on", Caps{}
	if !c.wantStream() {
		t.Error("on -> stream even without caps")
	}
}

func TestSetTitle_RetriesTakenTitle(t *testing.T) {
	// hermes-agent rejects a title another session already has: the second
	// session must still get one ("… (2)")
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		titles = append(titles, b.Title)
		if b.Title == "fix CI" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Title 'fix CI' is already in use by session hh-x","code":"invalid_title"}}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	testClient(srv.URL).Sessions().SetTitle(context.Background(), "hh-2", "fix CI")
	if strings.Join(titles, " | ") != "fix CI | fix CI (2)" {
		t.Errorf("titles tried = %q", titles)
	}
}

func TestStreamRun_GuardPolicyAndOperatorApproval(t *testing.T) {
	// with a policy: server-side work passes (shown as "server"), a call aimed
	// at the operator's machine is steered; an approval goes to the operator
	body := "" +
		`data: {"event": "tool.started", "tool": "execute_code", "preview": "print(1+1)"}` + "\n\n" +
		`data: {"event": "tool.started", "tool": "terminal", "preview": "make test"}` + "\n\n" +
		`data: {"event": "approval.request", "request_id": "ap-7", "command": "rm -rf /tmp/build"}` + "\n\n" +
		`data: {"event": "run.completed", "output": "{\"calls\":[],\"final\":\"ok\"}"}` + "\n\n"
	var steers, approvals []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/runs/run_p/events":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, body)
		case "/v1/runs/run_p/steer":
			steers = append(steers, string(b))
			io.WriteString(w, `{"accepted":true}`)
		case "/v1/runs/run_p/approval":
			approvals = append(approvals, string(b))
			io.WriteString(w, `{"resolved":1}`)
		}
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	c.Guard = func(tool, preview string) guard.Verdict {
		return guard.Check(tool, preview, t.TempDir(), "")
	}
	var asked string
	c.OnApproval = func(tool, what string) string { asked = what; return "once" }
	var seen []string
	c.OnHermesTool = func(tool, preview, action string) { seen = append(seen, tool+"/"+action) }
	if _, err := c.streamRun(context.Background(), srv.URL, "run_p", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, " ") != "execute_code/server terminal/steered approval/approved" {
		t.Errorf("hermes tools = %q", seen)
	}
	if len(steers) != 1 || !strings.Contains(steers[0], "`make`") || !strings.Contains(steers[0], "the exact command to install it") {
		t.Errorf("steer = %q, want the policy's reason and what to do about a missing tool", steers)
	}
	if asked != "rm -rf /tmp/build" || len(approvals) != 1 || !strings.Contains(approvals[0], `"choice":"once"`) {
		t.Errorf("approval: asked %q, sent %q", asked, approvals)
	}
}

// A streamed run counts Hermes' own server tools: each one is one more model
// call, so the run's prompt usage is no longer a single prompt.
func TestStream_CountsHermesTools(t *testing.T) {
	srv := hermesmock.NewStream("golden")
	defer srv.Close()
	c := testClient(srv.URL)
	if _, err := c.Server().Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.Runs().Ask(context.Background(), "show me the readme", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.HermesTools != 2 {
		t.Errorf("HermesTools = %d, want the 2 tool.started events", res.HermesTools)
	}
}
