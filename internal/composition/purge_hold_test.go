package composition

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestHeldUnlessDraining_ShutdownWaitsForAChainCloseUnderway(t *testing.T) {
	h := testRuntime(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	onPurge := heldUnlessDraining(h.HoldUnlessDraining, func(marotte.ChatID, []string) {
		close(entered)
		<-release
	})
	purged := make(chan struct{})
	go func() {
		defer close(purged)
		onPurge("c1", []string{"s1"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the chain close never ran")
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := h.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	}()
	select {
	case <-stopped:
		t.Error("Shutdown returned while the chain close was still running")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	<-purged
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return once the chain close finished")
	}
}

func TestHeldUnlessDraining_ClosesNothingOnceShutdownBegan(t *testing.T) {
	h := testRuntime(t)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("runtime shutdown: %v", err)
	}
	closed := false
	heldUnlessDraining(h.HoldUnlessDraining, func(marotte.ChatID, []string) { closed = true })("c1", []string{"s1"})
	if closed {
		t.Error("the chain was closed after Shutdown began, want the close skipped")
	}
}
