package api

import (
	"encoding/json"
	"strings"
)

// usageTokens pulls a best-effort token count out of a run status body's
// `usage` object. Hermes does not yet expose real context-window usage
// (NousResearch/hermes-agent#15618); this is the cumulative billing count:
// total_tokens if present, else input+output / prompt+completion.
func usageTokens(body []byte) int {
	num := usageNum(body)
	if num == nil {
		return 0
	}
	if t := num("total_tokens", "total", "context_tokens"); t > 0 {
		return t
	}
	return num("input_tokens", "prompt_tokens") + num("output_tokens", "completion_tokens")
}

// usagePrompt is the run's prompt tokens (input_tokens / prompt_tokens) —
// see AskResult.Prompt. 0 when absent.
func usagePrompt(body []byte) int {
	if num := usageNum(body); num != nil {
		return num("input_tokens", "prompt_tokens")
	}
	return 0
}

// usageNum reads numbers out of a body's `usage` object: the first of keys
// present, 0 when none is. nil when the body has no usage.
func usageNum(body []byte) func(keys ...string) int {
	var top struct {
		Usage map[string]json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &top) != nil || top.Usage == nil {
		return nil
	}
	return func(keys ...string) int {
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
}

// usageTokensRaw is usageTokens for a bare `usage` object (from an SSE frame).
func usageTokensRaw(usage json.RawMessage) int {
	if len(usage) == 0 {
		return 0
	}
	return usageTokens([]byte(`{"usage":` + string(usage) + `}`))
}

// usagePromptRaw is usagePrompt for a bare `usage` object.
func usagePromptRaw(usage json.RawMessage) int {
	if len(usage) == 0 {
		return 0
	}
	return usagePrompt([]byte(`{"usage":` + string(usage) + `}`))
}

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

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func stripNL(s string) string { return strings.ReplaceAll(s, "\n", "") }
