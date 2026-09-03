package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchdogFiresAfterTimeout(t *testing.T) {
	var fired atomic.Int32
	wd := &watchdog{timeout: 120 * time.Millisecond}
	wd.onFire = func() { fired.Add(1) }

	stop := make(chan struct{})
	go wd.guard(stop)
	defer close(stop)

	deadline := time.After(2 * time.Second)
	for fired.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watchdog never fired")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !wd.timedOut() {
		t.Error("timedOut() = false after fire")
	}
}

func TestWatchdogBumpDefersFire(t *testing.T) {
	var fired atomic.Int32
	wd := &watchdog{timeout: 150 * time.Millisecond}
	wd.onFire = func() { fired.Add(1) }

	stop := make(chan struct{})
	go wd.guard(stop)

	// Bump well inside the window for ~500ms: it must not fire.
	bumpStop := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(30 * time.Millisecond)
loop:
	for {
		select {
		case <-bumpStop:
			break loop
		case <-ticker.C:
			wd.bump()
		}
	}
	ticker.Stop()
	if fired.Load() != 0 {
		t.Fatalf("fired %d times while being bumped", fired.Load())
	}

	// Stop bumping — now it should fire.
	deadline := time.After(2 * time.Second)
	for fired.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watchdog did not fire after bumps stopped")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(stop)
}

func TestWatchdogProbeKeepsItAlive(t *testing.T) {
	old := watchdogIntervalFloor
	watchdogIntervalFloor = 20 * time.Millisecond
	defer func() { watchdogIntervalFloor = old }()

	var alive atomic.Bool
	alive.Store(true)
	var probes, fired atomic.Int32

	wd := &watchdog{
		timeout:  150 * time.Millisecond,
		interval: 20 * time.Millisecond,
	}
	wd.check = func(ctx context.Context) bool { probes.Add(1); return alive.Load() }
	wd.onFire = func() { fired.Add(1) }

	stop := make(chan struct{})
	go wd.guard(stop)

	// The real out-of-band probe (guard's own ticker) should keep it alive.
	time.Sleep(600 * time.Millisecond)
	if probes.Load() == 0 {
		t.Fatal("guard never ran the out-of-band probe")
	}
	if fired.Load() != 0 {
		t.Fatalf("fired %d times while the probe reported alive", fired.Load())
	}

	// Probe goes dead -> nothing bumps -> deadline runs down -> fire.
	alive.Store(false)
	deadline := time.After(2 * time.Second)
	for fired.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watchdog did not fire after the probe went dead")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(stop)
}

func TestWatchdogDisabled(t *testing.T) {
	var fired atomic.Int32
	wd := &watchdog{timeout: 0} // disabled
	wd.onFire = func() { fired.Add(1) }

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { wd.guard(stop); close(done) }()

	time.Sleep(120 * time.Millisecond)
	if fired.Load() != 0 {
		t.Errorf("disabled watchdog fired")
	}
	if wd.timedOut() {
		t.Errorf("disabled watchdog reports timedOut")
	}
	wd.bump() // must be a no-op, not panic
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("guard did not return after stop on a disabled watchdog")
	}
}

func TestWatchdogStaleProbeDoesNotBumpNextTurn(t *testing.T) {
	wd := &watchdog{timeout: time.Hour}

	// Arm turn 1, capture its generation, end it.
	stop1 := make(chan struct{})
	go wd.guard(stop1)
	time.Sleep(20 * time.Millisecond)
	wd.mu.Lock()
	gen1 := wd.gen
	wd.mu.Unlock()
	close(stop1)
	time.Sleep(20 * time.Millisecond)

	// Arm turn 2 with a short timeout.
	wd.mu.Lock()
	wd.timeout = 120 * time.Millisecond
	wd.mu.Unlock()
	var fired atomic.Int32
	wd.onFire = func() { fired.Add(1) }
	stop2 := make(chan struct{})
	go wd.guard(stop2)

	// A stale probe from turn 1 completes now — it must NOT push turn 2's deadline.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); wd.bumpGen(gen1) }()
	wg.Wait()

	deadline := time.After(2 * time.Second)
	for fired.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("turn 2 never fired — a stale probe extended its deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(stop2)
}
