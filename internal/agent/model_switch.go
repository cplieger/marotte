package agent

// A model switch never touches a turn: it writes pending_model and applies it when the chat is idle.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// resolveSwitchModel returns the effective model after the optional payload override.
func resolveSwitchModel(chat *marotte.Chat, p marotte.SwitchModelCommand) (model string, isSwitch bool) {
	model = chat.Model
	if p.Model == "" || p.Model == modelAuto || p.Model == model {
		return model, false
	}
	return p.Model, true
}

// responseOK2 is the success body for commands routed through h.respond.
var responseOK2 = map[string]bool{"ok": true}

// errModelNotServed is the 409 body for a pick the account cannot run.
var errModelNotServed = errors.New("that model is not available on this account")

// cmdSwitchModel records the pick as pending_model and applies it when the chat is
// idle. A request resolving to the model already set answers ok and writes nothing.
func (rt *Runtime) cmdSwitchModel(ctx context.Context, cmd *marotte.ClientCommand) (any, error) {
	if cmd.ChatID == "" {
		return nil, command.StatusError(http.StatusBadRequest, command.ErrMissingChatID)
	}
	var p marotte.SwitchModelCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
		}
	}
	if !ids.ValidIdent(p.Model) {
		return nil, command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
	}
	chat, ok := rt.chatStore.Get(ctx, cmd.ChatID)
	if !ok {
		return nil, command.StatusError(http.StatusNotFound, command.ErrChatNotFound)
	}
	model, isSwitch := resolveSwitchModel(chat, p)
	if !isSwitch {
		return responseOK2, nil
	}
	if err := rt.refuseUnservedModel(ctx, cmd.ChatID, chat, model); err != nil {
		return nil, err
	}
	if _, err := rt.chatStore.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.PendingModel = model
		return true
	}); err != nil {
		slog.Error("switch_model: write pending_model", "chat_id", cmd.ChatID, "error", err)
		return nil, command.StatusError(http.StatusInternalServerError, err)
	}
	rt.applyPendingModel(ctx, cmd.ChatID)
	return responseOK2, nil
}

// applyPendingModel applies pending_model on an idle chat: through set_config_option on a live
// bridge (model_switched entry between turns), or straight onto model with no bridge. A busy chat
// keeps it for the closer. Idleness is read under the lifecycle mutex; the bridge call holds only
// the chat's switch lock, so a second applier reads the pick the first already cleared.
func (rt *Runtime) applyPendingModel(ctx context.Context, chatID marotte.ChatID) {
	ctx = durable.Context(ctx)
	lc := rt.coord.turns.lifecycleFor(chatID)
	lc.switchMu.Lock()
	defer lc.switchMu.Unlock()
	if rt.coord.turns.live(chatID) {
		return
	}
	chat, ok := rt.chatStore.Get(ctx, chatID)
	if !ok || chat.PendingModel == "" {
		return
	}
	model, from := chat.PendingModel, chat.Model
	if !rt.coord.bridgeLive(chatID) {
		rt.coord.persistModelPick(ctx, chatID, model)
		return
	}
	// Resolved once: PersistModelSwitch clears Chat.Effort, so a second read would forget the tier.
	effort := rt.coord.EffortForSwitch(ctx, model)
	if !rt.coord.ApplyModelSwitch(ctx, chatID, model, effort) {
		rt.clearPendingModel(ctx, chatID)
		rt.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
			Code:    marotte.ErrCodeSwitchFailed,
			Message: rpcerr.Text(errSwitchRefused),
		}))
		return
	}
	rt.coord.PersistModelSwitch(ctx, chatID,
		marotte.EntryModelSwitched{From: from, To: model, Effort: effort}, chat.Usage.ContextSize)
}

// errSwitchRefused is what the reader sees when the session declined the swap.
var errSwitchRefused = errors.New("the session refused the model switch. Try again later")

// clearPendingModel drops a pick the session refused, so the badge stops pulsing.
func (rt *Runtime) clearPendingModel(ctx context.Context, chatID marotte.ChatID) {
	if _, err := rt.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.PendingModel == "" {
			return false
		}
		c.PendingModel = ""
		return true
	}); err != nil {
		slog.Error("switch_model: clear pending_model", "chat_id", chatID, "error", err)
	}
}

// refuseUnservedModel refuses a fresh pick the account cannot run (a spawn withholds an inherited
// one silently); KAS would reject it mid-prompt every turn. The chat record is the evidence:
// config_option_update refreshes it. An empty set is unknowable and allows.
func (rt *Runtime) refuseUnservedModel(
	ctx context.Context, chatID marotte.ChatID, chat *marotte.Chat, model string,
) error {
	if marotte.ModelServed(model, chat.ServedModelIDs) {
		return nil
	}
	slog.Warn("refusing a model switch this account does not serve",
		"chat_id", chatID, "model", model)
	rt.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code:    marotte.ErrCodeModelNotServed,
		Message: "\"" + model + "\" is not available on this account. Pick another model.",
	}))
	return command.StatusError(http.StatusConflict, errModelNotServed)
}
