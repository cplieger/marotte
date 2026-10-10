package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// The frame carries no key but is emitted between the append and the model call, so the one
// prompt-class turn IS that prompt's; session_id scopes the replay.
func (t *Translator) handleUserMessageID(ctx context.Context, chatID marotte.ChatID, kasID string) {
	turn, ok := t.turns.PromptTurn(chatID)
	if !ok {
		slog.Debug("user message id: no prompt turn to bind", "chat_id", chatID)
		return
	}
	sessionID := ""
	if c, ok := t.chats.Get(ctx, chatID); ok {
		sessionID = c.ACPSessionID
	}
	sealed, err := turn.TurnBind(ctx, marotte.EntryTurnBind{KASMessageID: kasID, SessionID: sessionID})
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), "turn_bind", err)
	}
}
