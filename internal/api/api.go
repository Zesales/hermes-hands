// Package api ports lib/api.sh: one Hermes turn over the merged Runs API
// (POST /v1/runs + poll GET /v1/runs/{id}), the /v1/capabilities preflight,
// and the best-effort PATCH /api/sessions/{id} title mirror. The transport is
// net/http instead of curl; every wire detail (paths, headers, retry/drop
// ladder, id minting, terminal-status handling) is kept as in the bash source.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Zesales/hermes-hands/internal/config"
)

// retrySleep is bash's fixed `sleep 2` between failed POST / capability
// attempts (not the poll interval, not exponential — see plan §2 #6).
const retrySleep = 2 * time.Second

// Client talks to one Hermes gateway.
type Client struct {
	HTTP         *http.Client
	BaseURL      string
	Key          string
	Profile      string
	SecretsPath  string
	AllowHTTP    bool
	Retries      int
	PollInterval time.Duration
	RunTimeout   time.Duration // whole poll loop ceiling (HERMES_API_RUN_TIMEOUT)
	Instructions string

	// Caps is filled by Check from /v1/capabilities so callers can gate
	// optional features (run_events_sse, session_compress, …).
	Caps Caps

	// OnRunStart, when set, is called with the run id as soon as POST /v1/runs
	// returns it — before polling. The REPL uses it so Ctrl-C can POST
	// /v1/runs/{id}/stop on the in-flight run.
	OnRunStart func(runID string)

	// StreamMode: "" / "auto" (stream when the gateway advertises
	// run_events_sse), "on", or "off". OnDelta receives answer-text chunks as
	// they arrive over the SSE stream.
	StreamMode string
	OnDelta    func(text string)

	Warnf func(string, ...any)
	Vlogf func(string, ...any)
	Log   func(string, ...any)

	now func() time.Time
	rnd func() int

	// retryPause overrides the fixed 2s pause between failed attempts; 0 keeps
	// the bash default. Set only by tests.
	retryPause time.Duration
}

// CheckResult is the outcome of a successful preflight.
type CheckResult struct{ Model, Base string }

// Caps is the parsed /v1/capabilities surface — used to gate features that a
// given Hermes build may not have (SSE run events, session compress, …).
type Caps struct {
	Model    string
	Features map[string]bool
}

// Has reports whether a feature flag is present and true.
func (c Caps) Has(f string) bool { return c.Features[f] }

// SrvSession is the subset of a server-side session record we render. Every
// field is best-effort — the /api/sessions response schema is not published, so
// missing keys just leave zero values.
type SrvSession struct {
	ID       string
	Title    string
	Messages int
	Parent   string // parent_session_id — compaction/split lineage
	Model    string
	Created  string
	Updated  string
	Tokens   int
	Ended    bool
}

// AskResult is a completed turn.
type AskResult struct {
	State     string // "completed"
	Text      string // the assistant's final output
	RunID     string
	SessionID string // .session_id from run status ("" if none)
	Threaded  bool   // false after a session-id drop
	Tokens    int    // best-effort token count from the run's .usage (0 if absent)
}

// usageTokens pulls a best-effort token count out of a run status body's
// `usage` object. Hermes does not yet expose real context-window usage
// (NousResearch/hermes-agent#15618); this is the cumulative billing count:
// total_tokens if present, else input+output / prompt+completion.
func usageTokens(body []byte) int {
	var top struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &top) != nil || top.Usage == nil {
		return 0
	}
	num := func(keys ...string) int {
		for _, k := range keys {
			if raw, ok := top.Usage[k]; ok {
				var n float64
				if json.Unmarshal(raw, &n) == nil {
					return int(n)
				}
			}
		}
		return 0
	}
	if t := num("total_tokens", "total", "context_tokens"); t > 0 {
		return t
	}
	return num("input_tokens", "prompt_tokens") + num("output_tokens", "completion_tokens")
}

