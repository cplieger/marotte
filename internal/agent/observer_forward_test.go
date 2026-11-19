package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// heldStreamSession is an observer session whose frame stream outlives its close until the test closes stream.
type heldStreamSession struct {
	*fakeBridge
	stream chan marotte.Notification
}

func (s *heldStreamSession) NotifCh() <-chan marotte.Notification { return s.stream }

// A pass's session may deliver frames after the pass returned; inflight covers them, so a drain of inflight (Shutdown,
// awaitBoot) finds every one applied.
func TestObserverPass_InflightCoversFramesAfterThePassReturned(t *testing.T) {
	h, _, _ := newTestHub()
	held := &heldStreamSession{fakeBridge: newFakeBridge(), stream: make(chan marotte.Notification, 1)}
	held.noGovernance, held.noDoorSeeds = true, true
	fakeSourceOf(t, h).setObserver(func() engineSession { return held })
	if err := h.observe(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
		t.Fatalf("observer pass: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		h.lifecycle.inflight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("inflight drained while the pass's stream was still open")
	case <-time.After(100 * time.Millisecond):
	}
	held.stream <- marotte.Notification{Msg: &marotte.RPCResponse{Method: methodV3SteeringDocs, Params: mustJSON(t, map[string]any{
		"sessionId": "s1", "status": "success", "documents": []map[string]any{{
			"uri":   "file:///workspace/.kiro/steering/a.md",
			"_meta": map[string]any{"kiro": map[string]any{"configIssues": []map[string]any{{"code": "contextReferenceUnresolved"}}}},
		}},
	})}}
	close(held.stream)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("inflight did not drain within 5s of the pass's stream ending")
	}
	if got := h.steeringIssues.snapshot(); len(got["workspace/.kiro/steering/a.md"]) != 1 {
		t.Errorf("issues after the drain = %+v, want the frame the stream delivered after the pass returned", got)
	}
}

// Once Shutdown has begun an observer pass is refused at its door: no frame consumer would join inflight, so the
// door's requests would wait out the handshake budget.
func TestObserverPass_RefusedOnceShutdownBegan(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	var doors atomic.Int32
	fakeSourceOf(t, h).setObserver(func() engineSession {
		b := newFakeBridge()
		b.onStart = func() { doors.Add(1) }
		return b
	})
	beginDraining(h)

	ran := false
	err := h.observe(t.Context(), func(context.Context, engineSession) error {
		ran = true
		return nil
	})
	if !errors.Is(err, errRuntimeDraining) {
		t.Errorf("observer pass once Shutdown began: err = %v, want errRuntimeDraining", err)
	}
	if ran {
		t.Error("the pass's fn ran once Shutdown began")
	}
	if n := doors.Load(); n != 0 {
		t.Errorf("doors opened = %d once Shutdown began, want 0", n)
	}
}
