package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Sessions is the session endpoints of a Client (/api/sessions).
type Sessions struct{ c *Client }

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

// List reads the server-side session list (GET /api/sessions). Lenient:
// it accepts a bare array or an object with a "sessions"/"data"/"items" array.
func (s Sessions) List(ctx context.Context) ([]SrvSession, error) {
	c := s.c
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

// Info reads a server-side session record (GET /api/sessions/{id}).
// The response schema is not published, so parsing is lenient; a non-2xx or an
// unparseable body returns an error and the caller falls back to the local
// index.
func (s Sessions) Info(ctx context.Context, hermesSessionID string) (SrvSession, error) {
	c := s.c
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

// SetTitle ports hh_api_set_title: a best-effort PATCH, all output and errors
// swallowed. No-op unless URL, key and id are all present.
func (s Sessions) SetTitle(ctx context.Context, hermesSessionID, title string) {
	c := s.c
	if c.BaseURL == "" || c.Key == "" || hermesSessionID == "" {
		return
	}
	body, err := marshalJSON(map[string]string{"title": title})
	if err != nil {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// hermes-agent wants titles unique: two sessions opened with the same
	// first message would leave the second untitled — retry with "(2)", "(3)".
	for n := 1; n <= 3; n++ {
		t := title
		if n > 1 {
			t = fmt.Sprintf("%s (%d)", title, n)
		}
		body, _ = marshalJSON(map[string]string{"title": t})
		code, resp, err := c.do(tctx, http.MethodPatch, c.titleBase()+"/api/sessions/"+hermesSessionID,
			body, map[string]string{"Content-Type": "application/json"})
		if err != nil || code != 400 || !strings.Contains(string(resp), "already in use") {
			return
		}
	}
}

// Fork branches the session (POST /api/sessions/{id}/fork) and returns the new
// session id.
func (s Sessions) Fork(ctx context.Context, hermesSessionID string) (string, error) {
	c := s.c
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
