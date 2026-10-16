package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Zesales/hermes-hands/internal/compat"
	"github.com/Zesales/hermes-hands/internal/debuglog"
	"github.com/Zesales/hermes-hands/internal/prompt"
	"github.com/Zesales/hermes-hands/internal/redact"
	"github.com/Zesales/hermes-hands/internal/session"
	"github.com/Zesales/hermes-hands/internal/ui"
)

// --rpc: a persistent JSON-lines session server for an editor / plugin. The
// caller holds one session for the process lifetime and only starts a new one
// on demand, instead of spawning a fresh session per turn (which would
// fragment the central Hermes). One request object per line on stdin; one
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

type rpcReq struct {
	ID      json.RawMessage `json:"id"`
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Session string          `json:"session"`
}

func runRPC(smode string) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	emit := func(v map[string]any) { _ = enc.Encode(v) }

	a, err := newApp()
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}
	defer a.shell.Stop()

	if notConfigured(a.cfg) {
		emit(map[string]any{"type": "error", "message": "not configured - run `hermes-hands setup`"})
		return 1
	}
	// Preflight like the REPL: it also reads /v1/capabilities, without which
	// the client never streams — and only the SSE stream carries Hermes' tool
	// events (the sandbox guard) and live deltas.
	srv, err := a.client.Check(context.Background())
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}

	approvals := "never"
	if ta, ok := a.disp.Approver.(*prompt.TTYApprover); ok {
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
	a.ui = nil // no terminal frames in rpc mode: the event log only records

	rec, err := a.store.Resolve(smode, a.repoRoot)
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}
	evlog := openSession(a, rec, false)

	ready := map[string]any{
		"type": "ready", "session": rec.ID, "cwd": a.repoRoot,
		"version": bareVersion(), "approvals": approvals, "hermes_agent": srv.Version,
	}
	if w := compatWarning(srv.Version); w != "" {
		ready["compat"] = w
	}
	emit(ready)

	// SIGINT cancels the in-flight turn (like the REPL): the turn context, the
	// shell child AND the server run. The caller drives the lifetime by
	// closing stdin.
	var (
		turnMu     sync.Mutex
		cancelTurn context.CancelFunc
		runID      string // in-flight Hermes run, for POST /v1/runs/{id}/stop
	)
	abortTurn := func() {
		turnMu.Lock()
		rid := runID
		if cancelTurn != nil {
			cancelTurn()
			a.shell.Kill()
		}
		turnMu.Unlock()
		if rid != "" {
			go a.client.StopRun(context.Background(), rid)
		}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			abortTurn()
		}
	}()

	// The same silence watchdog as the REPL: a streamed run has no overall
	// deadline of its own, so without it a gateway that goes quiet would hang
	// the editor's turn forever. Every sign of life pushes it back.
	wd := &watchdog{timeout: a.cfg.ResponseTimeout, interval: a.cfg.WatchdogInterval, onFire: abortTurn}
	wd.check = func(ctx context.Context) bool {
		turnMu.Lock()
		rid := runID
		turnMu.Unlock()
		if rid == "" {
			return true // between rounds: local dispatch is the holdup, not the brain
		}
		st, err := a.client.RunState(ctx, rid)
		if err != nil {
			return false
		}
		return st != "completed" && st != "failed" && st != "cancelled"
	}
	a.client.OnEvent = func(string) { wd.bump() }

	// Deltas carry the raw envelope ({"calls":…,"final":"…"}); the editor gets
	// only the final answer text, like the REPL's live preview. Reset per run,
	// so the final round of a multi-round turn streams too.
	var curID any
	fs := &finalStreamer{emit: func(t string) { emit(map[string]any{"type": "delta", "id": curID, "text": t}) }}
	a.client.OnRunStart = func(id string) {
		turnMu.Lock()
		runID = id
		turnMu.Unlock()
		fs.reset()
		wd.bump()
	}

	debugOn := a.cfg.Debug
	applyDebug := func() {
		if debugOn {
			a.client.Trace = debuglog.New(a.store.DebugPath(rec), a.cfg.APIKey).Write
		} else {
			a.client.Trace = nil
		}
	}
	applyDebug()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcReq
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
			evlog.beginTurn()
			evlog.add(session.Event{Kind: session.KindUser, Text: redact.Scrub(req.Text)})
			curID = id
			heads := map[int]string{} // ref -> the call's preview; a result event carries no args
			var local int64           // ms the hands' calls ran (turn_end's local share)
			a.loop.Emit = func(ev session.Event) {
				wd.bump()
				ev = evlog.add(ev)
				switch ev.Kind {
				case session.KindCall:
					heads[ev.Ref], _ = ui.Summarize(ev.Tool, ev.Args)
					emit(map[string]any{"type": "call", "id": id, "ref": ev.Ref, "tool": ev.Tool, "preview": heads[ev.Ref]})
				case session.KindResult:
					local += ev.Ms
					exit := 0
					if ev.Exit != nil {
						exit = *ev.Exit
					}
					emit(map[string]any{"type": "tool", "id": id, "ref": ev.Ref, "tool": ev.Tool, "preview": heads[ev.Ref], "exit": exit, "lines": ev.Lines})
				}
			}
			a.client.OnHermesTool = func(tool, preview, action string) {
				evlog.add(session.Event{Kind: session.KindHermesTool, Tool: tool, Text: preview, Action: action})
				emit(map[string]any{"type": "hermes_tool", "id": id, "tool": tool, "preview": preview, "action": action})
			}
			a.client.OnDelta = func(t string) { wd.bump(); fs.feed(t) }
			fs.reset()
			turnStart := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			turnMu.Lock()
			cancelTurn = cancel
			runID = ""
			turnMu.Unlock()
			wdStop := make(chan struct{})
			go wd.guard(wdStop)

			out := a.loop.Run(ctx, req.Text, rec, func(runID, sid string, tok int) { _ = a.store.BumpTurn(rec, runID, sid, tok) })

			close(wdStop)
			timedOut := wd.disarm()
			turnMu.Lock()
			cancelTurn = nil
			turnMu.Unlock()
			interrupted := ctx.Err() != nil
			cancel()
			a.loop.Emit = nil
			a.client.OnHermesTool = nil
			a.client.OnDelta = nil
			end := session.Event{Kind: session.KindTurnEnd, Ms: time.Since(turnStart).Milliseconds(), LocalMs: local}

			if timedOut {
				end.Outcome, end.Limit = "timeout", int(a.cfg.ResponseTimeout/time.Second)
				evlog.add(end)
				emit(map[string]any{"type": "error", "id": id, "message": fmt.Sprintf(
					"turn timed out — no reply from hermes-agent (HERMES_HANDS_RESPONSE_TIMEOUT=%ds)", end.Limit)})
				continue
			}
			if interrupted {
				end.Outcome = "interrupted"
				evlog.add(end)
				emit(map[string]any{"type": "error", "id": id, "message": "turn interrupted"})
				continue
			}
			if out.OK {
				if changed, _ := a.store.SetTitleLocal(rec, req.Text); changed && rec.HermesSessionID != "" {
					a.client.SetTitle(context.Background(), rec.HermesSessionID, rec.Title)
				}
			}
			evlog.add(session.Event{Kind: session.KindAnswer, Text: redact.Scrub(out.Answer), OK: session.Bool(out.OK)})
			evlog.add(end)
			emit(map[string]any{
				"type": "answer", "id": id, "ok": out.OK, "text": out.Answer,
				"session": rec.ID, "hermes_session": rec.HermesSessionID,
			})

		case "history":
			evs, e := a.store.LoadEvents(rec)
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
			nr, e := a.store.Resolve("new", a.repoRoot)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			rec = nr
			evlog = openSession(a, rec, false)
			applyDebug()
			emit(map[string]any{"type": "session", "id": id, "session": rec.ID, "hermes_session": rec.HermesSessionID})

		case "use":
			if req.Session == "" {
				emit(map[string]any{"type": "error", "id": id, "message": "use needs a session id"})
				continue
			}
			nr, e := a.store.Resolve(req.Session, a.repoRoot)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			rec = nr
			evlog = openSession(a, rec, false)
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
			emit(map[string]any{"type": "debug", "id": id, "on": debugOn, "path": a.store.DebugPath(rec)})

		case "check":
			res, e := a.client.Check(context.Background())
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
