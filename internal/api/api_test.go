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

	"github.com/Zesales/hermes-hands/internal/hermesmock"
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

	res, err := c.Ask(context.Background(), "hi", "hh-abc-ts", "hermes-hands:key")
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

	cr, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if cr.Model != "hermes-agent" {
		t.Errorf("Model = %q, want hermes-agent", cr.Model)
	}
	if cr.Base != srv.URL {
		t.Errorf("Base = %q, want %q", cr.Base, srv.URL)
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
	_, err := c.Ask(context.Background(), "hi", "hh-x", "k")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401 at /v1/runs - HERMES_API_KEY rejected.") {
		t.Fatalf("err = %v, want the 401 message", err)
	}
	if posts != 1 {
		t.Errorf("posts = %d, want 1 (401 must not retry)", posts)
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

	res, err := c.Ask(context.Background(), "hi", "hh-sess-id", "hermes-hands:sesskey")
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
	_, err := c.Ask(context.Background(), "hi", "", "")
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
	_, err := c.Ask(context.Background(), "hi", "", "")
	if err == nil || err.Error() != "run run_1 failed: boom" {
		t.Fatalf("err = %v, want 'run run_1 failed: boom'", err)
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
	c.SetTitle(context.Background(), "hh-abc", "my <title> & more") // must not panic

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
	testClient(srv.URL).StopRun(context.Background(), "run_9")
	if got != "POST /v1/runs/run_9/stop" {
		t.Errorf("StopRun hit %q", got)
	}
	// empty run id is a no-op
	got = ""
	testClient(srv.URL).StopRun(context.Background(), "")
	if got != "" {
		t.Errorf("StopRun('') should not call, hit %q", got)
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
	si, err := testClient(srv.URL).SessionInfo(context.Background(), "hh-x")
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
		ss, err := testClient(srv.URL).ListSessions(context.Background())
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
