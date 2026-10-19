// Package loop drives one user turn = N Hermes rounds. Send the message; if
// Hermes replies with a directive envelope run the calls locally, feed a
// {results:[...]} object back, repeat; stop on {final:"..."} or plain prose.
// Envelope recovery handles fenced / prosey / botched JSON.
package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"hermes-hands/internal/api"
	"hermes-hands/internal/compact"
	"hermes-hands/internal/dispatch"
	"hermes-hands/internal/session"
)

// Asker is the slice of the Hermes client the loop needs; *api.Client
// satisfies it. An interface here keeps the round/envelope logic unit-testable
// without a live server.
type Asker interface {
	Ask(ctx context.Context, msg, sess, skey string) (api.AskResult, error)
}

// HistoryAsker is an Asker that can send the transcript along
// (api.Runs.AskWith) — what a compacted session needs.
type HistoryAsker interface {
	AskWith(ctx context.Context, msg, sess, skey string, history []api.Message) (api.AskResult, error)
}

// Loop drives the delegation rounds for one turn.
type Loop struct {
	API      Asker
	Dispatch *dispatch.Dispatcher

	// OnResult sees each dispatched tool result (tool, raw output, exit code).
	// The knowsystem learner uses it to catch 127 "command not found" facts.
	OnResult func(tool, output string, exit int)
	Scrub    func(string) string

	// The operator's budget per turn (0 = no limit), see budget.go: rounds =
	// requests to Hermes, calls = hands calls run, and calls per envelope.
	MaxRounds, MaxCalls, MaxCallsPerRound int
	// MaxRoundOutput caps the bytes of one round's results together (0 = no
	// limit): what Hermes keeps of a round, it keeps until a compaction, so
	// no single round may crowd the context (session.FitRound).
	MaxRoundOutput int

	// Ctx is the open session's context keeper (nil: no meter, hermes-agent
	// holds the transcript): it predicts every request, compacts first when
	// one would come too close to the window, and supplies the transcript
	// once hermes-hands holds it.
	Ctx *compact.Keeper

	// OnProgress, when set, sees where the turn stands against the budget —
	// at every request to Hermes and after every call (the spinner, rpc).
	OnProgress func(Progress)

	// Emit, when set, receives every hands call as two events: KindCall right
	// BEFORE it runs (so the operator sees what is running, not only what
	// ran) and KindResult after it, carrying the scrubbed output in Event.Out.
	// The caller stores and renders them.
	Emit func(ev session.Event)

	// Paused, when set, reports how long this turn has waited on approval
	// prompts so far; a call's Ms leaves that wait out, so "#14 ✓ · 3s" is
	// the time the call ran, not the time the operator took to answer.
	Paused func() time.Duration

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

	rounds, calls int // this turn so far — reset by Run
	noted         int // the highest budget tier the operator was told about this turn
	ctxNow        int // the context of the request in flight (estimated tokens), for Progress
}

func (l *Loop) paused() time.Duration {
	if l.Paused == nil {
		return 0
	}
	return l.Paused()
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
		"{\"calls\":[{\"id\":\"c1\",\"tool\":\"local_shell|local_read|local_write|local_edit|local_output\",\"args\":{...}}],\"final\":null}\n" +
		"(give each call a short id; the hands echo it in results so you can map\n" +
		"output to call) — or {\"calls\":[],\"final\":\"...\"} once it is solved. Not a\n" +
		"chat reply, not a description of yourself or your tools. If you do not know\n" +
		"the directory yet, your instruction is a look — `local_shell` (ls / git / rg)\n" +
		"or `local_read`." + l.frameBudget()
}

