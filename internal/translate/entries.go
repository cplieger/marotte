package translate

// The entry log's live path: every frame is an append to the log the GET reads, a delta into
// an open entry, or a live-only replace.

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// entryScope names the log a frame's entries belong to: a chat's, addressed by
// the envelope's chat id, or a RUN's, addressed by an empty chat id plus the
// workflow id on every payload so the client routes to the run store with no
// lookup.
type entryScope struct {
	chatID marotte.ChatID
	runID  string
}

func chatScope(chatID marotte.ChatID) entryScope { return entryScope{chatID: chatID} }

func runScope(runID string) entryScope { return entryScope{runID: runID} }

// scopeOf is the log a content frame's entries go to: the run's for a step frame,
// else the chat's.
func scopeOf(chatID marotte.ChatID, attr FrameAttribution) entryScope {
	if attr.Step {
		return runScope(attr.RunID)
	}
	return chatScope(chatID)
}

func (sc entryScope) event(kind marotte.EventType, payload any) marotte.ServerEvent {
	return marotte.NewEvent(kind, sc.chatID, payload)
}

func (t *Translator) publishSealed(ctx context.Context, sc entryScope, sealed []turnlog.Sealed) {
	PublishSealed(ctx, t.bus, sc.chatID, sc.runID, sealed)
}

// publishSealedRefused is publishSealed carrying the turn's refusal note on the entry_sealed
// frame markRefusal's seal publishes. Only a lane that HAD an open entry carries it; the rest
// defer to turn_close.refusal.
func (t *Translator) publishSealedRefused(ctx context.Context, sc entryScope, sealed []turnlog.Sealed, refusal *marotte.RefusalInfo) {
	broadcastSealed(ctx, t.bus, sc.chatID, sc.runID, sealed, refusal)
}

func (t *Translator) publishAppended(ctx context.Context, sc entryScope, e *marotte.Entry) {
	PublishAppended(ctx, t.bus, sc.chatID, sc.runID, e)
}

// PublishSealed announces what one accumulator step froze, in order: a released
// carry as entry_delta ahead of its entry_sealed, a streamed entry as
// entry_sealed, and a born-sealed one as entry_appended. chatID addresses a
// chat's log; an empty chatID with runID set addresses a run's. Exported because
// the closers in the agent package seal through the same accumulator.
func PublishSealed(ctx context.Context, bus Broadcaster, chatID marotte.ChatID, runID string, sealed []turnlog.Sealed) {
	broadcastSealed(ctx, bus, chatID, runID, sealed, nil)
}

// broadcastSealed is PublishSealed's body, with the refusal note the refusal branch
// stamps on the seal frame. Every other caller passes nil: the note is a turn-level
// fact and only the one branch that latches it has one to send.
func broadcastSealed(
	ctx context.Context,
	bus Broadcaster,
	chatID marotte.ChatID,
	runID string,
	sealed []turnlog.Sealed,
	refusal *marotte.RefusalInfo,
) {
	sc := entryScope{chatID: chatID, runID: runID}
	for _, s := range sealed {
		e := s.Entry
		if s.N == 0 {
			PublishAppended(ctx, bus, chatID, runID, e)
			continue
		}
		if s.Delta != "" {
			bus.Broadcast(ctx, sc.event(marotte.EventEntryDelta, marotte.EntryDeltaPayload{
				Turn: e.Turn, EntryID: e.ID, Lane: e.Lane, Delta: s.Delta, N: s.N, WorkflowID: sc.runID,
			}))
		}
		bus.Broadcast(ctx, sc.event(marotte.EventEntrySealed, marotte.EntrySealedPayload{
			Refusal: refusal,
			Turn:    e.Turn, EntryID: e.ID, Lane: e.Lane, Seq: e.Seq, Ts: e.Ts, N: s.N, WorkflowID: sc.runID,
		}))
	}
}

