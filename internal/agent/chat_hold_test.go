package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestChatHold_CommitEndsTheLife(t *testing.T) {
	const chatID marotte.ChatID = "c-hold"
	t.Run("commit", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		turns := h.coord.turns
		lc := turns.testLifecycle(chatID)
		before := turns.gates.stamp()
		lc.mu.Lock()
		fence := command.TurnFence{Epoch: lc.epoch, Seq: lc.nextSeq}
		heldBefore := lc.fenceHoldsLocked(fence)
		lc.mu.Unlock()
		if !heldBefore {
			t.Fatalf("Setup: the fence does not hold before the commit")
		}
		hold := h.HoldChat(chatID)
		hold.Commit()
		if v, _ := turns.gates.admit(chatID, before); v != gateClosedSince {
			t.Errorf("a stamp from before the commit = %v, want gateClosedSince", v)
		}
		if !lc.endedNow() {
			t.Errorf("the life did not end at the commit")
		}
		if cause := context.Cause(lc.ctx); !errors.Is(cause, marotte.ErrChatClosedDuringCall) {
			t.Errorf("the life's context cause = %v, want ErrChatClosedDuringCall", cause)
		}
		lc.mu.Lock()
		held := lc.fenceHoldsLocked(fence)
		lc.mu.Unlock()
		if held {
			t.Errorf("a fence taken before the commit still holds")
		}
		if again, ok := turns.endLife(chatID); !ok || again != lc {
			t.Errorf("a second endLife = (%p, %v), want the same lifecycle %p", again, ok, lc)
		}
		hold.Release()
	})
	t.Run("release alone", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		turns := h.coord.turns
		lc := turns.testLifecycle(chatID)
		before := turns.gates.stamp()
		h.HoldChat(chatID).Release()
		if v, _ := turns.gates.admit(chatID, before); v != gateAdmitted {
			t.Errorf("a stamp from before an uncommitted hold = %v, want gateAdmitted", v)
		}
		if lc.endedNow() {
			t.Errorf("a hold released uncommitted ended the life")
		}
	})
	t.Run("a commit after the release ends nothing", func(t *testing.T) {
		h, _, _ := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		lc := h.coord.turns.testLifecycle(chatID)
		hold := h.HoldChat(chatID)
		hold.Release()
		hold.Commit()
		if lc.endedNow() {
			t.Errorf("a commit after the release ended the life")
		}
	})
}
