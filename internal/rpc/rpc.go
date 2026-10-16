// Package rpc is --rpc: a persistent JSON-lines session server for an editor
// or plugin. The caller holds one session for the process lifetime and only
// starts a new one on demand, instead of spawning a fresh session per turn
// (which would fragment the central Hermes). One request object per line on stdin; one
// response object per line on stdout; diagnostics go to stderr.
//
// Requests:
//
//	{"id":1,"type":"turn","text":"why is CI red?"}
//	{"id":2,"type":"new"}
//	{"id":3,"type":"use","session":"hh_20260902T101112_abc123"}
//	{"id":4,"type":"check"}
//	{"id":5,"type":"history"}                 the open session's event log
//	{"id":6,"type":"debug","text":"on"}       redacted wire trace on / off / toggle
//
// Responses:
//
//	{"type":"ready","session":"hh_...","cwd":"/repo","version":"X.Y.Z","approvals":"off","hermes_agent":"0.22.2"}
//	                                     (+ "compat":"…" when hermes_agent is outside the supported range)
//	{"type":"call","id":1,"ref":14,"tool":"local_shell","preview":"npm test"}       before it runs
//	{"type":"tool","id":1,"ref":14,"tool":"local_shell","preview":"npm test","exit":0,"lines":12}
//	{"type":"hermes_tool","id":1,"tool":"terminal","preview":"...","action":"steered"}
//	{"type":"answer","id":1,"ok":true,"text":"...","session":"hh_...","hermes_session":"..."}
//	{"type":"session","id":2,"session":"hh_...","hermes_session":"..."}
//	{"type":"check","id":4,"ok":true,"model":"...","base":"...","version":"0.22.2"}   (+ "compat" as above)
//	{"type":"history","id":5,"session":"hh_...","events":[...]}  (session.Event objects)
//	{"type":"debug","id":6,"on":true,"path":".../debug.jsonl"}
//	{"type":"error","id":1,"message":"..."}
//
// Turns are recorded into the session's event log exactly like REPL turns,
// so a session started in the editor reopens in the terminal (and back).
//
// Approvals cannot be prompted over the pipe, so --rpc requires
// HERMES_HANDS_APPROVE=auto (i.e. --yolo) or =never; the editor is expected to
// run its own approval UI before sending a turn.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Zesales/hermes-hands/internal/app"
	"github.com/Zesales/hermes-hands/internal/compat"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/turn"
)

type request struct {
	ID      json.RawMessage `json:"id"`
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Session string          `json:"session"`
}

// Deps is what --rpc needs from outside: how to build the app, and the
// version for the ready message.
type Deps struct {
	NewApp  func() (*app.App, error)
	Version string
}

