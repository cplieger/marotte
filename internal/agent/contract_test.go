package agent

import (
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// BridgeContractTest exercises the Start → Call → Notify → Respond → Stop lifecycle of any ACPBridge.
func BridgeContractTest(t *testing.T, newBridge func() ACPBridge) {
	t.Helper()

	t.Run("Start_sets_session_id", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id == "" {
			t.Error("SessionID empty after Start")
		}
	})

	t.Run("Start_with_existing_session", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), SessionID: "existing-sess", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		resp, err := b.Call(t.Context(), "session/prompt", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp == nil {
			t.Fatal("Call returned nil response")
		}
	})

	t.Run("Notify_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Notify(t.Context(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Respond_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Respond(t.Context(), 1, map[string]string{"ok": "true"}, nil); err != nil {
			t.Errorf("Respond: %v", err)
		}
	})

	t.Run("Stop_closes_NotifCh", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ch := b.NotifCh()
		b.Stop()
		// Closed after Stop.
		select {
		case _, ok := <-ch:
			if ok {
				// Draining is fine; eventually it must close.
				for range ch {
				}
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("NotifCh not closed after Stop")
		}
	})

	t.Run("ModelID_returns_value", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.ModelID(); id == "" {
			t.Error("ModelID empty after Start")
		}
	})
}

func TestFakeBridge_Contract(t *testing.T) {
	BridgeContractTest(t, func() ACPBridge {
		return newFakeBridge()
	})
}

// TestFakeBridge_SharedContract runs testsupport's ACPBridgePreStartContractTest against fakeBridge.
func TestFakeBridge_SharedContract(t *testing.T) {
	testsupport.ACPBridgePreStartContractTest(t, func() testsupport.ACPPreStartBridge {
		return newFakeBridge()
	})
}

func TestTestChatStore_Contract(t *testing.T) {
	testsupport.ChatStoreContractTest(t, func(t *testing.T) testsupport.ChatStoreContract {
		t.Helper()
		return newTestChatStore()
	})
}

func TestFakeMCPConfig_Contract(t *testing.T) {
	testsupport.MCPConfigContractTest(t, func(t *testing.T) testsupport.MCPNameSets {
		t.Helper()
		return &fakeMCPConfig{
			enabled:    map[string]struct{}{},
			configured: map[string]struct{}{},
		}
	})
}
