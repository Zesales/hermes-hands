package dispatch

// planner is an Approver that knows plan mode (prompt.TTYApprover).
type planner interface{ Planning() bool }

// Planning reports whether the operator is in plan mode (/approve plan):
// Hermes looks, the hands write nothing.
func (d *Dispatcher) Planning() bool {
	p, ok := d.Approver.(planner)
	return ok && p.Planning()
}

// notWritten is a write or edit refused in plan mode — Hermes is told why,
// and what to do with the change instead.
const notWritten = "(plan mode — not written: put this change into the plan in your final)"
