package translate

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// KAS self-reorients, so no context-recovery prompt is injected. Inside a turn the entry seals
// every lane first and sits at the position the compaction happened; between turns it joins the
// newest turn after its close.
func (t *Translator) handleCompactionCompleted(ctx context.Context, chatID marotte.ChatID, summaryPtr *string) {
	t.summaryFails.end(chatID)
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
		// KAS's next published figure is the pre-summary one (handleCompact leaves lastContextUsagePct, and a
		// session load seeds it from the last persisted reading) or, after a committed manual compaction, an
		// estimate; never a model reading until its next live one.
		c.Usage.ContextPct, c.Usage.HasRealData, c.Usage.ContextCompacted = 0, false, true
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

// summaryStreaks are the chats whose summary failed, shown, with none landed since. kiro-cli tries its summary again
// at each response at or above the threshold until one lands (its rearm guard arms only on a landed summary), across
// turns, so the reader is told of a streak once.
type summaryStreaks struct {
	failing map[marotte.ChatID]struct{}
	mu      sync.Mutex
}

// begin reports whether chatID's failure starts a streak, which the caller then shows.
func (s *summaryStreaks) begin(chatID marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.failing[chatID]; ok {
		return false
	}
	if s.failing == nil {
		s.failing = make(map[marotte.ChatID]struct{})
	}
	s.failing[chatID] = struct{}{}
	return true
}

func (s *summaryStreaks) end(chatID marotte.ChatID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failing, chatID)
}

func (t *Translator) handleCompactionFailed(ctx context.Context, chatID marotte.ChatID, errMsg string) {
	detail := cmp.Or(errMsg, "compaction failed")
	detail = textsafe.SanitizeSingleLineBounded(detail, maxCompactionDetailBytes)
	if !t.summaryFails.begin(chatID) {
		slog.Warn("kiro-cli reported the summary failed again before one landed", "chat_id", chatID, "reason", detail)
		if turn, ok := t.turns.OwnTurn(chatID); ok {
			t.turnInterrupt.CompactionFailed(chatID, turn.ID(), detail)
		}
		return
	}
	slog.Warn("kiro-cli reported the summary failed", "chat_id", chatID, "reason", detail)
	turnID := ""
	// A refused append means the chat is gone: no banner for a chat nobody holds.
	if !t.appendLaneless(durable.Context(ctx), chatID, marotte.EntryKindCompactionFailed,
		"", marotte.EntryCompactionFailed{Reason: detail},
		func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error) {
			turnID = turn.ID()
			return turn.CompactionFailed(ctx, detail)
		}) {
		t.summaryFails.end(chatID)
		return
	}
	// TurnScoped though no turn ends here: the entry just appended shows detail in its turn's card, which is all
	// the client's on-screen suppression needs.
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeCompactionFailed, Message: detail, TurnScoped: true,
	}))
	t.compaction.ObserveCompactionFailed(chatID)
	// A compaction failure does not prove the turn ended; CompactionFailed decides whether the host bounds it.
	// Recorded between turns, it names no turn to bound.
	if turnID != "" {
		t.turnInterrupt.CompactionFailed(chatID, turnID, detail)
	}
}
