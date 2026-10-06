package parallel

// Every assertion about the pool's SHAPE runs in a synctest bubble: on a real clock none of its
// properties is assertable as an equality, only as a bound wide enough to admit the behaviour being
// rejected.

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestBoundedParallel_EmptyItems(t *testing.T) {
	var called atomic.Int32
	Bounded(t.Context(), []int{}, 4, func(_, _ int) {
		called.Add(1)
	})
	if called.Load() != 0 {
		t.Errorf("fn called %d times for empty items, want 0", called.Load())
	}
}

// TestBoundedParallel_DegreeIsExactlyTheBound asserts BOTH directions of
// `min(len(items), maxWorkers)`: the pool never exceeds the bound, and it
// actually reaches it.
//
// The second direction is the one a real clock cannot hold. `peak <= maxWorkers`
// is satisfied by a pool that runs everything on one goroutine, so the test
// named for the concurrency bound could not tell a working pool from a serial
// loop.
func TestBoundedParallel_DegreeIsExactlyTheBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxWorkers = 3
		items := make([]int, 20)

		var peak, current atomic.Int32
		Bounded(t.Context(), items, maxWorkers, func(_, _ int) {
			cur := current.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
		})

		if got := peak.Load(); got != maxWorkers {
			t.Errorf("peak concurrency = %d, want exactly %d", got, maxWorkers)
		}
	})
}

// TestBoundedParallel_DegreeIsCappedByTheItemCount is the other arm of the min:
// a two-item call must not start eight goroutines.
func TestBoundedParallel_DegreeIsCappedByTheItemCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := make([]int, 2)

		var peak, current atomic.Int32
		Bounded(t.Context(), items, 8, func(_, _ int) {
			cur := current.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
		})

		if got := peak.Load(); got != int32(len(items)) {
			t.Errorf("peak concurrency = %d for %d items, want exactly %d",
				got, len(items), len(items))
		}
	})
}

// TestBoundedParallel_CancellationLandsOnTheNextItem pins the per-item ctx check
// the package doc claims ("cancellation lands mid-drain rather than only between
// batches") as an exact count.
//
// With two workers pulling in order, the item that cancels is index 5, its
// in-flight sibling is index 4, and both workers see the dead context on their
// next pull — so exactly six items run. `processed < len(items)` would also be
// satisfied by a check performed every tenth item.
func TestBoundedParallel_CancellationLandsOnTheNextItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cancelAt = 5
		ctx, cancel := context.WithCancel(t.Context())
		items := make([]int, 100)
		var processed atomic.Int32

		Bounded(ctx, items, 2, func(i, _ int) {
			processed.Add(1)
			if i == cancelAt {
				cancel()
			}
			time.Sleep(time.Millisecond)
		})

		// Indices 0..cancelAt: the canceller and every item dispatched before it.
		if got, want := processed.Load(), int32(cancelAt+1); got != want {
			t.Errorf("processed %d items after cancelling at index %d, want exactly %d: "+
				"the ctx check must run per item, not per batch", got, cancelAt, want)
		}
	})
}

// TestBounded_ReportsWhatItRanUnderCancellation: the return value tells a whole answer from a
// prefix, so it must survive the cancellation it reports.
func TestBounded_ReportsWhatItRanUnderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cancelAt = 5
		ctx, cancel := context.WithCancel(t.Context())
		items := make([]int, 100)
		var processed atomic.Int32

		done := Bounded(ctx, items, 2, func(i, _ int) {
			processed.Add(1)
			if i == cancelAt {
				cancel()
			}
			time.Sleep(time.Millisecond)
		})

		if got := int32(done); got != processed.Load() {
			t.Errorf("Bounded reported %d items run, fn ran %d times: the count must "+
				"survive the cancel that produced it", got, processed.Load())
		}
		if done >= len(items) {
			t.Errorf("Bounded reported %d of %d run after a cancel; a caller cannot "+
				"tell a prefix from the whole set", done, len(items))
		}
	})
}

// The ordinary path reports every item, or a caller that keys completeness on the
// count marks a healthy answer incomplete forever.
func TestBounded_ReportsEveryItemWhenNothingCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := make([]int, 25)
		done := Bounded(t.Context(), items, 4, func(_, _ int) {})
		if done != len(items) {
			t.Errorf("Bounded reported %d of %d run with no cancellation, want all",
				done, len(items))
		}
	})
}

// An empty call reports zero rather than falling through to whatever a caller's
// own `len(items)` comparison would make of it. `0 == len(nil)` keeps an empty
// scan COMPLETE, which is the honest verdict: there was nothing to read.
func TestBounded_EmptyReportsZero(t *testing.T) {
	if done := Bounded(context.Background(), []int(nil), 4, func(_, _ int) {}); done != 0 {
		t.Errorf("Bounded over no items reported %d, want 0", done)
	}
}

// TestBounded_SlowItemDoesNotStallTheRest is the property the pull-based shape exists for: a
// finished worker takes the next index while a slow item runs.
func TestBounded_SlowItemDoesNotStallTheRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			maxWorkers = 2
			slowIdx    = 0
		)
		items := make([]int, 12)
		var fastDone atomic.Int32
		// doneAtRelease is the count captured AT THE MOMENT the slow item is let
		// go. Reading fastDone after Bounded returns would be useless: by then
		// every item has run under either implementation.
		var doneAtRelease atomic.Int32
		slowReleased := make(chan struct{})

		go func() {
			// Everything else in the bubble is now durably blocked: the fast
			// items have all run or the implementation cannot run them, and the
			// slow one is parked on slowReleased.
			synctest.Wait()
			doneAtRelease.Store(fastDone.Load())
			close(slowReleased)
		}()

		Bounded(t.Context(), items, maxWorkers, func(i, _ int) {
			if i == slowIdx {
				<-slowReleased
				return
			}
			fastDone.Add(1)
		})

		if got, want := doneAtRelease.Load(), int32(len(items)-1); got != want {
			t.Errorf("%d of %d fast items finished while one item was held, want all %d: "+
				"a slow item stalled the others, which is the batching behaviour the "+
				"pull-based worker pool exists to avoid", got, want, want)
		}
	})
}
