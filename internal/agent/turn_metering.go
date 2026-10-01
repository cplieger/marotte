package agent

// Per-turn accounting, split by owner: credits are the CHAT's whoever spent them
// (a workflow step's spend is the launching chat's), while the conversation turn
// count and duration are the CONVERSATION's, so a step must not move them.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// AccumulateSpend adds a turn_completion's credit spend to the chat, step frame or
// not. Satisfies translate.TurnMetering. A zero-credit summary is skipped, since
// HasRealData is what switches the context popup from "unknown" to a figure and
// must not report a measured 0.00 the account never confirmed.
func (bc *BridgeCoordinator) AccumulateSpend(ctx context.Context, chatID marotte.ChatID, credits float64) {
	if credits <= 0 {
		return
	}
	bc.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		u.Credits += credits
		u.HasRealData = true
	})
}

// StageConversationTurnSummary records a CONVERSATION turn's reported duration on
// the header. Satisfies translate.TurnMetering. The per-turn sum lives on the
// open turn's aggregate, which the translator meters itself; the turn count is
// the log's, written by WriteCounters at the close.
func (bc *BridgeCoordinator) StageConversationTurnSummary(ctx context.Context, chatID marotte.ChatID, elapsedMs float64) {
	if elapsedMs <= 0 {
		return
	}
	bc.mutateUsage(ctx, chatID, func(u *marotte.Usage) {
		u.LastTurnMs = elapsedMs
	})
}

// mutateUsage applies a usage write to the chat. Every write here is LATE — it
// lands after the frame that caused it, on a chat the user may already have
// deleted — so chat.ErrTombstoned is the designed outcome rather than a fault.
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
