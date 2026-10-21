package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// SetDocked puts the REPL's box into the dock — rules, input line, footer —
// for good, a turn running or not; off, the dock is the working line alone.
func (u *UI) SetDocked(on bool) { u.change(func() { u.docked = on }) }

// SetInput is what the box's input line shows now (Input).
func (u *UI) SetInput(in Input) { u.change(func() { u.in = in }) }

// SetFooter is the dock's footer — the REPL's approval mode and what a key
// does now; "" = none.
func (u *UI) SetFooter(s string) { u.change(func() { u.footer = s }) }

// Repaint draws the dock again — the terminal changed its size.
func (u *UI) Repaint() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.live() {
		u.repaint()
	}
}

// ClearScreen clears the terminal, the dock back at its top (Ctrl-L).
func (u *UI) ClearScreen() {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := "\x1b[H\x1b[2J"
	u.up = 0
	if u.live() {
		s += u.draw()
	}
	io.WriteString(u.w, s)
}

// StartWorking puts the working line up until the returned stop is called
// (idempotent, safe from any goroutine). Non-tty: the old static line.
func (u *UI) StartWorking() (stop func()) {
	if !u.tty {
		u.Working() // the plain static line; no goroutine, no repaint
		return func() {}
	}
	u.change(func() {
		u.working, u.start, u.frame = true, time.Now(), 0
		u.paused = 0 // fresh turn: no approval waits counted against it yet
	})
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(220 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				u.mu.Lock()
				u.frame++
				if u.live() {
					u.repaint()
				}
				u.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			u.change(func() { u.working = false })
		})
	}
}

// Hold takes the dock off until the returned resume runs. Wrap it around
// anything that owns the terminal meanwhile — the setup wizard, a prompt
// with no box to answer in — so the dock can't overwrite it. The held
// interval is excluded from the elapsed counter (both the live one and the
// turn's final "worked for Xs", via Paused()): waiting on the operator is
// not hermes-agent taking time to work.
func (u *UI) Hold() (resume func()) {
	u.change(func() { u.held++ })
	heldSince := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			u.change(func() {
				u.paused += time.Since(heldSince)
				u.held--
			})
		})
	}
}

// Await is Hold for a question answered in the box: the dock stays, the
// working line says it waits for the operator, and the wait is left out of
// the turn's time, as Hold's.
func (u *UI) Await() (resume func()) {
	u.change(func() {
		if u.awaiting == 0 {
			u.awaitSince = time.Now()
		}
		u.awaiting++
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			u.change(func() {
				if u.awaiting--; u.awaiting == 0 {
					u.paused += time.Since(u.awaitSince)
				}
			})
		})
	}
}

// Paused reports how long the current turn has spent held (approval prompts)
// so far. Subtract it from a wall-clock turn duration to get actual working
// time, e.g. before TurnDone.
func (u *UI) Paused() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.paused
}

// Queue lists a message the operator queued during the turn in the dock,
// numbered; Added takes it off as it goes out with the next round. With the
// dock off it is a line in the conversation instead.
func (u *UI) Queue(text string) {
	u.mu.Lock()
	shown := u.docked && u.held == 0
	u.mu.Unlock()
	if !shown {
		u.sync(func() {
			u.nQueued++
			u.queue = append(u.queue, queuedMsg{u.nQueued, text})
			fmt.Fprintf(u.w, " %squeued %d for the next round: %s%s\n", u.cDim, u.nQueued, clipWidth(strings.Join(strings.Fields(text), " "), 100), u.cR)
		})
		return
	}
	u.change(func() {
		u.nQueued++
		u.queue = append(u.queue, queuedMsg{u.nQueued, text})
	})
}

// ClearQueue empties the dock's queue as the turn ends: what it did not take
// becomes the next turn, or waits in the input line.
func (u *UI) ClearQueue() { u.change(func() { u.queue, u.nQueued = nil, 0 }) }

// Print writes s as it is, above the dock.
func (u *UI) Print(s string) { u.sync(func() { io.WriteString(u.w, s) }) }

// Writer is an io.Writer onto the conversation, above the dock: what is
// written goes out a whole line at a time — the REPL's commands, the
// approval prompts' summaries.
func (u *UI) Writer() io.Writer {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lw == nil {
		u.lw = &lineWriter{u: u}
	}
	return u.lw
}

type lineWriter struct {
	u   *UI
	mu  sync.Mutex
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if i := bytes.LastIndexByte(w.buf, '\n'); i >= 0 {
		out := string(w.buf[:i+1])
		w.buf = append(w.buf[:0], w.buf[i+1:]...)
		w.u.Print(out)
	}
	return len(p), nil
}
