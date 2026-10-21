package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Log records an open session: every event of a turn is stamped with the
// turn and appended to events.jsonl, a result's output goes to out/<ref>.txt.
// Rendering is not its job — ui.Show draws an event, live and on replay.
type Log struct {
	store *Store
	Rec   *Record
	turn  int // current operator turn; events of turn N carry Turn N
}

// OpenLog loads rec's events and positions a log after them.
func (s *Store) OpenLog(rec *Record) (*Log, []Event) {
	evs, err := s.LoadEvents(rec)
	if err != nil {
		s.warnf("events: %v", err)
	}
	return &Log{store: s, Rec: rec, turn: LastTurn(evs)}, evs
}

// BeginTurn starts the next operator turn.
func (l *Log) BeginTurn() { l.turn++ }

// Add stamps ev with the current turn, stores a result's output separately
// (the event keeps only its size) and appends ev. It returns the stamped
// event, Out still attached for the live render. A storage failure warns —
// the turn itself goes on.
func (l *Log) Add(ev Event) Event {
	ev.Turn = l.turn
	if ev.Kind == KindResult {
		ev.Bytes = len(ev.Out)
		ev.Lines = countLines(ev.Out)
		if ev.Bytes >= ElideMin {
			ev.Hash = OutputHash(ev.Out)
		}
		if err := l.store.SaveOutput(l.Rec, ev.Ref, ev.Out); err != nil {
			l.store.warnf("events: output #%d: %v", ev.Ref, err)
		}
	}
	if err := l.store.AppendEvent(l.Rec, &ev); err != nil {
		l.store.warnf("events: %v", err)
	}
	return ev
}

// OutputHash identifies an output: two results with the same hash carried
// the same bytes.
func OutputHash(out string) string {
	sum := sha256.Sum256([]byte(out))
	return hex.EncodeToString(sum[:8])
}

// SameOutput stands in for an output Hermes has verbatim already, earlier in
// the conversation: seen live, a long turn read the same file sections again
// and again, each time in full.
func SameOutput(ref int) string {
	return fmt.Sprintf(`[same output as #%d — above; local_output {"ref":%d} reads it again]`, ref, ref)
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
