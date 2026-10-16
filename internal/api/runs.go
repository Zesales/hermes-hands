package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Runs is the Runs API of a Client (POST /v1/runs, its events or a poll of
// GET /v1/runs/{id}, stop, state). The turn hooks on Client follow a run.
type Runs struct{ c *Client }

// AskResult is a completed turn.
type AskResult struct {
	State     string // "completed"
	Text      string // the assistant's final output
	RunID     string
	SessionID string // .session_id from run status ("" if none)
	Threaded  bool   // false after a session-id drop
	Tokens    int    // best-effort token count from the run's .usage (0 if absent)
}

// runReq is the POST /v1/runs body, in the same key order jq builds it:
// input, then instructions (only when non-empty), then session_id (only when
// present and not dropped).
type runReq struct {
	Input        string `json:"input"`
	Instructions string `json:"instructions,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

// Ask runs one turn: submit a run, ride the POST retry / one-shot
// session-id-drop ladder, then poll to a terminal status. sess / skey are the
// record's hermes_session_id / hermes_session_key ("" when none). On success
// the caller adopts AskResult.SessionID + bumps the local index.
func (r Runs) Ask(ctx context.Context, msg, sess, skey string) (AskResult, error) {
	c := r.c
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
	c.trace("run.start", map[string]any{"run_id": runID, "session_id": sess, "threaded": threaded,
		"input": msg, "instructions_bytes": len(c.Instructions)})

	res, err := c.awaitRun(ctx, base, runID, threaded)
	f := map[string]any{"run_id": runID, "tokens": res.Tokens, "output": res.Text}
	if err != nil {
		f["err"] = err.Error()
	}
	c.trace("run.end", f)
	return res, err
}

// awaitRun waits for a submitted run: the SSE stream when wanted, else (or
// when the stream is unusable) the authoritative poll.
func (c *Client) awaitRun(ctx context.Context, base, runID string, threaded bool) (AskResult, error) {
	if c.wantStream() {
		if res, serr := c.streamRun(ctx, base, runID, threaded); serr == nil {
			return res, nil
		} else if ctx.Err() != nil {
			return AskResult{}, ctx.Err()
		} else {
			c.vlogf("run %s: SSE stream unusable (%v) - falling back to poll", runID, serr)
		}
	}
	return c.pollRun(ctx, base, runID, threaded)
}

// pollRun rides GET /v1/runs/{id} to a terminal status (the pre-SSE path, and
// the fallback when the event stream is unavailable).
func (c *Client) pollRun(ctx context.Context, base, runID string, threaded bool) (AskResult, error) {
	c.vlogf("run %s - polling", runID)
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
		} else if ctx.Err() != nil {
			return AskResult{}, ctx.Err()
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

// State returns a run's current status string from GET /v1/runs/{id} ("" if
// the field is absent). The REPL's silence watchdog uses it as an out-of-band
// "is hermes-agent still working?" probe between token deltas. A transport or
// non-2xx error is returned as-is so the caller can treat "couldn't ask" as
// "not confirmed alive" rather than assuming the run is fine.
func (r Runs) State(ctx context.Context, runID string) (string, error) {
	c := r.c
	if runID == "" {
		return "", fmt.Errorf("no run id")
	}
	if c.BaseURL == "" || c.Key == "" {
		return "", fmt.Errorf("client not configured")
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	code, body, err := c.do(tctx, http.MethodGet, c.base()+"/v1/runs/"+runID,
		nil, map[string]string{"Accept": "application/json"})
	if err != nil {
		return "", err
	}
	if !is2xx(code) {
		return "", fmt.Errorf("GET /v1/runs/%s -> HTTP %s", runID, httpCode(code))
	}
	return jsonStringOr(body, "status", ""), nil
}

// Stop asks Hermes to interrupt a running turn (POST /v1/runs/{id}/stop).
// Best-effort: fire it on Ctrl-C so the server turn doesn't keep burning
// tokens after the operator has moved on. Errors are swallowed.
func (r Runs) Stop(ctx context.Context, runID string) {
	c := r.c
	if runID == "" || c.BaseURL == "" || c.Key == "" {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, _ = c.do(tctx, http.MethodPost, c.base()+"/v1/runs/"+runID+"/stop", nil, nil)
}

// RawRun submits one run with no retry ladder and returns its ids — for the
// hidden `_events` inspector only.
func (r Runs) RawRun(ctx context.Context, input string) (runID, sessionID string, err error) {
	c := r.c
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

// RawRunBody POSTs a caller-supplied /v1/runs body verbatim — for probing run
// parameters. Returns the run + session ids.
func (r Runs) RawRunBody(ctx context.Context, body []byte) (runID, sessionID string, err error) {
	c := r.c
	if err := c.preflight(); err != nil {
		return "", "", err
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, resp, err := c.do(tctx, http.MethodPost, c.base()+"/v1/runs", body,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", "", err
	}
	if !is2xx(code) {
		return "", "", fmt.Errorf("POST /v1/runs -> HTTP %s: %s", httpCode(code), trunc(stripNL(string(resp)), 400))
	}
	return jsonString(resp, "run_id"), jsonString(resp, "session_id"), nil
}

// RawEvents copies the /v1/runs/{id}/events SSE stream to w verbatim, for
// inspecting the wire shape. Stops on stream end, ctx, or ~256KB.
func (r Runs) RawEvents(ctx context.Context, runID string, w io.Writer) error {
	c := r.c
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
