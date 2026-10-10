package command

// Supervised mode on v3 is a VALUE: KAS's `autopilot: "off"` gate reviews a whole turn, so this
// command sets that option and persists the choice, IN THAT ORDER, because the record must never
// hold a value the session refused. Writes land before review, so a watcher sees rejected content
// during it.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// cmdSetSupervisedMode records the chat's supervised choice and applies it live to a running
// session, so a mid-session toggle takes effect at once; a bridgeless chat's value reaches
// `session/new` through spawnBridge.
func cmdSetSupervisedMode(ctx context.Context, bridges bridgeAccess, chats chatStore, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetSupervisedModeCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}

	// Assert first, persist only on success (applySessionConfig's rule): a refused toggle persisted
	// would show supervised over a session in autopilot. A cold-spawning bridge grades as nil, so
	// the choice still persists for the session door.
	if err := applySessionConfig(ctx, bridges, cmd.ChatID, "set_supervised_mode",
		marotte.MethodSetConfigOption, configOptionParams(marotte.ConfigOptionAutopilot, autopilotValue(p.Enabled))); err != nil {
		return nil, err
	}

	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.SupervisedMode == p.Enabled {
			return false
		}
		c.SupervisedMode = p.Enabled
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}

	slog.Info("supervised mode set", "chat", cmd.ChatID, "enabled", p.Enabled)
	return responseWith(map[string]any{"enabled": p.Enabled}), nil
}

// autopilotValue maps supervised mode onto KAS's `autopilot` option: supervised ON is autopilot
// "off". A STRING: the option is a select, and a JSON boolean is refused with -32602 in both
// directions. One spelling for this sender and the session door.
func autopilotValue(supervised bool) string {
	if supervised {
		return marotte.ConfigValueAutopilotOff
	}
	return marotte.ConfigValueAutopilotOn
}
