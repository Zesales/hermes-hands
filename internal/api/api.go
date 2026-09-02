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
