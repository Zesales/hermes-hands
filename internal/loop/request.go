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
		if ev, err := l.Ctx.Compact(true); err == nil {
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
	if err != nil {
		if l.Ctx != nil && compact.IsOverflow(err.Error()) {
			l.Ctx.Overflow(err.Error())
			return res, overflowError{err, l.Ctx}
		}
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

// overflowError is a run refused because the conversation outgrew the
// model's window. Nothing is retried within the turn — the operator decides
// whether to go on, and the next request is compacted first.
type overflowError struct {
	err error
	k   *compact.Keeper
}

func (e overflowError) Error() string {
	const why = " — the conversation no longer fits the model's context window."
	if e.k.Window > 0 && e.k.At > 0 {
		return e.err.Error() + why + " hermes-hands compacts it before the next request (/compact does it now); then continue."
	}
	return e.err.Error() + why + " /compact compacts it; then continue."
}

func (e overflowError) Unwrap() error { return e.err }
