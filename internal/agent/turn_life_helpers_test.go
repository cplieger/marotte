package agent

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// testLifecycle admits a fresh stamp for chatID and returns its lifecycle, the way an admitted caller creates one.
func (r *turnRegistry) testLifecycle(chatID marotte.ChatID) *chatLifecycle {
	lc, err := r.lifecycleAt(context.Background(), chatID, r.gates.stamp())
	if err != nil {
		panic("testLifecycle: " + err.Error())
	}
	return lc
}

// tryReserveNow is TryReserveTurn with a stamp taken now, for a test that needs only the verdict.
func (bc *bridgeCoordinator) tryReserveNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := bc.TryReserveTurn(chatID, source, bc.LifeStamp())
	return ok
}

// tryReserveIdleNow is TryReserveIdleTurn with a stamp taken now.
func (bc *bridgeCoordinator) tryReserveIdleNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := bc.TryReserveIdleTurn(chatID, source, bc.LifeStamp())
	return ok
}

// reserveForPromptNow is ReserveTurnForPrompt with a stamp taken now and no message id.
func (bc *bridgeCoordinator) reserveForPromptNow(ctx context.Context, chatID marotte.ChatID, wait time.Duration) command.AdmissionOutcome {
	out, _ := bc.ReserveTurnForPrompt(ctx, chatID, wait, bc.LifeStamp(), "")
	return out
}

// releaseHeld releases whatever reservation chatID holds now.
func (bc *bridgeCoordinator) releaseHeld(chatID marotte.ChatID) { bc.turns.releaseHeld(chatID) }

func (r *turnRegistry) releaseHeld(chatID marotte.ChatID) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	token := lc.admission
	lc.mu.Unlock()
	r.releaseReservation(command.Reservation{Chat: chatID, Token: token})
}

// tryReserveIdleNow is the registry's tryReserveIdle with a stamp taken now.
func (r *turnRegistry) tryReserveIdleNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := r.tryReserveIdle(chatID, source, r.gates.stamp())
	return ok
}

// attachForwardNow attaches a forward to chatID's life with a stamp taken now, failing the test process on a
// refusal no test arranges.
func (r *turnRegistry) attachForwardNow(chatID marotte.ChatID) forwardAttachment {
	att, err := r.attachForward(context.Background(), chatID, r.gates.stamp())
	if err != nil {
		panic("attachForwardNow: " + err.Error())
	}
	return att
}

// orInsertNow is orInsertAt for a test: a stamp taken now and an always-admitting life.
func (bm *bridgeManager) orInsertNow(chatID marotte.ChatID) (*sharedBridge, bool) {
	sb, existed, _, err := bm.orInsertAt(chatID, bm.gates.stamp(), admitAllLife{})
	if err != nil {
		panic("orInsertNow: " + err.Error())
	}
	return sb, existed
}

// session is the entry's leased session, for a test's assertion.
func (sb *sharedBridge) session() engineSession {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.leased
}

// tryReserveFencedNow is TryReserveTurnFenced for a test that needs only the verdict.
func (bc *bridgeCoordinator) tryReserveFencedNow(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) bool {
	_, ok := bc.TryReserveTurnFenced(chatID, source, fence)
	return ok
}

// switchedOK is applyModelSwitch's verdict as a bool, for a test that checks only whether the swap landed.
func (bc *bridgeCoordinator) switchedOK(ctx context.Context, chatID marotte.ChatID, mv *effortDecision) bool {
	return bc.applyModelSwitch(ctx, chatID, mv) == nil
}

// openTurnNow is OpenTurn on a chat whose life a test creates first, as a reservation does in production.
func (bc *bridgeCoordinator) openTurnNow(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) (string, error) {
	if _, ok := bc.turns.lookup(chatID); !ok {
		bc.turns.testLifecycle(chatID)
	}
	return bc.OpenTurn(ctx, chatID, open)
}

// foldTargetNow is TurnFoldTarget on a chat whose life a test creates first, as a forward's attach does.
func (bc *bridgeCoordinator) foldTargetNow(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn {
	if _, ok := bc.turns.lookup(chatID); !ok {
		bc.turns.testLifecycle(chatID)
	}
	return bc.TurnFoldTarget(ctx, chatID)
}

// openTurnNow is the Runtime's OpenTurn on a chat whose life a test creates first.
func (rt *Runtime) openTurnNow(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) (string, error) {
	if _, ok := rt.coord.turns.lookup(chatID); !ok {
		rt.coord.turns.testLifecycle(chatID)
	}
	return rt.OpenTurn(ctx, chatID, open)
}

// ensureTestLife gives chatID the lifecycle an admitted caller or a forward's attach creates, unless it has one.
func (r *turnRegistry) ensureTestLife(chatID marotte.ChatID) {
	if _, ok := r.lookup(chatID); ok {
		return
	}
	if isRunChat(chatID) {
		r.lifecycleForRun(chatID)
		return
	}
	r.testLifecycle(chatID)
}

// insertBridgeNow is bridgeManager.insert for a test, giving the chat the life its door would have created.
func (rt *Runtime) insertBridgeNow(chatID marotte.ChatID, sb *sharedBridge) bool {
	rt.coord.turns.ensureTestLife(chatID)
	return rt.bridge.mgr.insert(chatID, sb)
}
