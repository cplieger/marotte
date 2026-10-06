package command

// A teardown must not depend on the bridge being present: a never-prompted chat, or one whose
// process is gone, still has permissions to clear and state to close.

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// closeDeps records the teardown steps and hands out the bridge the test
// scripts, including none at all.
type closeDeps struct {
	*benchDeps
	bridge  Bridge
	cleared []marotte.ChatID
	closed  []marotte.ChatID
}

func (d *closeDeps) Bridge(marotte.ChatID) Bridge { return d.bridge }

func (d *closeDeps) ClearPendingPermsForChat(id marotte.ChatID) {
	d.cleared = append(d.cleared, id)
}

func (d *closeDeps) CloseChatState(_ context.Context, id marotte.ChatID) {
	d.closed = append(d.closed, id)
}

// cancelBridge records the notifications the close cascade sends, which
// recordingBridge drops (it records Call, and a cancel is a Notify).
type cancelBridge struct {
	recordingBridge
	notified  []string
	notifyErr error
}

func (b *cancelBridge) Notify(_ context.Context, method string, _ any) error {
	b.notified = append(b.notified, method)
	return b.notifyErr
}

// A chat with no live bridge has no turn to cancel, and the rest of the
// teardown still has to run.
func TestCloseChatTeardown_TearsDownAChatWithNoBridge(t *testing.T) {
	deps := &closeDeps{benchDeps: newBenchDeps()}

	closeChatTeardown(t.Context(), deps, deps, deps, deps, "c1")

	if len(deps.cleared) != 1 || deps.cleared[0] != "c1" {
		t.Errorf("cleared permissions for %v, want exactly [c1]", deps.cleared)
	}
	if len(deps.closed) != 1 || deps.closed[0] != "c1" {
		t.Errorf("closed state for %v, want exactly [c1]", deps.closed)
	}
}

// With a live bridge the turn IS cancelled, and a cancel kiro-cli accepted
// leaves no failure line behind.
func TestCloseChatTeardown_CancelsTheTurnAndLogsNoFailure(t *testing.T) {
	logs := captureLogs(t)
	bridge := &cancelBridge{}
	deps := &closeDeps{benchDeps: newBenchDeps(), bridge: bridge}

	closeChatTeardown(t.Context(), deps, deps, deps, deps, "c1")

	if len(bridge.notified) != 1 || bridge.notified[0] != marotte.MethodCancel {
		t.Errorf("bridge saw notifications %v, want exactly [%s]", bridge.notified, marotte.MethodCancel)
	}
	if len(deps.closed) != 1 {
		t.Errorf("closed state for %v, want exactly [c1]", deps.closed)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("an accepted cancel logged a warning: %s", logs.String())
	}
}

// stopOrderBridge fails a Notify that arrives before the chat's stop was recorded:
// the stop has to be on the turn sequence before any signal can reach a bridge.
type stopOrderBridge struct {
	cancelBridge
	deps       *closeDeps
	unrecorded int
}

func (b *stopOrderBridge) Notify(ctx context.Context, method string, params any) error {
	if len(b.deps.stops) == 0 {
		b.unrecorded++
	}
	return b.cancelBridge.Notify(ctx, method, params)
}

// TestCmdCancel_RecordsTheStopBeforeSignalling: the empty-turn retry reads the stop
// after it registers its prompt call, which is only sound if the cancel records it
// before it signals.
func TestCmdCancel_RecordsTheStopBeforeSignalling(t *testing.T) {
	t.Run("with a bridge", func(t *testing.T) {
		deps := &closeDeps{benchDeps: newBenchDeps()}
		bridge := &stopOrderBridge{deps: deps}
		deps.bridge = bridge

		if _, err := CmdCancel(t.Context(), deps, deps, deps, deps, newStubSteerQueue(), &marotte.ClientCommand{ChatID: "c1"}); err != nil {
			t.Fatalf("CmdCancel: %v", err)
		}
		if len(bridge.notified) != 1 || bridge.unrecorded != 0 {
			t.Errorf("notifications = %v with %d before the stop was recorded, want one after it", bridge.notified, bridge.unrecorded)
		}
		if len(deps.stops) != 1 || deps.stops[0] != "c1" {
			t.Errorf("stops = %v, want [c1]", deps.stops)
		}
	})
	t.Run("with no bridge, the respawn window", func(t *testing.T) {
		deps := &closeDeps{benchDeps: newBenchDeps()}

		if _, err := CmdCancel(t.Context(), deps, deps, deps, deps, newStubSteerQueue(), &marotte.ClientCommand{ChatID: "c1"}); err != nil {
			t.Fatalf("CmdCancel: %v", err)
		}
		if len(deps.stops) != 1 || deps.stops[0] != "c1" {
			t.Errorf("stops = %v, want [c1]: a bridgeless cancel is exactly the stop a retry must see", deps.stops)
		}
	})
}

// TestCloseChatTeardown_RecordsTheStopBeforeSignalling: closing the tab is a stop, so
// a retry mid-respawn must not spawn a bridge and run the prompt for a closed tab.
func TestCloseChatTeardown_RecordsTheStopBeforeSignalling(t *testing.T) {
	deps := &closeDeps{benchDeps: newBenchDeps()}
	bridge := &stopOrderBridge{deps: deps}
	deps.bridge = bridge

	closeChatTeardown(t.Context(), deps, deps, deps, deps, "c1")

	if len(bridge.notified) != 1 || bridge.unrecorded != 0 {
		t.Errorf("notifications = %v with %d before the stop was recorded, want one after it", bridge.notified, bridge.unrecorded)
	}
	if len(deps.stops) != 1 || deps.stops[0] != "c1" {
		t.Errorf("stops = %v, want [c1]", deps.stops)
	}
}