// Run serves --rpc in session smode ("" or "new" = a fresh one, else an id)
// until stdin closes.
func Run(smode string, d Deps) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	emit := func(v map[string]any) { _ = enc.Encode(v) }

	a, err := d.NewApp()
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}
	defer a.Shell.Stop()

	if !a.Cfg.Configured() {
		emit(map[string]any{"type": "error", "message": "not configured - run `hermes-hands setup`"})
		return 1
	}
	// Preflight like the REPL: it also reads /v1/capabilities, without which
	// the client never streams — and only the SSE stream carries Hermes' tool
	// events (the sandbox guard) and live deltas.
	srv, err := a.Client.Server().Check(context.Background())
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}

	approvals := "never"
	if ta, ok := a.Disp.Approver.(*prompt.TTYApprover); ok {
		switch ta.Mode {
		case "auto":
			approvals = "off"
		case "never":
			approvals = "never"
		default:
			emit(map[string]any{"type": "error", "message": "--rpc needs --yolo (HERMES_HANDS_APPROVE=auto) or HERMES_HANDS_APPROVE=never - interactive approval over the pipe is not supported"})
			return 1
		}
	}
	a.UI = nil // no terminal frames in rpc mode: the event log only records

	rec, err := a.Store.Resolve(smode, a.RepoRoot)
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}
	evlog := a.OpenSession(rec, false)

	ready := map[string]any{
		"type": "ready", "session": rec.ID, "cwd": a.RepoRoot,
		"version": d.Version, "approvals": approvals, "hermes_agent": srv.Version,
	}
	if w := app.CompatWarning(srv.Version); w != "" {
		ready["compat"] = w
	}
	emit(ready)

	// SIGINT cancels the in-flight turn (like the REPL): the turn context, the
	// shell child AND the server run. The caller drives the lifetime by
	// closing stdin. The runner's silence watchdog keeps a gateway that goes
	// quiet from hanging the editor's turn forever.
	runner := &turn.Runner{}
	a.Bind(runner)
	runner.Use(evlog)
	defer runner.CancelOnInterrupt()()

	debugOn := a.Cfg.Debug
	applyDebug := func() { a.SetDebug(rec, debugOn) }
	applyDebug()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			emit(map[string]any{"type": "error", "message": "invalid JSON request"})
			continue
		}
		id := any(nil)
		if len(req.ID) > 0 {
			_ = json.Unmarshal(req.ID, &id)
		}

		switch req.Type {
		case "turn":
			if req.Text == "" {
				emit(map[string]any{"type": "error", "id": id, "message": "turn needs a non-empty text"})
				continue
			}
			res := runner.Run(req.Text, &front{emit: emit, id: id, heads: map[int]string{}})
			switch res.End.Outcome {
			case "timeout":
				emit(map[string]any{"type": "error", "id": id, "message": fmt.Sprintf(
					"turn timed out — no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=%ds)", res.End.Limit)})
			case "interrupted":
				emit(map[string]any{"type": "error", "id": id, "message": "turn interrupted"})
			default:
				emit(map[string]any{
					"type": "answer", "id": id, "ok": res.Out.OK, "text": res.Out.Answer,
					"session": rec.ID, "hermes_session": rec.HermesSessionID,
				})
			}

		case "history":
			evs, e := a.Store.LoadEvents(rec)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			if evs == nil {
				evs = []session.Event{}
			}
			emit(map[string]any{
				"type": "history", "id": id, "session": rec.ID,
				"hermes_session": rec.HermesSessionID, "events": evs,
			})

		case "new":
			nr, e := a.Store.Resolve("new", a.RepoRoot)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			rec = nr
			runner.Use(a.OpenSession(rec, false))
			applyDebug()
			emit(map[string]any{"type": "session", "id": id, "session": rec.ID, "hermes_session": rec.HermesSessionID})

		case "use":
			if req.Session == "" {
				emit(map[string]any{"type": "error", "id": id, "message": "use needs a session id"})
				continue
			}
			nr, e := a.Store.Resolve(req.Session, a.RepoRoot)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			rec = nr
			runner.Use(a.OpenSession(rec, false))
			applyDebug()
			emit(map[string]any{"type": "session", "id": id, "session": rec.ID, "hermes_session": rec.HermesSessionID})

		case "debug":
			switch req.Text {
			case "on", "true":
				debugOn = true
			case "off", "false":
				debugOn = false
			default:
				debugOn = !debugOn
			}
			applyDebug()
			emit(map[string]any{"type": "debug", "id": id, "on": debugOn, "path": a.Store.DebugPath(rec)})

		case "check":
			res, e := a.Client.Server().Check(context.Background())
			if e != nil {
				emit(map[string]any{"type": "check", "id": id, "ok": false, "message": e.Error()})
				continue
			}
			msg := map[string]any{"type": "check", "id": id, "ok": true, "model": res.Model, "base": res.Base, "version": res.Version}
			if n := compat.Note(res.Version); n != "" {
				msg["compat"] = n
			}
			emit(msg)

		case "compact", "compress":
			emit(map[string]any{"type": "compact", "id": id, "ok": false,
				"message": "this gateway exposes no REST compaction endpoint; Hermes compacts automatically"})

		default:
			emit(map[string]any{"type": "error", "id": id, "message": fmt.Sprintf("unknown request type %q", req.Type)})
		}
	}
	return 0
}
