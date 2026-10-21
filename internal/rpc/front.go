package rpc

import (
	"hermes-hands/internal/loop"
	"hermes-hands/internal/session"
	"hermes-hands/internal/ui"
)

// front shows a turn as --rpc JSON lines: Hermes' sentence beside a round's
// calls (say), a hands call before it runs (call) and its result (tool), a
// call of an envelope the hands did not run (not_run), Hermes' own tool
// events (hermes_tool), the
// answer text as it streams (delta), a terminal note (note: a budget
// countdown, a compaction) and where the turn stands (progress). The answer itself — or why there is
// none — is written by Run from the turn's result.
type front struct {
	emit  func(map[string]any)
	id    any            // the request's id, echoed on every message
	heads map[int]string // ref -> the call's preview; a result event carries no args
}

func (f *front) Show(ev session.Event) {
	switch ev.Kind {
	case session.KindCall:
		f.heads[ev.Ref], _ = ui.Summarize(ev.Tool, ev.Args)
		f.emit(map[string]any{"type": "call", "id": f.id, "ref": ev.Ref, "tool": ev.Tool, "preview": f.heads[ev.Ref]})
	case session.KindResult:
		exit := 0
		if ev.Exit != nil {
			exit = *ev.Exit
		}
		f.emit(map[string]any{"type": "tool", "id": f.id, "ref": ev.Ref, "tool": ev.Tool, "preview": f.heads[ev.Ref], "exit": exit, "lines": ev.Lines})
	case session.KindHermesTool:
		f.emit(map[string]any{"type": "hermes_tool", "id": f.id, "tool": ev.Tool, "preview": ev.Text, "action": ev.Action})
	case session.KindAdded:
		f.emit(map[string]any{"type": "added", "id": f.id, "text": ev.Text})
	case session.KindSay:
		f.emit(map[string]any{"type": "say", "id": f.id, "text": ev.Text})
	case session.KindNotRun:
		f.emit(map[string]any{"type": "not_run", "id": f.id, "call_id": ev.ID, "tool": ev.Tool, "why": ev.Text})
	case session.KindNote, session.KindCompact:
		f.emit(map[string]any{"type": "note", "id": f.id, "text": ev.Text})
	}
}

// Progress: where the turn stands against the operator's budget (a max of
// 0 = no limit) — at every request to Hermes and after every call — and, once
// known, the estimated context of the request in flight against the window.
func (f *front) Progress(p loop.Progress) {
	m := map[string]any{"type": "progress", "id": f.id, "round": p.Round, "max_rounds": p.MaxRounds,
		"calls": p.Calls, "max_calls": p.MaxCalls}
	if p.Context > 0 {
		m["context"], m["window"] = p.Context, p.Window
	}
	f.emit(m)
}

func (f *front) Preview(s string) {
	f.emit(map[string]any{"type": "delta", "id": f.id, "text": s})
}

func (f *front) EndPreview() bool { return false }
func (f *front) Quiet()           {}
