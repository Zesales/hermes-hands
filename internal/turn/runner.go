package turn

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"hermes-hands/internal/api"
	"hermes-hands/internal/loop"
	"hermes-hands/internal/redact"
	"hermes-hands/internal/session"
)

// Runner runs the operator's turns in one open session.
type Runner struct {
	Client *api.Client
	Loop   *loop.Loop
	Store  *session.Store
	Shell  interface{ Kill() } // the hands' shell: Cancel kills its running child

	// Timeout / Interval configure the silence watchdog
	// (HERMES_HANDS_RESPONSE_TIMEOUT / HERMES_HANDS_WATCHDOG_INTERVAL).
	Timeout, Interval time.Duration
	// Paused reports how long this turn has waited on approval prompts —
	// left out of the worked time and of each call's duration. nil: none.
	Paused func() time.Duration
	// ServerApproval answers an approval request of Hermes' own server tool
	// ("once" | "session" | "deny"). nil denies.
	ServerApproval func(tool, what string) string

	log *session.Log

	mu     sync.Mutex
	cancel context.CancelFunc // the running turn's, nil between turns
	runID  string             // the in-flight Hermes run, for POST /v1/runs/{id}/stop
}

// Result is how a turn ended: the loop's outcome and the recorded footer
// (Outcome "" | interrupted | timeout).
type Result struct {
	Out loop.Outcome
	End session.Event
}

// Use makes log the session the next turns run in.
func (r *Runner) Use(log *session.Log) { r.log = log }

// Log is the open session's log.
func (r *Runner) Log() *session.Log { return r.log }

// Record adds an event a front end has already shown itself — an answered
// approval prompt — to the open session's log.
func (r *Runner) Record(ev session.Event) session.Event { return r.log.Add(ev) }

// Run runs one turn for input and shows it on fe.
func (r *Runner) Run(input string, fe Frontend) Result {
	log, rec := r.log, r.log.Rec
	log.BeginTurn()
	fe.Show(log.Add(session.Event{Kind: session.KindUser, Text: redact.Scrub(input)}))
	start := time.Now()

	var (
		shown strings.Builder // this run's previewed answer text (dedupes the final render)
		local int64           // ms the hands' calls ran
	)
	pv := &preview{emit: func(s string) { shown.WriteString(s); fe.Preview(s) }}
	wd := r.watchdog()
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancel, r.runID = cancel, ""
	r.mu.Unlock()

	r.wire(wd, log, fe, pv, &shown, &local)
	defer r.unwire()

	stop := make(chan struct{})
	go wd.guard(stop)
	out := r.Loop.Run(ctx, input, rec, func(runID, sid string, tok int) {
		prev, ran := rec.HermesSessionID, rec.Turns
		_ = r.Store.BumpTurn(rec, runID, sid, tok)
		if ran > 0 && prev != "" && rec.HermesSessionID != prev {
			// a new hermes-agent session id mid-conversation: it compacted
			// (or split) the context — worth one line, the work goes on
			fe.Show(log.Add(session.Event{Kind: session.KindNote,
				Text: "hermes-agent continued in a new session — it compacted (or split) the conversation"}))
		}
	})
	close(stop)
	timedOut := wd.disarm() // barrier: authoritative "did the watchdog fire?"
	fe.Quiet()
	streamed := fe.EndPreview()
	r.mu.Lock()
	r.cancel = nil
	r.mu.Unlock()
	interrupted := ctx.Err() != nil
	cancel()

	waited := r.paused()
	end := session.Event{Kind: session.KindTurnEnd, Ms: (time.Since(start) - waited).Milliseconds(),
		LocalMs: local, WaitMs: waited.Milliseconds(), Rounds: out.Rounds, Calls: out.Calls}
	switch {
	case timedOut:
		end.Outcome, end.Limit = "timeout", int(r.Timeout/time.Second)
	case interrupted:
		end.Outcome = "interrupted"
	default:
		if out.OK {
			if changed, _ := r.Store.SetTitleLocal(rec, input); changed && rec.HermesSessionID != "" {
				r.Client.Sessions().SetTitle(context.Background(), rec.HermesSessionID, rec.Title)
			}
		}
		ans := log.Add(session.Event{Kind: session.KindAnswer, Text: redact.Scrub(out.Answer), OK: session.Bool(out.OK)})
		// The preview already showed exactly this answer, in the same shape
		// — don't show it twice.
		if !streamed || strings.TrimSpace(shown.String()) != strings.TrimSpace(out.Answer) {
			fe.Show(ans)
		}
	}
	end = log.Add(end)
	fe.Show(end)
	return Result{Out: out, End: end}
}

