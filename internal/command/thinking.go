package command

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// CmdSetThinking records the chat's thinking choice: the effort slider's Off
// stop sends enabled:false. A running session is switched in place first, so a
// refusal is reported rather than persisted; KAS caps a high effort tier in the
// same call. A bridgeless chat persists the choice for its next session.
func CmdSetThinking(
	ctx context.Context,
	bridges BridgeAccess,
	chats ChatStore,
	cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetThinkingCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	choice := marotte.ThinkingOff
	if p.Enabled {
		choice = marotte.ThinkingOn
	}
	if err := setThinking(ctx, bridges, cmd.ChatID, choice); err != nil {
		return nil, err
	}
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			c.Name = marotte.DefaultChatName
		} else if c.Thinking == choice {
			return false
		}
		c.Thinking = choice
		return true
	}); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("thinking set", "chat", cmd.ChatID, "thinking", choice)
	return responseWith(map[string]any{"thinking": choice}), nil
}

// setThinking asserts the thinking option on a running session. The value is a
// STRING; KAS ignores a boolean without changing anything.
func setThinking(ctx context.Context, bridges BridgeAccess, chatID marotte.ChatID, choice string) error {
	return applySessionConfig(ctx, bridges, chatID, "set_thinking",
		marotte.MethodSetConfigOption, configOptionParams(marotte.ConfigOptionThinking, choice))
}
