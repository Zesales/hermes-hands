// Package compact keeps the context of one open session: it meters the
// conversation the model sees, and when that nears the model's window it
// compacts it — hermes-hands' own compaction, not hermes-agent's.
//
// hermes-agent compacts by summarising the middle of a conversation and keeps
// its latest messages whole (compression.protect_last_n). In a hands session
// those are the results of the last rounds — file contents, diffs, test
// output — and once they alone outgrow the window no summary helps: every
// request fails, a "continue" included. hermes-hands holds a full local record
// of the session (internal/session), every call's output among it, so it can
// compact without losing anything: old outputs go as a stub naming their #ref
// (local_output reads them back), old replies without their long arguments,
// the oldest turns as question + answer. From the first compaction on,
// hermes-hands sends that transcript with every run (conversation_history) —
// same hermes-agent session, no switch, no summary written by a model.
//
// Hermes' API has no context meter (NousResearch/hermes-agent#15618), so the
// meter is built from what it does report: a run's usage.input_tokens is the
// prompt of its model calls; with one call (no server tool of Hermes' own
// ran) that is exactly the prompt the model saw. Between measurements the
// meter estimates from bytes.
package compact

import (
	"errors"
	"fmt"
	"strconv"

	"hermes-hands/internal/session"
)

// Keeper is the context keeper of the open session.
type Keeper struct {
	Store *session.Store
	Rec   *session.Record

	// Window is the model's context window in tokens (0 = unknown: the meter
	// still counts, nothing compacts on its own). At is the share of it, in
	// percent, a request may reach before hermes-hands compacts first (0 =
	// only /compact). RoundOutput is the round output budget, which a
	// rebuilt round gets again.
	Window, At, RoundOutput int

	// ElidedNow is what the last Compact newly turned into lines of the
	// latest turn ("#6–#8") — outputs Hermes read moments ago; the loop tells
	// it not to read them again in full. "" when none.
	ElidedNow string
}

// minReclaim: an automatic compaction that would save less than this many
// tokens is skipped — it would only rewrite the prompt's prefix, which the
// server's prompt cache then misses (seen live: "~34k → ~34k" when what
// filled the prompt was the round being sent, which no compaction elides).
const minReclaim = 4096

// DefaultOverhead is the guess for the fixed part of every prompt —
// hermes-agent's system prompt, its tool schemas, the hands' instructions —
// until a measured run calibrates it.
const DefaultOverhead = 16000

func (k *Keeper) events() []session.Event {
	evs, _ := k.Store.LoadEvents(k.Rec)
	return evs
}

func (k *Keeper) opts(c session.Event) session.TranscriptOpts {
	return session.TranscriptOpts{
		Upto: c.Upto, Collapse: c.Collapse, RoundOutput: k.RoundOutput,
		Output: func(ref int) string { out, _ := k.Store.LoadOutput(k.Rec, ref); return out },
	}
}

// Owned reports whether hermes-hands holds this session's transcript — it
// compacted it at least once.
func (k *Keeper) Owned() bool {
	if k == nil {
		return false
	}
	_, ok := session.LastCompact(k.events())
	return ok
}

// ErrNothing: nothing a compaction could elide yet — no call output and no
// turn that is not elided already.
var ErrNothing = errors.New("nothing to compact yet — no call output or turn to elide")

// keepRounds is how many of the latest rounds a compaction leaves whole when
// that is enough: the replies and results the model is working with.
const keepRounds = 2

// Depth is how far a compaction goes. Outputs are elided first — that loses
// nothing, local_output reads them back. Turns are collapsed to question +
// answer only as a last resort: that drops which call did what (the files a
// collapsed turn read and changed stay named).
type Depth int

const (
	// Auto, before a request that would come too close to the window:
	// outputs elided only as far as the target — half the trigger — needs
	// (every round but the latest keepRounds, then every round); turns
	// collapsed while the prompt would stay above that target too. Deep
	// rather than often: a compaction that leaves half the trigger free buys
	// rounds, and every compaction rewrites the prompt's prefix, which the
	// server's prompt cache then misses.
	Auto Depth = iota
	// Now, asked for (/compact, rpc compact): every output so far elided;
	// turns collapsed only like Auto's.
	Now
	// Aggressive, asked for (/compact-aggressive, rpc compact "aggressive")
	// or after a request was refused for its size: every output elided and
	// every turn but the latest as question + answer.
	Aggressive
)

