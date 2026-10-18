package agent

import (
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A record still in its door holds no session: every forward refuses rather than dereferencing it, and the command
// layer sees no bridge.
func TestSharedBridge_BeforeItsDoorAttachesEveryForwardRefuses(t *testing.T) {
	sb := &sharedBridge{state: bridgeStarting, settled: newSessionSettle()}
	ctx := t.Context()
	if _, err := sb.Call(ctx, marotte.MethodPrompt, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Call before attach = %v, want ErrSessionClosed", err)
	}
	if _, _, err := sb.CallAt(ctx, marotte.MethodPrompt, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("CallAt before attach = %v, want ErrSessionClosed", err)
	}
	if err := sb.Notify(ctx, marotte.MethodCancel, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Notify before attach = %v, want ErrSessionClosed", err)
	}
	if err := sb.Respond(ctx, 1, nil, nil); !errors.Is(err, marotte.ErrSessionClosed) {
		t.Errorf("Respond before attach = %v, want ErrSessionClosed", err)
	}
	if id := sb.SessionID(); id != "" {
		t.Errorf("SessionID before attach = %q, want empty", id)
	}

	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.bridge.mgr.insert("c1", sb)
	if got := (bridgeRole{coord: h.coord}).Bridge("c1"); got != nil {
		t.Errorf("Bridge for a record in its door = %v, want nil", got)
	}
	sb.attach(newFakeBridge())
	if got := (bridgeRole{coord: h.coord}).Bridge("c1"); got == nil {
		t.Error("Bridge once the door attached its session = nil, want the record")
	}
}
