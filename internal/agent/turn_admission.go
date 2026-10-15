package agent

// The per-chat admission slot, reserved synchronously before any bridge exists, so a prompt is admitted or refused
// before its turn_open. OpenTurn then makes the Turn and StartTurn stamps model and baseline. A wire turn holds no reservation.

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// Every reservation sends a session/prompt or opens a turn, so none is taken beside a bracketed wire turn
// (bracketedWireLocked). Caller holds mu.
func (lc *chatLifecycle) reserveLocked(source marotte.TurnOpenSource) bool {
	if lc.reserved || lc.bracketedWireLocked() {
		return false
	}
	lc.reserved = true
	lc.reservedSource = source
	return true
}

// holderSourceLocked names the admission holder: the bracket-owed prompt turn, else the reservation, else own. A
// prompt admitted beside a wire turn must read as Busy, and a bare wire turn still shows. Caller holds mu.
func (lc *chatLifecycle) holderSourceLocked() (marotte.TurnOpenSource, bool) {
	if lc.pending != nil {
		return lc.pending.Source, true
	}
	if lc.reserved {
		return lc.reservedSource, true
	}
	if lc.own != nil {
		return lc.own.Source, true
	}
	return 0, false
}

// tryReserve takes the slot iff free and the fence holds, never waiting (shell door, recovery retry, drain).
func (r *turnRegistry) tryReserve(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.fenceHoldsLocked(fence) && lc.reserveLocked(source)
}

// tryReserveIdle takes the slot iff nothing holds admission, an open wire turn included, in one acquisition.
func (r *turnRegistry) tryReserveIdle(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if _, held := lc.holderSourceLocked(); held {
		return false
	}
	return lc.reserveLocked(source)
}

// promptHolder names the prompt-class turn holding admission: the bracket-owed one, else a non-finalizing own one.
func (r *turnRegistry) promptHolder(chatID marotte.ChatID) (string, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return "", false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	switch {
	case lc.pending != nil && !lc.pending.finalizing:
		return lc.pending.ID, true
	case lc.own != nil && !lc.own.finalizing && lc.own.Source.PromptClass():
		return lc.own.ID, true
	}
	return "", false
}

// releaseReservation frees the admission slot and wakes every parked waiter,
// of which at most one acquires.
func (r *turnRegistry) releaseReservation(chatID marotte.ChatID) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.reserved = false
	lc.reservedSource = 0
	lc.wakeLocked()
}

// bracketedWireLocked reports an open wire turn a prompt must not reach: KAS aborts its running turn on any client
// session/prompt. A fold-opened wire turn is left out: no turn_end is owed for it, so it may never close, and
// refusing prompts beside it would strand the chat. Caller holds mu.
func (lc *chatLifecycle) bracketedWireLocked() bool {
	t := lc.own
	return t != nil && !t.finalizing && t.bracketed && t.Source == marotte.TurnSourceWireTurnStart
}

// reserveOrHolder tries the reservation and on refusal returns the holder and the next-change channel in one
// acquisition, so a release between try and park cannot strand the waiter.
func (r *turnRegistry) reserveOrHolder(chatID marotte.ChatID, source marotte.TurnOpenSource) (ok bool, holder marotte.TurnOpenSource, changed <-chan struct{}) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.reserveLocked(source) {
		return true, 0, nil
	}
	holder, _ = lc.holderSourceLocked()
	return false, holder, lc.changed
}

// admissionHolder reports the holder's source (open turn, else reservation), false when neither.
func (r *turnRegistry) admissionHolder(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.holderSourceLocked()
}

// TryReserveTurn takes the admission slot iff free, minting no Turn and never waiting.
func (bc *bridgeCoordinator) TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return bc.turns.tryReserve(chatID, source, command.TurnFence{})
}

// TryReserveIdleTurn takes the admission slot iff no turn is open and nothing holds it, never waiting.
func (bc *bridgeCoordinator) TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return bc.turns.tryReserveIdle(chatID, source)
}

// TryReserveTurnFenced is TryReserveTurn for a close's drain, refused once a turn
// opened after the closed one. The open checks the fence again where it appends.
func (bc *bridgeCoordinator) TryReserveTurnFenced(chatID marotte.ChatID, source marotte.TurnOpenSource, fence command.TurnFence) bool {
	return bc.turns.tryReserve(chatID, source, fence)
}

// PromptHolder names the prompt-class turn holding the chat's admission.
func (bc *bridgeCoordinator) PromptHolder(chatID marotte.ChatID) (string, bool) {
	return bc.turns.promptHolder(chatID)
}

// ReleaseTurnReservation frees the admission slot TryReserveTurn or
// ReserveTurnForPrompt took, waking every waiter.
func (bc *bridgeCoordinator) ReleaseTurnReservation(chatID marotte.ChatID) {
	bc.turns.releaseReservation(chatID)
}

// AdmissionHolderSource reports the holder: the open turn's source (a wire turn holds no reservation), else the
// reservation's. Satisfies command.turnOutcomeAccess.
func (bc *bridgeCoordinator) AdmissionHolderSource(chatID marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return bc.turns.admissionHolder(chatID)
}

