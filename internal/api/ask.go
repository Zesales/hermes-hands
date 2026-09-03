package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// runReq is the POST /v1/runs body, in the same key order jq builds it:
// input, then instructions (only when non-empty), then session_id (only when
// present and not dropped).
type runReq struct {
	Input        string `json:"input"`
	Instructions string `json:"instructions,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// Ask ports hh_api_ask: submit a run, ride the POST retry / one-shot
// session-id-drop ladder, then poll to a terminal status. sess / skey are the
// record's hermes_session_id / hermes_session_key ("" when none). On success
// the caller adopts AskResult.SessionID + bumps the local index.
func (c *Client) Ask(ctx context.Context, msg, sess, skey string) (AskResult, error) {
	if err := c.preflight(); err != nil {
		return AskResult{}, err
	}
	base := c.base()

	// Idempotency-Key: minted once, reused across POST retries.
	ikey := fmt.Sprintf("hh-%d-%d%d", c.nowFn().Unix(), c.rndFn(), c.rndFn())

	var runID string
	dropSess := false
	for attempt := 1; ; {
		sid := ""
		if sess != "" && !dropSess {
			sid = sess
		}
		body, err := marshalJSON(runReq{Input: msg, Instructions: c.Instructions, SessionID: sid})
		if err != nil {
			return AskResult{}, err
		}
		hdr := map[string]string{
			"Content-Type":    "application/json",
			"Idempotency-Key": ikey,
		}
		if sess != "" {
			hdr["X-Hermes-Session-Id"] = sess
		}
		if skey != "" {
			hdr["X-Hermes-Session-Key"] = skey
		}

		code, respBody, err := c.do(ctx, http.MethodPost, base+"/v1/runs", body, hdr)
		if err == nil && is2xx(code) {
			if runID = jsonString(respBody, "run_id"); runID != "" {
				if c.OnRunStart != nil {
					c.OnRunStart(runID)
				}
				break
			}
			return AskResult{}, fmt.Errorf("POST /v1/runs 2xx but no run_id: %s",
				trunc(stripNL(string(respBody)), 200))
		}
		switch code {
		case 401, 403:
			return AskResult{}, fmt.Errorf("HTTP %d at /v1/runs - HERMES_API_KEY rejected.", code)
		case 400, 404, 422:
			if sess != "" && !dropSess {
				c.warnf("server rejected session_id (HTTP %d) - retrying without it, local recap on", code)
				dropSess = true
				continue // no attempt increment, no pause
			}
			return AskResult{}, fmt.Errorf("POST /v1/runs -> HTTP %d: %s", code,
				trunc(stripNL(string(respBody)), 200))
		}
		if attempt > c.Retries {
			return AskResult{}, fmt.Errorf("POST /v1/runs unreachable - %s, HTTP %s",
				errText(err), httpCode(code))
		}
		attempt++
		if serr := sleep(ctx, c.pause()); serr != nil {
			return AskResult{}, serr
		}
	}

	threaded := !dropSess
	sd := sess
	if sd == "" {
		sd = "none"
	}
	if dropSess {
		sd += " DROPPED"
	}
	c.vlogf("run %s (session=%s) - polling", runID, sd)

	waited := time.Duration(0)
	status := ""
	for {
		code, respBody, err := c.do(ctx, http.MethodGet, base+"/v1/runs/"+runID,
			nil, map[string]string{"Accept": "application/json"})
		if err == nil && is2xx(code) && looksJSON(respBody) {
			status = jsonStringOr(respBody, "status", "?")
			switch status {
			case "completed":
				out := jsonString(respBody, "output")
				if strings.ReplaceAll(out, " ", "") == "" {
					return AskResult{}, fmt.Errorf("run %s completed but empty output", runID)
				}
				return AskResult{
					State:     "completed",
					Text:      out,
					RunID:     runID,
					SessionID: jsonString(respBody, "session_id"),
					Threaded:  threaded,
					Tokens:    usageTokens(respBody),
				}, nil
			case "failed", "cancelled":
				return AskResult{}, fmt.Errorf("run %s %s: %s", runID, status,
					trunc(stripNL(failDetail(respBody)), 400))
			case "started", "running", "queued", "stopping", "":
				// non-terminal: keep polling
			default:
				c.logf("unknown run status '%s' - still polling", status)
			}
		} else {
			c.logf("poll: HTTP %s %s - retrying", httpCode(code), errText(err))
		}
		waited += c.PollInterval
		if waited > c.RunTimeout {
			return AskResult{}, fmt.Errorf("run %s still '%s' after %ds",
				runID, emptyToQ(status), int(c.RunTimeout/time.Second))
		}
		if serr := sleep(ctx, c.PollInterval); serr != nil {
			return AskResult{}, serr
		}
	}
}

func (c *Client) pause() time.Duration {
	if c.retryPause > 0 {
		return c.retryPause
	}
	return retrySleep
}

// jsonStringOr ports `jq -r '.key // "def"'`: .key as a string unless it is
// missing / null / false.
func jsonStringOr(b []byte, key, def string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return def
	}
	raw, ok := m[key]
	if !ok || string(raw) == "null" || string(raw) == "false" {
		return def
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return def
}

func emptyToQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
