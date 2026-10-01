package translate

// The `user_message_id_assigned` sub-kind: the agent naming the record id it has just
// persisted a prompt under. What that id is for: the turn_bind entry.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// handleUserMessageID appends turn_bind {kas_message_id, session_id} to the chat's
// prompt-class turn. The frame carries {kind, userMessageId} and no key, and it is
// emitted between the append and the model call, so the chat's one prompt-class
// turn awaiting or holding its bracket IS that prompt's; session_id is the
// header's session at this moment, the one the prompt went out on, which scopes
// the replay merge.
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
