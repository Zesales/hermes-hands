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
//	{"id":7,"type":"approvals","text":"auto"} hands approvals auto | never ("" = report) — a runtime
//	                                          switch: the next new / use starts from config / env / flags
//	{"id":8,"type":"compact"}                 compact the session's chat now (a chat checkpoint, internal/compact)
//	{"id":9,"type":"context"}                 what Hermes reads now
//
// Responses:
//
//	{"type":"ready","session":"hh_...","cwd":"/repo","version":"X.Y.Z","approvals":"off","hermes_agent":"0.22.2"}
//	                                     (+ "compat":"…" when hermes_agent is outside the supported range)
//	{"type":"call","id":1,"ref":14,"tool":"local_shell","preview":"npm test"}       before it runs
//	{"type":"tool","id":1,"ref":14,"tool":"local_shell","preview":"npm test","exit":0,"lines":12}
//	{"type":"hermes_tool","id":1,"tool":"terminal","preview":"...","action":"steered"}
//	{"type":"progress","id":1,"round":3,"max_rounds":25,"calls":12,"max_calls":100}  the turn's budget (max 0 = no limit)
//	                                     (+ "context":48000,"window":131072 once the context is known)
//	{"type":"note","id":1,"text":"budget: 10 of 25 rounds · 29 of 100 calls left — …"}  what the terminal notes
//	{"type":"answer","id":1,"ok":true,"text":"...","session":"hh_...","hermes_session":"...","rounds":12,"calls":47}
//	{"type":"session","id":2,"session":"hh_...","hermes_session":"...","approvals":"off"}
//	{"type":"check","id":4,"ok":true,"model":"...","base":"...","version":"0.22.2"}   (+ "compat" as above)
//	{"type":"history","id":5,"session":"hh_...","events":[...]}  (session.Event objects)
//	{"type":"debug","id":6,"on":true,"path":".../debug.jsonl"}
//	{"type":"approvals","id":7,"approvals":"off"}                  off (= auto) | never
//	{"type":"compact","id":8,"ok":true,"session":"hh_...","before":98000,"after":31000,"text":"compacted #1 · …","checkpoint":"chat-checkpoints/0001.json"}
//	{"type":"context","id":9,"session":"hh_...","owned":true,"checkpoint":"chat-checkpoints/0001.json","n":1,
//	 "messages":[{"role":"user","content":"…"},…],"since":[…],"context":31000,"measured":false,"window":131072}
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
	"io"
	"os"
	"strings"

	"hermes-hands/internal/app"
	"hermes-hands/internal/compact"
	"hermes-hands/internal/compat"
	"hermes-hands/internal/session"
	"hermes-hands/internal/turn"
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
	s, ok := start(a, smode, d.Version, emit)
	if !ok {
		return 1
	}
	// SIGINT cancels the in-flight turn (like the REPL): the turn context, the
	// shell child AND the server run. The caller drives the lifetime by
	// closing stdin. The runner's silence watchdog keeps a gateway that goes
	// quiet from hanging the editor's turn forever.
	defer s.runner.CancelOnInterrupt()()
	s.serve(os.Stdin)
	return 0
}

// server is one --rpc process: the app, the open session and its runner.
type server struct {
	a       *app.App
	rec     *session.Record
	runner  *turn.Runner
	emit    func(map[string]any)
	debugOn bool
}

