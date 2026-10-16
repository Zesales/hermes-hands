package rpc

import (
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

// front shows a turn as --rpc JSON lines: a hands call before it runs
// (call) and its result (tool), Hermes' own tool events (hermes_tool), the
// answer text as it streams (delta). The answer itself — or why there is
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
	}
}

func (f *front) Preview(s string) {
	f.emit(map[string]any{"type": "delta", "id": f.id, "text": s})
}

func (f *front) EndPreview() bool { return false }
func (f *front) Quiet()           {}
