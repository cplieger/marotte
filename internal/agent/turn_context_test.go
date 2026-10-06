package agent

// Runtime.TurnContext, the lifecycle seam command handlers use instead of the shutdown context.

import (
	"context"
	"testing"
	"time"
)

// hubOnLifetime builds a runtime whose lifetime the test controls, so a case can end
// the APP's lifetime without going through Shutdown.
func hubOnLifetime(t *testing.T) (*Runtime, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h := New(ctx, "/tmp/work", func() ACPBridge { return newFakeBridge() }, newTestChatStore())
	return h, cancel
}

// TestTurnContext_SurvivesRequestCancel pins that a client drop cancels the POST context, but the turn's context must survive
// or the Call aborts and the reply is lost while kiro-cli keeps running.
func TestTurnContext_SurvivesRequestCancel(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	reqCtx, reqCancel := context.WithCancel(t.Context())
	turnCtx, cleanup := h.lifecycle.TurnContext(reqCtx)
	defer cleanup()

	// A mid-turn client disconnect.
	reqCancel()

	select {
	case <-turnCtx.Done():
		t.Fatal("turn context cancelled by request disconnect: the bridge Call would abort before turn_closed")
	case <-time.After(50 * time.Millisecond):
	}
	if err := turnCtx.Err(); err != nil {
		t.Fatalf("turn context Err = %v after request cancel, want nil", err)
	}
}

// TestTurnContext_CancelsOnShutdown pins that cancellation moves to the runtime's lifetime rather than disappearing.
func TestTurnContext_CancelsOnShutdown(t *testing.T) {
	h, _, _ := newTestHub()

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	defer cleanup()

	shutdownHub(t, h)

	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn context not cancelled on runtime shutdown")
	}
}

// TestTurnContext_CancelsOnAppLifetimeEnd pins that ending the app's lifetime reaches a turn without Runtime.Shutdown.
func TestTurnContext_CancelsOnAppLifetimeEnd(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	defer cleanup()

	cancelLifetime()

	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("turn context not cancelled when the app's lifetime ended; the runtime's " +
			"shutdown context is not derived from the context New was given")
	}
}

// TestTurnContext_CleanupCancels pins that cleanup cancels the turn context and unregisters the AfterFunc.
func TestTurnContext_CleanupCancels(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	turnCtx, cleanup := h.lifecycle.TurnContext(t.Context())
	cleanup()
	select {
	case <-turnCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cleanup did not cancel the turn context")
	}
}

// TestTurnContext_PreservesValues pins that WithoutCancel severs cancellation, not values.
func TestTurnContext_PreservesValues(t *testing.T) {
	h, cancelLifetime := hubOnLifetime(t)
	defer cancelLifetime()

	type ctxKey string
	const k ctxKey = "trace-id"
	reqCtx := context.WithValue(t.Context(), k, "abc123")

	turnCtx, cleanup := h.lifecycle.TurnContext(reqCtx)
	defer cleanup()

	if got := turnCtx.Value(k); got != "abc123" {
		t.Fatalf("turn context lost request-scoped value: got %v, want abc123", got)
	}
}