// PublishAppended announces one born-sealed entry, or a turn_open and turn_close
// under their own event names.
func PublishAppended(ctx context.Context, bus Broadcaster, chatID marotte.ChatID, runID string, e *marotte.Entry) {
	sc := entryScope{chatID: chatID, runID: runID}
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		bus.Broadcast(ctx, sc.event(marotte.EventTurnOpened, marotte.TurnOpenedPayload{Entry: *e, WorkflowID: sc.runID}))
	case marotte.EntryKindTurnClose:
		bus.Broadcast(ctx, sc.event(marotte.EventTurnClosed, marotte.TurnClosedPayload{Entry: *e, WorkflowID: sc.runID}))
	default:
		bus.Broadcast(ctx, sc.event(marotte.EventEntryAppended, marotte.EntryAppendedPayload{Entry: *e, WorkflowID: sc.runID}))
	}
}

// publishOpen announces a delta that reached lane key's open entry: entry_opened when it
// opened it (N is 1), else entry_delta. No refusal: markRefusal seals instead.
func (t *Translator) publishOpen(ctx context.Context, sc entryScope, turn *turnlog.Turn, key, delta string) {
	open, ok := turn.Open(key)
	if !ok {
		return
	}
	if open.N == 1 {
		t.bus.Broadcast(ctx, sc.event(marotte.EventEntryOpened, marotte.EntryOpenedPayload{
			Open: open, WorkflowID: sc.runID,
		}))
		return
	}
	t.bus.Broadcast(ctx, sc.event(marotte.EventEntryDelta, marotte.EntryDeltaPayload{
		Turn: open.Turn, EntryID: open.ID, Lane: open.Lane, Delta: delta, N: open.N, WorkflowID: sc.runID,
	}))
}

// appendFailed is the one place a refused append is reported. A write the store
// refused latches for the process's life (chat.EntryLog's rule), so every frame
// after the first says so at Warn and nothing reaches the wire for it.
func appendFailed(sc entryScope, what string, err error) {
	slog.Warn("entry log: append refused; the frame is dropped",
		"chat_id", sc.chatID, "workflow_id", sc.runID, "entry", what, "error", err)
}

// BetweenTurnsAppender is the store's between-turns append, as AppendBetweenTurns
// reaches it.
type BetweenTurnsAppender interface {
	AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) ([]*marotte.Entry, error)
}

// AppendBetweenTurns files a chat's lane-less entry after its newest turn's close
// and announces it, preceded by the minted carrier's turn_open and turn_close when
// the log was empty: the one owner of that sequence for the translator and the
// agent's closers alike. id is "" for a kind whose id the store derives from its
// position.
func AppendBetweenTurns(ctx context.Context, bus Broadcaster, store BetweenTurnsAppender, chatID marotte.ChatID, kind marotte.EntryKind, id string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := marotte.Entry{Kind: kind, ID: id, Payload: raw}
	minted, err := store.AppendBetweenTurns(ctx, chatID, &e)
	if err != nil {
		return err
	}
	for _, m := range minted {
		PublishAppended(ctx, bus, chatID, "", m)
	}
	PublishAppended(ctx, bus, chatID, "", &e)
	return nil
}

// appendBetweenTurns is AppendBetweenTurns over the translator's own roles, with a
// refusal reported the way every other dropped frame is. False when refused.
func (t *Translator) appendBetweenTurns(ctx context.Context, chatID marotte.ChatID, kind marotte.EntryKind, id string, payload any) bool {
	if err := AppendBetweenTurns(ctx, t.bus, t.turns, chatID, kind, id, payload); err != nil {
		appendFailed(chatScope(chatID), string(kind), err)
		return false
	}
	return true
}

// appendLaneless files a chat's lane-less entry: through the open turn, where
// inTurn is that kind's own turnlog operation and every lane seals first, else
// after the newest turn's close by the between-turns rule. id is the entry id the
// between-turns arm files under, empty for a kind whose id the store derives from
// its position; the turn arm mints its own.
func (t *Translator) appendLaneless(
	ctx context.Context, chatID marotte.ChatID, kind marotte.EntryKind, id string, payload any,
	inTurn func(ctx context.Context, turn *turnlog.Turn) ([]turnlog.Sealed, error),
) bool {
	if turn, ok := t.turns.OwnTurn(chatID); ok {
		sealed, err := inTurn(ctx, turn)
		t.publishSealed(ctx, chatScope(chatID), sealed)
		if err != nil {
			appendFailed(chatScope(chatID), string(kind), err)
			return false
		}
		return true
	}
	return t.appendBetweenTurns(ctx, chatID, kind, id, payload)
}
