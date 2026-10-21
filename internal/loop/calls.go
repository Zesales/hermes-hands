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
	Ref      int             `json:"ref,omitempty"` // the session-wide #ref the operator sees; none for a call not run
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	ExitCode int             `json:"exit_code"`
	Output   string          `json:"output"`
	NotRun   string          `json:"not_run,omitempty"` // why the hands did not run it
	Context  *string         `json:"context,omitempty"`
}

type resultsPayload struct {
	Results []resultElem `json:"results"`
}

// runCalls runs one envelope's calls in order — the first n of them: the
// rest come back not run, with why (the round's budget). With stopOnError
// the calls after the first that fails come back not run too. A non-nil
// Outcome means the operator stopped the turn at an approval: nothing after
// that call ran, and the turn ends with that outcome.
func (l *Loop) runCalls(calls []callSpec, n int, why string, stopOnError bool, round int, turnlog *string) ([]resultElem, *Outcome) {
	ids := newIDs(len(calls))
	elems := make([]resultElem, 0, len(calls))
	l.Dispatch.NextRound() // rules handed out before now were seen when this batch was written
	for i, c := range calls {
		id := ids(i, c.id())
		if i >= n {
			e := l.notRun(c, id, why, round)
			elems = append(elems, e)
			*turnlog += fmt.Sprintf("[round %d]   %s [%s] -> not run: %s\n", round, e.Tool, e.ID, why)
			continue
		}
		e, stopped := l.runCall(c, id, round)
		if stopped != nil {
			return nil, stopped
		}
		elems = append(elems, e)
		*turnlog += fmt.Sprintf("[round %d]   %s [%s](%s) -> exit %d: %s\n",
			round, e.Tool, e.ID, trunc(string(e.Args), 120), e.ExitCode, trunc(oneLine(e.Output), 160))
		if stopOnError && e.ExitCode != 0 && i+1 < len(calls) {
			n, why = i+1, fmt.Sprintf("stop_on_error: %s (#%d) failed with exit %d", e.ID, e.Ref, e.ExitCode)
			l.noteOperator(fmt.Sprintf("stop_on_error: #%d failed — the %d calls after it not run", e.Ref, len(calls)-i-1))
		}
	}
	return elems, nil
}

// notRun is a call the hands did not run, for the results and the log: why
// says what held it back; Hermes sends it again if it still needs it.
func (l *Loop) notRun(c callSpec, id, why string, round int) resultElem {
	tool := firstNonEmpty(c.Tool, c.Name)
	if canon := dispatch.Canonical(tool); canon != "" {
		tool = canon
	}
	args := callArgs(c)
	why += " — send it again if it is still needed"
	l.emit(session.Event{Kind: session.KindNotRun, ID: id, Tool: tool, Args: args, Text: why, Round: round})
	return resultElem{ID: id, Tool: tool, Args: args, ExitCode: -1, NotRun: why}
}

// runCall runs one call of request round: shown before it runs (KindCall)
// and after (KindResult), counted against the turn's call budget.
func (l *Loop) runCall(c callSpec, id string, round int) (resultElem, *Outcome) {
	tool := firstNonEmpty(c.Tool, c.Name)
	if canon := dispatch.Canonical(tool); canon != "" {
		tool = canon // events, results and the terminal speak local_*
	}
	args := callArgs(c)
	l.Ref++
	ref := l.Ref
	l.emit(session.Event{Kind: session.KindCall, Ref: ref, ID: id, Tool: tool, Args: args, Round: round})
	l.calls++
	l.progress()

	start, held := time.Now(), l.paused()
	result, derr := l.Dispatch.Dispatch(tool, args)
	ran := max(time.Since(start)-(l.paused()-held), 0).Milliseconds()
	if errors.Is(derr, dispatch.ErrAbortTurn) {
		l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(125),
			Ms: ran, Out: "(declined by the operator — turn stopped)", Round: round})
		// Ctrl-C / "q" at an approval = decline this call AND stop the
		// turn. Nothing more runs; Hermes is told at the start of the
		// next turn so its transcript stays coherent.
		l.PendingNote = "[the previous turn was cancelled by the operator at the \"" + tool +
			"\" approval — that call was declined and nothing further was run. Treat it as not done.]"
		return resultElem{}, &Outcome{Answer: "(cancelled — declined " + tool + " and stopped the turn)", OK: true}
	}
	out := l.scrub(result.Out)
	l.emit(session.Event{Kind: session.KindResult, Ref: ref, Tool: tool, Exit: session.Int(result.Exit),
		Ms: ran, Out: out, Round: round})
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

// dedupe sends an output Hermes has verbatim already — earlier this
// session, or earlier in this round — as a reference to it: seen live, a
// long turn read the same file sections again and again, each in full.
func (l *Loop) dedupe(elems []resultElem) []resultElem {
	first := l.Ctx.Verbatim()
	if len(first) == 0 {
		return elems
	}
	for i, e := range elems {
		if len(e.Output) < session.ElideMin {
			continue
		}
		if ref, ok := first[session.OutputHash(e.Output)]; ok && ref < e.Ref {
			elems[i].Output = session.SameOutput(ref)
		}
	}
	return elems
}

// fitRound fits a round's results into limit bytes together (0 = no limit)
// and returns the fitted copy: Hermes gets that, the session keeps every
// output whole (stored when its result was recorded) — local_output reads a
// cut part back. The operator is told what was cut.
func (l *Loop) fitRound(elems []resultElem, limit int) []resultElem {
	fitted := append([]resultElem(nil), elems...)
	outs := make([]string, len(elems))
	refs := make([]int, len(elems))
	for i, e := range elems {
		outs[i], refs[i] = e.Output, e.Ref
	}
	var cut []string
	for i, out := range session.FitRound(outs, refs, limit) {
		if out != elems[i].Output {
			fitted[i].Output = out
			cut = append(cut, fmt.Sprintf("#%d", elems[i].Ref))
		}
	}
	if len(cut) > 0 {
		l.noteOperator(fmt.Sprintf("round output over %d bytes: %s cut for Hermes — the full output stays local (local_output)",
			limit, strings.Join(cut, " ")))
	}
	return fitted
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
