package main

import (
	"context"
	"sync"
	"time"
)

// watchdog cancels the running turn when hermes-agent goes silent for too
// long. It is (re)armed at the start of every turn with a deadline of
// `timeout` from now, pushed back again ("bumped") on every sign of life —
// a streamed answer chunk, a completed tool round, a fresh sub-run — and, as
// a fallback for the pure "thinking" phase where nothing streams, on a
// periodic out-of-band probe (`interval`) that the server run is still going.
// If the deadline is ever reached, onFire runs (cancel the turn + stop the
// server run) and timedOut reports true so the REPL says "timeout", not
// "interrupted".
//
// timeout <= 0 disables the mechanism entirely. interval <= 0 or a nil check
// keeps the deadline but drops the out-of-band probe — local signs of life
// still bump it. interval is floored at 15s so a mistyped config can't turn
// it into a hammer loop.
// watchdogIntervalFloor is the smallest out-of-band probe interval honoured,
// so a mistyped HERMES_HANDS_WATCHDOG_INTERVAL can't turn the probe into a
// hammer loop. A package var only so tests can shrink it.
var watchdogIntervalFloor = 15 * time.Second

type watchdog struct {
	timeout  time.Duration
	interval time.Duration
	check    func(ctx context.Context) bool // out-of-band "still working?" probe
	onFire   func()                         // cancel the turn

	mu       sync.Mutex
	deadline time.Time
	fired    bool
	armed    bool
	gen      int64 // bumped on each arm; a stale async probe carries an old gen
}

// bump pushes the deadline out to now+timeout. No-op when disabled, unarmed,
// or already fired. Called from the run/delta/tool callbacks (which only fire
// during the armed turn).
func (w *watchdog) bump() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.armed && !w.fired && w.timeout > 0 {
		w.deadline = time.Now().Add(w.timeout)
	}
	w.mu.Unlock()
}

// bumpGen is bump for the async probe: it counts only if the turn it was
// launched for is still the armed one (a probe from turn N must not extend
// turn N+1's deadline).
func (w *watchdog) bumpGen(gen int64) {
	w.mu.Lock()
	if w.armed && !w.fired && w.gen == gen && w.timeout > 0 {
		w.deadline = time.Now().Add(w.timeout)
	}
	w.mu.Unlock()
}

func (w *watchdog) timedOut() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// disarm ends the current turn's guarding and reports whether the watchdog
// fired. Call it once, right after the turn's work returns and stop is closed:
// it takes w.mu, so it either wins the race against a deadline tick that was
// about to fire (armed=false => that tick is a no-op) or observes the fire
// that already happened. Either way the bool it returns is authoritative.
func (w *watchdog) disarm() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = false
	return w.fired
}

// guard arms the watchdog for one turn and blocks until stop is closed (the
// turn finished) or the deadline fires. Run it in its own goroutine per turn.
func (w *watchdog) guard(stop <-chan struct{}) {
	w.mu.Lock()
	timeout, interval := w.timeout, w.interval
	if timeout <= 0 {
		w.mu.Unlock()
		<-stop
		return
	}
	w.deadline = time.Now().Add(timeout)
	w.fired = false
	w.armed = true
	w.gen++
	gen := w.gen
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.armed = false
		w.mu.Unlock()
	}()

	gran := timeout / 4
	if gran > time.Second {
		gran = time.Second
	}
	if gran < 10*time.Millisecond {
		gran = 10 * time.Millisecond
	}
	tick := time.NewTicker(gran)
	defer tick.Stop()

	var probeC <-chan time.Time
	if interval > 0 && w.check != nil {
		if interval < watchdogIntervalFloor {
			interval = watchdogIntervalFloor
		}
		p := time.NewTicker(interval)
		defer p.Stop()
		probeC = p.C
	}

	for {
		select {
		case <-stop:
			return
		case <-probeC:
			go func(gen int64) {
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				if w.check(ctx) {
					w.bumpGen(gen)
				}
			}(gen)
		case <-tick.C:
			w.mu.Lock()
			over := w.armed && !w.fired && time.Now().After(w.deadline)
			if over {
				w.fired = true
			}
			w.mu.Unlock()
			if over {
				if w.onFire != nil {
					w.onFire()
				}
				return
			}
		}
	}
}
