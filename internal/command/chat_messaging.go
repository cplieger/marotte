package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// cmdSetChatMessaging records the chat's messaging choice and hands its session to the applier, answering at once: the
// record is the choice whatever kiro-cli answers, and a turn running now takes it at its close. A chat with no record
// yet gets one, so a choice made before its first message reaches its first door.
func cmdSetChatMessaging(
	ctx context.Context, chats chatStore, applier messagingApplier, cmd *marotte.ClientCommand,
) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetChatMessagingCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	off := !p.Enabled
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			c.Name = marotte.DefaultChatName
		} else if c.MessagingOff == off {
			return false
		}
		c.MessagingOff = off
		return true
	}); err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	applier.ApplyChatMessaging(ctx, cmd.ChatID)
	slog.Info("chat messaging set", "chat", cmd.ChatID, "enabled", p.Enabled)
	return responseWith(map[string]any{"enabled": p.Enabled}), nil
}