// wire points the client's and the loop's hooks at this turn: every sign of
// life bumps the watchdog, every event is recorded, then shown.
func (r *Runner) wire(wd *watchdog, log *session.Log, fe Frontend, pv *preview, shown *strings.Builder, local *int64) {
	c := r.Client
	c.OnRunStart = func(id string) {
		r.mu.Lock()
		r.runID = id
		r.mu.Unlock()
		// each round is a fresh envelope: a calls-round must not keep the
		// preview muted for the final round that follows it. A preview a
		// previous round left open (a prose reply sent back for a fix-up)
		// is closed, so the dedupe compares this round's text only.
		pv.reset()
		fe.EndPreview()
		shown.Reset()
		wd.bump()
	}
	c.OnDelta = func(s string) { wd.bump(); pv.feed(s) } // previews the `final` text; any token = progress
	c.OnEvent = func(string) { wd.bump() }               // any SSE frame is a sign of life
	c.OnApproval = r.ServerApproval
	c.OnHermesTool = func(tool, preview, action string) {
		fe.Show(log.Add(session.Event{Kind: session.KindHermesTool, Tool: tool, Text: preview, Action: action}))
	}
	r.Loop.Paused = r.Paused
	if pf, ok := fe.(ProgressFrontend); ok {
		r.Loop.OnProgress = pf.Progress
	}
	// every hands call: recorded and shown BEFORE it runs, then its result
	r.Loop.Emit = func(ev session.Event) {
		wd.bump() // a call starting or finishing = progress
		ev = log.Add(ev)
		if ev.Kind == session.KindResult {
			*local += ev.Ms
		}
		fe.Show(ev)
	}
}

func (r *Runner) unwire() {
	c := r.Client
	c.OnRunStart, c.OnDelta, c.OnEvent, c.OnApproval, c.OnHermesTool = nil, nil, nil, nil, nil
	r.Loop.Emit, r.Loop.OnProgress = nil, nil
}

// watchdog cancels a turn when hermes-agent goes quiet past Timeout, but
// pushes the deadline back on every sign of life — and while a periodic
// out-of-band probe still finds the server run running.
func (r *Runner) watchdog() *watchdog {
	return &watchdog{timeout: r.Timeout, interval: r.Interval, onFire: r.Cancel,
		check: func(ctx context.Context) bool {
			r.mu.Lock()
			rid := r.runID
			r.mu.Unlock()
			if rid == "" {
				return true // between rounds: local dispatch is the holdup, not the brain
			}
			st, err := r.Client.Runs().State(ctx, rid)
			if err != nil {
				return false // can't confirm it's alive — let the deadline run down
			}
			switch st {
			case "completed", "failed", "cancelled":
				return false
			default: // "", queued, started, running, stopping, or anything unknown
				return true
			}
		}}
}

// Cancel stops the running turn — what Ctrl-C and the watchdog do: cancel
// its context, kill the shell's child, and stop the server run too, not
// just the local wait. Between turns it does nothing.
func (r *Runner) Cancel() {
	r.mu.Lock()
	rid := r.runID
	if r.cancel != nil {
		r.cancel()
		r.Shell.Kill()
	}
	r.mu.Unlock()
	if rid != "" {
		go r.Client.Runs().Stop(context.Background(), rid)
	}
}

// CancelOnInterrupt makes SIGINT cancel the running turn until the returned
// stop is called. At the REPL prompt liner turns Ctrl-C into its own error
// (raw mode, no signal), so this only ever fires mid-turn.
func (r *Runner) CancelOnInterrupt() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		for range ch {
			r.Cancel()
		}
	}()
	return func() { signal.Stop(ch) }
}

func (r *Runner) paused() time.Duration {
	if r.Paused == nil {
		return 0
	}
	return r.Paused()
}
