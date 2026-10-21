package loop

import (
	"encoding/json"
	"strings"
)

// callSpec is one call of an envelope, with the field aliases models use.
type callSpec struct {
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"` // OpenAI-ish alias; tool_call_id handled separately
	Tool      string          `json:"tool"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c callSpec) id() string { return firstNonEmpty(c.ID, c.CallID) }

// envelope is a reply read as {"say":…,"calls":[…],"final":…}; parsed is
// false when it holds no valid JSON object. say is Hermes' one sentence to
// the operator beside its calls; stopOnError ("stop_on_error": true) stops
// the calls after the first that fails.
type envelope struct {
	say         string
	calls       []callSpec
	final       string
	stopOnError bool
	parsed      bool
}

// parseEnvelope reads reply as an envelope (extractObj finds the object).
func parseEnvelope(reply string) envelope {
	obj, ok := extractObj(reply)
	if !ok || !json.Valid([]byte(obj)) {
		return envelope{}
	}
	env := envelope{parsed: true}
	var top map[string]json.RawMessage
	_ = json.Unmarshal([]byte(obj), &top)
	if raw, ok := top["calls"]; ok && !isJSONNull(raw) {
		_ = json.Unmarshal(raw, &env.calls)
	}
	if raw, ok := top["final"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			env.final = s
		}
	}
	if raw, ok := top["say"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			env.say = strings.TrimSpace(s)
		}
	}
	if raw, ok := top["stop_on_error"]; ok {
		_ = json.Unmarshal(raw, &env.stopOnError)
	}
	return env
}

// extractObj ports _hh_extract_obj: strip ```json / ``` fences, then either the
// whole thing (if it is a JSON object) or the first '{' .. last '}' slice of
// the ORIGINAL reply.
func extractObj(reply string) (string, bool) {
	s := reply
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")

	trimmed := strings.TrimSpace(s)
	if json.Valid([]byte(trimmed)) {
		var v any
		if json.Unmarshal([]byte(trimmed), &v) == nil {
			if _, isObj := v.(map[string]any); isObj {
				return s, true
			}
		}
	}

	start := strings.IndexByte(reply, '{')
	if start < 0 {
		return "", false
	}
	end := strings.LastIndexByte(reply, '}')
	if end <= start {
		return "", false
	}
	return reply[start : end+1], true
}

// callArgs ports `.args // .arguments // {}` plus the one-shot unwrap when the
// value arrived as a JSON string.
func callArgs(c callSpec) json.RawMessage {
	raw := c.Args
	if len(raw) == 0 || isJSONNull(raw) {
		raw = c.Arguments
	}
	if len(raw) == 0 || isJSONNull(raw) {
		return json.RawMessage("{}")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Valid([]byte(s)) {
			return compactJSON(json.RawMessage(s))
		}
		return json.RawMessage("{}")
	}
	return compactJSON(raw)
}
