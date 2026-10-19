package ui

import (
	"fmt"
	"time"

	"hermes-hands/internal/session"
)

// Show draws one event of a session's log. Live turns and reopened sessions
// both go through it, so a replay looks exactly like the turn did. out is a
// result's output — in hand live, read back from out/<ref>.txt on replay.
// An approval event is drawn as the answered prompt.
func (u *UI) Show(ev session.Event, out string) {
	switch ev.Kind {
	case session.KindUser:
		u.Line("")
		u.Rule()
		u.You(ev.Text)
	case session.KindCall:
		head, more := Summarize(ev.Tool, ev.Args)
		u.CallStart(ev.Ref, ev.Tool, head, more)
	case session.KindResult:
		exit := 0
		if ev.Exit != nil {
			exit = *ev.Exit
		}
		u.CallDone(ev.Ref, ev.Tool, exit, ms(ev.Ms), out)
	case session.KindHermesTool:
		u.HermesTool(ev.Tool, ev.Text, ev.Action)
	case session.KindApproval:
		u.Approval(ev.Text, ev.Detail, ev.Action)
	case session.KindNote:
		u.Note(ev.Text)
	case session.KindAnswer:
		u.Line("")
		u.Answer(ev.Text)
	case session.KindTurnEnd:
		u.TurnDone(ms(ev.Ms), ev.Outcome, ev.Limit, ms(ev.LocalMs), ms(ev.WaitMs), ev.Rounds, ev.Calls)
	}
}

// Replay draws the last n turns of a session's events (all when n <= 0) and
// says how many earlier ones it left out. output reads a result's stored
// output by its #ref; id names the session when it has no history yet.
func (u *UI) Replay(evs []session.Event, n int, id string, output func(ref int) string) {
	turns := session.Turns(evs)
	if len(turns) == 0 {
		u.Note("no history yet for " + id)
		u.Line("")
		return
	}
	if n > 0 && len(turns) > n {
		u.Note(fmt.Sprintf("… %d earlier turn(s) — /history shows all", len(turns)-n))
		turns = turns[len(turns)-n:]
	}
	for _, t := range turns {
		for _, ev := range t {
			out := ""
			if ev.Kind == session.KindResult {
				out = output(ev.Ref)
			}
			u.Show(ev, out)
		}
	}
}

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }
