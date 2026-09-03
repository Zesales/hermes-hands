package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
//	{"event":"reasoning.available","text":"..."}
//	{"event":"run.completed","output":"Hi.","usage":{"total_tokens":42},"session_id":"..."}
//	{"event":"run.failed","error":"..."}
type sseEvent struct {
	Event     string          `json:"event"`
	Delta     string          `json:"delta"`
	Text      string          `json:"text"`
	Output    string          `json:"output"`
	SessionID string          `json:"session_id"`
	Error     string          `json:"error"`
	Message   string          `json:"message"`
	Usage     json.RawMessage `json:"usage"`
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
		return AskResult{}, err
	}
	defer resp.Body.Close()
	if !is2xx(resp.StatusCode) {
		return AskResult{}, fmt.Errorf("events -> HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var data strings.Builder
	var answer strings.Builder
	sawDelta := false

	dispatch := func() (AskResult, bool, error) {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" || raw == "[DONE]" {
			return AskResult{}, false, nil
		}
		var e sseEvent
		if json.Unmarshal([]byte(raw), &e) != nil {
			return AskResult{}, false, nil // tolerate an unknown frame
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
