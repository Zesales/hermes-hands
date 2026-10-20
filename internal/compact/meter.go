package compact

import (
	"regexp"
	"strconv"

	"hermes-hands/internal/api"
)

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
	t := k.trigger()
	return t > 0 && predicted >= t
}

// Room kept above a request: Hermes' reply, and a buffer for what a run adds
// before it — its own tool calls, the estimate's error. The round's results
// need none: they are part of the request the trigger is checked against.
const (
	replyRoom   = 20 << 10
	replyBuffer = 4 << 10
)

// trigger is the prompt, in tokens, past which a request is compacted first:
// At percent of the Window — never so high that the reply and the buffer no
// longer fit above it, which would cost a refused request and a hard
// compaction (a window too small for even that keeps the share). 0: no
// automatic compaction.
func (k *Keeper) trigger() int {
	if k == nil || k.Window <= 0 || k.At <= 0 {
		return 0
	}
	t := k.Window * k.At / 100
	if room := k.Window - replyRoom - replyBuffer; room >= k.Window/2 && t > room {
		t = room
	}
	return t
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
