package translate

import (
	"cmp"
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/runesafe/v2"
)

// KAS self-reorients, so no context-recovery prompt is injected. Inside a turn the entry seals
// every lane first and sits at the position the compaction happened; between turns it joins the
// newest turn after its close.
func (t *Translator) handleCompactionCompleted(ctx context.Context, chatID marotte.ChatID, summaryPtr *string) {
	summary := ""
	if summaryPtr != nil {
		summary = *summaryPtr
	}
	// One detached context: a watermark naming an entry the shutdown refused would be worse.
	ctx = durable.Context(ctx)
	empties, err := t.chats.EmptyCompactions(ctx, chatID)
	if errors.Is(err, chat.ErrTombstoned) || errors.Is(err, chat.ErrChatNotFound) {
		return
	}
	if err != nil {
		slog.Error("compaction: count", "chat_id", chatID, "error", err)
		return
	}
	id := marotte.CompactionEntryID([]byte(summary), empties+1)
	// A refused append means the chat is gone, so the watermark could only be refused too.
	if !t.appendLaneless(ctx, chatID, marotte.EntryKindCompaction, id, marotte.EntryCompaction{Summary: summary},
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.Compaction(ctx, summary, empties+1)
		}) {
		return
	}
	_, err = t.chats.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.CompactionWatermark = id
		return true
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("compaction: set watermark", "chat_id", chatID, "error", err)
	}
}

const maxCompactionDetailBytes = 200

func (t *Translator) handleCompactionFailed(ctx context.Context, chatID marotte.ChatID, errMsg string) {
	detail := cmp.Or(errMsg, "compaction failed")
	detail = runesafe.SanitizeSingleLineBounded(detail, maxCompactionDetailBytes)
	// A refused append means the chat is gone: no banner for a chat nobody holds.
	if !t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindCompactionFailed,
		"", marotte.EntryCompactionFailed{Reason: detail},
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			return turn.CompactionFailed(ctx, detail)
		}) {
		return
	}
	// Turn-scoped: the turn holding this entry grades as failed.
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeCompactionFailed, Message: detail, TurnScoped: true,
	}))
	// A compaction failure does not prove the turn ended: the host bounds the silence, then interrupts.
	t.turnInterrupt.CompactionFailed(chatID, detail)
}
