package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"hermes-hands/internal/dispatch"
	"hermes-hands/internal/session"
)

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

// runCalls runs one envelope's calls in order and returns their results. A
// non-nil Outcome means the operator stopped the turn at an approval: nothing
// after that call ran, and the turn ends with that outcome.
func (l *Loop) runCalls(calls []callSpec, round int, turnlog *string) ([]resultElem, *Outcome) {
	ids := newIDs(len(calls))
	elems := make([]resultElem, 0, len(calls))
	l.Dispatch.NextRound() // rules handed out before now were seen when this batch was written
	for i, c := range calls {
		e, stopped := l.runCall(c, ids(i, c.id()))
		if stopped != nil {
			return nil, stopped
		}
		elems = append(elems, e)
		*turnlog += fmt.Sprintf("[round %d]   %s [%s](%s) -> exit %d: %s\n",
			round, e.Tool, e.ID, trunc(string(e.Args), 120), e.ExitCode, trunc(oneLine(e.Output), 160))
	}
	return elems, nil
}

// runCall runs one call: shown before it runs (KindCall) and after
// (KindResult), counted against the turn's call budget.
func (l *Loop) runCall(c callSpec, id string) (resultElem, *Outcome) {
	tool := firstNonEmpty(c.Tool, c.Name)
	if canon := dispatch.Canonical(tool); canon != "" {
		tool = canon // events, results and the terminal speak local_*
	}
	args := callArgs(c)
	l.Ref++
	ref := l.Ref
	l.emit(session.Event{Kind: session.KindCall, Ref: ref, ID: id, Tool: tool, Args: args})
	l.calls++
	l.progress()

	start, held := time.Now(), l.paused()
	result, derr := l.Dispatch.Dispatch(tool, args)
	ran := max(time.Since(start)-(l.paused()-held), 0).Milliseconds()
	if errors.Is(derr, dispatch.ErrAbortTurn) {
		l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(125),
			Ms: ran, Out: "(declined by the operator — turn stopped)"})
		// Ctrl-C / "q" at an approval = decline this call AND stop the
		// turn. Nothing more runs; Hermes is told at the start of the
		// next turn so its transcript stays coherent.
		l.PendingNote = "[the previous turn was cancelled by the operator at the \"" + tool +
			"\" approval — that call was declined and nothing further was run. Treat it as not done.]"
		return resultElem{}, &Outcome{Answer: "(cancelled — declined " + tool + " and stopped the turn)", OK: true}
	}
	out := l.scrub(result.Out)
	l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(result.Exit),
		Ms: ran, Out: out})
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
	if ctx := l.scrub(result.Ctx); ctx != "" {
		e.Context = &ctx
	}
	return e, nil
}

// newIDs hands out one correlation id per call of an envelope. Hermes may
// send its own `id`; we keep it when present and unique, otherwise assign a
// positional `c<N>`. results[] always carries the (possibly synthesized) id
// so Hermes can map each output back to the call it asked for — same role as
// OpenAI's tool_call_id / split-runtime's tool_call.request id.
func newIDs(n int) func(i int, sent string) string {
	used := make(map[string]bool, n)
	return func(i int, sent string) string {
		id := strings.TrimSpace(sent)
		if id == "" || used[id] {
			id = fmt.Sprintf("c%d", i+1)
		}
		for used[id] {
			id += "'"
		}
		used[id] = true
		return id
	}
}
