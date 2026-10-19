package loop

import (
	"context"
	"errors"

	"hermes-hands/internal/api"
	"hermes-hands/internal/compact"
	"hermes-hands/internal/session"
)

// request sends round's message to Hermes. The context keeper predicts the
// prompt first and, when it would come too close to the window, compacts
// BEFORE the request — never after it failed. Once hermes-hands holds the
// transcript it goes along. Hermes' reply is recorded (KindReply): the
// transcript of a compacted session is built from those.
func (l *Loop) request(ctx context.Context, round int, t *turnState, rec *session.Record) (api.AskResult, error) {
	history := l.Ctx.History()
	msg := t.send
	if t.recap && history == nil { // hermes-hands' own transcript carries the turn already
		msg = "[conversation so far this turn]\n" + t.turnlog + "\n[latest tool results]\n" + t.send
	}
	predicted := l.Ctx.Predict(msg, history)
	if l.Ctx.Due(predicted) {
		if ev, err := l.Ctx.Compact(compact.Auto); err == nil {
			l.emit(ev)
			history, msg = l.Ctx.History(), t.send
			predicted = l.Ctx.Predict(msg, history)
		} else if !errors.Is(err, compact.ErrNothing) {
			l.warnf("compaction failed: %v", err)
		}
	}
	l.ctxNow = predicted
	l.progress()

	res, err := l.ask(ctx, msg, rec, history)
	for try := 0; err != nil && l.Ctx != nil && compact.IsOverflow(err.Error()); try++ {
		// refused for its size: handled here, locally — the same request
		// again, smaller — before the turn gives up
		l.Ctx.Overflow(err.Error())
		if try == maxRecover || !l.recover(t, try) {
			return res, overflowError{err}
		}
		history, msg = l.Ctx.History(), t.send
		l.ctxNow = l.Ctx.Predict(msg, history)
		l.progress()
		res, err = l.ask(ctx, msg, rec, history)
	}
	if err != nil {
		return res, err
	}
	l.Ctx.Observe(res, msg, history, predicted)
	l.ctxNow = l.Ctx.Meter().Tokens
	l.emit(session.Event{Kind: session.KindReply, Text: l.scrub(res.Text), Round: round})
	return res, nil
}

// ask sends one request: with the transcript when hermes-hands holds it.
func (l *Loop) ask(ctx context.Context, msg string, rec *session.Record, history []api.Message) (api.AskResult, error) {
	if ha, ok := l.API.(HistoryAsker); ok && history != nil {
		return ha.AskWith(ctx, msg, rec.HermesSessionID, rec.HermesSessionKey, history)
	}
	return l.API.Ask(ctx, msg, rec.HermesSessionID, rec.HermesSessionKey)
}

// blocked ends a turn whose request failed.
func (l *Loop) blocked(err error) Outcome {
	reason := err.Error()
	if reason == "" {
		reason = "Hermes API turn failed"
	}
	return Outcome{Answer: "BLOCKED: " + reason, OK: false}
}

// maxRecover bounds the local retries of a request refused for its size.
const maxRecover = 2

// recover makes a request that was refused for its size smaller, locally:
// first the transcript compacted as far as it goes — every output elided,
// every turn but this one as question + answer — then the results the
// request carries fitted tighter (their full outputs stay local). false:
// nothing left to make smaller. Same session, nothing thrown away.
func (l *Loop) recover(t *turnState, try int) bool {
	if try == 0 {
		ev, err := l.Ctx.Compact(compact.Aggressive)
		if err == nil {
			l.emit(ev)
			l.noteOperator("the request was refused for its size — compacted locally, sending it again")
			return true
		}
		if !errors.Is(err, compact.ErrNothing) {
			l.warnf("compaction failed: %v", err)
		}
	}
	if t.pending == nil {
		return false
	}
	limit := l.MaxRoundOutput
	if limit <= 0 {
		limit = refitFrom
	}
	t.send = l.resultsMsg(l.fitRound(t.pending, max(limit/4, refitMin)), t.userMsg, t.pendingRound)
	t.pending = nil
	l.noteOperator("the request was refused for its size — this round's results fitted tighter, sending it again")
	return true
}

// refitFrom / refitMin: the round output budget a refused request's results
// are fitted to a quarter of (refitFrom when there is none), at least
// refitMin bytes.
const (
	refitFrom = 60000
	refitMin  = 4000
)

// overflowError is a run refused for its size that the local retries could
// not make fit: the fixed part of the prompt and this request alone are too
// large for the model's window.
type overflowError struct{ err error }

func (e overflowError) Error() string {
	return e.err.Error() + " — the request does not fit the model's context window, even compacted locally" +
		" (every output elided, every earlier turn as question + answer). Start a /new session or use a model with a larger window."
}

func (e overflowError) Unwrap() error { return e.err }
