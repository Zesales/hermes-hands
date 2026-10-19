package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"hermes-hands/internal/excerpt"
)

// elidedBatch: every result of the batch is elided (or never ran).
func elidedBatch(calls []Event, results map[int]Event, upto int) bool {
	for _, c := range calls {
		if r, ran := results[c.Ref]; ran && r.Seq > upto {
			return false
		}
	}
	return true
}

// elidedResults renders an elided batch as one line per call — the #ref, the
// tool and what it ran, how it ended, its size and the gist of a command's
// output (excerpt.Gist) — instead of the results JSON: once its outputs are
// stubs, the JSON around them is what fills the context. A short output
// costs less than its line and stays. Hermes' own replies stay JSON, so the
// envelope it must answer in is never shown in another form.
func elidedResults(calls []Event, results map[int]Event, o TranscriptOpts) string {
	var b strings.Builder
	b.WriteString(`[hands results — elided by hermes-hands; local_output {"ref":N} reads any of them back]`)
	for _, c := range calls {
		fmt.Fprintf(&b, "\n#%d %s %s → ", c.Ref, c.Tool, callHead(c))
		r, ran := results[c.Ref]
		if !ran {
			b.WriteString("not run — the turn stopped before this call")
			continue
		}
		exit := intOr(r.Exit, 0)
		out := ""
		if o.Output != nil {
			out = o.Output(c.Ref)
		}
		switch {
		case r.Bytes < ElideMin:
			fmt.Fprintf(&b, "exit %d", exit)
			if s := clip(out, ElideMin); s != "" {
				b.WriteString(": " + s)
			}
		default:
			fmt.Fprintf(&b, "exit %d · %s · %s", exit, lines(r.Lines), size(r.Bytes))
			if g := gist(c.Tool, out, exit); g != "" {
				b.WriteString(" · " + g)
			}
		}
	}
	return b.String()
}

// callHead is what a call ran, short: a command, a path, a #ref.
func callHead(c Event) string {
	var args map[string]any
	_ = json.Unmarshal(c.Args, &args)
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch c.Tool {
	case "local_shell":
		return "`" + clip(str("cmd"), 160) + "`"
	case "local_read", "local_write", "local_edit":
		return str("path")
	case "local_output":
		return fmt.Sprintf("#%v", args["ref"])
	}
	return clip(string(c.Args), 160)
}

// gist is the gist of an elided output: a command's last line, its error
// line when it failed; nothing for a file read or written.
func gist(tool, out string, exit int) string {
	switch tool {
	case "local_read", "local_write", "local_edit":
		if exit == 0 {
			return ""
		}
	}
	return excerpt.Gist(out, exit, gistMax)
}

// lines is "1 line" / "312 lines".
func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// size is "18 KB" / "950 B".
func size(n int) string {
	if n >= 1024 {
		return fmt.Sprintf("%d KB", (n+512)/1024)
	}
	return fmt.Sprintf("%d B", n)
}

// envelope rebuilds the reply that asked for calls.
func envelope(calls []Event, elided bool) string {
	type call struct {
		ID   string          `json:"id"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	cs := make([]call, 0, len(calls))
	for _, c := range calls {
		args := c.Args
		if elided {
			args = elideArgs(args)
		}
		cs = append(cs, call{ID: c.ID, Tool: c.Tool, Args: args})
	}
	b, _ := marshalNoEscape(map[string]any{"calls": cs, "final": nil})
	return string(b)
}

// elideReply shortens an old reply: its prose clipped, the envelope's long
// string arguments replaced by their size. The final stays whole — it is what
// the operator was told.
func elideReply(text string) string {
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return clipKeep(text, elideProse)
	}
	prose := strings.TrimSpace(text[:start])
	var env map[string]json.RawMessage
	if json.Unmarshal([]byte(text[start:end+1]), &env) != nil {
		return clipKeep(text, elideProse)
	}
	if raw, ok := env["calls"]; ok {
		var calls []map[string]json.RawMessage
		if json.Unmarshal(raw, &calls) == nil {
			for _, c := range calls {
				for _, k := range []string{"args", "arguments"} {
					if a, ok := c[k]; ok {
						c[k] = elideArgs(a)
					}
				}
			}
			if b, err := marshalNoEscape(calls); err == nil {
				env["calls"] = b
			}
		}
	}
	b, err := marshalNoEscape(env)
	if err != nil {
		return clipKeep(text, elideProse)
	}
	if prose == "" {
		return string(b)
	}
	return clipKeep(prose, elideProse) + "\n" + string(b)
}

// elideArgs replaces every string argument longer than elideArg by its size.
func elideArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return raw
	}
	changed := false
	for k, v := range args {
		if s, ok := v.(string); ok && len(s) > elideArg {
			args[k] = fmt.Sprintf("[%d bytes — elided by hermes-hands]", len(s))
			changed = true
		}
	}
	if !changed {
		return raw
	}
	b, err := marshalNoEscape(args)
	if err != nil {
		return raw
	}
	return b
}

// toolCounts is "local_shell ×4, local_read ×2".
func toolCounts(m map[string]int) string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s ×%d", n, m[n]))
	}
	return strings.Join(parts, ", ")
}

// clipKeep cuts s to n bytes (on a rune boundary), keeping its line breaks.
func clipKeep(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:runeStart(s, n)] + " […]"
}
