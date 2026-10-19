// Package turn runs one operator turn — the same way for every front end.
// The Runner owns what a turn is: the event log, the silence watchdog, the
// run id that Ctrl-C stops, the live answer preview, the answer and the
// footer. A Frontend only shows it: the REPL draws the terminal, --rpc
// writes JSON lines.
package turn

import (
	"hermes-hands/internal/loop"
	"hermes-hands/internal/session"
)

// Frontend shows a turn as it happens.
type Frontend interface {
	// Show gets every recorded event, in order: the operator's message, a
	// hands call before it runs and its result, Hermes' own tool events, the
	// answer (left out when the preview already showed it), the footer.
	Show(ev session.Event)
	// Preview streams the answer text as it arrives. EndPreview closes what
	// was streamed — at the next run and at the end of the turn — and says
	// whether anything was.
	Preview(text string)
	EndPreview() bool
	// Quiet says the turn is no longer waiting: stop a "working" indicator.
	Quiet()
}

// ProgressFrontend is a Frontend that also shows where the turn stands
// against the operator's budget (round x/y, calls x/y) while it runs. Optional.
type ProgressFrontend interface {
	Progress(p loop.Progress)
}
