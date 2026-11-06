package bridge

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func shortenStallChecks(t *testing.T, quiet, recheck, sample, ceiling time.Duration) {
	t.Helper()
	origQuiet, origRecheck, origSample, origCeiling := stallQuiet, stallRecheck, stallSample, stallExtendCeiling
	stallQuiet, stallRecheck, stallSample, stallExtendCeiling = quiet, recheck, sample, ceiling
	t.Cleanup(func() {
		stallQuiet, stallRecheck, stallSample, stallExtendCeiling = origQuiet, origRecheck, origSample, origCeiling
	})
}

// newIdleEngine is an engine with no process, for a synctest bubble: made inside it, so its channels block durably,
// with its lane pump stopped before the bubble ends.
func newIdleEngine(t *testing.T) *Engine {
	b := NewEngine("/nonexistent", "/work")
	t.Cleanup(b.engineLane.close)
	return b
}

// switchableCPU advances while busy is set and stays flat once it is cleared: a stall that ends.
func switchableCPU(busy *atomic.Bool) func() (uint64, bool) {
	var ticks atomic.Uint64
	return func() (uint64, bool) {
		if busy.Load() {
			return ticks.Add(1), true
		}
		return ticks.Load(), true
	}
}

// at sleeps the bubble's clock to start+offset and lets every timer due by then run.
func at(start time.Time, offset time.Duration) {
	time.Sleep(time.Until(start.Add(offset)))
	synctest.Wait()
}

const stallBudget = 10 * time.Second

// The production values, set in each test so a change to a default cannot move these timings.
func defaultStallChecks(t *testing.T) {
	shortenStallChecks(t, 2*time.Second, 2*time.Second, 250*time.Millisecond, 120*time.Second)
}

func TestStallAware_ExtendsUntilTheStallEnds(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		var busy atomic.Bool
		busy.Store(true)
		b.cpuTicks = switchableCPU(&busy)
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()

		at(start, stallBudget+30*time.Second)
		if err := ctx.Err(); err != nil {
			t.Fatalf("a call to an engine silent but burning CPU expired at its budget: %v, want it extended", err)
		}
		busy.Store(false)
		at(start, stallBudget+30*time.Second+stallRecheck+2*stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v once the engine's CPU went flat, want context.DeadlineExceeded", err)
		}
	})
}

// A stall the engine ends by reading a frame (the late answer) gives the call its budget again, for the calls a door
// makes after that answer.
func TestStallAware_AnEngineAnsweringAfterAStallGetsAFreshBudget(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		var busy atomic.Bool
		busy.Store(true)
		b.cpuTicks = switchableCPU(&busy)
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()

		const answered = stallBudget + 30*time.Second
		at(start, answered)
		busy.Store(false)
		b.noteFrame(1)
		at(start, answered+stallBudget-time.Second)
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired %v after the engine answered out of a stall: %v, want a fresh %v budget",
				time.Since(start)-answered, err, stallBudget)
		}
		at(start, answered+stallBudget+stallRecheck+2*stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v past the restored budget on a quiet engine, want context.DeadlineExceeded", err)
		}
	})
}

func TestStallAware_StopsAtTheExtensionCeiling(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		b.cpuTicks = advancingCPU()
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()
		if d, ok := ctx.Deadline(); !ok || !d.Equal(start.Add(stallBudget+stallExtendCeiling)) {
			t.Errorf("Deadline() = %v, %v, want the ceiling %v", d, ok, start.Add(stallBudget+stallExtendCeiling))
		}

		at(start, stallBudget+stallExtendCeiling-time.Millisecond)
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired at %v, want extended to the %v ceiling", time.Since(start), stallBudget+stallExtendCeiling)
		}
		at(start, stallBudget+stallExtendCeiling+stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v past the ceiling, want context.DeadlineExceeded", err)
		}
	})
}

// An engine that answers other sessions has a running event loop: a call it has not answered is slow, not stalled.
func TestStallAware_ExpiresWhileOtherFramesFlow(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		b.cpuTicks = advancingCPU()
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()
		flowing := make(chan struct{})
		go func() {
			defer close(flowing)
			for ctx.Err() == nil {
				b.noteFrame(1)
				time.Sleep(500 * time.Millisecond)
			}
		}()

		at(start, stallBudget+stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("ctx.Err() = %v at the budget while frames flowed, want context.DeadlineExceeded", err)
		}
		cancel()
		<-flowing
	})
}

func TestStallAware_ExpiresAtItsBudgetOnAHungEngine(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		b.cpuTicks = func() (uint64, bool) { return 42, true }
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()

		at(start, stallBudget-time.Millisecond)
		if err := ctx.Err(); err != nil {
			t.Fatalf("expired at %v, before its %v budget", time.Since(start), stallBudget)
		}
		at(start, stallBudget+stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v on a silent engine at flat CPU, want context.DeadlineExceeded", err)
		}
	})
}

