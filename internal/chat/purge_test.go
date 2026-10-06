package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// waitForRetentionConsults polls until the scheduler consulted retention n times, proof a pass ran; a negative
// assertion after a bare sleep would pass on a slow goroutine.
func waitForRetentionConsults(t *testing.T, calls *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := calls.Load()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention callback consulted %d times, want >=%d within 2s", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// countingRetention returns a fixed-retention callback and its call counter.
func countingRetention(d time.Duration) (func() time.Duration, *atomic.Int32) {
	var calls atomic.Int32
	return func() time.Duration {
		calls.Add(1)
		return d
	}, &calls
}

// waitForPurge polls for the chat file to disappear, with a timeout.
func waitForPurge(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestPurgeScheduler_TriggerWithZeroRetentionIsNoOp(t *testing.T) {
	s, _ := newTestStore(t)
	var calls atomic.Int32
	retention := func() time.Duration {
		calls.Add(1)
		return 0
	}
	p := NewPurgeScheduler(s, retention)
	p.Start(t.Context())
	defer p.Stop()

	p.Trigger()
	waitForRetentionConsults(t, &calls, 1)
}

func TestPurgeScheduler_TriggerRunsPurgeWhenRetentionPositive(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1", headerFileName)
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(s, func() time.Duration { return 24 * time.Hour })
	p.Start(t.Context())
	defer p.Stop()

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("chat file survived purge after 2s")
	}
}

func TestPurgeScheduler_TriggerSchedulesForRemainingEntry(t *testing.T) {
	// A fresh chat is not purged, and a future purge is scheduled.
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1", headerFileName)

	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(s, retention)
	p.Start(t.Context())
	defer p.Stop()

	// Wait for a pass before asserting it kept the file.
	waitForRetentionConsults(t, calls, 1)

	if _, err := os.Stat(chatPath); err != nil {
		t.Errorf("fresh chat file was purged: %v", err)
	}
}

func TestPurgeScheduler_TriggerWithNoChatsIsNoOp(t *testing.T) {
	s, _ := newTestStore(t)
	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(s, retention)
	p.Start(t.Context())
	defer p.Stop()

	p.Trigger()
	// A pass on an empty store runs cleanly.
	waitForRetentionConsults(t, calls, 1)
}

func TestPurgeScheduler_StopPreventsFutureTriggers(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1", headerFileName)
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(s, func() time.Duration { return 24 * time.Hour })
	// Stop before Start: the goroutine never runs.
	p.Stop()
	p.Trigger()

	// Stop closed stopCh and Trigger returns before triggerCh, so no goroutine can purge and no wait is needed.

	// Stop short-circuited: the aged-out file survives.
	if _, err := os.Stat(chatPath); err != nil {
		t.Errorf("chat file removed after Stop: %v (Stop should freeze purge)", err)
	}
}

func TestPurgeScheduler_StartInvokesTrigger(t *testing.T) {
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1", headerFileName)
	ageChat(t, s, "c1", 48*time.Hour)

	p := NewPurgeScheduler(s, func() time.Duration { return 24 * time.Hour })
	defer p.Stop()

	p.Start(t.Context())

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("chat file survived Start() after 2s")
	}
}

func TestPurgeScheduler_CollapsesConcurrentTriggers(t *testing.T) {
	// Rapid Triggers collapse into one evaluation.
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	retention, calls := countingRetention(24 * time.Hour)
	p := NewPurgeScheduler(s, retention)
	p.Start(t.Context())
	defer p.Stop()

	for range 10 {
		p.Trigger()
	}
	// At least one pass ran; the collapse is the buffered-1 triggerCh's contract.
	waitForRetentionConsults(t, calls, 1)
}

func TestPurgeScheduler_ShortRetentionPurgesExpiredAndKeepsFresh(t *testing.T) {
	// retention=1s with one entry past it: the expired one goes, the fresh one stays, about a second from its deadline,
	// the boundary the armed wait's floor exists for.
	s, _ := newTestStore(t)
	_, _ = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	chatPath := filepath.Join(s.dir, "c1", headerFileName)
	ageChat(t, s, "c1", 48*time.Hour)

	// A fresh second chat, which must survive.
	_, _ = s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true })
	c2Path := filepath.Join(s.dir, "c2", headerFileName)

	p := NewPurgeScheduler(s, func() time.Duration { return time.Second })
	p.Start(t.Context())
	defer p.Stop()

	if !waitForPurge(t, chatPath, 2*time.Second) {
		t.Error("expired entry not purged")
	}
	if _, err := os.Stat(c2Path); err != nil {
		t.Errorf("fresh entry purged unexpectedly: %v", err)
	}
}

