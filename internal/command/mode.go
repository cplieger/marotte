package command

// On v3 every role, bundled workflow modes and workspace custom agents alike, is an entry in the
// session's availableModes, switched in place via session/set_mode.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// cmdSetMode switches the chat's session mode: live via session/set_mode when a bridge runs, then
// persisted and broadcast so every client's pill flips. For a chat with no bridge yet the mode is
// persisted and applied at session/new (SessionOpts.Mode).
func cmdSetMode(
	ctx context.Context, bridges bridgeAccess, chats chatStore, bus broadcaster,
	recorder modeRecorder, modeLocks *keylock.Set[marotte.ChatID], cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil || p.ModeID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if err := refuseBotMode(ctx, chats, cmd.ChatID, p.ModeID); err != nil {
		return nil, err
	}
	unlock, lockErr := lockSessionChange(ctx, modeLocks, cmd.ChatID)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()

	if err := applySessionConfig(ctx, bridges, cmd.ChatID, "set_mode",
		marotte.MethodSetMode, map[string]any{"modeId": p.ModeID}); err != nil {
		return nil, err
	}

	// Whether anything changed, and nothing more: a refused write is reported by
	// Mutate's error, not by this flag. from is the mode being left, empty on a chat
	// whose mode was never recorded.
	var (
		changed bool
		from    string
	)
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			// A fresh chat is auto-created so the pick survives to session/new; Mutate refuses
			// tombstoned ids.
			c.Name = marotte.DefaultChatName
			c.AdoptMode(p.ModeID)
			changed = true
			return true
		}
		from = c.CurrentModeID
		changed = c.AdoptMode(p.ModeID)
		return changed
	}); err != nil {
		// A tombstoned id (deleted in the last ten minutes) is the whole 404 condition; a repeat
		// pick is a no-op success.
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if changed {
		bus.Broadcast(ctx, marotte.NewEvent(marotte.EventModeChanged, cmd.ChatID, marotte.ModeChangedPayload(p)))
		recorder.PersistModeSwitch(ctx, cmd.ChatID, marotte.EntryModeSwitched{
			From: from, To: p.ModeID, Source: marotte.ModeSwitchSourceUser,
		})
	}
	slog.Info("mode set", "chat", cmd.ChatID, "mode", p.ModeID)
	return responseWith(map[string]any{"mode_id": p.ModeID}), nil
}

// errBotMode is set_mode's refusal on a Bot, and of the Bot role on any other chat.
var errBotMode = errors.New("a Bot runs only on its own role")

const reasonBotMode = "bot_mode"

// refuseBotMode answers 409 bot_mode when the switch would move a Bot off its role or put a regular chat on it,
// before any session is asked. A chat's bot block is set at creation and never removed, so the read needs no lock.
func refuseBotMode(ctx context.Context, chats chatStore, chatID marotte.ChatID, modeID string) error {
	if modeID == marotte.BotRoleID {
		return StatusErrorReason(http.StatusConflict, reasonBotMode, errBotMode)
	}
	c, found, err := chats.Lookup(ctx, chatID)
	if err != nil {
		return StatusError(http.StatusInternalServerError, err)
	}
	if found && c.Bot != nil {
		return StatusErrorReason(http.StatusConflict, reasonBotMode, errBotMode)
	}
	return nil
}

// lockSessionChange holds a chat's lock for one change verb across its engine call and its persist. A change
// detached past its answer budget lets the client send the next one, and kiro-cli applies the two in write order;
// without the lock their persists can land in the other order and leave the record on a value the session left.
func lockSessionChange(ctx context.Context, locks *keylock.Set[marotte.ChatID], chatID marotte.ChatID) (func(), error) {
	unlock, err := locks.Lock(ctx, chatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	return unlock, nil
}

func configOptionParams(id, value string) map[string]any {
	return map[string]any{"configId": id, "value": value}
}

// applySessionConfig sends one live session-config change and grades the outcome, so set_mode,
// set_effort and set_supervised_mode answer a cold session open alike. A nil error means the caller may
// persist: the change landed, or there is no session yet to land it on.
// A bridge that exists but has not started (registered before Start so opens coalesce, or whose
// Start failed) refuses with marotte.ErrBridgeNotStarted, and is treated as no session. That is
// safe because persistNewSessionMetadata resets the record to the mode the session took
// (reportModeNotApplied tells the user) and repairEffort re-asserts the level; a refusal by the
// SESSION stays a 502 and is never persisted.
func applySessionConfig(
	ctx context.Context,
	bridges bridgeAccess,
	chatID marotte.ChatID,
	verb, method string,
	params map[string]any,
) error {
	bridge := bridges.Bridge(chatID)
	if bridge == nil {
		return nil
	}
	cctx, cancel := bridges.CallBudget(ctx, engineChangeBudget)
	defer cancel()
	_, err := bridge.Call(cctx, method, sessionParams(bridge, params))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, marotte.ErrBridgeNotStarted):
		slog.Debug(verb+": no session yet; persisting for the session door",
			"chat", chatID, keyError, err)
		return nil
	default:
		slog.Warn(verb+": bridge call failed", "chat", chatID, keyError, err)
		return StatusError(http.StatusBadGateway, err)
	}
}