func TestStallAware_AnUnreadableProbeBuysNoTime(t *testing.T) {
	defaultStallChecks(t)
	synctest.Test(t, func(t *testing.T) {
		b := newIdleEngine(t)
		var calls atomic.Uint64
		b.cpuTicks = func() (uint64, bool) { return calls.Add(1), false }
		start := time.Now()
		ctx, cancel := b.StallAware(t.Context(), stallBudget)
		defer cancel()

		at(start, stallBudget+stallSample)
		if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v with an unreadable CPU probe, want context.DeadlineExceeded at the budget", err)
		}
	})
}

// Callers test errors.Is(err, context.DeadlineExceeded) on the context they hold and on any context derived from
// it, and every other end keeps its own error.
func TestStallAware_KeepsEachEndsError(t *testing.T) {
	defaultStallChecks(t)
	cases := map[string]struct {
		run  func(t *testing.T, b *Engine) error
		want error
	}{
		"budget, seen through a derived context": {
			run: func(t *testing.T, b *Engine) error {
				ctx, cancel := b.StallAware(t.Context(), stallBudget)
				defer cancel()
				child, childCancel := context.WithTimeout(ctx, time.Hour)
				defer childCancel()
				<-child.Done()
				return child.Err()
			},
			want: context.DeadlineExceeded,
		},
		"caller's cancel": {
			run: func(t *testing.T, b *Engine) error {
				ctx, cancel := b.StallAware(t.Context(), stallBudget)
				cancel()
				return ctx.Err()
			},
			want: context.Canceled,
		},
		"parent cancelled": {
			run: func(t *testing.T, b *Engine) error {
				parent, parentCancel := context.WithCancel(t.Context())
				ctx, cancel := b.StallAware(parent, stallBudget)
				defer cancel()
				parentCancel()
				<-ctx.Done()
				return ctx.Err()
			},
			want: context.Canceled,
		},
		"parent's earlier deadline": {
			run: func(t *testing.T, b *Engine) error {
				parent, parentCancel := context.WithTimeout(t.Context(), time.Second)
				defer parentCancel()
				ctx, cancel := b.StallAware(parent, stallBudget)
				defer cancel()
				<-ctx.Done()
				if d, ok := ctx.Deadline(); !ok || d.After(time.Now()) {
					t.Errorf("Deadline() = %v, %v, want the parent's", d, ok)
				}
				return ctx.Err()
			},
			want: context.DeadlineExceeded,
		},
		"parent already done": {
			run: func(t *testing.T, b *Engine) error {
				parent, parentCancel := context.WithCancel(t.Context())
				parentCancel()
				ctx, cancel := b.StallAware(parent, stallBudget)
				defer cancel()
				return ctx.Err()
			},
			want: context.Canceled,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newIdleEngine(t)
				b.cpuTicks = func() (uint64, bool) { return 42, true }
				if err := tc.run(t, b); !errors.Is(err, tc.want) {
					t.Errorf("Err() = %v, want %v", err, tc.want)
				}
			})
		})
	}
}

func TestStalled(t *testing.T) {
	defaultStallChecks(t)
	flat := func() (uint64, bool) { return 42, true }
	cases := map[string]struct {
		cpu   func() (uint64, bool)
		setup func(b *Engine)
		want  bool
	}{
		"silent for stallQuiet and burning CPU": {
			cpu:   advancingCPU(),
			setup: func(b *Engine) { b.noteFrame(1); time.Sleep(stallQuiet) },
			want:  true,
		},
		"never read a frame, burning CPU": {cpu: advancingCPU(), want: true},
		"read a frame just now":           {cpu: advancingCPU(), setup: func(b *Engine) { b.noteFrame(1) }},
		"flat CPU":                        {cpu: flat},
		"unreadable probe":                {cpu: func() (uint64, bool) { return 0, false }},
		"engine ended":                    {cpu: advancingCPU(), setup: func(b *Engine) { close(b.done) }},
		"read a frame during the sample": {
			cpu:   advancingCPU(),
			setup: func(b *Engine) { go func() { time.Sleep(stallSample / 2); b.noteFrame(1) }() },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newIdleEngine(t)
				b.cpuTicks = tc.cpu
				if tc.setup != nil {
					tc.setup(b)
				}
				if got := b.Stalled(); got != tc.want {
					t.Errorf("Stalled() = %v, want %v", got, tc.want)
				}
				time.Sleep(stallSample) // lets a setup's goroutine end inside the bubble
			})
		})
	}
}

// initialize runs before any group is noted, so Stalled notes node's group itself, else it could never extend the
// handshake.
func TestStalled_NotesTheEngineGroupBeforeInitializeDoes(t *testing.T) {
	b := NewEngine("cli", t.TempDir())
	t.Cleanup(b.engineLane.close)
	b.cmd = exec.Command("sh", "-c", "setsid sleep 60 & wait")
	if err := b.cmd.Start(); err != nil {
		t.Fatalf("Setup: start the stand-in relay: %v", err)
	}
	t.Cleanup(func() { _ = b.cmd.Process.Kill(); _ = b.cmd.Wait() })
	b.cpuTicks = func() (uint64, bool) { return 0, false }
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.Stalled()
		if pid, ok := b.groupLedBy(b.cmd.Process.Pid); ok {
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Stalled never noted the group the relay's child leads")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
