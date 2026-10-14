// Package loop drives one user turn = N Hermes rounds. Send the message; if
// Hermes replies with a directive envelope run the calls locally, feed a
// {results:[...]} object back, repeat; stop on {final:"..."} or plain prose.
// Envelope recovery handles fenced / prosey / botched JSON.
package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Zesales/hermes-hands/internal/api"
	"github.com/Zesales/hermes-hands/internal/dispatch"
	"github.com/Zesales/hermes-hands/internal/session"
)

// Asker is the slice of the Hermes client the loop needs; *api.Client
// satisfies it. An interface here keeps the round/envelope logic unit-testable
// without a live server.
type Asker interface {
	Ask(ctx context.Context, msg, sess, skey string) (api.AskResult, error)
}

// Loop drives the delegation rounds for one turn.
type Loop struct {
	API      Asker
	Dispatch *dispatch.Dispatcher

	// OnResult sees each dispatched tool result (tool, raw output, exit code).
	// The knowsystem learner uses it to catch 127 "command not found" facts.
	OnResult  func(tool, output string, exit int)
	MaxRounds int
	Scrub     func(string) string

	// Emit, when set, receives every hands call as two events: KindCall right
	// BEFORE it runs (so the operator sees what is running, not only what
	// ran) and KindResult after it, carrying the scrubbed output in Event.Out.
	// The caller stores and renders them.
	Emit func(ev session.Event)

	// Ref is the last session-wide call number handed out; every call takes
	// the next one (#Ref). The caller seeds it from the session's event log.
	Ref int

	// RepoRoot / GitBranch feed the per-turn frame that reminds Hermes it is
	// driving a remote terminal and names the cwd. Both optional; an empty
	// RepoRoot disables framing (tests).
	RepoRoot  string
	GitBranch func() string

	// PendingNote carries a one-off line to prepend to the next turn's message
	// (e.g. "the previous turn was cancelled"). Set by Run, consumed by Run.
	PendingNote string

	// ResumeNote carries a one-off block to prepend to the FIRST turn of a
	// resumed session whose server-side history was lost (the local replay,
	// see session.Recap). Set by the caller, consumed by Run.
	ResumeNote string

	Vlogf func(string, ...any)
	Warnf func(string, ...any)
}

// frame presents the operator's message to Hermes as a *problem* to solve —
// not a chat turn — with the working directory named and the response contract
// (reasoning, then one envelope) restated right after it. The behavioural rules
// live in the cached instructions field; this stays a thin task wrapper.
// Without a RepoRoot it is a passthrough (tests).
// locNote is " at <cwd>" for the results reminder ("" when RepoRoot is unset).
func (l *Loop) locNote() string {
	if l.RepoRoot == "" {
		return ""
	}
	return " at " + l.RepoRoot
}

func (l *Loop) frame(userMsg string) string {
	if l.RepoRoot == "" {
		return userMsg
	}
	loc := "cwd " + l.RepoRoot
	if l.GitBranch != nil {
		if b := l.GitBranch(); b != "" {
			loc += " (git branch: " + b + ")"
		}
	}
	return "[hermes-hands — you are the brain; hermes-hands is your hands at the\n" +
		"operator's Linux terminal, a persistent bash shell in the working directory.]\n\n" +
		"PROBLEM — operator's working directory: " + loc + "\n\n" +
		userMsg + "\n\n" +
		"Your reply: reason it through, then ONE instruction and nothing else —\n" +
		"{\"calls\":[{\"id\":\"c1\",\"tool\":\"shell|read_file|write_file|edit_file\",\"args\":{...}}],\"final\":null}\n" +
		"(give each call a short id; the hands echo it in results so you can map\n" +
		"output to call) — or {\"calls\":[],\"final\":\"...\"} once it is solved. Not a\n" +
		"chat reply, not a description of yourself or your tools. If you do not know\n" +
		"the directory yet, your instruction is a look — `shell` (ls / git / rg) or\n" +
		"`read_file`." + l.budgetNote()
}

