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
	"regexp"
	"strconv"

	"hermes-hands/internal/api"
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
}

// DefaultOverhead is the guess for the fixed part of every prompt —
// hermes-agent's system prompt, its tool schemas, the hands' instructions —
// until a measured run calibrates it.
const DefaultOverhead = 16000

// Tokens estimates the tokens of s: 3 bytes a token, on the high side for
// prose (about 4) and about right for code and JSON — the meter would rather
// compact a little early than send a request that does not fit.
func Tokens(s string) int { return (len(s) + 2) / 3 }

// tokensOf estimates a transcript: its text plus a few tokens of chat
// template per message.
func tokensOf(h []api.Message) int {
	n := 0
	for _, m := range h {
		n += Tokens(m.Content) + 4
	}
	return n
}

func (k *Keeper) overhead() int {
	if k.Rec.Overhead > 0 {
		return k.Rec.Overhead
	}
	return DefaultOverhead
}

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

// History is the transcript to send with the next run — nil while
// hermes-agent holds it. The operator's message or the results the loop is
// about to send are already in the log; they go as the run's input, so a
// trailing user message is left out here.
func (k *Keeper) History() []api.Message {
	if k == nil {
		return nil
	}
	evs := k.events()
	c, ok := session.LastCompact(evs)
	if !ok {
		return nil
	}
	return history(session.Transcript(evs, k.opts(c)))
}

func history(msgs []session.Msg) []api.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs = msgs[:n-1]
	}
	h := make([]api.Message, 0, len(msgs))
	for _, m := range msgs {
		h = append(h, api.Message{Role: m.Role, Content: m.Content})
	}
	return h
}

// Predict estimates the prompt of the next request: the context after the
// last run plus msg, and — once hermes-hands holds the transcript — the fixed
// part plus that transcript plus msg; the larger of the two, so a refused
// run (Overflow) is compacted first either way. 0 = unknown (nothing
// measured yet).
func (k *Keeper) Predict(msg string, history []api.Message) int {
	if k == nil {
		return 0
	}
	p := 0
	if k.Rec.Context > 0 {
		p = k.Rec.Context + Tokens(msg)
	}
	if history != nil {
		p = max(p, k.overhead()+tokensOf(history)+Tokens(msg))
	}
	return p
}

// Due reports whether a request of predicted tokens should be compacted
// first.
func (k *Keeper) Due(predicted int) bool {
	return k != nil && k.Window > 0 && k.At > 0 && predicted*100 >= k.Window*k.At
}

// Observe updates the meter after a completed run: res is its result, msg
// and history what was sent, predicted what Predict said.
func (k *Keeper) Observe(res api.AskResult, msg string, history []api.Message, predicted int) {
	if k == nil {
		return
	}
	reply := Tokens(res.Text)
	switch {
	case res.Prompt > 0 && res.HermesTools == 0:
		// one model call: the prompt it saw, measured
		switch {
		case history != nil:
			k.Rec.Overhead = max(res.Prompt-tokensOf(history)-Tokens(msg), 0)
		case k.Rec.Turns == 0: // the session's first run: nothing but msg in the conversation
			k.Rec.Overhead = max(res.Prompt-Tokens(msg), 0)
		}
		k.Rec.Context, k.Rec.Measured = res.Prompt+reply, true
	case predicted > 0:
		k.Rec.Context, k.Rec.Measured = predicted+reply, false
	}
}

// overflowRe matches a provider's or hermes-agent's "the prompt does not fit
// the context window" — wordings differ by model server.
var overflowRe = regexp.MustCompile(`(?i)context.{0,60}(no room|exceed|too (long|large|many)|overflow|maximum|window|limit)|(no room|exceed|too long|overflow|maximum).{0,60}context|context_(overflow|length_exceeded|compression_exhausted)`)

// promptRe finds the prompt's size in such an error ("prompt (150000
// tokens)", "you requested 150000 tokens").
var promptRe = regexp.MustCompile(`(\d{4,8}) tokens`)

