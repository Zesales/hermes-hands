package loop

import (
	"sync"

	"hermes-hands/internal/session"
)

// Inbox holds what the operator writes while a turn runs (the REPL's type-
// ahead, rpc "append"): each round's request takes it along, after the
// results, and what the turn did not take becomes the next turn. Safe for
// one writer and the loop at once.
type Inbox struct {
	mu    sync.Mutex
	items []string
}

// Push queues text for the next round.
func (b *Inbox) Push(text string) {
	b.mu.Lock()
	b.items = append(b.items, text)
	b.mu.Unlock()
}

// Take empties the inbox and returns what was in it, oldest first.
func (b *Inbox) Take() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.items
	b.items = nil
	return items
}

// takeAdded moves what the operator wrote while the turn ran into this
// round's request — after the results, marked as theirs, @path attachments
// and all — so Hermes reads it with what it asked for. The round's log
// shows it as "you (added)"; a retry or a stopped run's re-ask sends it again.
func (l *Loop) takeAdded(t *turnState) {
	for _, text := range l.Inbox.Take() {
		withFiles, images := l.attach(text)
		t.send += "\n\n" + session.AddedMark + withFiles
		t.images = append(t.images, images...)
		t.turnlog += "[operator, added] " + text + "\n"
		l.emit(session.Event{Kind: session.KindAdded, Text: l.scrub(text)})
	}
}