// budgetNote is the round-budget line of the task frame: the model plans
// against a known ceiling instead of discovering it as a BLOCKED wall. One
// sentence — it must not crowd out the task it wraps.
func (l *Loop) budgetNote() string {
	if l.MaxRounds <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\nThis turn has a tool-round budget of %d envelopes with calls; batch several calls per envelope and plan before large writes — a final that says what is unfinished beats being stopped at the limit.", l.MaxRounds)
}

// Outcome is the turn result. OK == false means Answer is a "BLOCKED: ..."
// string and the process should exit 1.
type Outcome struct {
	Answer string
	OK     bool
}

const (
	fixupMsg = `{"error":"your previous message was not a single valid JSON object of the form {\"calls\":[...],\"final\":null}. Resend ONLY that object, no prose, no code fences."}`
	emptyMsg = `{"error":"empty envelope. Either put tool calls in .calls or your answer in .final."}`

	// maxFixups bounds the botched-envelope / empty-envelope recovery nudges
	// per turn. Seen against a real gateway: a model can spend its first
	// couple of attempts on genuinely malformed JSON (missing a closing
	// bracket, a stray trailing tag) while a large write_file call's content
	// grows, then land a good one on the next try - now that questionNote
	// keeps the task in view across nudges (see below), an extra attempt is
	// usually productive rather than wasted.
	maxFixups = 3
)

var botchedRe = regexp.MustCompile(`(?i)"(calls|tool)"[[:space:]]*:`)

type callSpec struct {
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"` // OpenAI-ish alias; tool_call_id handled separately
	Tool      string          `json:"tool"`
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c callSpec) id() string { return firstNonEmpty(c.ID, c.CallID) }

