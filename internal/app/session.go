package app

import (
	"context"
	"time"

	"hermes-hands/internal/session"
)

// OpenSession makes rec the open session: loads its event log, continues the
// call numbering, replays the last turns into the terminal when show is set,
// and prepares the first turn when the server lost the conversation.
func (a *App) OpenSession(rec *session.Record, show bool) *session.Log {
	a.ResetRuntime() // a session starts from the configured settings, never the last one's switches
	log, evs := a.Store.OpenLog(rec)
	a.Loop.Ref = session.LastRef(evs)
	if show && len(evs) > 0 {
		a.UI.Replay(evs, ReplayTurns, rec.ID, StoredOutput(a.Store, rec))
	}
	a.prepareResume(rec, evs)
	return log
}

// ReplayTurns is how many turns a reopened session shows; /history shows all.
const ReplayTurns = 5

// StoredOutput reads a call's stored output back for a replay.
func StoredOutput(st *session.Store, rec *session.Record) func(ref int) string {
	return func(ref int) string {
		out, _ := st.LoadOutput(rec, ref)
		return out
	}
}

// prepareResume: hermes-agent holds the conversation the model sees; when it
// still has this session there is nothing to do. When it lost it (404 / empty
// — a Hermes update, a retention purge), the first turn carries a recap of
// the local event log, so the brain continues instead of starting blind.
// Nothing is ever synced from the server into the local log.
func (a *App) prepareResume(rec *session.Record, evs []session.Event) {
	a.Loop.ResumeNote = ""
	if rec.HermesSessionID == "" || rec.Turns == 0 {
		return // nothing has been said in this session yet
	}
	if idle := session.IdleFor(rec, time.Now()); idle > session.StaleAfter {
		Logf("session %s: idle %s — model prompt caches (gateway/hermes-agent/Claude-style) likely expired; the first turn re-reads the full conversation (token cost)", rec.ID, idle.Round(time.Minute))
	}
	if si, err := a.Client.Sessions().Info(context.Background(), rec.HermesSessionID); err == nil && si.Messages > 0 {
		return
	}
	if r := session.Recap(evs); r != "" {
		a.Loop.ResumeNote = r
		Logf("session %s: hermes-agent no longer has this conversation — the first turn carries a recap of the local history", rec.ID)
	}
}
