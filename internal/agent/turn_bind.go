package agent

// A binding must be revisable: `agentInitiated` rides content frames, never the
// bracket, so a prompted turn_start cannot be told from an agent-initiated one.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// withLifecycle runs fn with the chat's mutex HELD, after waiting out a finalizing
// own turn, and reports ctx.Err() when ctx died first. It is the one door through
// which a turn_open append and its registry record become one operation: fn
// appends through the store and then calls openLocked, so lock order is lifecycle
// then store and no fold can open a turn between the two.
func (r *turnRegistry) withLifecycle(ctx context.Context, chatID marotte.ChatID, fn func(lc *chatLifecycle) error) error {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return ctx.Err()
	}
	defer lc.mu.Unlock()
	return fn(lc)
}

// bindPending binds an unacknowledged wire turn_start to this chat's pending turn,
// reporting whether one took it and, when it did not, the id of a DIFFERENT
// bracketed own turn whose turn_end was lost and which the caller closes first. The
// pending turn leaves that slot and BECOMES own. An empty lost id with bound false
// means the chat holds no turn for the bracket, and the caller opens one. It waits
// out a finalizing own turn like every other door, so a finalizing turn is absent
// by the time the three cases are read; false on both when ctx died first.
func (r *turnRegistry) bindPending(ctx context.Context, chatID marotte.ChatID) (bound bool, lost string) {
	err := r.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		p := lc.pending
		if p == nil || p.acked || p.finalizing {
			if lc.own != nil && lc.own.acked {
				lost = lc.own.ID
			}
			return nil
		}
		if lc.own != nil && lc.own != p {
			lost = lc.own.ID
			return nil
		}
		p.acked = true
		lc.pending = nil
		lc.own = p
		lc.setStateLocked(turnOpen)
		bound = true
		return nil
	})
	if err != nil {
		return false, ""
	}
	return bound, lost
}

// foldTarget is the own turn's accumulator, or false when the chat has none. False
// also for a turn mid-finalize, so the caller opens a wire_turn_start turn rather
// than folding into one whose closer already took its content.
func (r *turnRegistry) foldTarget(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		return nil, false
	}
	return lc.own.Log, true
}

// promptTurn is the chat's prompt-class turn awaiting or holding its bracket, the
// one a turn_bind joins; false when none is open.
func (r *turnRegistry) promptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.pending != nil && !lc.pending.finalizing {
		return lc.pending.Log, true
	}
	if lc.own != nil && lc.own.Source.Acknowledgeable() && !lc.own.finalizing {
		return lc.own.Log, true
	}
	return nil, false
}

// revisableLocked reports the provisionally bound prompt turn a frame carrying
// agentInitiated re-targets, nil when the chat holds none. Caller holds mu.
func (lc *chatLifecycle) revisableLocked() *Turn {
	pre := lc.own
	if pre == nil || !pre.acked || !pre.Source.Acknowledgeable() || pre.finalizing {
		return nil
	}
	return pre
}

// reviseLocked re-targets routing after the caller appended the agent turn's
// turn_open and opened it as own: the agent's turn began when the bracket did, and
// the prompt's turn drops back to pending-only, owed its bracket again. Entries
// already sealed into the prompt's turn stay where they are. Caller holds mu.
func (lc *chatLifecycle) reviseLocked(pre, agent *Turn) {
	agent.acked = true
	agent.Opened = pre.Opened
	pre.acked = false
	lc.pending = pre
	slog.Info("a frame revised a provisional turn binding to agent-initiated",
		"chat_id", pre.Chat, "prompt_turn", pre.ID, "agent_turn", agent.ID)
}
