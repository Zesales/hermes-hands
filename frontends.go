package main

import (
	"fmt"

	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

// replFront shows a turn in the terminal: every event through ui.Show (the
// same drawing a replay uses), the "working" line from the operator's message
// until the answer starts streaming, the running call's name on it.
type replFront struct {
	ui   *ui.UI
	stop func() // stops the "working" line; set by the operator's message
}

func (f *replFront) Show(ev session.Event) {
	f.ui.Show(ev, ev.Out)
	switch ev.Kind {
	case session.KindUser:
		f.stop = f.ui.StartWorking()
	case session.KindCall:
		f.ui.SetActivity(fmt.Sprintf("#%d %s", ev.Ref, ev.Tool))
	case session.KindResult:
		f.ui.SetActivity("")
	}
}

// Preview: the first streamed chunk means output is flowing — retire the
// "working" line.
func (f *replFront) Preview(s string) {
	f.quiet()
	f.ui.Delta(s)
}

func (f *replFront) EndPreview() bool { return f.ui.StreamDone() }

func (f *replFront) Quiet() {
	f.quiet()
	f.ui.SetActivity("")
}

func (f *replFront) quiet() {
	if f.stop != nil {
		f.stop()
	}
}

// rpcFront shows a turn as --rpc JSON lines: a hands call before it runs
// (call) and its result (tool), Hermes' own tool events (hermes_tool), the
// answer text as it streams (delta). The answer itself — or why there is
// none — is written by runRPC from the turn's result.
type rpcFront struct {
	emit  func(map[string]any)
	id    any            // the request's id, echoed on every message
	heads map[int]string // ref -> the call's preview; a result event carries no args
}

func (f *rpcFront) Show(ev session.Event) {
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

func (f *rpcFront) Preview(s string) {
	f.emit(map[string]any{"type": "delta", "id": f.id, "text": s})
}

func (f *rpcFront) EndPreview() bool { return false }
func (f *rpcFront) Quiet()           {}
