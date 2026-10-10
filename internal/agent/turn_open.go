package agent

// The turn_open append and the registry record are one operation under the lifecycle mutex, so the log and the
// registry never disagree about which turns exist.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// OpenTurn appends the turn_open, creates its registry record and returns the turn id. A dead ctx refuses before the
// append; a held slot answers errTurnSlotHeld; a broken fence answers command.ErrTurnSuperseded. The turn_count
// rewrite runs after both locks are released, so racing opens both write the higher value. The caller holds one
// completion handle until ReleaseTurn.
func (bc *bridgeCoordinator) OpenTurn(ctx context.Context, chatID marotte.ChatID, open command.TurnOpen) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source := open.Source
	spec := &chat.TurnSpec{Prompt: open.Prompt, Source: source.Name()}
	var t *activeTurn
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if !lc.fenceHoldsLocked(open.Fence) {
			return command.ErrTurnSuperseded
		}
		if !lc.slotFreeLocked(source) {
			return errTurnSlotHeld
		}
		opened, err := bc.chatStore.OpenTurn(ctx, chatID, spec, open.Init)
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
		if open.Awaited {
			t.holds++
		}
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

// Nil when refused, and the caller drops the frame. It takes no completion handle: nobody would
// release it.
func (bc *bridgeCoordinator) openWireTurn(ctx context.Context, chatID marotte.ChatID) *activeTurn {
	var t *activeTurn
	created := false
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		if lc.own != nil && !lc.own.finalizing {
			// Lost the open race: fold into the turn already announced.
			t = lc.own
			return nil
		}
		if lc.own != nil {
			return errTurnSlotHeld
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

// announceTurnOpened broadcasts turn_opened for a turn the registry just recorded, re-reading the entry so the
// frame is built from the record that was written.
func (bc *bridgeCoordinator) announceTurnOpened(ctx context.Context, chatID marotte.ChatID, t *activeTurn) {
	if t == nil || t.opened == nil {
		return
	}
	bc.broadcast(ctx, marotte.ServerEvent{
		Type:    marotte.EventTurnOpened,
		ChatID:  chatID,
		Payload: marotte.TurnOpenedPayload{Entry: *t.opened},
	})
}

// StartTurn stamps the model and the credit baseline onto the named turn just before the call that drives it, and
// makes it own when own is nil. False for a dead ctx or an id the registry no longer holds; the caller then runs
// the turn end rule itself.
func (bc *bridgeCoordinator) StartTurn(ctx context.Context, chatID marotte.ChatID, turnID string) bool {
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

// AwaitTurn blocks until the named turn has finalized and reports what it did. It runs on the caller's goroutine:
// deciding inside the finalizer deadlocks against the close it awaits.
func (bc *bridgeCoordinator) AwaitTurn(ctx context.Context, chatID marotte.ChatID, turnID string) (marotte.TurnResult, error) {
	return bc.turns.await(ctx, chatID, turnID)
}

// AwaitTurnBound blocks until KAS binds the named turn's prompt (true) or the turn finalizes unbound (false).
func (bc *bridgeCoordinator) AwaitTurnBound(ctx context.Context, chatID marotte.ChatID, turnID string) (bool, error) {
	return bc.turns.awaitBound(ctx, chatID, turnID)
}

// ReleaseTurn gives up the completion handle OpenTurn issued; the finalized record is dropped with the last handle.
func (bc *bridgeCoordinator) ReleaseTurn(chatID marotte.ChatID, turnID string) {
	bc.turns.release(chatID, turnID)
}

// TurnOpenedAfter reports whether any own turn opened after the named one (the empty-turn gate's structural half).
func (bc *bridgeCoordinator) TurnOpenedAfter(chatID marotte.ChatID, turnID string) bool {
	return bc.turns.openedAfter(chatID, turnID)
}

// RequestStop records the reader's stop on the chat's turn sequence, so a replacing turn (the empty-turn retry)
// can see it.
func (bc *bridgeCoordinator) RequestStop(chatID marotte.ChatID) {
	bc.turns.requestStop(chatID)
}

// StopRequestedAfter reports whether a stop was requested after the named turn opened.
func (bc *bridgeCoordinator) StopRequestedAfter(chatID marotte.ChatID, turnID string) bool {
	return bc.turns.stoppedAfter(chatID, turnID)
}

// turnOpenModel reads the model a turn records at open, empty for a local shell. From the chat record: a resumed
// session's accessors answer the zero value for fields session/load omitted, often the model.
func (bc *bridgeCoordinator) turnOpenModel(ctx context.Context, chatID marotte.ChatID, source marotte.TurnOpenSource) string {
	ch, ok := bc.chatStore.Get(ctx, chatID)
	if !ok || source == marotte.TurnSourceLocalShell {
		return ""
	}
	return ch.Model
}
