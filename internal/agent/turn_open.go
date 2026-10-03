package agent

// A turn begins in exactly one place: the turn_open append and the registry record
// are one operation under the chat's lifecycle mutex, so the log and the registry
// never disagree about which turns exist.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// OpenTurn appends the turn_open for a turn of source and creates its registry
// record, answering the turn id every later handle names. It refuses on a dead ctx
// BEFORE it appends and waits out a finalizing own turn first; a source whose slot
// is held (local_shell over an open turn, a prompt while one is still owed its
// bracket) is refused with ErrTurnSlotHeld. The header's turn_count rewrite runs
// after both locks are released, so two opens racing to it both write the higher
// value. The caller holds one completion handle on the id until ReleaseTurn.
func (bc *BridgeCoordinator) OpenTurn(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource, prompt *marotte.EntryPrompt, init func(*marotte.Chat)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	spec := &chat.TurnSpec{Prompt: prompt, Source: source.Name()}
	var t *Turn
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if !lc.slotFreeLocked(source) {
			return ErrTurnSlotHeld
		}
		opened, err := bc.chatStore.OpenTurn(ctx, chatID, spec, init)
		if err != nil {
			return err
		}
		model := bc.turnOpenModel(ctx, chatID, source)
		log := turnlog.Open(opened.ID, bc.chatStore.Sink(chatID))
		if model != "" {
			log.SetModel(model)
		}
		t = lc.openLocked(chatID, opened, source, model, log)
		t.holds++
		return nil
	})
	if err != nil {
		return "", err
	}
	bc.announceTurnOpened(ctx, chatID, t)
	if err := bc.chatStore.WriteCounters(ctx, chatID); err != nil {
		slog.Warn("turn opened but the header's counters did not follow", "chat_id", chatID, "turn", t.ID, "error", err)
	}
	return t.ID, nil
}

// openWireTurn opens turn_open{source: wire_turn_start} as own for a frame of the
// chat's own session that found no open turn. Nil when the open was refused, and
// the caller drops the frame. Takes no completion handle: nobody would release it.
func (bc *BridgeCoordinator) openWireTurn(ctx context.Context, chatID marotte.ChatID) *Turn {
	var t *Turn
	created := false
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if lc.own != nil && !lc.own.finalizing {
			// A frame that lost the open race to another: the turn is already
			// announced, so the caller folds into it and nothing below runs.
			t = lc.own
			return nil
		}
		if lc.own != nil {
			return ErrTurnSlotHeld
		}
		opened, err := bc.chatStore.OpenTurn(ctx, chatID, &chat.TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil)
		if err != nil {
			return err
		}
		model := bc.turnOpenModel(ctx, chatID, marotte.TurnSourceWireTurnStart)
		log := turnlog.Open(opened.ID, bc.chatStore.Sink(chatID))
		if model != "" {
			log.SetModel(model)
		}
		t = lc.openLocked(chatID, opened, marotte.TurnSourceWireTurnStart, model, log)
		t.acked = true
		created = true
		return nil
	})
	if err != nil {
		slog.Warn("a frame with no open turn could not open one", "chat_id", chatID, "error", err)
		return nil
	}
	if !created {
		return t
	}
	if bc.steerTurnBound != nil {
		bc.steerTurnBound(chatID, t.ID)
	}
	bc.announceTurnOpened(ctx, chatID, t)
	if err := bc.chatStore.WriteCounters(ctx, chatID); err != nil {
		slog.Warn("turn opened but the header's counters did not follow", "chat_id", chatID, "turn", t.ID, "error", err)
	}
	return t
}

// announceTurnOpened broadcasts turn_opened for a turn the registry just recorded.
// The entry is re-read from the accumulator's id rather than carried, so the one
// frame every client keys the card on is built from the record that was written.
func (bc *BridgeCoordinator) announceTurnOpened(ctx context.Context, chatID marotte.ChatID, t *Turn) {
	if t == nil || t.opened == nil {
		return
	}
	bc.broadcast(ctx, marotte.ServerEvent{
		Type:    marotte.EventTurnOpened,
		ChatID:  chatID,
		Payload: marotte.TurnOpenedPayload{Entry: *t.opened},
	})
}

// StartTurn stamps the model and the credit baseline onto the turn the id names,
// with the bridge live, immediately before the call that drives it, and makes it
// own when own is still nil. False for a dead ctx and for an id the registry no
// longer holds; the caller then runs the turn end rule on that turn itself.
func (bc *BridgeCoordinator) StartTurn(ctx context.Context, chatID marotte.ChatID, turnID string) bool {
	if ctx.Err() != nil {
		return false
	}
	lc := bc.turns.lifecycleFor(chatID)
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.liveLocked(turnID)
	if t == nil || t.finalizing {
		return false
	}
	model := bc.turnOpenModel(ctx, chatID, t.Source)
	t.Model = model
	if model != "" {
		t.Log.SetModel(model)
	}
	if lc.own == nil {
		lc.own = t
		lc.setStateLocked(turnOpen)
	}
	if bc.steerTurnStarted != nil && t.Source.PromptClass() {
		bc.steerTurnStarted(chatID, t.ID)
	}
	return true
}

// AwaitTurn blocks until the named turn has finalized and reports what it did, so a
// caller reads the turn's account rather than state the finalize has consumed. It
// runs on the CALLER's goroutine: deciding inside the finalizer deadlocks against
// the close it awaits.
func (bc *BridgeCoordinator) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	return bc.turns.await(ctx, chatID, turnID)
}

// ReleaseTurn gives up the completion handle OpenTurn issued. The finalized record
// is dropped when its last handle goes, which is what bounds retention.
func (bc *BridgeCoordinator) ReleaseTurn(chatID marotte.ChatID, turnID string) {
	bc.turns.release(chatID, turnID)
}

// TurnOpenedAfter reports whether any own turn on the chat opened after the named
// one: the structural half of the empty-turn gate.
func (bc *BridgeCoordinator) TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	return bc.turns.openedAfter(chatID, turnID)
}

// turnOpenModel reads the model a turn records at open, empty for a local shell.
// From the chat record rather than the bridge: a resumed session's own accessors
// answer the zero value for whatever session/load omitted, which routinely
// includes the model.
func (bc *BridgeCoordinator) turnOpenModel(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource) string {
	ch, ok := bc.chatStore.Get(ctx, chatID)
	if !ok || source == marotte.TurnSourceLocalShell {
		return ""
	}
	return ch.Model
}