// Run executes the turn. persist mirrors _hh_session_write: it is called after
// every completed api.Ask with (runID, serverSessionID) so the caller can bump
// the local index and adopt a re-issued session id (mutating rec, which Run
// re-reads next round).
func (l *Loop) Run(ctx context.Context, userMsg string, rec *session.Record, persist func(runID, serverSID string, tokens int)) Outcome {
	round := 1
	send := l.frame(userMsg)
	if l.ResumeNote != "" { // resumed session, server history gone — replay local copy
		send = l.ResumeNote + "\n\n" + send
		l.ResumeNote = ""
	}
	if l.PendingNote != "" { // a prior turn was cancelled — tell Hermes now
		send = l.PendingNote + "\n\n" + send
		l.PendingNote = ""
	}
	turnlog := "[operator] " + userMsg + "\n"
	recap := false
	fixups := 0

	for {
		l.vlogf("round %d -> Hermes", round)

		msg := send
		if recap {
			msg = "[conversation so far this turn]\n" + turnlog + "\n[latest tool results]\n" + send
		}

		res, err := l.API.Ask(ctx, msg, rec.HermesSessionID, rec.HermesSessionKey)
		if err != nil {
			reason := err.Error()
			if reason == "" {
				reason = "Hermes API turn failed"
			}
			return Outcome{Answer: "BLOCKED: " + reason, OK: false}
		}
		persist(res.RunID, res.SessionID, res.Tokens)
		if round > 1 && !res.Threaded {
			recap = true
		}

		reply := res.Text
		obj, haveObj := extractObj(reply)

		callsN := 0
		final := ""
		parsed := false
		var calls []callSpec
		if haveObj && json.Valid([]byte(obj)) {
			parsed = true
			var top map[string]json.RawMessage
			_ = json.Unmarshal([]byte(obj), &top)
			if raw, ok := top["calls"]; ok && !isJSONNull(raw) {
				_ = json.Unmarshal(raw, &calls)
			}
			callsN = len(calls)
			if raw, ok := top["final"]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					final = s
				}
			}
		}

		if !parsed {
			if botchedRe.MatchString(reply) && fixups < maxFixups {
				fixups++
				l.warnf("reply was not a valid envelope - asking Hermes to resend just the JSON")
				// fixupMsg alone is a bare format complaint with no task in
				// it — a model that only sees this has no way to know it was
				// ever asked to do anything, and (correctly, given what it
				// was shown) just apologizes for the formatting and asks what
				// to do. Restate the question so fixing the envelope doesn't
				// also mean abandoning the task.
				send = fixupMsg + "\n" + questionNote(userMsg)
				turnlog += fmt.Sprintf("[round %d] (invalid envelope, requested resend)\n", round)
				round++
				continue
			}
			// prose answer
			ans := strings.TrimPrefix(reply, "[INPUT_REQUIRED]")
			ans = ltrimFirstLine(ans)
			return Outcome{Answer: ans, OK: true}
		}

		if callsN == 0 {
			if final != "" {
				return Outcome{Answer: final, OK: true}
			}
			if fixups < maxFixups {
				fixups++
				send = emptyMsg + "\n" + questionNote(userMsg) // same reasoning as the fixupMsg case above
				round++
				continue
			}
			return Outcome{Answer: "BLOCKED: Hermes returned an empty envelope repeatedly.", OK: false}
		}

		if round > l.MaxRounds {
			// Tell the NEXT turn where this one stopped: a capped turn is
			// unfinished work, not a failed one — a "continue" should resume,
			// not redo completed steps.
			l.PendingNote = fmt.Sprintf("[the previous turn was stopped at its round budget (%d rounds) while still requesting tool calls — the work is unfinished. If the operator asks to continue, resume from where it stopped instead of re-doing completed steps.]", l.MaxRounds)
			return Outcome{Answer: fmt.Sprintf("BLOCKED: Hermes still requesting data after %d rounds.", l.MaxRounds), OK: false}
		}

		l.vlogf("round %d: %d call(s)", round, callsN)

		// Correlation id per call. Hermes may send its own `id`; we keep it when
		// present and unique, otherwise assign a positional `c<N>`. results[]
		// always carries the (possibly synthesized) id so Hermes can map each
		// output back to the call it asked for — same role as OpenAI's
		// tool_call_id / split-runtime's tool_call.request id.
		usedIDs := make(map[string]bool, callsN)
		mkID := func(i int, sent string) string {
			id := strings.TrimSpace(sent)
			if id == "" || usedIDs[id] {
				id = fmt.Sprintf("c%d", i+1)
			}
			for usedIDs[id] {
				id += "'"
			}
			usedIDs[id] = true
			return id
		}

		elems := make([]resultElem, 0, callsN)
		for i := 0; i < callsN; i++ {
			id := mkID(i, calls[i].id())
			tool := firstNonEmpty(calls[i].Tool, calls[i].Name)
			args := callArgs(calls[i])
			l.Ref++
			ref := l.Ref
			l.emit(session.Event{Kind: session.KindCall, Ref: ref, ID: id, Tool: tool, Args: args})

			start := time.Now()
			result, derr := l.Dispatch.Dispatch(tool, args)
			if errors.Is(derr, dispatch.ErrAbortTurn) {
				l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(125),
					Ms: time.Since(start).Milliseconds(), Out: "(declined by the operator — turn stopped)"})
				// Ctrl-C / "q" at an approval = decline this call AND stop the
				// turn. Nothing more runs; Hermes is told at the start of the
				// next turn so its transcript stays coherent.
				l.PendingNote = "[the previous turn was cancelled by the operator at the \"" + tool +
					"\" approval — that call was declined and nothing further was run. Treat it as not done.]"
				return Outcome{Answer: "(cancelled — declined " + tool + " and stopped the turn)", OK: true}
			}
			out := l.scrub(result.Out)
			l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(result.Exit),
				Ms: time.Since(start).Milliseconds(), Out: out})
			if l.OnResult != nil { // knowsystem learner: sees raw output + exit
				l.OnResult(tool, result.Out, result.Exit)
			}

			e := resultElem{
				ID:       id,
				Ref:      ref,
				Tool:     tool,
				Args:     args,
				ExitCode: result.Exit,
				Output:   out,
			}
			if c := l.scrub(result.Ctx); c != "" {
				e.Context = &c
			}
			elems = append(elems, e)

			turnlog += fmt.Sprintf("[round %d]   %s [%s](%s) -> exit %d: %s\n",
				round, tool, id, trunc(string(args), 120), result.Exit, trunc(oneLine(e.Output), 160))
		}

		payload, _ := marshalNoHTML(resultsPayload{Results: elems})
		// The operator's question is restated on every round, not just round 1.
		// A gateway can accept our session_id (Threaded=true) without actually
		// replaying the transcript to the model - in that case this is the ONLY
		// place downstream of round 1 where the model still sees what was asked.
		// Cheap even when the server does thread it correctly: one restated
		// sentence, no meaningful token cost.
		send = "[hands results] Your hands ran the instruction you just gave, in the operator's" +
			" working directory" + l.locNote() + ". This is real output from the operator's" +
			" machine — the operator did NOT paste it, and it is NOT from your sandbox. Each" +
			" result's output is the actual file contents / command output; exit_code 0 means" +
			" it worked (empty or non-zero: try another instruction, never \"I can't read it\")." +
			questionNote(userMsg) +
			" Now give the operator your answer in final — or another instruction if you need" +
			" more.\n" + string(payload) + l.roundBudgetNote(round+1)
		round++
	}
}

