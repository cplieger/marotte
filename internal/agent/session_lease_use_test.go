package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestSessionLease_ARefusedLeaseRunsNothing(t *testing.T) {
	r := newTurnRegistry(nopTurnRecords{})
	const chatID marotte.ChatID = "chat-lease"
	sb := newSharedBridge(r.testLifecycle(chatID))
	fb := newFakeBridge()
	sb.attach(fb)
	if _, ok := r.endLife(chatID); !ok {
		t.Fatalf("Setup: endLife found no lifecycle")
	}
	ran := false
	err := sb.use(t.Context(), func(context.Context, engineSession) error { ran = true; return nil })
	if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
		t.Errorf("use after the life ended = %v, want ErrChatClosedDuringCall", err)
	}
	if !errors.Is(err, marotte.ErrSessionClosing) {
		t.Errorf("use after the life ended = %v, want it to match ErrSessionClosing for the closing arms", err)
	}
	if ran {
		t.Errorf("the refused lease ran its callback")
	}
	if _, cErr := sb.Call(t.Context(), marotte.MethodCancel, nil); !errors.Is(cErr, marotte.ErrChatClosedDuringCall) {
		t.Errorf("Call after the life ended = %v, want ErrChatClosedDuringCall", cErr)
	}
	if calls := fb.callLog(); len(calls) != 0 {
		t.Errorf("the engine saw %v, want no request from a refused lease", calls)
	}
}

// lockProbeLife records whether its entry's mutex was free while beginLease ran.
type lockProbeLife struct {
	sb   *sharedBridge
	free *bool
}

func (l lockProbeLife) beginLease(*sharedBridge, context.CancelCauseFunc) (context.Context, func(), error) {
	if l.sb.mu.TryLock() {
		*l.free = true
		l.sb.mu.Unlock()
	}
	return context.Background(), func() {}, nil
}

func (lockProbeLife) closingSession(*sharedBridge) bool { return false }

func TestSessionLease_UseHoldsNoLockAcrossBeginLease(t *testing.T) {
	free := false
	life := &lockProbeLife{free: &free}
	sb := newSharedBridge(life)
	life.sb = sb
	sb.attach(newFakeBridge())
	if err := sb.use(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
		t.Fatalf("use = %v", err)
	}
	if !free {
		t.Errorf("beginLease ran with the entry's mutex held, want the two locks taken one after the other")
	}
}

func TestNewSharedBridge_PanicsOnANilLife(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "life") {
			t.Errorf("newSharedBridge(nil) panicked with %v, want a message naming the missing life", r)
		}
	}()
	newSharedBridge(nil)
	t.Errorf("newSharedBridge(nil) returned, want a panic")
}

func TestSessionLease_TheCommitCutsARunningLease(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	const chatID marotte.ChatID = "chat-cut"
	lc := h.coord.turns.testLifecycle(chatID)
	sb := newSharedBridge(lc)
	fb := newFakeBridge()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	fb.blockOn = map[string]chan struct{}{"x/held": block}
	sb.attach(fb)
	got := make(chan error, 1)
	go func() {
		_, _, err := sb.CallAt(context.Background(), "x/held", nil)
		got <- err
	}()
	if !awaitCall(t, fb, "x/held") {
		t.Fatal("Setup: the lease's call never reached the engine")
	}
	h.HoldChat(chatID).Commit()
	select {
	case err := <-got:
		if !errors.Is(err, marotte.ErrChatClosedDuringCall) {
			t.Errorf("the cut call = %v, want an error wrapping ErrChatClosedDuringCall", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the commit did not cut the running lease")
	}
	lc.mu.Lock()
	n := lc.work[workSession]
	lc.mu.Unlock()
	if n != 0 {
		t.Errorf("session work after the cut call returned = %d, want 0", n)
	}
}

// The commit has cut a running lease by the time it returns, so a check the lease makes after the commit fails.
func TestSessionLease_TheCommitHasCutTheLeaseWhenItReturns(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	const chatID marotte.ChatID = "chat-cut-sync"
	sb := newSharedBridge(h.coord.turns.testLifecycle(chatID))
	sb.attach(newFakeBridge())
	leased, proceed := make(chan context.Context, 1), make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- sb.use(context.Background(), func(ctx context.Context, _ engineSession) error {
			leased <- ctx
			<-proceed
			return ctx.Err()
		})
	}()
	ctx := <-leased

	h.HoldChat(chatID).Commit()
	cause := context.Cause(ctx)
	close(proceed)

	if !errors.Is(cause, marotte.ErrChatClosedDuringCall) {
		t.Errorf("the lease's cause when the commit returned = %v, want ErrChatClosedDuringCall", cause)
	}
	if err := <-returned; !errors.Is(err, marotte.ErrChatClosedDuringCall) {
		t.Errorf("use = %v, want the close's cut", err)
	}
}
