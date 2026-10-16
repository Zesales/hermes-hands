package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"hermes-hands/internal/config"
)

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
	if c.Trace != nil {
		start := time.Now()
		defer func() {
			f := map[string]any{"method": method, "path": strings.TrimPrefix(url, c.titleBase()),
				"status": code, "ms": time.Since(start).Milliseconds(), "bytes": len(respBody)}
			if err != nil {
				f["err"] = err.Error()
			}
			if code != 0 && !is2xx(code) {
				f["body"] = trunc(string(respBody), 2000)
			}
			c.Trace("http", f)
		}()
	}
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

func (c *Client) trace(kind string, fields map[string]any) {
	if c.Trace != nil {
		c.Trace(kind, fields)
	}
}

func (c *Client) pause() time.Duration {
	if c.retryPause > 0 {
		return c.retryPause
	}
	return retrySleep
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
