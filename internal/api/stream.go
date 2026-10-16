package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Zesales/hermes-hands/internal/guard"
)

// wantStream decides whether Ask reads the answer from GET
// /v1/runs/{id}/events (live token deltas) or polls the run status.
//
//	StreamMode "on"  -> always stream
//	StreamMode "off" -> always poll
//	""/"auto"        -> stream iff /v1/capabilities advertised run_events_sse
func (c *Client) wantStream() bool {
	switch c.StreamMode {
	case "on":
		return true
	case "off":
		return false
	default:
		return c.Caps.Has("run_events_sse")
	}
}

// sseEvent is one Hermes run event. On this gateway every SSE frame is a single
// `data:` line holding a JSON object whose "event" key is the type; e.g.
//
//	{"event":"message.delta","delta":"Hi"}
//	{"event":"tool.started","tool":"terminal","preview":"make test"}
//	{"event":"approval.request","request_id":"...","command":"..."}
//	{"event":"run.completed","output":"Hi.","usage":{"total_tokens":42},"session_id":"..."}
//	{"event":"run.failed","error":"..."}
//
// Frames the dispatcher has no case for (reasoning.available, tool.completed,
// message.interim, …) still reach OnEvent and the /debug trace.
type sseEvent struct {
	Event     string          `json:"event"`
	Delta     string          `json:"delta"`
	Text      string          `json:"text"`
	Output    string          `json:"output"`
	SessionID string          `json:"session_id"`
	Error     string          `json:"error"`
	Message   string          `json:"message"`
	Usage     json.RawMessage `json:"usage"`
	Tool      string          `json:"tool"`
	Preview   string          `json:"preview"`
	RequestID string          `json:"request_id"`
	Command   string          `json:"command"`
	Desc      string          `json:"description"`
}