// Outcome is the turn result. OK == false means Answer is a "BLOCKED: ..."
// string and the process should exit 1.
type Outcome struct {
	Answer string
	OK     bool

	Rounds, Calls int // requests to Hermes and hands calls this turn took
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

// Run executes the turn. persist mirrors _hh_session_write: it is called after
// every completed api.Ask with (runID, serverSessionID) so the caller can bump
// the local index and adopt a re-issued session id (mutating rec, which Run
// re-reads next round).
func (l *Loop) Run(ctx context.Context, userMsg string, rec *session.Record, persist func(runID, serverSID string, tokens int)) (out Outcome) {
	l.rounds, l.calls, l.noted = 0, 0, tierNone
	defer func() { out.Rounds, out.Calls = l.rounds, l.calls }()
	t := &turnState{userMsg: userMsg, send: l.opening(userMsg), turnlog: "[operator] " + userMsg + "\n"}
	for round := 1; ; round++ {
		l.vlogf("round %d -> Hermes", round)
		l.rounds = round
		res, err := l.request(ctx, round, t, rec)
		if err != nil {
			return l.blocked(err)
		}
		persist(res.RunID, res.SessionID, res.Tokens)
		if round > 1 && !res.Threaded {
			t.recap = true
		}
		if o, done := l.handle(res.Text, round, t); done {
			return o
		}
	}
}

// turnState is what one turn carries from round to round.
type turnState struct {
	userMsg string
	send    string // the next message to Hermes
	turnlog string // the turn so far, for a gateway that does not thread it (recap)
	recap   bool

	fixups, rejects int // recovery nudges spent: a botched / empty envelope, one over the budget

	// pending: the results send carries, unfitted, and the round they ran
	// in — what a request refused for its size is fitted tighter from
	pending      []resultElem
	pendingRound int
}

// opening is round 1's message: the framed problem, behind the one-off notes
// a resumed or a cancelled turn left.
func (l *Loop) opening(userMsg string) string {
	send := l.frame(userMsg)
	if l.ResumeNote != "" { // resumed session, server history gone — replay local copy
		send = l.ResumeNote + "\n\n" + send
		l.ResumeNote = ""
	}
	if l.PendingNote != "" { // a prior turn was cancelled — tell Hermes now
		send = l.PendingNote + "\n\n" + send
		l.PendingNote = ""
	}
	return send
}

// handle acts on Hermes' reply of round: a final or prose ends the turn
// (done); an envelope's calls run and their results become the next message;
// a botched, empty or over-budget envelope gets a nudge instead.
func (l *Loop) handle(reply string, round int, t *turnState) (Outcome, bool) {
	env := parseEnvelope(reply)
	switch {
	case !env.parsed:
		if botchedRe.MatchString(reply) && t.fixups < maxFixups {
			t.fixups++
			l.warnf("reply was not a valid envelope - asking Hermes to resend just the JSON")
			// fixupMsg alone is a bare format complaint with no task in
			// it — a model that only sees this has no way to know it was
			// ever asked to do anything, and (correctly, given what it
			// was shown) just apologizes for the formatting and asks what
			// to do. Restate the question so fixing the envelope doesn't
			// also mean abandoning the task.
			t.send, t.pending = fixupMsg+"\n"+questionNote(t.userMsg), nil
			t.turnlog += fmt.Sprintf("[round %d] (invalid envelope, requested resend)\n", round)
			return Outcome{}, false
		}
		// prose answer
		return Outcome{Answer: ltrimFirstLine(strings.TrimPrefix(reply, "[INPUT_REQUIRED]")), OK: true}, true
	case len(env.calls) == 0:
		if env.final != "" {
			return Outcome{Answer: env.final, OK: true}, true
		}
		if t.fixups < maxFixups {
			t.fixups++
			t.send, t.pending = emptyMsg+"\n"+questionNote(t.userMsg), nil // same reasoning as the fixupMsg case above
			return Outcome{}, false
		}
		return Outcome{Answer: "BLOCKED: Hermes returned an empty envelope repeatedly.", OK: false}, true
	}
	if o, stop := l.capped(round); stop {
		return o, true
	}
	if why, note := l.oversize(len(env.calls)); why != "" {
		// All or nothing: an envelope over the budget runs not at all, so
		// no half-run batch (file A edited, the dependent B not) remains.
		if t.rejects >= maxFixups {
			return Outcome{Answer: "BLOCKED: Hermes kept sending envelopes over the call budget.", OK: false}, true
		}
		t.rejects++
		l.noteOperator(note)
		t.send, t.pending = why+"\n"+questionNote(t.userMsg)+l.roundBudgetNote(round+1), nil
		t.turnlog += fmt.Sprintf("[round %d] (envelope of %d calls over the budget, nothing run)\n", round, len(env.calls))
		return Outcome{}, false
	}

	l.vlogf("round %d: %d call(s)", round, len(env.calls))
	elems, stopped := l.runCalls(env.calls, round, &t.turnlog)
	if stopped != nil {
		return *stopped, true
	}
	t.pending, t.pendingRound = elems, round
	t.send = l.resultsMsg(l.fitRound(elems, l.MaxRoundOutput), t.userMsg, round)
	l.announce(round + 1)
	return Outcome{}, false
}

// resultsMsg is the message that hands a round's results back.
func (l *Loop) resultsMsg(elems []resultElem, userMsg string, round int) string {
	payload, _ := marshalNoHTML(resultsPayload{Results: elems})
	// The operator's question is restated on every round, not just round 1.
	// A gateway can accept our session_id (Threaded=true) without actually
	// replaying the transcript to the model - in that case this is the ONLY
	// place downstream of round 1 where the model still sees what was asked.
	// Cheap even when the server does thread it correctly: one restated
	// sentence, no meaningful token cost.
	return "[hands results] Your hands ran the instruction you just gave, in the operator's" +
		" working directory" + l.locNote() + ". This is real output from the operator's" +
		" machine — the operator did NOT paste it, and it is NOT from your sandbox. Each" +
		" result's output is the actual file contents / command output; exit_code 0 means" +
		" it worked (empty or non-zero: try another instruction, never \"I can't read it\")." +
		questionNote(userMsg) +
		" Now give the operator your answer in final — or another instruction if you need" +
		" more.\n" + string(payload) + l.roundBudgetNote(round+1)
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

// questionNote restates the operator's original question. Appended to every
// message sent after round 1 - including the terse fixupMsg/emptyMsg recovery
// nudges, which otherwise carry no task context at all, so fixing the
// envelope format also means the model has no idea what it was doing and
// answers "what can I do for you?" instead of finishing the task.
func questionNote(userMsg string) string {
	return " The operator's question this turn, verbatim, was: \"" + trunc(oneLine(userMsg), 500) + "\"."
}

func compactJSON(raw json.RawMessage) json.RawMessage {
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
