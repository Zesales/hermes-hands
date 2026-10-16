package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Server is the gateway itself, as a Client sees it: the preflight, its
// version, its capabilities, a raw read for the dev commands.
type Server struct{ c *Client }

// CheckResult is the outcome of a successful preflight.
// Version is hermes-agent's own version from GET /health ("" when the
// gateway doesn't say) — what a bug report or a "works with" note names.
type CheckResult struct{ Model, Base, Version string }

// Check ports hh_api_check: GET /v1/capabilities with a fixed-2s retry ladder.
func (s Server) Check(ctx context.Context) (CheckResult, error) {
	c := s.c
	if err := c.preflight(); err != nil {
		return CheckResult{}, err
	}
	base := c.base()
	for attempt := 1; ; attempt++ {
		code, body, err := c.do(ctx, http.MethodGet, base+"/v1/capabilities",
			nil, map[string]string{"Accept": "application/json"})
		if err == nil && is2xx(code) && looksJSON(body) {
			c.Caps = parseCaps(body)
			return CheckResult{Model: checkModel(body), Base: base, Version: c.serverVersion(ctx)}, nil
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

// serverVersion reads hermes-agent's version from its health endpoint
// (GET /health -> {"version": "0.22.0"}), best-effort: "" on any failure.
func (c *Client) serverVersion(ctx context.Context) string {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodGet, c.titleBase()+"/health",
		nil, map[string]string{"Accept": "application/json"})
	if err != nil || !is2xx(code) {
		return ""
	}
	return jsonString(body, "version")
}

// RawGet does one authenticated GET of an API path (joined onto base) and
// returns the body — for the hidden `_raw` schema inspector.
func (s Server) RawGet(ctx context.Context, path string) (string, error) {
	c := s.c
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