// ReserveTurnForPrompt takes the slot for a prompt, waiting up to wait; one waiter acquires per wake. A
// steerable holder (a prompt-class turn, or a wire turn KAS bracketed) answers Busy at once; a local_shell holder
// parks the waiter and answers Starting at the budget. A dead ctx answers Starting.
func (bc *bridgeCoordinator) ReserveTurnForPrompt(ctx context.Context, chatID marotte.ChatID, wait time.Duration) command.AdmissionOutcome {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		ok, holder, changed := bc.turns.reserveOrHolder(chatID, marotte.TurnSourcePrompt)
		if ok {
			return command.AdmissionAcquired
		}
		if holder.SteerableHolder() {
			return command.AdmissionBusy
		}
		select {
		case <-changed:
		case <-timer.C:
			return bc.expiredAdmission(chatID)
		case <-ctx.Done():
			return command.AdmissionStarting
		}
	}
}

// wireTurn names the chat's open wire turn, bracketed or fold-opened, false when none.
func (r *turnRegistry) wireTurn(chatID marotte.ChatID) (*activeTurn, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.own
	if t == nil || t.finalizing || t.Source != marotte.TurnSourceWireTurnStart {
		return nil, false
	}
	return t, true
}

// ArmWireCancelGrace backstops a cancel of a turn KAS started: unless a turn_end closes it within d, it closes as
// cancelled, so a lost bracket, or a fold-opened turn no turn_end is owed for, cannot hold the chat's sends behind
// it until the bridge dies. The close names the turn, so it never reaches a later one. False when no such turn is
// open.
func (bc *bridgeCoordinator) ArmWireCancelGrace(chatID marotte.ChatID, d time.Duration) bool {
	t, open := bc.turns.wireTurn(chatID)
	if !open {
		return false
	}
	time.AfterFunc(d, func() {
		// Shutdown closes its own turns, with its own account.
		if _, open := bc.turns.turnByID(chatID, t.ID); !open || bc.draining() {
			return
		}
		slog.Warn("cancel unacked within grace; closing the agent's turn", "chat_id", chatID, "turn", t.ID, "grace", d)
		bc.finalizeTurn(context.Background(), chatID, &turnClose{Closer: closerCancelGrace, Stop: marotte.StopReasonCancelled, Turn: t.ID})
	})
	return true
}

// defaultIdleEndGrace is how long a wire turn waits for its turn_end once KAS reports no turn running. KAS 2.31
// sends that report just before the turn's turn_completion and turn_end, so only a lost end outlasts it.
const defaultIdleEndGrace = 10 * time.Second

// WireTurnActivity takes KAS's report of whether the chat's session runs a turn. While KAS reports none, the wire
// turn admission refuses prompts beside closes after the grace unless its turn_end or a report of a running turn
// comes first. No local silence closes it: KAS's activity counts its executions, so a slow model call, a retry or
// a compaction keeps the turn open.
func (bc *bridgeCoordinator) WireTurnActivity(chatID marotte.ChatID, active bool) {
	t, arm, ok := bc.turns.markKASActivity(chatID, active)
	if !ok {
		return
	}
	grace := cmp.Or(bc.idleEndGrace, defaultIdleEndGrace)
	time.AfterFunc(grace, func() {
		if !bc.turns.idleSince(chatID, t, arm) || bc.draining() {
			return
		}
		slog.Warn("the agent went idle without ending its turn; closing it", "chat_id", chatID, "turn", t.ID, "grace", grace)
		bc.finalizeTurn(context.Background(), chatID, &turnClose{
			Closer: closerKASIdle, Stop: marotte.StopReasonUnknown, Reason: kasIdleReason, Turn: t.ID,
		})
	})
}

// markKASActivity records the report on the open bracketed wire turn, returning the arm an idle close keys on when
// this report starts an idle stretch.
func (r *turnRegistry) markKASActivity(chatID marotte.ChatID, active bool) (*activeTurn, uint64, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return nil, 0, false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if !lc.bracketedWireLocked() {
		return nil, 0, false
	}
	t := lc.own
	if active || t.kasIdle {
		t.kasIdle = !active
		return nil, 0, false
	}
	t.kasIdle = true
	t.idleArms++
	return t, t.idleArms, true
}

// idleSince reports whether t is still the open bracketed wire turn and KAS has reported no running turn since arm.
func (r *turnRegistry) idleSince(chatID marotte.ChatID, t *activeTurn, arm uint64) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.own == t && lc.bracketedWireLocked() && t.kasIdle && t.idleArms == arm
}

// One last try first.
func (bc *bridgeCoordinator) expiredAdmission(chatID marotte.ChatID) command.AdmissionOutcome {
	ok, holder, _ := bc.turns.reserveOrHolder(chatID, marotte.TurnSourcePrompt)
	if ok {
		return command.AdmissionAcquired
	}
	if holder.SteerableHolder() {
		return command.AdmissionBusy
	}
	return command.AdmissionStarting
}

// The manager registers before Start, so presence is not liveness.
func (bc *bridgeCoordinator) bridgeLive(chatID marotte.ChatID) bool {
	sb := bc.bridge.mgr.get(chatID)
	return sb != nil && sb.startedPastSpawn()
}
