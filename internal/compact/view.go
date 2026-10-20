package compact

import (
	"fmt"
	"strings"

	"hermes-hands/internal/api"
	"hermes-hands/internal/session"
)

// History is the transcript to send with the next run — nil while
// hermes-agent holds it: the latest chat checkpoint and every event after
// it. The operator's message or the results the loop is about to send are
// already in the log; they go as the run's input, so a trailing user message
// is left out here.
func (k *Keeper) History() []api.Message {
	if k == nil {
		return nil
	}
	cp, since, ok := k.current()
	if !ok {
		return nil
	}
	return history(session.Join(cp, since))
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

// current is what Hermes reads: the latest checkpoint's messages and those
// rendered from the events after it. ok is false while hermes-agent holds the
// transcript. A checkpoint that cannot be read is rendered from the log
// again, with its compaction's bounds.
func (k *Keeper) current() (cp, since []session.Msg, ok bool) {
	evs := k.events()
	c, ok := session.LastCompact(evs)
	if !ok {
		return nil, nil, false
	}
	if c.Checkpoint != "" {
		if chk, err := k.Store.LoadChatCheckpoint(k.Rec, c.Checkpoint); err == nil {
			verbatim := k.opts(session.Event{})
			return chk.Messages, session.Transcript(session.After(evs, chk.Seq), verbatim), true
		}
	}
	return session.Transcript(evs, k.opts(c)), nil, true
}

// View is what Hermes reads now, for /context and rpc context.
type View struct {
	Owned      bool          // false: hermes-agent holds the transcript (not compacted yet)
	Checkpoint string        // the latest chat checkpoint, relative to the session directory
	N          int           // its compaction number
	Messages   []session.Msg // the checkpoint's messages
	Since      []session.Msg // what came after it
	Meter      Meter
}

// View is the current view.
func (k *Keeper) View() View {
	v := View{Meter: k.Meter()}
	cp, since, ok := k.current()
	if !ok {
		return v
	}
	v.Owned, v.Messages, v.Since, v.N = true, cp, since, k.Rec.Compactions
	if c, ok := session.LastCompact(k.events()); ok {
		v.Checkpoint = c.Checkpoint
	}
	return v
}

// Describe is /context: the meter, where Hermes reads from, and with full
// every message of it — the checkpoint's, then what came after it.
func Describe(v View, full bool) string {
	var b strings.Builder
	m := v.Meter
	switch {
	case m.Tokens == 0:
		b.WriteString("  context      : not measured yet (after the first run)\n")
	case m.Window > 0:
		fmt.Fprintf(&b, "  context      : ~%s of %s tokens (%d%%) after the last run, %s\n",
			kilo(m.Tokens), kilo(m.Window), m.Tokens*100/m.Window, measured(m.Measured))
	default:
		fmt.Fprintf(&b, "  context      : ~%s tokens after the last run, %s\n", kilo(m.Tokens), measured(m.Measured))
	}
	if !v.Owned {
		b.WriteString("  hermes reads : its own stored transcript — not compacted yet (/compact)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  hermes reads : %s (compaction #%d, %d messages) + %d after it\n",
		orRendered(v.Checkpoint), v.N, len(v.Messages), len(v.Since))
	if full {
		section := func(name string, msgs []session.Msg) {
			fmt.Fprintf(&b, "\n  ──── %s ────\n", name)
			for _, msg := range msgs {
				fmt.Fprintf(&b, "  [%s]\n", msg.Role)
				for _, l := range strings.Split(msg.Content, "\n") {
					b.WriteString("    " + l + "\n")
				}
			}
		}
		section(orRendered(v.Checkpoint), v.Messages)
		if len(v.Since) > 0 {
			section("after it", v.Since)
		}
	}
	return b.String()
}

func measured(ok bool) string {
	if ok {
		return "measured"
	}
	return "estimated"
}

func orRendered(checkpoint string) string {
	if checkpoint == "" {
		return "the log, rendered"
	}
	return checkpoint
}

// Verbatim maps each output Hermes still has verbatim in its conversation —
// a result after the last compaction's elision, worth a hash (ElideMin bytes
// and up) — to the earliest #ref that carried it; the loop sends a repeat of
// it as a reference (session.SameOutput). nil without a keeper.
func (k *Keeper) Verbatim() map[string]int {
	if k == nil {
		return nil
	}
	evs := k.events()
	last, _ := session.LastCompact(evs)
	m := map[string]int{}
	for _, e := range evs {
		if e.Kind == session.KindResult && e.Hash != "" && e.Seq > last.Upto {
			if _, ok := m[e.Hash]; !ok {
				m[e.Hash] = e.Ref
			}
		}
	}
	return m
}
