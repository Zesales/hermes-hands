package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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
}

// sandboxTools are Hermes' OWN tools that act on the API server's sandbox — a
// shell and filesystem that are NOT the operator's machine. hermes-hands exists
// so the brain works through the hands; a run that reaches for one of these is
// steered back (POST /v1/runs/{id}/steer). Thinking-side tools (web_search,
// memory, session_search, skills, …) stay allowed.
var sandboxTools = map[string]bool{
	"terminal": true, "process_manage": true, "process": true, // `process` = pre-v0.22 name, still accepted
	"execute_code": true, "read_file": true, "write_file": true, "patch": true, "search_files": true,
}

const steerSandbox = "[hermes-hands] `%s` just ran in YOUR server sandbox — not on the operator's machine, so its result says nothing about the operator's files. Do not use your own terminal / process_manage / execute_code / read_file / write_file / patch / search_files for this work: everything runs on the operator's machine through the hands. Reply with a hands envelope {\"calls\":[...],\"final\":null} instead."

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
	steered := false // the sandbox guard steers a run once; later attempts are only reported

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
			if sandboxTools[e.Tool] {
				action = "steered"
				if !steered {
					steered = true
					c.steer(ctx, base, runID, fmt.Sprintf(steerSandbox, e.Tool))
				}
			}
			if c.OnHermesTool != nil {
				c.OnHermesTool(e.Tool, e.Preview, action)
			}
		case "approval.request":
			// Only Hermes' own (sandbox) tools ask for approval — the hands
			// never do. Unanswered, the run would hang until the watchdog.
			c.denyApproval(ctx, base, runID, e.RequestID)
			if c.OnHermesTool != nil {
				c.OnHermesTool(firstNonEmptyStr(e.Tool, "approval"), firstNonEmptyStr(e.Command, e.Preview), "denied")
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

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// usageTokensRaw is usageTokens for a bare `usage` object (from an SSE frame).
func usageTokensRaw(usage json.RawMessage) int {
	if len(usage) == 0 {
		return 0
	}
	return usageTokens([]byte(`{"usage":` + string(usage) + `}`))
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

// denyApproval answers a pending approval request with "deny".
func (c *Client) denyApproval(ctx context.Context, base, runID, requestID string) {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req := map[string]string{"choice": "deny"}
	if requestID != "" {
		req["request_id"] = requestID
	}
	body, _ := marshalJSON(req)
	code, _, err := c.do(tctx, http.MethodPost, base+"/v1/runs/"+runID+"/approval", body,
		map[string]string{"Content-Type": "application/json"})
	c.trace("guard.deny", map[string]any{"run_id": runID, "request_id": requestID, "status": code})
	if err != nil || !is2xx(code) {
		c.warnf("sandbox guard: approval deny refused (%s, HTTP %s)", errText(err), httpCode(code))
	}
}
