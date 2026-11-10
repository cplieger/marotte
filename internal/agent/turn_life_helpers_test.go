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

// tryReserveNow is turnRegistry.tryReserve with a stamp taken now, for a test that needs only the verdict.
func (bc *bridgeCoordinator) tryReserveNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := bc.turns.tryReserve(chatID, source, bc.LifeStamp())
	return ok
}

// tryReserveIdleNow takes the admission slot with a stamp taken now iff nothing holds it.
func (bc *bridgeCoordinator) tryReserveIdleNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := bc.turns.tryReserveIdle(chatID, source, bc.LifeStamp())
	return ok
}

// admissionOutcome is reservePromptSlot's answer.
type admissionOutcome int

const (
	admissionAcquired admissionOutcome = iota
	admissionBusy
	admissionClosed
	admissionChatGone
	admissionStarting
)

// reservePromptSlot takes the chat's admission slot for a test's own prompt-class open, waiting up to wait for a
// holder that is not steerable: a steerable holder answers busy at once, a close committed since stamp closed.
func (bc *bridgeCoordinator) reservePromptSlot(
	ctx context.Context, chatID marotte.ChatID, wait time.Duration, stamp uint64, messageID string,
) (admissionOutcome, command.Reservation) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		lc, gate, err := bc.turns.admitLife(chatID, stamp)
		if err != nil {
			return admissionClosed, command.Reservation{}
		}
		var changed <-chan struct{}
		if lc != nil {
			lc.mu.Lock()
			a := bc.turns.reserveLocked(lc, marotte.TurnSourcePrompt, stamp, messageID)
			holder, held := lc.holderSourceLocked()
			busy := lc.kas.may().steerHolder && held && holder.SteerableHolder()
			changed, gate = lc.changed, a.idle
			lc.mu.Unlock()
			switch {
			case a.res.Token != 0:
				return admissionAcquired, a.res
			case a.gone:
				return admissionChatGone, command.Reservation{}
			case a.verdict == gateClosedSince:
				return admissionClosed, command.Reservation{}
			case busy:
				return admissionBusy, command.Reservation{}
			}
		}
		select {
		case <-changed:
		case <-gate:
		case <-timer.C:
			return admissionStarting, command.Reservation{}
		case <-ctx.Done():
			return admissionStarting, command.Reservation{}
		}
	}
}

// reserveForPromptNow is reservePromptSlot with a stamp taken now and no message id.
func (bc *bridgeCoordinator) reserveForPromptNow(ctx context.Context, chatID marotte.ChatID, wait time.Duration) admissionOutcome {
	out, _ := bc.reservePromptSlot(ctx, chatID, wait, bc.LifeStamp(), "")
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

// tryReserveIdleNow is tryReserveIdle with a stamp taken now.
func (r *turnRegistry) tryReserveIdleNow(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	_, ok := r.tryReserveIdle(chatID, source, r.gates.stamp())
	return ok
}

// tryReserveIdle takes the slot for a test iff nothing holds admission, an open wire turn included.
func (r *turnRegistry) tryReserveIdle(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (command.Reservation, bool) {
	lc, ok := r.tryLifecycleAt(chatID, stamp)
	if !ok {
		return command.Reservation{}, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, held := lc.holderSourceLocked(); held {
		return command.Reservation{}, false
	}
	a := r.reserveLocked(lc, source, stamp, "")
	return a.res, a.res.Token != 0
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

// switchedOK is applyModelSwitch's verdict as a bool, for a test that checks only whether the swap landed.
func (bc *bridgeCoordinator) switchedOK(ctx context.Context, chatID marotte.ChatID, mv *effortDecision) bool {
	return bc.applyModelSwitch(ctx, chatID, mv) == nil
}

// openTurnNow is openTurn on a chat whose life a test creates first, as a reservation does in production.
func (bc *bridgeCoordinator) openTurnNow(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) (string, error) {
	if _, ok := bc.turns.lookup(chatID); !ok {
		bc.turns.testLifecycle(chatID)
	}
	return bc.openTurn(ctx, chatID, open)
}

// foldTargetNow is TurnFoldTarget on a chat whose life a test creates first, as a forward's attach does.
func (bc *bridgeCoordinator) foldTargetNow(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn {
	if _, ok := bc.turns.lookup(chatID); !ok {
		bc.turns.testLifecycle(chatID)
	}
	return bc.TurnFoldTarget(ctx, chatID)
}

// openTurnNow is the Runtime's OpenTurn on a chat whose life a test creates first.
func (rt *Runtime) openTurnNow(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) error {
	if _, ok := rt.coord.turns.lookup(chatID); !ok {
		rt.coord.turns.testLifecycle(chatID)
	}
	_, err := rt.OpenTurn(ctx, chatID, open)
	return err
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

// held reports whether a close of chatID is being decided or torn down.
func (g *chatGates) held(chatID marotte.ChatID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.chats[chatID]
	return st != nil && st.holds > 0
}

// stopRequestedAfter reports a stop requested after turnID opened (chatLifecycle.stoppedAfterLocked).
func (bc *bridgeCoordinator) stopRequestedAfter(chatID marotte.ChatID, turnID string) bool {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.stoppedAfterLocked(turnID)
}

// turnOpenedAfter reports a turn opened after turnID on the chat's life.
func (bc *bridgeCoordinator) turnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(turnID)
	return t != nil && lc.nextSeq > t.Seq
}