type resultElem struct {
	ID       string          `json:"id"`
	Ref      int             `json:"ref"` // the session-wide #ref the operator sees
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	ExitCode int             `json:"exit_code"`
	Output   string          `json:"output"`
	Context  *string         `json:"context,omitempty"`
}

type resultsPayload struct {
	Results []resultElem `json:"results"`
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
			return compact(json.RawMessage(s))
		}
		return json.RawMessage("{}")
	}
	return compact(raw)
}

func (l *Loop) emit(ev session.Event) {
	if l.Emit != nil {
		l.Emit(ev)
	}
}

func ltrimFirstLine(s string) string {
	const cut = " \t\r\v\f"
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		return strings.TrimLeft(s[:nl], cut) + s[nl:]
	}
	return strings.TrimLeft(s, cut)
}

func (l *Loop) scrub(s string) string {
	if l.Scrub != nil {
		return l.Scrub(s)
	}
	return s
}

func (l *Loop) vlogf(f string, a ...any) {
	if l.Vlogf != nil {
		l.Vlogf(f, a...)
	}
}

func (l *Loop) warnf(f string, a ...any) {
	if l.Warnf != nil {
		l.Warnf(f, a...)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// oneLine collapses newlines to spaces so a tool result or question can sit
// on a single turnlog/reminder line without breaking its shape.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// roundBudgetNote is the countdown to the round cap, appended to the results
// payload when the ceiling gets close: a soft nudge for the last 5 rounds
// (finish, or plan the continuation), a heads-up before the last round whose
// calls still run, and once none is left a hard instruction that the reply must
// be a final. Making the ceiling visible
// while rounds are still left is what turns the cap from a punishment into a
// planning input — the model batches calls or lands a clean final instead of
// burning a round to hit the wall.
func (l *Loop) roundBudgetNote(nextRound int) string {
	if l.MaxRounds <= 0 {
		return ""
	}
	left := l.MaxRounds - nextRound + 1
	switch {
	case left <= 0:
		return "\n[round budget: no tool rounds remain — your reply must be {\"calls\":[],\"final\":\"...\"} stating what is done and what a continuation must do.]"
	case left == 1:
		return fmt.Sprintf("\n[round budget: your next envelope is the LAST round (%d/%d) whose calls still run — put everything you still need into it. After its results your reply must be {\"calls\":[],\"final\":\"...\"} stating what is done and what a continuation must do.]", nextRound, l.MaxRounds)
	case left > 1 && left <= 5:
		return fmt.Sprintf("\n[round budget: %d/%d rounds used, %d left. Finish now, or batch the remaining calls and land a final that says what the continuation must do.]", nextRound-1, l.MaxRounds, left)
	}
	return ""
}

// questionNote restates the operator's original question. Appended to every
// message sent after round 1 - including the terse fixupMsg/emptyMsg recovery
// nudges, which otherwise carry no task context at all, so fixing the
// envelope format also means the model has no idea what it was doing and
// answers "what can I do for you?" instead of finishing the task.
func questionNote(userMsg string) string {
	return " The operator's question this turn, verbatim, was: \"" + trunc(oneLine(userMsg), 500) + "\"."
}

func compact(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(buf.Bytes())
}

func marshalNoHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
