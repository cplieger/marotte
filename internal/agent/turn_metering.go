package agent

// Per-turn accounting by owner: credits are the chat's whoever spent them (a step's spend is the launching chat's);
// turn count and duration are the conversation's, which a step must not move.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// AccumulateSpend adds a turn_completion's credits to the chat, step or not (translate.TurnMetering). Zero is
// skipped: HasRealData must not report a measured 0.00 the account never confirmed.
func (bc *BridgeCoordinator) AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	bc.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		u.Credits += credits
		u.HasRealData = true
	})
}

// StageConversationTurnSummary records a conversation turn's duration on the header (translate.TurnMetering). The
// per-turn sum is the open turn's aggregate; the count is WriteCounters'.
func (bc *BridgeCoordinator) StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64) {
	if elapsedMs <= 0 {
		return
	}
	bc.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		u.LastTurnMs = elapsedMs
	})
}

// mutateUsage applies a usage write to the chat. Every write here lands after its frame, on a chat that may be
// deleted by then, so chat.ErrTombstoned is the designed outcome, not a fault.
func (bc *BridgeCoordinator) mutateUsage(ctx context.Context, chatID marotte.ChatID, apply func(*marotte.Usage)) {
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		apply(&c.Usage)
		return true
	})
	if err == nil || errors.Is(err, chat.ErrTombstoned) {
		return
	}
	slog.Error("persist turn metering", "chat_id", chatID, "error", err)
}