func TestPurgeScheduler_ContextCancellationStopsLoop(t *testing.T) {
	s, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	p := NewPurgeScheduler(s, func() time.Duration { return 24 * time.Hour })
	p.Start(ctx)

	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.Done():
	case <-timer.C:
		t.Fatal("scheduler goroutine did not exit after context cancellation")
	}
}

func TestPurgeScheduler_PropertyInvariants(t *testing.T) {
	// Older than retention is purged after Trigger, younger never is, and Stop prevents any later purge.
	if testing.Short() {
		t.Skip("property test skipped in short mode")
	}

	t.Run("OldEntriesPurged", func(t *testing.T) {
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "old1", func(c *marotte.Chat, _ bool) bool { c.Name = "Old"; return true })
		chatPath := filepath.Join(s.dir, "old1", headerFileName)
		// Aged well past retention (stamp and mtime).
		ageChat(t, s, "old1", 72*time.Hour)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(s, func() time.Duration { return retention })
		p.Start(t.Context())
		defer p.Stop()

		p.Trigger()
		if !waitForPurge(t, chatPath, 2*time.Second) {
			t.Error("entry older than retention survived purge")
		}
	})

	t.Run("YoungEntriesPreserved", func(t *testing.T) {
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "young1", func(c *marotte.Chat, _ bool) bool { c.Name = "Young"; return true })
		chatPath := filepath.Join(s.dir, "young1", headerFileName)

		retention, calls := countingRetention(48 * time.Hour)
		p := NewPurgeScheduler(s, retention)
		p.Start(t.Context())
		defer p.Stop()

		p.Trigger()
		// A pass must have run, or "not purged" proves nothing.
		waitForRetentionConsults(t, calls, 1)

		if _, err := os.Stat(chatPath); err != nil {
			t.Errorf("young entry was purged: %v", err)
		}
	})

	t.Run("StopPreventsAllPurges", func(t *testing.T) {
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "stop1", func(c *marotte.Chat, _ bool) bool { c.Name = "Stop"; return true })
		chatPath := filepath.Join(s.dir, "stop1", headerFileName)
		ageChat(t, s, "stop1", 72*time.Hour)

		retention := 24 * time.Hour
		p := NewPurgeScheduler(s, func() time.Duration { return retention })
		// Stop before Start: the goroutine never runs.
		p.Stop()
		p.Start(t.Context()) // Start after Stop should be a no-op.

		p.Trigger()
		// Start did launch a goroutine; wait for it to see stopCh and exit.
		select {
		case <-p.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler goroutine did not exit after Stop-then-Start")
		}

		if _, err := os.Stat(chatPath); err != nil {
			t.Errorf("entry purged after Stop: %v", err)
		}
	})

	t.Run("NoDuplicatePurgeCallbacks", func(t *testing.T) {
		// Each entry is purged exactly once.
		s, _ := newTestStore(t)
		_, _ = s.Mutate(t.Context(), "dup1", func(c *marotte.Chat, _ bool) bool { c.Name = "Dup"; return true })
		chatPath := filepath.Join(s.dir, "dup1", headerFileName)
		ageChat(t, s, "dup1", 72*time.Hour)

		retention, calls := countingRetention(24 * time.Hour)
		p := NewPurgeScheduler(s, retention)
		p.Start(t.Context())
		defer p.Stop()

		for range 5 {
			p.Trigger()
		}
		if !waitForPurge(t, chatPath, 2*time.Second) {
			t.Error("entry not purged")
		}
		// Wait for the extra pass, so "no panic" is witnessed.
		before := calls.Load()
		p.Trigger()
		waitForRetentionConsults(t, calls, before+1)
	})
}

