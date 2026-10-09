package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// held is a store with one server, whose wait setting reads on, and a SetEnabled parked inside its persist (on the
// write turn, holding the write lock) until release runs; release waits for that write to finish.
type held struct {
	s       *Store
	on      *atomic.Bool
	release func()
	kasPath string
	id      ServerID
}

func heldStore(t *testing.T) held {
	t.Helper()
	dir := t.TempDir()
	kasPath := filepath.Join(dir, "kas", "mcp.json")
	on := new(atomic.Bool)
	var park atomic.Bool
	parked := make(chan struct{})
	freed := make(chan struct{})
	s, err := New(t.Context(), dir, nil, WithKASConfigPath(kasPath),
		WithWaitForReady(func(context.Context) (bool, bool) {
			if park.CompareAndSwap(true, false) {
				close(parked)
				<-freed
			}
			return on.Load(), true
		}))
	if err != nil {
		t.Fatalf("Setup: New: %v", err)
	}
	created, err := s.Create(t.Context(), waitServers()[0])
	if err != nil {
		t.Fatalf("Setup: Create: %v", err)
	}
	park.Store(true)
	written := make(chan error, 1)
	go func() {
		_, err := s.SetEnabled(context.Background(), created.ID, !created.Enabled)
		written <- err
	}()
	<-parked
	var once atomic.Bool
	release := func() {
		if once.CompareAndSwap(false, true) {
			close(freed)
			if err := <-written; err != nil {
				t.Errorf("the parked SetEnabled: %v", err)
			}
		}
	}
	t.Cleanup(release)
	return held{s: s, on: on, release: release, kasPath: kasPath, id: created.ID}
}

// awaitWithin fails the test when fn has not returned within limit; fn keeps running after a failure.
func awaitWithin[T any](t *testing.T, limit time.Duration, what string, fn func() T) T {
	t.Helper()
	got := make(chan T, 1)
	go func() { got <- fn() }()
	select {
	case v := <-got:
		return v
	case <-time.After(limit):
		t.Fatalf("%s had not returned after %v while another writer held the store", what, limit)
		var zero T
		return zero
	}
}

func TestRenderKASConfig_GivesUpOnAHeldStoreAndTheNextRenderLands(t *testing.T) {
	h := heldStore(t)
	s, kasPath, on, release := h.s, h.kasPath, h.on, h.release

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	on.Store(true)
	err := awaitWithin(t, 5*time.Second, "RenderKASConfig with a 50ms context", func() error { return s.RenderKASConfig(ctx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RenderKASConfig over a held store = %v, want context.DeadlineExceeded", err)
	}
	on.Store(false)

	release()
	if wait, known := s.RenderedWaitForReady(); !known || wait {
		t.Errorf("RenderedWaitForReady after a render that gave up = (%t, %t), want the parked write's (false, true)", wait, known)
	}
	on.Store(true)
	if err := s.RenderKASConfig(t.Context()); err != nil {
		t.Fatalf("RenderKASConfig once the store is free: %v", err)
	}
	if v := readKASServers(t, kasPath)["local"]["waitForReady"]; v != true {
		t.Errorf("after the retry, waitForReady = %v, want true", v)
	}
	if wait, known := s.RenderedWaitForReady(); !known || !wait {
		t.Errorf("RenderedWaitForReady after the retry = (%t, %t), want (true, true)", wait, known)
	}
}

func TestRenderKASConfig_AnAbandonedWaitLeavesNoGoroutineBehind(t *testing.T) {
	s := heldStore(t).s
	baseline := runtime.NumGoroutine()

	for range 20 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
		err := s.RenderKASConfig(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RenderKASConfig over a held store = %v, want context.DeadlineExceeded", err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Errorf("goroutines after 20 abandoned renders over a held store = %d, want at most %d: a wait outlived its caller", n, baseline)
	}
}

func TestSetEnabled_WaitingBehindAnotherWriterGivesUpWithItsContext(t *testing.T) {
	h := heldStore(t)
	s, id, release := h.s, h.id, h.release

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := awaitWithin(t, 5*time.Second, "SetEnabled with a 50ms context", func() error {
		_, err := s.SetEnabled(ctx, id, true)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SetEnabled behind a held store = %v, want context.DeadlineExceeded", err)
	}
	release()
}

func TestRenderedWaitForReady_AnswersWhileTheStoreIsHeld(t *testing.T) {
	s := heldStore(t).s

	type answer struct{ wait, known bool }
	got := awaitWithin(t, 5*time.Second, "RenderedWaitForReady", func() answer {
		wait, known := s.RenderedWaitForReady()
		return answer{wait, known}
	})
	if got != (answer{wait: false, known: true}) {
		t.Errorf("RenderedWaitForReady over a held store = %+v, want the last landed write's (false, true)", got)
	}
}