// start preflights like the REPL — /v1/capabilities, without which the
// client never streams, and only the SSE stream carries Hermes' tool events
// (the sandbox guard) and live deltas — opens the session and says ready.
// On a failure it has written the error line.
func start(a *app.App, smode, version string, emit func(map[string]any)) (*server, bool) {
	fail := func(msg string) (*server, bool) {
		emit(map[string]any{"type": "error", "message": msg})
		return nil, false
	}
	if !a.Cfg.Configured() {
		return fail("not configured - run `hermes-hands setup`")
	}
	srv, err := a.Client.Server().Check(context.Background())
	if err != nil {
		return fail(err.Error())
	}
	approvals, ok := pipeApprovals(a.Approvals())
	if !ok {
		return fail("--rpc needs --yolo (HERMES_HANDS_APPROVE=auto) or HERMES_HANDS_APPROVE=never - interactive approval over the pipe is not supported")
	}
	a.UI = nil // no terminal frames in rpc mode: the event log only records

	rec, err := a.Store.Resolve(smode, a.RepoRoot)
	if err != nil {
		return fail(err.Error())
	}
	log := a.OpenSession(rec, false)
	ready := map[string]any{
		"type": "ready", "session": rec.ID, "cwd": a.RepoRoot,
		"version": version, "approvals": approvals, "hermes_agent": srv.Version,
	}
	if w := app.CompatWarning(srv.Version); w != "" {
		ready["compat"] = w
	}
	emit(ready)

	s := &server{a: a, rec: rec, runner: &turn.Runner{}, emit: emit, debugOn: a.Cfg.Debug}
	a.Bind(s.runner)
	s.runner.Use(log)
	s.applyDebug()
	return s, true
}

// serve answers one request per line until in closes.
func (s *server) serve(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			s.emit(map[string]any{"type": "error", "message": "invalid JSON request"})
			continue
		}
		id := any(nil)
		if len(req.ID) > 0 {
			_ = json.Unmarshal(req.ID, &id)
		}
		h, ok := handlers[req.Type]
		if !ok {
			s.emit(map[string]any{"type": "error", "id": id, "message": fmt.Sprintf("unknown request type %q", req.Type)})
			continue
		}
		h(s, req, id)
	}
}

// handlers answers each request type; id is the request's id, echoed back.
var handlers = map[string]func(s *server, req request, id any){
	"turn":      (*server).turn,
	"history":   (*server).history,
	"new":       (*server).newSession,
	"use":       (*server).use,
	"debug":     (*server).debug,
	"approvals": (*server).approvals,
	"check":     (*server).check,
	"compact":   (*server).compact,
	"context":   (*server).context,
	"compress":  (*server).compact,
}

func (s *server) fail(id any, msg string) {
	s.emit(map[string]any{"type": "error", "id": id, "message": msg})
}

// turn runs one turn; the answer — or why there is none — closes it.
func (s *server) turn(req request, id any) {
	if req.Text == "" {
		s.fail(id, "turn needs a non-empty text")
		return
	}
	res := s.runner.Run(req.Text, &front{emit: s.emit, id: id, heads: map[int]string{}})
	switch res.End.Outcome {
	case "timeout":
		s.fail(id, fmt.Sprintf("turn timed out — no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=%ds)", res.End.Limit))
	case "interrupted":
		s.fail(id, "turn interrupted")
	default:
		s.emit(map[string]any{
			"type": "answer", "id": id, "ok": res.Out.OK, "text": res.Out.Answer,
			"session": s.rec.ID, "hermes_session": s.rec.HermesSessionID,
			"rounds": res.Out.Rounds, "calls": res.Out.Calls,
		})
	}
}

// history returns the open session's event log.
func (s *server) history(_ request, id any) {
	evs, err := s.a.Store.LoadEvents(s.rec)
	if err != nil {
		s.fail(id, err.Error())
		return
	}
	if evs == nil {
		evs = []session.Event{}
	}
	s.emit(map[string]any{
		"type": "history", "id": id, "session": s.rec.ID,
		"hermes_session": s.rec.HermesSessionID, "events": evs,
	})
}

func (s *server) newSession(_ request, id any) {
	nr, err := s.a.Store.Resolve("new", s.a.RepoRoot)
	if err != nil {
		s.fail(id, err.Error())
		return
	}
	s.switchTo(nr, id)
}

func (s *server) use(req request, id any) {
	if req.Session == "" {
		s.fail(id, "use needs a session id")
		return
	}
	nr, err := s.a.Store.Resolve(req.Session, s.a.RepoRoot)
	if err != nil {
		s.fail(id, err.Error())
		return
	}
	s.switchTo(nr, id)
}