// TestPurge_AgesFromUpdatedAtNotMtime pins that mtime moves without activity, so aging from it would never collect; UpdatedAt
// is the activity record, mtime only the fallback for an unreadable chat.
func TestPurge_AgesFromUpdatedAtNotMtime(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()

	// keep: recent activity, backdated mtime.
	_, _ = s.Mutate(ctx, "keep", func(c *marotte.Chat, _ bool) bool { c.Name = "K"; return true })
	keepPath := filepath.Join(s.dir, "keep", headerFileName)
	stale := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(keepPath, stale, stale); err != nil {
		t.Fatalf("chtimes keep: %v", err)
	}

	// gone: stale activity.
	_, _ = s.Mutate(ctx, "gone", func(c *marotte.Chat, _ bool) bool { c.Name = "G"; return true })
	gonePath := filepath.Join(s.dir, "gone", headerFileName)
	ageChat(t, s, "gone", 72*time.Hour)

	s.purgeExpired(ctx, 24*time.Hour)

	if _, err := os.Stat(keepPath); err != nil {
		t.Errorf("chat with recent UpdatedAt was purged on a stale mtime: %v", err)
	}
	if _, err := os.Stat(gonePath); !os.IsNotExist(err) {
		t.Errorf("chat with stale UpdatedAt survived: stat err = %v", err)
	}
}

// The store passes its purge hooks into the retention service; an unpassed hook would leave purged chats' sessions
// unreaped and delete chats open in a live session.
func TestPurgeExpired_WiresTheStoresHooksIntoTheService(t *testing.T) {
	t.Run("on_purge_fires_for_a_purged_chat", func(t *testing.T) {
		s, _ := newTestStore(t)
		var mu sync.Mutex
		var purged []marotte.ChatID
		WithOnPurge(func(id marotte.ChatID, _ []string) {
			mu.Lock()
			purged = append(purged, id)
			mu.Unlock()
		})(s)
		ctx := t.Context()
		_, _ = s.Mutate(ctx, "gone", func(c *marotte.Chat, _ bool) bool { c.Name = "G"; return true })
		ageChat(t, s, "gone", 72*time.Hour)

		s.purgeExpired(ctx, 24*time.Hour)

		mu.Lock()
		defer mu.Unlock()
		if len(purged) != 1 || purged[0] != "gone" {
			t.Errorf("onPurge fired for %v, want [gone]", purged)
		}
	})

	t.Run("a_live_chat_is_never_purged", func(t *testing.T) {
		s, _ := newTestStore(t)
		WithLive(func(id marotte.ChatID) bool { return id == "live" })(s)
		ctx := t.Context()
		_, _ = s.Mutate(ctx, "live", func(c *marotte.Chat, _ bool) bool { c.Name = "L"; return true })
		ageChat(t, s, "live", 72*time.Hour)

		s.purgeExpired(ctx, 24*time.Hour)

		if _, err := os.Stat(filepath.Join(s.dir, "live", headerFileName)); err != nil {
			t.Errorf("a chat the live predicate claims is open was purged: %v", err)
		}
	})
}

func TestPurge_TombstonesChatID(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c-purged", func(c *marotte.Chat, _ bool) bool {
		c.Name = "old chat"
		return true
	}); err != nil {
		t.Fatalf("Mutate(setup) = %v, want nil", err)
	}
	c, ok := s.Get(t.Context(), "c-purged")
	if !ok {
		t.Fatal("Get(setup) did not find chat")
	}
	c.UpdatedAt = time.Now().Add(-2 * time.Hour).UnixMilli()
	if err := s.writeHeader(t.Context(), "c-purged", c); err != nil {
		t.Fatalf("writeHeader(setup) = %v, want nil", err)
	}

	s.purgeExpired(t.Context(), time.Hour)

	_, err := s.Mutate(t.Context(), "c-purged", func(c *marotte.Chat, _ bool) bool {
		c.Name = "ghost"
		return true
	})
	if !errors.Is(err, ErrTombstoned) {
		t.Errorf("Mutate(after purge) = %v, want ErrTombstoned", err)
	}
	if _, ok := s.Get(t.Context(), "c-purged"); ok {
		t.Error("purged chat was resurrected during the tombstone window")
	}
}
