package loop

import (
	"fmt"
	"strings"

	"hermes-hands/internal/session"
)

// The operator's budget for one turn: MaxRounds requests to Hermes, MaxCalls
// hands calls, at most MaxCallsPerRound calls per envelope (0 = no limit).
// Hermes sees it from the start (frameBudget) and a countdown in every result
// once it gets close (roundBudgetNote); the operator sees the same countdown
// (announce, Progress). The budget is a planning input, not a punishment: the
// model batches calls and lands a final that says what is done and what is
// open — the note a continuation, and a compacted context, builds on.

// Progress is where a turn stands against its budget: Round is the request to
// Hermes in flight or just answered, Calls the hands calls run so far. A Max
// of 0 means no limit. Context is the estimated prompt of the request in
// flight (0 = unknown), Window the model's (0 = unknown).
type Progress struct {
	Round, MaxRounds, Calls, MaxCalls int
	Context, Window                   int
}

// Countdown tiers, in rising urgency.
const (
	tierNone   = iota
	tierWarn   // the limit is getting close: prioritise, batch, plan the rest
	tierFinish // finish now, or land a final that plans the continuation
	tierLast   // the next envelope is the last whose calls still run
	tierOut    // nothing left: the reply must be a final
)

// budget is where the turn stands before request next: rounds and calls left
// (-1 = no limit) and the countdown tier.
type budget struct{ roundsLeft, callsLeft, tier int }

// budgetAt places request next on the countdown. Rounds: a heads-up over the
// last 40% of them, "finish" over the last 20% (at least 2), then the last
// round. Calls: a heads-up from 75% used, "finish" from 90%.
func (l *Loop) budgetAt(next int) budget {
	b := budget{roundsLeft: -1, callsLeft: -1}
	if l.MaxRounds > 0 {
		b.roundsLeft = l.MaxRounds - next + 1
		switch left := b.roundsLeft; {
		case left <= 0:
			b.tier = tierOut
		case left == 1:
			b.tier = tierLast
		case left <= max(l.MaxRounds/5, 2):
			b.tier = tierFinish
		case left <= l.MaxRounds*2/5:
			b.tier = tierWarn
		}
	}
	if l.MaxCalls > 0 {
		b.callsLeft = max(l.MaxCalls-l.calls, 0)
		t := tierNone
		switch {
		case b.callsLeft == 0:
			t = tierOut
		case l.calls*10 >= l.MaxCalls*9:
			t = tierFinish
		case l.calls*4 >= l.MaxCalls*3:
			t = tierWarn
		}
		b.tier = max(b.tier, t)
	}
	return b
}

// frameBudget is the budget line of the task frame: the model plans against a
// known ceiling instead of discovering it as a BLOCKED wall, and learns it is
// the operator's limit. Short — it must not crowd out the task it wraps.
func (l *Loop) frameBudget() string {
	var lim []string
	if l.MaxRounds > 0 {
		lim = append(lim, fmt.Sprintf("%d rounds (envelopes with calls)", l.MaxRounds))
	}
	if l.MaxCalls > 0 {
		lim = append(lim, fmt.Sprintf("%d hands calls", l.MaxCalls))
	}
	if l.MaxCallsPerRound > 0 {
		lim = append(lim, fmt.Sprintf("at most %d calls per envelope", l.MaxCallsPerRound))
	}
	if len(lim) == 0 {
		return ""
	}
	return "\n\nBudget: the operator limited this turn to " + strings.Join(lim, ", ") +
		". An envelope over the budget is not run at all. Batch several calls per envelope and plan" +
		" before large writes — a final that says what is done and what is open beats being stopped at the limit."
}

// roundBudgetNote is the countdown appended to the results before request
// next — empty while the limit is far. Making the ceiling visible while
// rounds are still left is what turns the cap into a planning input: the
// model batches calls or lands a clean final instead of hitting the wall.
func (l *Loop) roundBudgetNote(next int) string {
	const final = `{"calls":[],"final":"..."} stating what is done, what is open and what a continuation must do`
	b := l.budgetAt(next)
	status := l.budgetStatus(next, b)
	switch b.tier {
	case tierOut:
		gone := "rounds"
		if b.roundsLeft != 0 && b.callsLeft == 0 {
			gone = "hands calls"
		}
		return "\n[budget: no " + gone + " remain — your reply must be " + final + ".]"
	case tierLast:
		return fmt.Sprintf("\n[budget: %s — your next envelope is the LAST round (%d/%d) whose calls still run — put everything you still need into it. After its results your reply must be %s.]", status, next, l.MaxRounds, final)
	case tierFinish:
		return "\n[budget: " + status + ". Finish now, or batch the remaining calls and land a final that says what is done, what is open and what a continuation must do.]"
	case tierWarn:
		return "\n[budget: " + status + " — the operator's limit is getting close. Prioritise what matters most, batch several calls per envelope and plan the rest so it fits; a final that says what is unfinished beats being stopped at the limit.]"
	}
	return ""
}

// budgetStatus is the "5/25 rounds used, 20 left · 31/100 hands calls used,
// 69 left" part of a countdown, for the limits that are set.
func (l *Loop) budgetStatus(next int, b budget) string {
	var parts []string
	if l.MaxRounds > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d rounds used, %d left", next-1, l.MaxRounds, max(b.roundsLeft, 0)))
	}
	if l.MaxCalls > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d hands calls used, %d left", l.calls, l.MaxCalls, b.callsLeft))
	}
	return strings.Join(parts, " · ")
}

// capped ends a turn whose budget is used up while Hermes still asks for
// calls. A capped turn is unfinished work, not a failed one: PendingNote tells
// the NEXT turn where this one stopped, so a "continue" resumes instead of
// redoing completed steps.
func (l *Loop) capped(round int) (Outcome, bool) {
	const resume = " while still requesting hands calls — the work is unfinished. If the operator asks to continue, resume from where it stopped instead of re-doing completed steps.]"
	switch {
	case l.MaxRounds > 0 && round > l.MaxRounds:
		l.PendingNote = fmt.Sprintf("[the previous turn was stopped at its round budget (%d rounds)", l.MaxRounds) + resume
		return Outcome{Answer: fmt.Sprintf("BLOCKED: Hermes still requesting data after %d rounds.", l.MaxRounds)}, true
	case l.MaxCalls > 0 && l.calls >= l.MaxCalls:
		l.PendingNote = fmt.Sprintf("[the previous turn was stopped at its call budget (%d hands calls)", l.MaxCalls) + resume
		return Outcome{Answer: fmt.Sprintf("BLOCKED: Hermes still requesting tool calls after %d calls.", l.MaxCalls)}, true
	}
	return Outcome{}, false
}

// oversize says why an envelope of n calls may not run (why, for Hermes; note,
// for the operator) — both empty when it may. It runs whole or not at all.
func (l *Loop) oversize(n int) (why, note string) {
	if l.MaxCallsPerRound > 0 && n > l.MaxCallsPerRound {
		return fmt.Sprintf(`{"error":"call budget: this envelope has %d calls; the operator allows at most %d per envelope. NOTHING was run. Split it: resend at most %d calls (the first steps), the rest after their results."}`, n, l.MaxCallsPerRound, l.MaxCallsPerRound),
			fmt.Sprintf("budget: envelope of %d calls not run — at most %d per envelope; Hermes was asked to split it", n, l.MaxCallsPerRound)
	}
	if left := l.MaxCalls - l.calls; l.MaxCalls > 0 && n > left {
		return fmt.Sprintf(`{"error":"call budget: this envelope has %d calls, but the operator limited this turn to %d hands calls and only %d remain. NOTHING was run. Resend at most %d calls — the ones that matter most — or a final that says what is done, what is open and what a continuation must do."}`, n, l.MaxCalls, left, left),
			fmt.Sprintf("budget: envelope of %d calls not run — only %d of %d calls left; Hermes was asked to resend at most %d", n, left, l.MaxCalls, left)
	}
	return "", ""
}

// announce tells the operator — once per tier and turn — which countdown
// Hermes was just shown, as a note the terminal and the session log keep.
func (l *Loop) announce(next int) {
	b := l.budgetAt(next)
	if b.tier <= l.noted {
		return
	}
	l.noted = b.tier
	what := map[int]string{
		tierWarn:   "Hermes was told the limit is getting close",
		tierFinish: "Hermes was told to finish",
		tierLast:   "the next round is the last whose calls run",
		tierOut:    "used up — Hermes may only answer now",
	}[b.tier]
	var left []string
	if b.roundsLeft >= 0 {
		left = append(left, fmt.Sprintf("%d of %d rounds", max(b.roundsLeft, 0), l.MaxRounds))
	}
	if b.callsLeft >= 0 {
		left = append(left, fmt.Sprintf("%d of %d calls", b.callsLeft, l.MaxCalls))
	}
	l.noteOperator("budget: " + strings.Join(left, " · ") + " left — " + what)
}

// noteOperator records and shows one dim line for the operator.
func (l *Loop) noteOperator(text string) {
	l.emit(session.Event{Kind: session.KindNote, Text: text})
}

func (l *Loop) progress() {
	if l.OnProgress != nil {
		l.OnProgress(Progress{Round: l.rounds, MaxRounds: l.MaxRounds, Calls: l.calls, MaxCalls: l.MaxCalls,
			Context: l.ctxNow, Window: l.Ctx.Meter().Window})
	}
}
