package agent

// Per-turn accounting by owner: a step's credits are the launching chat's, added to the chat's own session total;
// turn count and duration are the conversation's, which a step must not move.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// AccumulateSpend adds a step turn_completion's credits to the launching chat (translate.turnMetering, Chat.AddSpend).
func (bc *bridgeCoordinator) AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	bc.mutateMetering(ctx, chatID, func(c *marotte.Chat) {
		c.AddSpend(credits)
	})
}

// StageConversationTurnSummary records a conversation turn's duration on the header (translate.turnMetering). The
// per-turn sum is the open turn's aggregate; the count is WriteCounters'.
func (bc *bridgeCoordinator) StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64) {
	if elapsedMs <= 0 {
		return
	}
	bc.mutateMetering(ctx, chatID, func(c *marotte.Chat) {
		c.Usage.LastTurnMs = elapsedMs
	})
}

// Every write here lands after its frame, on a chat that may be deleted by then, so
// chat.ErrTombstoned is the designed outcome, not a fault.
func (bc *bridgeCoordinator) mutateMetering(ctx context.Context, chatID marotte.ChatID, apply func(*marotte.Chat)) {
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		apply(c)
		return true
	})
	if err == nil || errors.Is(err, chat.ErrTombstoned) {
		return
	}
	slog.Error("persist turn metering", "chat_id", chatID, "error", err)
}
