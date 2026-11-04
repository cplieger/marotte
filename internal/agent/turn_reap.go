package agent

import (
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Matches KiroCrew's silence budget for upstream issue #3583.
const compactionFailedTurnBudget = 60 * time.Second

// compactionStallCeiling caps how long an engine stall defers the reap past the budget, above the 81.6 s event-loop
// stall measured on kiro-cli 2.37 (kirodotdev/Kiro#12261).
const compactionStallCeiling = 120 * time.Second

// CompactionFailed bounds the named turn, which the failure was recorded in, since it may never get its response.
// Backend activity and live tools restart the silence budget. A turn no longer own is not armed.
func (bc *bridgeCoordinator) CompactionFailed(chatID marotte.ChatID, turnID, detail string) {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.ID != turnID || lc.own.finalizing {
		slog.Debug("compaction reap: the turn is not open", "chat_id", chatID, "turn", turnID)
		return
	}
	// The chain's origin, set once so it survives every re-arm: live tools are judged against it, and a per-arm cutoff
	// would retire a tool running since the failure.
	if lc.own.reapChainAt.IsZero() {
		lc.own.reapChainAt = time.Now()
	}
	bc.armCompactionReapLocked(lc, lc.own, detail)
}

// armCompactionReapLocked starts a fresh silence budget; it ends any stall extension, so a later stall gets its own.
func (bc *bridgeCoordinator) armCompactionReapLocked(lc *chatLifecycle, turn *activeTurn, detail string) {
	turn.reapStallUntil = time.Time{}
	bc.armCompactionReapForLocked(lc, turn, compactionFailedTurnBudget, detail)
}

func (bc *bridgeCoordinator) armCompactionReapForLocked(lc *chatLifecycle, turn *activeTurn, d time.Duration, detail string) {
	lc.stopReapLocked(turn)
	turn.reapArmID++
	turn.reapArmedSeq = lc.observedSeq
	turn.reapArmedGen = lc.fwdGen
	// Value copies only: capturing the timer races the armer's assignment (the callback can run before AfterFunc
	// returns). The arm id is the identity, compared under lc.mu.
	armID := turn.reapArmID
	turnID := turn.ID
	seq := turn.reapArmedSeq
	gen := turn.reapArmedGen
	chatID := turn.Chat
	turn.reapTimer = time.AfterFunc(d, func() {
		bc.expireCompactionReap(chatID, turnID, armID, seq, gen, detail)
	})
}

type compactionReapVerdict int

const (
	reapStale compactionReapVerdict = iota // the turn closed, or a newer arm owns the timer
	reapRearm                              // the turn spoke inside the budget
	reapDue                                // the turn stayed silent for the whole budget
)

func (bc *bridgeCoordinator) expireCompactionReap(chatID marotte.ChatID, turnID string, armID, seq, gen uint64, detail string) {
	// lookup: an expiry outliving forget must not mint an empty lifecycle for the chat.
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return
	}
	lc.mu.Lock()
	turn, verdict := compactionReapVerdictLocked(lc, turnID, armID, seq, gen)
	if verdict == reapDue {
		// Stalled blocks for its CPU sample, so it runs unlocked and the verdict is re-read after it.
		lc.mu.Unlock()
		stalled := bc.engineStalled()
		lc.mu.Lock()
		turn, verdict = compactionReapVerdictLocked(lc, turnID, armID, seq, gen)
		if verdict == reapDue && stalled && bc.extendCompactionReapLocked(lc, turn, detail) {
			lc.mu.Unlock()
			return
		}
	}
	if verdict == reapStale {
		lc.mu.Unlock()
		return
	}
	if verdict == reapRearm {
		bc.armCompactionReapLocked(lc, turn, detail)
		lc.mu.Unlock()
		return
	}
	turn.reapTimer = nil
	// Claimed in the section that validated the turn, so a close and a new prompt after the unlock meet only
	// this turn's own cancel.
	cancel, claimed := lc.interruptLocked(turn, marotte.InterruptCause(detail))
	lc.mu.Unlock()
	if !claimed {
		slog.Debug("compaction reap: another cause already claimed the turn", "chat_id", chatID, "turn", turnID)
		return
	}
	slog.Warn("the turn stayed silent after a failed compaction; interrupting it",
		"chat_id", chatID, "turn", turnID, "budget", compactionFailedTurnBudget)
	tripPromptCall(chatID, turnID, cancel)
}

func compactionReapVerdictLocked(lc *chatLifecycle, turnID string, armID, seq, gen uint64) (*activeTurn, compactionReapVerdict) {
	if lc.own == nil || lc.own.ID != turnID || lc.own.finalizing {
		return nil, reapStale
	}
	turn := lc.own
	// nil: a closer already stopped this reap; another arm id: a newer arm owns the timer.
	if turn.reapTimer == nil || turn.reapArmID != armID || turn.reapArmedSeq != seq || turn.reapArmedGen != gen {
		return nil, reapStale
	}
	if lc.observedSeq != seq || lc.fwdGen != gen || turn.Log.HasCallSince(turn.reapChainAt.UnixMilli()) {
		return turn, reapRearm
	}
	return turn, reapDue
}

// extendCompactionReapLocked re-arms a due reap while the whole engine is stalled (kirodotdev/Kiro#12261), whose
// silence is not the turn's, up to compactionStallCeiling past the first stalled expiry. False once that has passed.
func (bc *bridgeCoordinator) extendCompactionReapLocked(lc *chatLifecycle, turn *activeTurn, detail string) bool {
	until := turn.reapStallUntil
	if until.IsZero() {
		until = time.Now().Add(compactionStallCeiling)
	}
	left := time.Until(until)
	if left <= 0 {
		return false
	}
	bc.armCompactionReapForLocked(lc, turn, min(compactionFailedTurnBudget, left), detail)
	turn.reapStallUntil = until
	slog.Warn("the turn is silent after a failed compaction, but kiro-cli's engine is stalled while working; waiting",
		"chat_id", turn.Chat, "turn", turn.ID, "wait_left_s", int(left.Seconds()))
	return true
}

func (bc *bridgeCoordinator) engineStalled() bool {
	h, _ := bc.engine.current()
	return h != nil && h.Stalled()
}

func (*chatLifecycle) stopReapLocked(turn *activeTurn) {
	if turn.reapTimer == nil {
		return
	}
	turn.reapTimer.Stop()
	turn.reapTimer = nil
}
