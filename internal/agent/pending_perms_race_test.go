package agent

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestPendingPermsTracker_ConcurrentAddTakeList races Add, TakeIfPresent, ClearForChat and List under -race.
func TestPendingPermsTracker_ConcurrentAddTakeList(t *testing.T) {
	tracker := newPendingPermsTracker()
	const N = 100

	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range N {
			evt := marotte.ServerEvent{ChatID: marotte.ChatID("chat-1"), Type: "permission_needed"}
			tracker.Add(int64(i), evt)
		}
	})

	wg.Go(func() {
		for i := range N {
			evt := marotte.ServerEvent{ChatID: marotte.ChatID("chat-2"), Type: "permission_needed"}
			tracker.Add(int64(N+i), evt)
		}
	})

	wg.Go(func() {
		for i := range N {
			tracker.TakeIfPresent("chat-1", int64(i))
		}
	})

	wg.Go(func() {
		for range 10 {
			tracker.ClearForChat("chat-2")
		}
	})

	wg.Go(func() {
		for range N {
			_ = tracker.List("")
			_ = tracker.List("chat-1")
		}
	})

	wg.Wait()
}

// TestPendingPermsTracker_TakeIfPresent_OneWinnerPerRequest pins that every goroutine contends for one
// id, and exactly one may win; kiro-cli silently discards a loser's answer.
func TestPendingPermsTracker_TakeIfPresent_OneWinnerPerRequest(t *testing.T) {
	const answerers = 16
	const rounds = 200

	for round := range rounds {
		tracker := newPendingPermsTracker()
		id := int64(round)
		want := marotte.ServerEvent{ChatID: "chat-1", Type: marotte.EventPermissionNeeded}
		tracker.Add(id, want)

		var wins atomic.Int64
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range answerers {
			wg.Go(func() {
				<-start
				got, ok := tracker.TakeIfPresent("chat-1", id)
				if !ok {
					return
				}
				wins.Add(1)
				// The winner also gets the event, naming which kind it settled.
				if got.Type != want.Type || got.ChatID != want.ChatID {
					t.Errorf("winner got event %+v, want %+v", got, want)
				}
			})
		}
		close(start)
		wg.Wait()

		if n := wins.Load(); n != 1 {
			t.Fatalf("round %d: %d answerers claimed one request, want exactly 1", round, n)
		}
		if _, ok := tracker.TakeIfPresent("chat-1", id); ok {
			t.Fatalf("round %d: request still claimable after being taken", round)
		}
	}
}