// switchTo makes rec the open session and says which one it is.
func (s *server) switchTo(rec *session.Record, id any) {
	s.rec = rec
	s.runner.Use(s.a.OpenSession(rec, false)) // drops the approvals / debug switches of the session before
	s.debugOn = s.a.Cfg.Debug
	s.applyDebug()
	approvals, _ := pipeApprovals(s.a.Approvals())
	s.emit(map[string]any{"type": "session", "id": id, "session": rec.ID, "hermes_session": rec.HermesSessionID,
		"approvals": approvals})
}

// approvals switches the hands' approvals for this process: auto (also off /
// yolo) | never; "" only reports. Interactive "ask" cannot work over the pipe
// — the editor runs its own approval UI before sending a turn.
func (s *server) approvals(req request, id any) {
	switch strings.ToLower(strings.TrimSpace(req.Text)) {
	case "":
	case "auto", "off", "yolo":
		s.a.SetApprovals("auto")
	case "never":
		s.a.SetApprovals("never")
	default:
		s.fail(id, "approvals takes auto | never - interactive approval over the pipe is not supported")
		return
	}
	approvals, _ := pipeApprovals(s.a.Approvals())
	s.emit(map[string]any{"type": "approvals", "id": id, "approvals": approvals})
}

// pipeApprovals names an approvals mode the way the protocol does: auto is
// "off", never is "never"; ask (anything else) has no pipe form.
func pipeApprovals(mode string) (string, bool) {
	switch mode {
	case "auto":
		return "off", true
	case "never":
		return "never", true
	}
	return "", false
}

// debug switches the redacted wire trace: on | off | anything else = toggle.
func (s *server) debug(req request, id any) {
	switch req.Text {
	case "on", "true":
		s.debugOn = true
	case "off", "false":
		s.debugOn = false
	default:
		s.debugOn = !s.debugOn
	}
	s.a.NoteDebug(s.debugOn)
	s.applyDebug()
	s.emit(map[string]any{"type": "debug", "id": id, "on": s.debugOn, "path": s.a.Store.DebugPath(s.rec)})
}

func (s *server) check(_ request, id any) {
	res, err := s.a.Client.Server().Check(context.Background())
	if err != nil {
		s.emit(map[string]any{"type": "check", "id": id, "ok": false, "message": err.Error()})
		return
	}
	msg := map[string]any{"type": "check", "id": id, "ok": true, "model": res.Model, "base": res.Base, "version": res.Version}
	if n := compat.Note(res.Version); n != "" {
		msg["compat"] = n
	}
	s.emit(msg)
}

// compact compacts the held session's transcript now (internal/compact).
func (s *server) compact(_ request, id any) {
	ev, err := s.a.Keeper.Compact(compact.Now)
	if err != nil {
		s.emit(map[string]any{"type": "compact", "id": id, "ok": false, "session": s.rec.ID, "message": err.Error()})
		return
	}
	ev = s.runner.Record(ev)
	s.emit(map[string]any{"type": "compact", "id": id, "ok": true, "session": s.rec.ID,
		"before": ev.Before, "after": ev.Tokens, "text": ev.Text, "checkpoint": ev.Checkpoint})
}

// context is what Hermes reads now: the latest chat checkpoint's messages and
// those after it (owned false: hermes-agent holds the transcript, nothing is
// compacted yet), and the meter.
func (s *server) context(_ request, id any) {
	v := s.a.Keeper.View()
	s.emit(map[string]any{"type": "context", "id": id, "session": s.rec.ID, "owned": v.Owned,
		"checkpoint": v.Checkpoint, "n": v.N, "messages": v.Messages, "since": v.Since,
		"context": v.Meter.Tokens, "measured": v.Meter.Measured, "window": v.Meter.Window})
}

func (s *server) applyDebug() { s.a.SetDebug(s.rec, s.debugOn) }