// IsOverflow reports whether a failed run's error says the conversation no
// longer fits the model's context window.
func IsOverflow(reason string) bool { return overflowRe.MatchString(reason) }

// Overflow records a run refused for its size: the meter goes to what the
// error names (or the full window), so the next request is compacted first.
func (k *Keeper) Overflow(reason string) {
	if k == nil {
		return
	}
	n := k.Window
	if m := promptRe.FindStringSubmatch(reason); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			n = v
		}
	}
	if n > k.Rec.Context {
		k.Rec.Context, k.Rec.Measured = n, true
		_ = k.Store.Save(k.Rec)
	}
}

// Meter is the meter's reading for the working line and /session.
type Meter struct {
	Tokens, Window int
	Measured       bool
}

// Meter is the context after the last run (0 tokens: nothing known yet).
func (k *Keeper) Meter() Meter {
	if k == nil {
		return Meter{}
	}
	return Meter{Tokens: k.Rec.Context, Window: k.Window, Measured: k.Rec.Measured}
}

// ErrNothing: nothing a compaction could elide yet — no call output and no
// turn that is not elided already.
var ErrNothing = errors.New("nothing to compact yet — no call output or turn to elide")

// keepRounds is how many of the latest rounds a compaction leaves whole when
// that is enough: the replies and results the model is working with.
const keepRounds = 2

// Compact compacts the transcript and returns the KindCompact event the
// caller appends to the log; the log itself is never rewritten. Before a
// request (auto) it shrinks only as far as needed to reach its target — half
// the share At of the Window: every round but the latest keepRounds elided,
// then every round, then the oldest turns as question + answer (never the
// latest turn). Asked for (/compact, rpc) it elides every output so far right
// away, then collapses turns while still above the target (all but the
// latest when no window is known).
func (k *Keeper) Compact(auto bool) (session.Event, error) {
	evs := k.events()
	last := session.LastTurn(evs)
	if len(evs) == 0 || last == 0 {
		return session.Event{}, ErrNothing
	}
	prev, _ := session.LastCompact(evs)
	before := k.Rec.Context // the last run's, measured when it could be
	if before == 0 {
		before = k.size(evs, prev)
	}
	target := 0
	if k.Window > 0 && k.At > 0 {
		target = k.Window * k.At / 200
	}
	top := evs[len(evs)-1].Seq
	c := session.Event{Kind: session.KindCompact, Upto: top, Collapse: prev.Collapse}
	if auto {
		if c.Upto = max(prev.Upto, keepFrom(evs, keepRounds)); k.size(evs, c) > target {
			c.Upto = top
		}
	}
	for k.size(evs, c) > target && c.Collapse < last-1 {
		c.Collapse++
	}
	if !elides(evs, prev, c) {
		return session.Event{}, ErrNothing
	}
	c.Tokens, c.Before = k.size(evs, c), before
	c.Text = note(evs, c)

	k.Rec.Compactions++
	k.Rec.Context, k.Rec.Measured = c.Tokens, false
	_ = k.Store.Save(k.Rec)
	return c, nil
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

// note is the line the operator sees for compaction c.
func note(evs []session.Event, c session.Event) string {
	lo, hi := 0, 0
	for _, e := range evs {
		if e.Kind == session.KindResult && e.Bytes >= session.ElideMin && e.Seq <= c.Upto {
			if lo == 0 {
				lo = e.Ref
			}
			hi = e.Ref
		}
	}
	s := fmt.Sprintf("compacted: ~%s → ~%s tokens", kilo(c.Before), kilo(c.Tokens))
	if hi > 0 {
		s += fmt.Sprintf(" · outputs #%d–#%d elided (local_output reads them back)", lo, hi)
	}
	if c.Collapse > 0 {
		s += fmt.Sprintf(" · turns 1–%d as question + answer", c.Collapse)
	}
	return s + " · Hermes now reads hermes-hands' transcript"
}

// kilo is "48k" / "950".
func kilo(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}
