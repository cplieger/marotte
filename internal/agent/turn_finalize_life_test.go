package agent

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// A close that commits while a turn's tail runs stops the tail before its next step: the steps after the commit
// write nothing into the chat's record.
func TestAfterTurnClose_ALifeEndedMidTailRunsNoLaterStep(t *testing.T) {
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, "c1")
	var mu sync.Mutex
	var steps []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, s)
	}
	committed := make(chan struct{})
	settled := h.coord.steerKASSettled
	h.coord.steerKASSettled = func(chatID marotte.ChatID) {
		record("kas settled")
		if settled != nil {
			settled(chatID)
		}
		hold := h.HoldChat(chatID)
		hold.Commit()
		t.Cleanup(hold.Release)
		close(committed)
	}
	h.coord.settleNotices = func(context.Context, marotte.ChatID) { record("notices") }
	tailHooks(h, func(_ marotte.ChatID, ended bool) {
		if ended {
			record("tail ended")
		}
	})

	turnID, _ := h.stagePromptTurn(t, "c1")
	endTurn(t, h, "c1", turnID)
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn's tail never reached its KAS-idle step")
	}
	lc, ok := h.coord.turns.lookup("c1")
	if !ok {
		t.Fatal("the committed chat's lifecycle is gone before its teardown")
	}
	select {
	case <-lc.quiet:
	case <-time.After(5 * time.Second):
		t.Fatal("the ended life's tail never finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(steps, []string{"kas settled"}) {
		t.Errorf("tail steps = %v, want only the step the commit landed in", steps)
	}
}
