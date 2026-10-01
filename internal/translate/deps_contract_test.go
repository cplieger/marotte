package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestStubDeps_Contract verifies that baseDeps satisfies the Deps
// interface contract: non-nil returns for required accessors and
// no panics on basic operations.
func TestStubDeps_Contract(t *testing.T) {
	d := newBaseDeps()

	// Verify interface satisfaction at compile time.
	var _ hostDouble = d

	ctx := t.Context()

	// Roles holds each interface directly, so the double implements the methods.
	// What is worth asserting is that they WORK, not that a getter is non-nil.
	if _, ok := d.Get(ctx, "no-such-chat"); ok {
		t.Error("Get on the nop store reported found")
	}
	if d.TurnFoldTarget(ctx, "c1") == nil {
		t.Error("TurnFoldTarget returned nil")
	}
	if _, ok := d.OwnTurn("no-such-chat"); ok {
		t.Error("OwnTurn(no-such-chat) reported an open turn")
	}
	d.RecordFromDiffs("c1", nil, 0, "")

	// MCPRecorder must be non-nil.
	if d.MCPRecorder() == nil {
		t.Error("MCPRecorder() returned nil")
	}

	// Broadcast must not panic.
	d.Broadcast(ctx, marotte.ServerEvent{})
}

// TestBaseDeps_FullContract mirrors the runtime's TranslateDepsContractTest
// assertions to catch drift between baseDeps and the Deps interface.
func TestBaseDeps_FullContract(t *testing.T) {
	d := newBaseDeps()
	ctx := t.Context()

	// The default store is nopChatRecords, so what is assertable here is that the
	// promoted methods reach it without panicking and report its no-op answers. A
	// round-trip belongs to the tests that install a real store.
	t.Run("chat_store_methods_are_reachable", func(t *testing.T) {
		if _, err := d.Mutate(ctx, "c1", func(*marotte.Chat, bool) bool { return true }); err != nil {
			t.Errorf("Mutate on the nop store returned %v, want nil", err)
		}
		if _, ok := d.Get(ctx, "c1"); ok {
			t.Error("Get on the nop store reported found")
		}
		if prompts, err := d.PromptTexts(ctx, "c1"); err != nil || len(prompts) != 0 {
			t.Errorf("PromptTexts on the nop store = %v, %v; want none, nil", prompts, err)
		}
	})

	t.Run("Broadcast_does_not_panic", func(t *testing.T) {
		d.Broadcast(ctx, marotte.ServerEvent{Type: "test_event", ChatID: "chat-1"})
	})

	t.Run("ParentACPSession_empty_for_unknown_chat", func(t *testing.T) {
		if s := d.ParentACPSession("unknown-chat"); s != "" {
			t.Errorf("ParentACPSession(unknown) = %q, want empty", s)
		}
	})

	t.Run("MCPRecorder_does_not_panic", func(t *testing.T) {
		r := d.MCPRecorder()
		if r == nil {
			t.Fatal("MCPRecorder() returned nil")
		}
		r.RecordConnected(ctx, "test-server", nil, nil, nil)
		r.SignalReady()
	})

	t.Run("PendingPermsAdd_does_not_panic", func(t *testing.T) {
		d.PendingPermsAdd(42, marotte.ServerEvent{Type: "permission_needed", ChatID: "c1"})
	})

	t.Run("NotifyPush_does_not_panic", func(t *testing.T) {
		d.NotifyPush(ctx, "test body", marotte.PushKindPermission, "")
	})

	t.Run("turn_and_line_methods_work", func(t *testing.T) {
		if d.TurnFoldTarget(ctx, "c1") == nil {
			t.Error("TurnFoldTarget returned nil")
		}
		d.RecordFromDiffs("c1", nil, 0, "")
	})

	t.Run("IsHookStatusEnabled_returns_bool", func(t *testing.T) {
		_ = d.IsHookStatusEnabled()
	})

	t.Run("IsScheduledRun_false_for_an_unmarked_run", func(t *testing.T) {
		// False is the default that matters: a manual run must never be reported
		// as scheduled, so the stub's zero value is the manual case.
		if d.IsScheduled("wf-unknown") {
			t.Error("IsScheduled(unknown) = true, want false")
		}
	})
}