// streamRun consumes the run's SSE stream: it forwards answer-text deltas to
// c.OnDelta as they arrive and returns the completed AskResult from the
// terminal run.completed event. Any stream-shape surprise (no completed event,
// HTTP error, decode failure) is returned as an error so Ask falls back to
// pollRun — the poll is always authoritative.
func (c *Client) streamRun(ctx context.Context, base, runID string, threaded bool) (AskResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/runs/"+runID+"/events", nil)
	if err != nil {
		return AskResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "text/event-stream")

	// A stream must not be cut by the client's overall per-request Timeout.
	hc := &http.Client{Transport: c.HTTP.Transport}
	resp, err := hc.Do(req)
	if err != nil {
		c.trace("http", map[string]any{"method": "GET", "path": "/v1/runs/" + runID + "/events", "err": err.Error()})
		return AskResult{}, err
	}
	defer resp.Body.Close()
	c.trace("http", map[string]any{"method": "GET", "path": "/v1/runs/" + runID + "/events", "status": resp.StatusCode, "stream": true})
	if !is2xx(resp.StatusCode) {
		return AskResult{}, fmt.Errorf("events -> HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var data strings.Builder
	var answer strings.Builder
	sawDelta := false
	steered := map[string]bool{} // the guard steers a run once per kind of mistake; repeats are only reported

	dispatch := func() (AskResult, bool, error) {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" || raw == "[DONE]" {
			return AskResult{}, false, nil
		}
		c.trace("sse", map[string]any{"run_id": runID, "data": raw})
		var e sseEvent
		if json.Unmarshal([]byte(raw), &e) != nil {
			return AskResult{}, false, nil // tolerate an unknown frame
		}
		if c.OnEvent != nil {
			c.OnEvent(e.Event)
		}
		switch e.Event {
		case "message.delta", "response.output_text.delta", "assistant.delta", "token":
			t := e.Delta
			if t == "" {
				t = e.Text
			}
			if t != "" {
				sawDelta = true
				answer.WriteString(t)
				if c.OnDelta != nil {
					c.OnDelta(t)
				}
			}
		case "tool.started":
			action := ""
			if guard.SandboxTools[e.Tool] {
				action = "server" // server-side work, unless the guard says otherwise
			}
			check := c.Guard
			if check == nil {
				check = guard.Strict
			}
			if v := check(e.Tool, e.Preview); v.Steer {
				action = "steered"
				if !steered[v.Kind] {
					steered[v.Kind] = true
					c.steer(ctx, base, runID, v.Message)
				}
			}
			if c.OnHermesTool != nil {
				c.OnHermesTool(e.Tool, e.Preview, action)
			}
		case "approval.request":
			// Only Hermes' own server-side tools ask for approval — the hands
			// never do. The operator decides (OnApproval); without one it is a
			// deny. Unanswered, the run would hang until the watchdog.
			what := firstNonEmptyStr(e.Command, e.Desc, e.Preview)
			choice := "deny"
			if c.OnApproval != nil {
				choice = c.OnApproval(e.Tool, what)
			}
			c.answerApproval(ctx, base, runID, e.RequestID, choice)
			action := "denied"
			if choice != "deny" {
				action = "approved"
			}
			if c.OnHermesTool != nil {
				c.OnHermesTool(firstNonEmptyStr(e.Tool, "approval"), what, action)
			}
		case "run.completed", "response.completed", "done":
			out := e.Output
			if out == "" {
				out = answer.String()
			}
			if strings.TrimSpace(out) == "" {
				return AskResult{}, true, fmt.Errorf("stream completed with empty output")
			}
			return AskResult{
				State: "completed", Text: out, RunID: runID,
				SessionID: e.SessionID, Threaded: threaded,
				Tokens: usageTokensRaw(e.Usage),
			}, true, nil
		case "run.failed", "run.cancelled", "error":
			m := firstNonEmptyStr(e.Error, e.Message, "run failed")
			return AskResult{}, true, fmt.Errorf("run %s: %s", runID, m)
		}
		return AskResult{}, false, nil
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // frame boundary
			res, done, derr := dispatch()
			if derr != nil {
				return AskResult{}, derr
			}
			if done {
				return res, nil
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, ":"):
			// SSE comment / keep-alive
		}
		if ctx.Err() != nil {
			return AskResult{}, ctx.Err()
		}
	}
	if err := sc.Err(); err != nil {
		return AskResult{}, err
	}
	// stream ended without a terminal event
	if sawDelta {
		return AskResult{}, fmt.Errorf("stream ended before run.completed")
	}
	return AskResult{}, fmt.Errorf("no usable stream events")
}

// steer redirects a running run (POST /v1/runs/{id}/steer). Best-effort: the
// guard's display line still tells the operator when the server refuses.
func (c *Client) steer(ctx context.Context, base, runID, text string) {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, _ := marshalJSON(map[string]string{"input": text})
	code, _, err := c.do(tctx, http.MethodPost, base+"/v1/runs/"+runID+"/steer", body,
		map[string]string{"Content-Type": "application/json"})
	c.trace("guard.steer", map[string]any{"run_id": runID, "status": code, "text": text})
	if err != nil || !is2xx(code) {
		c.warnf("sandbox guard: steer refused (%s, HTTP %s)", errText(err), httpCode(code))
	}
}

// answerApproval answers a pending approval request: once | session | deny.
func (c *Client) answerApproval(ctx context.Context, base, runID, requestID, choice string) {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req := map[string]string{"choice": choice}
	if requestID != "" {
		req["request_id"] = requestID
	}
	body, _ := marshalJSON(req)
	code, _, err := c.do(tctx, http.MethodPost, base+"/v1/runs/"+runID+"/approval", body,
		map[string]string{"Content-Type": "application/json"})
	c.trace("guard.approval", map[string]any{"run_id": runID, "request_id": requestID, "choice": choice, "status": code})
	if err != nil || !is2xx(code) {
		c.warnf("approval answer %q refused (%s, HTTP %s)", choice, errText(err), httpCode(code))
	}
}
