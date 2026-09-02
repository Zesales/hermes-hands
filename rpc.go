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

	"github.com/Zesales/hermes-hands/internal/prompt"
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
//
// Responses:
//
//	{"type":"ready","session":"hh_...","cwd":"/repo","version":"0.3.0","approvals":"off"}
//	{"type":"tool","id":1,"tool":"shell","preview":"npm test","exit":0}
//	{"type":"answer","id":1,"ok":true,"text":"...","session":"hh_...","hermes_session":"..."}
//	{"type":"session","id":2,"session":"hh_...","hermes_session":"..."}
//	{"type":"check","id":4,"ok":true,"model":"...","base":"..."}
//	{"type":"error","id":1,"message":"..."}
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
	a.loop.UI = nil // no terminal frames in rpc mode

	rec, err := a.store.Resolve(smode, a.repoRoot)
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return 1
	}

	emit(map[string]any{
		"type": "ready", "session": rec.ID, "cwd": a.repoRoot,
		"version": bareVersion(), "approvals": approvals,
	})

	// SIGINT cancels the in-flight turn (like the REPL); the caller drives the
	// lifetime by closing stdin.
	var (
		turnMu     sync.Mutex
		cancelTurn context.CancelFunc
	)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			turnMu.Lock()
			if cancelTurn != nil {
				cancelTurn()
				a.shell.Kill()
			}
			turnMu.Unlock()
		}
	}()

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
			a.loop.OnTool = func(tool, preview string, exit int) {
				emit(map[string]any{"type": "tool", "id": id, "tool": tool, "preview": preview, "exit": exit})
			}
			ctx, cancel := context.WithCancel(context.Background())
			turnMu.Lock()
			cancelTurn = cancel
			turnMu.Unlock()

			out := a.loop.Run(ctx, req.Text, rec, func(runID, sid string) { _ = a.store.BumpTurn(rec, runID, sid) })

			turnMu.Lock()
			cancelTurn = nil
			turnMu.Unlock()
			interrupted := ctx.Err() != nil
			cancel()
			a.loop.OnTool = nil

			if interrupted {
				emit(map[string]any{"type": "error", "id": id, "message": "turn interrupted"})
				continue
			}
			if out.OK {
				if changed, _ := a.store.SetTitleLocal(rec, req.Text); changed && rec.HermesSessionID != "" {
					a.client.SetTitle(context.Background(), rec.HermesSessionID, rec.Title)
				}
			}
			emit(map[string]any{
				"type": "answer", "id": id, "ok": out.OK, "text": out.Answer,
				"session": rec.ID, "hermes_session": rec.HermesSessionID,
			})

		case "new":
			nr, e := a.store.Resolve("new", a.repoRoot)
			if e != nil {
				emit(map[string]any{"type": "error", "id": id, "message": e.Error()})
				continue
			}
			rec = nr
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
			emit(map[string]any{"type": "session", "id": id, "session": rec.ID, "hermes_session": rec.HermesSessionID})

		case "check":
			res, e := a.client.Check(context.Background())
			if e != nil {
				emit(map[string]any{"type": "check", "id": id, "ok": false, "message": e.Error()})
				continue
			}
			emit(map[string]any{"type": "check", "id": id, "ok": true, "model": res.Model, "base": res.Base})

		default:
			emit(map[string]any{"type": "error", "id": id, "message": fmt.Sprintf("unknown request type %q", req.Type)})
		}
	}
	return 0
}
