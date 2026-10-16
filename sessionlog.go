package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

// sessionLog is the open session's event log. Every terminal line of a turn
// goes through add (store it) and show (render it); a reopened session is
// rendered by replaying the stored events through the same show — so it looks
// exactly like it ran. With ui == nil (--rpc) it only records.
type sessionLog struct {
	store *session.Store
	ui    *ui.UI
	rec   *session.Record
	turn  int // current operator turn; events of turn N carry Turn N
}

// replayTurns is how many turns a reopened session shows; /history shows all.
const replayTurns = 5

// openLog loads rec's events and positions the log after them.
func openLog(store *session.Store, u *ui.UI, rec *session.Record) (*sessionLog, []session.Event) {
	evs, err := store.LoadEvents(rec)
	if err != nil {
		warnf("events: %v", err)
	}
	return &sessionLog{store: store, ui: u, rec: rec, turn: session.LastTurn(evs)}, evs
}

func (l *sessionLog) beginTurn() { l.turn++ }

// add stamps ev with the current turn, stores a result's output separately
// (out/<ref>.txt; the event keeps only its size) and appends ev to the log.
// A storage failure warns — the turn itself goes on.
func (l *sessionLog) add(ev session.Event) session.Event {
	ev.Turn = l.turn
	if ev.Kind == session.KindResult {
		ev.Bytes = len(ev.Out)
		ev.Lines = countLines(ev.Out)
		if err := l.store.SaveOutput(l.rec, ev.Ref, ev.Out); err != nil {
			warnf("events: output #%d: %v", ev.Ref, err)
		}
	}
	if err := l.store.AppendEvent(l.rec, &ev); err != nil {
		warnf("events: %v", err)
	}
	return ev
}

// show renders one event. out is a result's output (live: in hand; replay:
// read back from out/<ref>.txt).
func (l *sessionLog) show(ev session.Event, out string) {
	u := l.ui
	if u == nil {
		return
	}
	switch ev.Kind {
	case session.KindUser:
		u.Line("")
		u.Rule()
		u.You(ev.Text)
	case session.KindCall:
		head, more := ui.Summarize(ev.Tool, ev.Args)
		u.CallStart(ev.Ref, ev.Tool, head, more)
	case session.KindResult:
		exit := 0
		if ev.Exit != nil {
			exit = *ev.Exit
		}
		u.CallDone(ev.Ref, ev.Tool, exit, time.Duration(ev.Ms)*time.Millisecond, out)
	case session.KindHermesTool:
		u.HermesTool(ev.Tool, ev.Text, ev.Action)
	case session.KindNote:
		u.Note(ev.Text)
	case session.KindAnswer:
		u.Line("")
		u.Answer(ev.Text)
	case session.KindTurnEnd:
		u.TurnDone(time.Duration(ev.Ms)*time.Millisecond, ev.Outcome, ev.Limit)
	}
}

// replay renders the last n turns of evs (all when n <= 0), saying how many
// earlier turns it left out.
func (l *sessionLog) replay(evs []session.Event, n int) {
	if l.ui == nil {
		return
	}
	turns := session.Turns(evs)
	if len(turns) == 0 {
		l.ui.Note("no history yet for " + l.rec.ID)
		l.ui.Line("")
		return
	}
	if n > 0 && len(turns) > n {
		l.ui.Note(fmt.Sprintf("… %d earlier turn(s) — /history shows all", len(turns)-n))
		turns = turns[len(turns)-n:]
	}
	for _, t := range turns {
		for _, ev := range t {
			out := ""
			if ev.Kind == session.KindResult {
				out, _ = l.store.LoadOutput(l.rec, ev.Ref)
			}
			l.show(ev, out)
		}
	}
}

// showOutput prints the stored full output of call #ref (`/out <ref>`).
func (l *sessionLog) showOutput(arg string) {
	ref, err := strconv.Atoi(arg)
	if err != nil || ref <= 0 {
		l.ui.Note("usage: /out <n> — n is the #number of a call")
		l.ui.Line("")
		return
	}
	out, err := l.store.LoadOutput(l.rec, ref)
	if err != nil {
		l.ui.Note(fmt.Sprintf("no stored output for #%d in this session", ref))
		l.ui.Line("")
		return
	}
	l.ui.OutputBlock(ref, out)
	l.ui.Line("")
}

func countLines(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	if len(s) > 0 && s[len(s)-1] != '\n' {
		n++
	}
	return n
}