// Compact compacts the transcript as deep as d says and returns the
// KindCompact event the caller appends to the log; the log itself is never
// rewritten, and the latest turn is never collapsed.
func (k *Keeper) Compact(d Depth) (session.Event, error) {
	evs := k.events()
	last := session.LastTurn(evs)
	if len(evs) == 0 || last == 0 {
		return session.Event{}, ErrNothing
	}
	prev, _ := session.LastCompact(evs)
	before, from := k.before(evs, prev)
	target, collapseAbove := 0, 0 // no window known: outputs elided, nothing collapsed
	if trigger := k.trigger(); trigger > 0 {
		target = trigger / 2
		collapseAbove = target
	}
	top := evs[len(evs)-1].Seq
	c := session.Event{Kind: session.KindCompact, Upto: top, Collapse: prev.Collapse}
	if d == Auto {
		if c.Upto = max(prev.Upto, keepFrom(evs, keepRounds)); k.size(evs, c) > target {
			c.Upto = top
		}
	}
	switch {
	case d == Aggressive:
		c.Collapse = max(c.Collapse, last-1)
	case collapseAbove > 0:
		for k.size(evs, c) > collapseAbove && c.Collapse < last-1 {
			c.Collapse++
		}
	}
	if !elides(evs, prev, c) {
		return session.Event{}, ErrNothing
	}
	c.Tokens, c.Before = k.size(evs, c), before
	if d == Auto && before > 0 && before-c.Tokens < minReclaim {
		return session.Event{}, ErrNothing
	}
	k.ElidedNow = elidedInTurn(evs, last, prev, c)
	cp := &session.ChatCheckpoint{
		N: k.Rec.Compactions + 1, Seq: top, Turn: last, Depth: d.String(),
		Before: c.Before, BeforeFrom: from, After: c.Tokens,
		Elided: elidedRange(evs, c), Collapsed: collapsedRange(c),
		Messages: session.Transcript(evs, k.opts(c)),
	}
	rel, err := k.Store.SaveChatCheckpoint(k.Rec, cp)
	if err != nil {
		return session.Event{}, fmt.Errorf("chat checkpoint: %w", err)
	}
	c.Checkpoint = rel
	c.Text = note(cp, rel)

	k.Rec.Compactions = cp.N
	k.Rec.Context, k.Rec.Measured = c.Tokens, false
	_ = k.Store.Save(k.Rec)
	return c, nil
}

// before is the context a compaction starts from, and where that number
// comes from: the meter after the last run (measured / estimated) or — when
// the meter has nothing yet, a session from before it — the whole log
// rendered in full ("log"), more than hermes-agent ever held of it.
func (k *Keeper) before(evs []session.Event, prev session.Event) (int, string) {
	switch {
	case k.Rec.Context > 0 && k.Rec.Measured:
		return k.Rec.Context, "measured"
	case k.Rec.Context > 0:
		return k.Rec.Context, "estimated"
	case prev.Kind == session.KindCompact:
		return k.size(evs, prev), "estimated"
	}
	return k.size(evs, prev), "log"
}

// String is the depth's name in a checkpoint: auto | now | aggressive.
func (d Depth) String() string {
	return [...]string{"auto", "now", "aggressive"}[d]
}

// elides reports whether compaction c elides more than prev did: a call
// output (one worth a stub) or a turn more. Replies alone are not worth one —
// the outputs are what fills a hands session.
func elides(evs []session.Event, prev, c session.Event) bool {
	if c.Collapse > prev.Collapse {
		return true
	}
	for _, e := range evs {
		if e.Kind == session.KindResult && e.Bytes >= session.ElideMin && e.Seq > prev.Upto && e.Seq <= c.Upto {
			return true
		}
	}
	return false
}

// size estimates the prompt with compaction c in effect.
func (k *Keeper) size(evs []session.Event, c session.Event) int {
	return k.overhead() + tokensOf(history(session.Transcript(evs, k.opts(c))))
}

// keepFrom is the Seq up to which a compaction elides so that the latest n
// replies — and what came after them — stay whole. A log without replies
// (older sessions) cannot tell its rounds apart: everything is elided.
func keepFrom(evs []session.Event, n int) int {
	seen := 0
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == session.KindReply {
			if seen++; seen == n {
				return evs[i].Seq - 1
			}
		}
	}
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

// elidedRange is "#1–#212": the outputs compaction c sends as lines.
func elidedRange(evs []session.Event, c session.Event) string {
	lo, hi := 0, 0
	for _, e := range evs {
		if e.Kind == session.KindResult && e.Bytes >= session.ElideMin && e.Seq <= c.Upto {
			if lo == 0 {
				lo = e.Ref
			}
			hi = e.Ref
		}
	}
	if hi == 0 {
		return ""
	}
	return fmt.Sprintf("#%d–#%d", lo, hi)
}

// elidedInTurn is "#6–#8": the outputs of turn that compaction c newly sends
// as lines (prev had not), or "".
func elidedInTurn(evs []session.Event, turn int, prev, c session.Event) string {
	lo, hi := 0, 0
	for _, e := range evs {
		if e.Kind == session.KindResult && e.Turn == turn && e.Bytes >= session.ElideMin && e.Seq > prev.Upto && e.Seq <= c.Upto {
			if lo == 0 {
				lo = e.Ref
			}
			hi = e.Ref
		}
	}
	switch {
	case hi == 0:
		return ""
	case lo == hi:
		return fmt.Sprintf("#%d", lo)
	}
	return fmt.Sprintf("#%d–#%d", lo, hi)
}

// collapsedRange is "turns 1–5": the turns compaction c sends as question +
// answer.
func collapsedRange(c session.Event) string {
	switch c.Collapse {
	case 0:
		return ""
	case 1:
		return "turn 1"
	}
	return fmt.Sprintf("turns 1–%d", c.Collapse)
}

// note is the divider line the terminal and the session log show for a
// compaction.
func note(cp *session.ChatCheckpoint, rel string) string {
	from := "~" + kilo(cp.Before)
	if cp.BeforeFrom == "log" { // never what Hermes held — the whole log in full
		from = "log ~" + kilo(cp.Before)
	}
	s := fmt.Sprintf("compacted #%d · %s → ~%s tokens", cp.N, from, kilo(cp.After))
	if cp.Depth == Aggressive.String() {
		s = fmt.Sprintf("compacted #%d (aggressive) · %s → ~%s tokens", cp.N, from, kilo(cp.After))
	}
	if cp.Elided != "" {
		s += " · outputs " + cp.Elided + " as lines"
	}
	if cp.Collapsed != "" {
		s += " · " + cp.Collapsed + " as question + answer"
	}
	return s + " · " + rel
}

// kilo is "48k" / "950".
func kilo(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}
