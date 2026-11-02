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

// resolveSwitchModel is the model a request names: the chat's own for none. `auto` is a catalog
// model like any other.
func resolveSwitchModel(chat *marotte.Chat, p marotte.SwitchModelCommand) string {
	if p.Model == "" {
		return chat.Model
	}
	return p.Model
}

var responseOK2 = map[string]bool{"ok": true}

// errModelNotServed is the 409 body for a pick the account cannot run.
var errModelNotServed = errors.New("that model is not available on this account")

// cmdSwitchModel admits the request (admitSwitchModel), then applies what it parked when the chat
// is idle.
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
	if !ids.ValidIdent(p.Model) || (p.Effort != "" && p.Effort != marotte.SwitchEffortOff && !p.Effort.Valid()) {
		return nil, command.StatusError(http.StatusBadRequest, command.ErrInvalidPayload)
	}
	if err := rt.admitSwitchModel(ctx, cmd.ChatID, p); err != nil {
		return nil, err
	}
	rt.applyPendingModel(ctx, cmd.ChatID)
	return responseOK2, nil
}

// admitSwitchModel resolves the request once and applies that admission whole: the stop in place,
// then the park. It holds the chat's switch lock, which every writer of the parked pair takes, so
// no landing falls between its read, its stop and its park.
func (rt *Runtime) admitSwitchModel(ctx context.Context, chatID marotte.ChatID, p marotte.SwitchModelCommand) error {
	lc := rt.coord.turns.lifecycleFor(chatID)
	lc.switchMu.Lock()
	defer lc.switchMu.Unlock()
	chat, ok := rt.chatStore.Get(ctx, chatID)
	if !ok {
		return command.StatusError(http.StatusNotFound, command.ErrChatNotFound)
	}
	in := rt.coord.effortInputFor(ctx, chat, resolveSwitchModel(chat, p), p.Effort)
	in.admit = true
	d := resolveEffort(&in)
	if d.err != nil {
		return command.StatusError(http.StatusBadRequest, d.err)
	}
	if d.park.model != "" {
		if err := rt.refuseUnservedModel(ctx, chatID, chat, d.park.model); err != nil {
			return err
		}
	}
	if err := rt.applyStopInPlace(ctx, chatID, d.inPlace); err != nil {
		return err
	}
	if d.noop {
		return nil
	}
	if d.inPlace != "" {
		// The stop has landed, so a client walking away cannot leave it without its park.
		ctx = durable.Context(ctx)
	}
	return rt.parkSwitch(ctx, chatID, d.park)
}

// parkSwitch writes an admitted park onto the record; an empty pair clears the one parked.
func (rt *Runtime) parkSwitch(ctx context.Context, chatID marotte.ChatID, park pendingPair) error {
	if _, err := rt.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.PendingModel, c.PendingEffort = park.model, park.effort
		return true
	}); err != nil {
		slog.Error("switch_model: write pending_model", "chat_id", chatID, "error", err)
		return command.StatusError(http.StatusInternalServerError, err)
	}
	return nil
}

// applyStopInPlace runs a switch's stop through set_effort's or set_thinking's own handler, so the
// one request does what that command does in place (its row, seed, thinking and rollback).
func (rt *Runtime) applyStopInPlace(ctx context.Context, chatID marotte.ChatID, stop string) error {
	cmd := marotte.ClientCommand{ChatID: chatID}
	switch stop {
	case "":
		return nil
	case pickOff:
		cmd.Type, cmd.Payload = marotte.CmdSetThinking, json.RawMessage(`{"enabled":false}`)
	default:
		payload, err := json.Marshal(marotte.SetEffortCommand{Level: marotte.EffortLevel(stop)})
		if err != nil {
			return command.StatusError(http.StatusInternalServerError, err)
		}
		cmd.Type, cmd.Payload = marotte.CmdSetEffort, payload
	}
	return rt.dispatcher.Run(ctx, &cmd)
}

// applyPendingModel applies pending_model, with pending_effort, on an idle chat: through
// set_config_option on a live bridge (one model_switched entry between turns), or straight onto
// the record with no bridge. A busy chat, or one KAS reports running, keeps both for the closer.
// Idleness is read under the lifecycle mutex; the bridge call holds only the chat's switch lock, so
// a second applier reads the pick the first already cleared.
func (rt *Runtime) applyPendingModel(ctx context.Context, chatID marotte.ChatID) {
	ctx = durable.Context(ctx)
	lc := rt.coord.turns.lifecycleFor(chatID)
	lc.switchMu.Lock()
	defer lc.switchMu.Unlock()
	if rt.coord.turns.live(chatID) || !rt.coord.turns.kasAllowsSessionCall(chatID) {
		return
	}
	chat, ok := rt.chatStore.Get(ctx, chatID)
	if !ok || chat.PendingModel == "" {
		return
	}
	if !rt.coord.bridgeLive(chatID) {
		rt.coord.persistModelPick(ctx, chatID, chat)
		return
	}
	// Resolved once: the swap asserts mv.sent, and the record keeps mv.pref.
	in := rt.coord.effortInputFor(ctx, chat, chat.PendingModel, "")
	mv := resolveEffort(&in)
	if !rt.coord.applyModelSwitch(ctx, chatID, &mv) {
		rt.clearPendingModel(ctx, chatID)
		rt.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
			Code:    marotte.ErrCodeSwitchFailed,
			Message: rpcerr.Text(errSwitchRefused),
		}))
		return
	}
	rt.coord.persistModelSwitch(ctx, chatID, &mv, chat.Usage.ContextSize)
}

var errSwitchRefused = errors.New("the session refused the model switch. Try again later")

// clearPendingModel drops a pick the session refused, with its effort, so the badge stops pulsing.
func (rt *Runtime) clearPendingModel(ctx context.Context, chatID marotte.ChatID) {
	if _, err := rt.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.PendingModel == "" {
			return false
		}
		c.PendingModel, c.PendingEffort = "", ""
		return true
	}); err != nil {
		slog.Error("switch_model: clear pending_model", "chat_id", chatID, "error", err)
	}
}

// refuseUnservedModel refuses a fresh pick the account cannot run (a session door withholds an inherited
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
