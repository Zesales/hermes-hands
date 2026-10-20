package app

import "hermes-hands/internal/prompt"

// Runtime switches — /approve, /yolo, /debug, the rpc approvals and debug requests —
// change the running process only. They are written into Cfg (source
// "runtime") so /config shows what is in effect, never into the config file,
// and ResetRuntime drops them whenever a session is opened: every session, a
// resumed one too, starts from the configured value (default < config < env
// < flag).

const (
	keyApprove = "HERMES_HANDS_APPROVE"
	keyDebug   = "HERMES_HANDS_DEBUG"
)

// startup is what the runtime switches reset to.
type startup struct {
	approve, approveSrc string
	debug               bool
	debugSrc            string
}

func (a *App) rememberStartup() {
	a.start = &startup{
		approve: a.Cfg.Approve, approveSrc: a.Cfg.Sources[keyApprove],
		debug: a.Cfg.Debug, debugSrc: a.Cfg.Sources[keyDebug],
	}
}

// SetApprovals switches the hands' approvals (ask | plan | auto | never) for this
// process. Back at the start-up value it is no runtime switch any more.
func (a *App) SetApprovals(mode string) {
	if ta, ok := a.approver(); ok {
		ta.Mode = mode
	}
	a.Cfg.Approve = mode
	if a.start != nil && mode == a.start.approve {
		a.setSource(keyApprove, a.start.approveSrc)
	} else {
		a.setSource(keyApprove, "runtime")
	}
}

// Approvals is the approvals mode in effect: ask | plan | auto | never.
func (a *App) Approvals() string {
	if ta, ok := a.approver(); ok {
		return ta.Mode
	}
	return a.Cfg.Approve
}

// NoteDebug records a /debug switch so /config shows it; the trace itself is
// SetDebug.
func (a *App) NoteDebug(on bool) {
	a.Cfg.Debug = on
	if a.start != nil && on == a.start.debug {
		a.setSource(keyDebug, a.start.debugSrc)
	} else {
		a.setSource(keyDebug, "runtime")
	}
}

// ResetRuntime drops every runtime switch: approvals and debug go back to
// their configured start-up values.
func (a *App) ResetRuntime() {
	if a.start == nil {
		return
	}
	a.SetApprovals(a.start.approve)
	a.NoteDebug(a.start.debug)
}

func (a *App) approver() (*prompt.TTYApprover, bool) {
	if a.Disp == nil {
		return nil, false
	}
	ta, ok := a.Disp.Approver.(*prompt.TTYApprover)
	return ta, ok
}

func (a *App) setSource(key, src string) {
	if a.Cfg.Sources == nil {
		a.Cfg.Sources = map[string]string{}
	}
	if src == "" {
		delete(a.Cfg.Sources, key)
		return
	}
	a.Cfg.Sources[key] = src
}
