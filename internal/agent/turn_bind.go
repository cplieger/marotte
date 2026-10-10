package agent

// A binding must be revisable: `agentInitiated` rides content frames, never the
// bracket, so a prompted turn_start cannot be told from an agent-initiated one.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// withLifecycle runs fn with the chat's mutex held, after waiting out a finalizing own turn, or returns ctx.Err().
// It is the one door making a turn_open append and its registry record one operation (lifecycle then store).
func (r *turnRegistry) withLifecycle(ctx context.Context, chatID marotte.ChatID, fn func(lc *chatLifecycle) error) error {
	lc := r.lifecycleFor(chatID)
	if !lc.awaitNotFinalizing(ctx) {
		return ctx.Err()
	}
	defer lc.mu.Unlock()
	return fn(lc)
}

// If not, it returns the id of a bracketed own turn whose turn_end was lost, for the caller to
// close. Neither means open a turn. It waits out finalizing; false on both when ctx died.
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

// foldTarget is the own turn's accumulator, false when none or finalizing, so the caller opens a wire turn instead.
func (r *turnRegistry) foldTarget(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	lc := r.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.own == nil || lc.own.finalizing {
		return nil, false
	}
	return lc.own.Log, true
}

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
func (lc *chatLifecycle) revisableLocked() *activeTurn {
	pre := lc.own
	if pre == nil || !pre.acked || !pre.Source.Acknowledgeable() || pre.finalizing {
		return nil
	}
	return pre
}

// reviseLocked re-targets routing after the agent turn's turn_open was appended and opened as own: the prompt's
// turn drops back to pending, owed its bracket. Sealed entries stay. Caller holds mu.
func (lc *chatLifecycle) reviseLocked(pre, agent *activeTurn) {
	agent.acked = true
	agent.Opened = pre.Opened
	pre.acked = false
	lc.pending = pre
	slog.Info("a frame revised a provisional turn binding to agent-initiated",
		"chat_id", pre.Chat, "prompt_turn", pre.ID, "agent_turn", agent.ID)
}
