package agent

import (
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Matches KiroCrew's silence budget for upstream issue #3583.
const compactionFailedTurnBudget = 60 * time.Second

// CompactionFailed bounds a turn that may never get its response after a failed compaction. Backend activity and
// live tools restart the silence budget.
func (bc *bridgeCoordinator) CompactionFailed(chatID marotte.ChatID, detail string) {
	lc := bc.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		slog.Debug("compaction reap: no turn open", "chat_id", chatID)
		return
	}
	// The chain's origin, set once so it survives every re-arm: live tools are judged against it, and a per-arm cutoff
	// would retire a tool running since the failure.
	if lc.own.reapChainAt.IsZero() {
		lc.own.reapChainAt = time.Now()
	}
	bc.armCompactionReapLocked(lc, lc.own, detail)
}

func (bc *bridgeCoordinator) armCompactionReapLocked(lc *chatLifecycle, turn *activeTurn, detail string) {
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
	turn.reapTimer = time.AfterFunc(compactionFailedTurnBudget, func() {
		bc.expireCompactionReap(chatID, turnID, armID, seq, gen, detail)
	})
}

func (bc *bridgeCoordinator) expireCompactionReap(chatID marotte.ChatID, turnID string, armID, seq, gen uint64, detail string) {
	lc := bc.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	if lc.own == nil || lc.own.ID != turnID || lc.own.finalizing {
		lc.mu.Unlock()
		return
	}
	turn := lc.own
	// nil: a closer already stopped this reap; another arm id: a newer arm owns the timer.
	if turn.reapTimer == nil || turn.reapArmID != armID {
		lc.mu.Unlock()
		return
	}
	if turn.reapArmedSeq != seq || turn.reapArmedGen != gen {
		lc.mu.Unlock()
		return
	}
	if lc.observedSeq != seq || lc.fwdGen != gen || turn.Log.HasCallSince(turn.reapChainAt.UnixMilli()) {
		bc.armCompactionReapLocked(lc, turn, detail)
		lc.mu.Unlock()
		return
	}
	turn.reapTimer = nil
	lc.mu.Unlock()

	bc.InterruptTurn(chatID, detail)
}

func (*chatLifecycle) stopReapLocked(turn *activeTurn) {
	if turn.reapTimer == nil {
		return
	}
	turn.reapTimer.Stop()
	turn.reapTimer = nil
}
