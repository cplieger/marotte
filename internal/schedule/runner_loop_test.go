package schedule

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// These tests drive Run in a synctest bubble so the PRODUCTION TickInterval is used and the
// counts are exact; Run parks in a select, and a sweep's file I/O is transient.

// bubbleFixture builds a store with one 5-minute schedule plus a runner on the bubble's
// clock. anchorOffset selects whether a slot is already due at t=0.
func bubbleFixture(t *testing.T, anchorOffset time.Duration) (*Store, *fakeLauncher, *Runner) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e := Entry{
		ID:     "s1",
		Source: "bundled://demo",
		// The floor interval, so the schedule's step cannot be confused with the tick cadence.
		Spec:    Spec{Freq: FreqMinutely, Interval: minMinuteInterval},
		Enabled: true,
		Anchor:  time.Now().Add(anchorOffset),
	}
	if err := st.Put(t.Context(), &e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	l := &fakeLauncher{}
	r := NewRunner(st, l)
	return st, l, r
}

// TestRun_DoesNotSweepOnEntry pins that a slot due during downtime is not fired at boot.
// The slot sits inside MissGrace at t=0, so only WHEN the first sweep ran is measured.
func TestRun_DoesNotSweepOnEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, l, r := bubbleFixture(t, -2*time.Minute)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)

		// Half a tick in: a sweep on entry would already have launched.
		synctest.Sleep(TickInterval / 2)
		if srcs, _, _ := l.snap(); len(srcs) != 0 {
			t.Errorf("launches after %v = %v, want none: Run must not sweep on entry",
				TickInterval/2, srcs)
		}
		synctest.Sleep(TickInterval)
		if got := l.launched(); got != 1 {
			t.Errorf("launches after the first tick = %d, want exactly 1", got)
		}
		cancel()
	})
}

// TestRun_FiresOncePerSlotAtTheTickCadence asserts at exact equality that a 5-minute
// schedule under a 1-minute ticker fires once per slot.
func TestRun_FiresOncePerSlotAtTheTickCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, l, r := bubbleFixture(t, 0)
		start := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)

		const slots = 4
		synctest.Sleep(slots * minMinuteInterval * time.Minute)
		if got := l.launched(); got != slots {
			t.Errorf("launches in %d minutes = %d, want %d (one per 5-minute slot, not one per tick)",
				slots*minMinuteInterval, got, slots)
		}
		// The anchor sits on the slot, not the observing tick, or the schedule drifts.
		wantAnchor := start.Add(slots * minMinuteInterval * time.Minute)
		if got := st.List()[0].Anchor; !got.Equal(wantAnchor) {
			t.Errorf("anchor = %v, want the last slot %v", got, wantAnchor)
		}
		cancel()
	})
}

// TestRun_ReturnsOnCancel checks the exit path: a Run ignoring cancellation would leave a
// goroutine the bubble reports as a deadlock.
func TestRun_ReturnsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, _, r := bubbleFixture(t, 0)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Run(ctx)
		}()
		synctest.Sleep(TickInterval / 2)
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("Run did not return after its context was cancelled")
		}
	})
}