// NewHTTPClient builds a client with curl-equivalent timeouts: a per-dial
// connect timeout and an overall per-request deadline.
func NewHTTPClient(connectTimeout, maxTime time.Duration) *http.Client {
	return &http.Client{
		Timeout: maxTime,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
			TLSHandshakeTimeout:   connectTimeout,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// base ports hh_api_base: strip exactly one trailing slash, then append
// /p/<profile> when a profile is set. Endpoints add their own /v1/... .
func (c *Client) base() string {
	b := strings.TrimSuffix(c.BaseURL, "/")
	if c.Profile != "" {
		b += "/p/" + c.Profile
	}
	return b
}

// titleBase ports `hh_api_base | sed 's#/v1$##'`.
func (c *Client) titleBase() string {
	return strings.TrimSuffix(c.base(), "/v1")
}

// preflight ports hh_api_preflight (minus the dropped `hh_need jq curl`).
func (c *Client) preflight() error {
	if config.LooksUnset(c.BaseURL) {
		return fmt.Errorf("HERMES_API_URL not set (or placeholder) - run: hermes-hands setup")
	}
	if config.LooksUnset(c.Key) {
		return fmt.Errorf("HERMES_API_KEY not set (or placeholder) - run: hermes-hands setup")
	}
	return config.RequireHTTPS(c.BaseURL, "HERMES_API_URL", c.AllowHTTP)
}

// do is the _hh_curl equivalent: one request carrying the bearer token, plus
// any extra headers. code is the HTTP status (0 when the request never
// completed, matching curl's "000").
func (c *Client) do(ctx context.Context, method, url string, body []byte, hdr map[string]string) (code int, respBody []byte, err error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, rerr := io.ReadAll(resp.Body)
	return resp.StatusCode, b, rerr
}

// Check ports hh_api_check: GET /v1/capabilities with a fixed-2s retry ladder.
func (c *Client) Check(ctx context.Context) (CheckResult, error) {
	if err := c.preflight(); err != nil {
		return CheckResult{}, err
	}
	base := c.base()
	for attempt := 1; ; attempt++ {
		code, body, err := c.do(ctx, http.MethodGet, base+"/v1/capabilities",
			nil, map[string]string{"Accept": "application/json"})
		if err == nil && is2xx(code) && looksJSON(body) {
			c.Caps = parseCaps(body)
			return CheckResult{Model: checkModel(body), Base: base}, nil
		}
		switch code {
		case 401, 403:
			return CheckResult{}, fmt.Errorf("HTTP %d at /v1/capabilities - HERMES_API_KEY rejected.", code)
		case 404:
			return CheckResult{}, fmt.Errorf("HTTP 404 at %s/v1/capabilities - wrong base URL / profile, or API server disabled.", base)
		}
		if attempt > c.Retries {
			return CheckResult{}, fmt.Errorf("cannot reach %s/v1 - %s, last HTTP %s", base, errText(err), httpCode(code))
		}
		if serr := sleep(ctx, retrySleep); serr != nil {
			return CheckResult{}, serr
		}
	}
}

// SetTitle ports hh_api_set_title: a best-effort PATCH, all output and errors
// swallowed. No-op unless URL, key and id are all present.
func (c *Client) SetTitle(ctx context.Context, hermesSessionID, title string) {
	if c.BaseURL == "" || c.Key == "" || hermesSessionID == "" {
		return
	}
	body, err := marshalJSON(map[string]string{"title": title})
	if err != nil {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, _, _ = c.do(tctx, http.MethodPatch, c.titleBase()+"/api/sessions/"+hermesSessionID,
		body, map[string]string{"Content-Type": "application/json"})
}

// RawRun submits one run with no retry ladder and returns its ids — for the
// hidden `_events` inspector only.
func (c *Client) RawRun(ctx context.Context, input string) (runID, sessionID string, err error) {
	if err := c.preflight(); err != nil {
		return "", "", err
	}
	body, _ := marshalJSON(map[string]string{"input": input})
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, resp, err := c.do(tctx, http.MethodPost, c.base()+"/v1/runs", body,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", "", err
	}
	if !is2xx(code) {
		return "", "", fmt.Errorf("POST /v1/runs -> HTTP %s: %s", httpCode(code), trunc(stripNL(string(resp)), 300))
	}
	return jsonString(resp, "run_id"), jsonString(resp, "session_id"), nil
}

// RawEvents copies the /v1/runs/{id}/events SSE stream to w verbatim, for
// inspecting the wire shape. Stops on stream end, ctx, or ~256KB.
func (c *Client) RawEvents(ctx context.Context, runID string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/v1/runs/"+runID+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "text/event-stream")
	hc := &http.Client{Transport: c.HTTP.Transport} // no overall Timeout for a stream
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !is2xx(resp.StatusCode) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return fmt.Errorf("events -> HTTP %d: %s", resp.StatusCode, stripNL(string(b)))
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, 256*1024))
	return err
}

// RawGet does one authenticated GET of an API path (joined onto base) and
// returns the body — for the hidden `_raw` schema inspector.
func (c *Client) RawGet(ctx context.Context, path string) (string, error) {
	if err := c.preflight(); err != nil {
		return "", err
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodGet, c.base()+path, nil,
		map[string]string{"Accept": "application/json"})
	if err != nil {
		return "", err
	}
	if !is2xx(code) {
		return "", fmt.Errorf("GET %s -> HTTP %s: %s", path, httpCode(code), stripNL(string(body)))
	}
	return string(body), nil
}

// StopRun asks Hermes to interrupt a running turn (POST /v1/runs/{id}/stop).
// Best-effort: fire it on Ctrl-C so the server turn doesn't keep burning
// tokens after the operator has moved on. Errors are swallowed.
func (c *Client) StopRun(ctx context.Context, runID string) {
	if runID == "" || c.BaseURL == "" || c.Key == "" {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, _ = c.do(tctx, http.MethodPost, c.base()+"/v1/runs/"+runID+"/stop", nil, nil)
}

// SessionInfo reads a server-side session record (GET /api/sessions/{id}).
// The response schema is not published, so parsing is lenient; a non-2xx or an
// unparseable body returns an error and the caller falls back to the local
// index.
func (c *Client) SessionInfo(ctx context.Context, hermesSessionID string) (SrvSession, error) {
	if hermesSessionID == "" {
		return SrvSession{}, fmt.Errorf("no hermes-agent session id yet")
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodGet, c.titleBase()+"/api/sessions/"+hermesSessionID,
		nil, map[string]string{"Accept": "application/json"})
	if err != nil {
		return SrvSession{}, err
	}
	if !is2xx(code) {
		return SrvSession{}, fmt.Errorf("GET /api/sessions/%s -> HTTP %s", hermesSessionID, httpCode(code))
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return SrvSession{}, fmt.Errorf("unparseable session body")
	}
	m := top
	if raw, ok := top["session"]; ok { // {"object":"hermes.session","session":{...}}
		var inner map[string]json.RawMessage
		if json.Unmarshal(raw, &inner) == nil {
			m = inner
		}
	}
	return srvSessionFrom(m), nil
}

// ListSessions reads the server-side session list (GET /api/sessions). Lenient:
// it accepts a bare array or an object with a "sessions"/"data"/"items" array.
func (c *Client) ListSessions(ctx context.Context) ([]SrvSession, error) {
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodGet, c.titleBase()+"/api/sessions?limit=100",
		nil, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if !is2xx(code) {
		return nil, fmt.Errorf("GET /api/sessions -> HTTP %s", httpCode(code))
	}
	var arr []map[string]json.RawMessage
	if json.Unmarshal(body, &arr) != nil {
		var wrap map[string]json.RawMessage
		if json.Unmarshal(body, &wrap) != nil {
			return nil, fmt.Errorf("unparseable session list")
		}
		for _, k := range []string{"sessions", "data", "items", "results"} {
			if raw, ok := wrap[k]; ok && json.Unmarshal(raw, &arr) == nil {
				break
			}
		}
	}
	out := make([]SrvSession, 0, len(arr))
	for _, m := range arr {
		if s := srvSessionFrom(m); s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// srvSessionFrom pulls the fields we render out of a raw session object. Field
// names verified against a live gateway (2026-09-03).
func srvSessionFrom(m map[string]json.RawMessage) SrvSession {
	str := func(keys ...string) string {
		for _, k := range keys {
			if raw, ok := m[k]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil && s != "" {
					return s
				}
			}
		}
		return ""
	}
	num := func(keys ...string) int {
		for _, k := range keys {
			if raw, ok := m[k]; ok {
				var f float64
				if json.Unmarshal(raw, &f) == nil {
					return int(f)
				}
			}
		}
		return 0
	}
	// timestamps arrive as unix-second floats
	ts := func(keys ...string) string {
		for _, k := range keys {
			if raw, ok := m[k]; ok {
				var f float64
				if json.Unmarshal(raw, &f) == nil && f > 0 {
					return time.Unix(int64(f), 0).UTC().Format("2006-01-02T15:04:05Z")
				}
			}
		}
		return ""
	}
	s := SrvSession{
		ID:       str("id", "session_id"),
		Title:    str("title", "name"),
		Parent:   str("parent_session_id", "previous_session_id"),
		Model:    str("model", "model_name"),
		Created:  ts("started_at", "created_at"),
		Updated:  ts("last_active", "updated_at", "last_active_at"),
		Messages: num("message_count", "messages"),
		// context ~ tokens sent on the last call: fresh input + the cached prefix
		Tokens: num("input_tokens", "prompt_tokens") + num("cache_read_tokens"),
	}
	var ended *bool
	if raw, ok := m["ended_at"]; ok && string(raw) != "null" {
		t := true
		ended = &t
	}
	if str("end_reason") != "" || ended != nil {
		s.Ended = true
	}
	return s
}

// Fork branches the session (POST /api/sessions/{id}/fork) and returns the new
// session id.
func (c *Client) Fork(ctx context.Context, hermesSessionID string) (string, error) {
	if hermesSessionID == "" {
		return "", fmt.Errorf("no hermes-agent session id yet")
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodPost,
		c.titleBase()+"/api/sessions/"+hermesSessionID+"/fork", []byte("{}"),
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", err
	}
	if !is2xx(code) {
		return "", fmt.Errorf("fork -> HTTP %s: %s", httpCode(code), trunc(stripNL(string(body)), 200))
	}
	id := jsonString(body, "id")
	if id == "" {
		id = jsonString(body, "session_id")
	}
	if id == "" { // {"session":{"id":...}}
		var top struct {
			Session map[string]json.RawMessage `json:"session"`
		}
		if json.Unmarshal(body, &top) == nil {
			if raw, ok := top.Session["id"]; ok {
				_ = json.Unmarshal(raw, &id)
			}
		}
	}
	if id == "" {
		return "", fmt.Errorf("fork ok but no id in response")
	}
	return id, nil
}

// parseCaps reads the feature map out of a /v1/capabilities body.
func parseCaps(body []byte) Caps {
	var top struct {
		Model    string                     `json:"model"`
		Features map[string]json.RawMessage `json:"features"`
		Session  map[string]json.RawMessage `json:"session"`
	}
	_ = json.Unmarshal(body, &top)
	feat := map[string]bool{}
	take := func(src map[string]json.RawMessage) {
		for k, raw := range src {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				feat[k] = b
			}
		}
	}
	take(top.Features)
	take(top.Session)
	return Caps{Model: top.Model, Features: feat}
}

func (c *Client) warnf(f string, a ...any) { logOr(c.Warnf, "hermes-hands: WARNING: "+f, a...) }
func (c *Client) logf(f string, a ...any)  { logOr(c.Log, "hermes-hands: "+f, a...) }

func (c *Client) vlogf(f string, a ...any) {
	if c.Vlogf != nil {
		c.Vlogf(f, a...)
	}
}

func (c *Client) nowFn() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) rndFn() int {
	if c.rnd != nil {
		return c.rnd()
	}
	return rand.Intn(32768)
}

func logOr(fn func(string, ...any), f string, a ...any) {
	if fn != nil {
		fn(f, a...)
		return
	}
	fmt.Fprintf(os.Stderr, f+"\n", a...)
}

func is2xx(code int) bool { return code >= 200 && code <= 299 }

func httpCode(code int) string {
	if code == 0 {
		return "000"
	}
	return fmt.Sprintf("%d", code)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sleep is a context-cancellable pause (bash just `sleep`s; the Go REPL cancels
// the turn context on Ctrl-C).
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- JSON helpers (thin, matching the `jq` expressions they replace) ---

// looksJSON mirrors `jq -e .`: valid JSON whose top value is not null / false.
func looksJSON(b []byte) bool {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return false
	}
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	default:
		return true
	}
}

// jsonString returns .key as a string, or "" (mirrors `jq -r '.key // ""'` for
// string-valued fields).
func jsonString(b []byte, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// checkModel ports `jq -r '.model // .runtime.mode // "hermes"'`.
func checkModel(b []byte) string {
	if s := jsonString(b, "model"); s != "" {
		return s
	}
	var m struct {
		Runtime struct {
			Mode string `json:"mode"`
		} `json:"runtime"`
	}
	if json.Unmarshal(b, &m) == nil && m.Runtime.Mode != "" {
		return m.Runtime.Mode
	}
	return "hermes"
}

// failDetail ports `jq -r '.output // .error // "no detail"'`.
func failDetail(b []byte) string {
	for _, key := range []string{"output", "error"} {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			break
		}
		raw, ok := m[key]
		if !ok || string(raw) == "null" {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return string(raw)
	}
	return "no detail"
}

// marshalJSON encodes without Go's default HTML escaping so `<`, `>`, `&` in
// the payload survive verbatim, matching jq, and drops the Encoder's trailing
// newline (command substitution `$(jq ...)` strips it).
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func stripNL(s string) string { return strings.ReplaceAll(s, "\n", "") }
