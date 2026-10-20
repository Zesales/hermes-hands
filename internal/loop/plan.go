package loop

// Plan mode (/approve plan): Hermes looks and answers with a plan; the
// hands write nothing (dispatch.Dispatcher.Planning).

const (
	planModeNote = "\n\n[plan mode — the operator wants a plan first. Look as far as you need: local_read and" +
		" local_output run, local_shell asks the operator for each command — use it for commands that only read." +
		" Change nothing: local_write / local_edit are not applied. End with a final that lays out the plan: what" +
		" you found, each change file by file, the commands to run. The operator switches to ask or auto to have" +
		" it carried out.]"
	planOverNote = "\n\n[plan mode is over: local_write / local_edit apply again. Carry out what the operator" +
		" asks — the plan above, if that is what they approve.]"
)

// planNote tells Hermes at the start of a turn that the operator is in plan
// mode (/approve plan) — or, once, that a plan phase is over.
func (l *Loop) planNote() string {
	planning := l.Dispatch != nil && l.Dispatch.Planning()
	was := l.planned
	l.planned = planning
	switch {
	case planning:
		return planModeNote
	case was:
		return planOverNote
	}
	return ""
}
