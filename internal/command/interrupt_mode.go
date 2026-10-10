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

var errUnknownInterruptMode = errors.New("mode must be steer or queue")

// cmdSetInterruptMode records what Send does while this chat's turn runs. It
// touches no turn and calls no bridge: a switch mid-turn moves nothing already
// sent or queued. Auto-creates the record like set_mode, so a pick on a chat that
// has not prompted yet survives to its first turn.
func cmdSetInterruptMode(ctx context.Context, chats chatMutator, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetInterruptModeCommand
	if json.Unmarshal(cmd.Payload, &p) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !p.Mode.Valid() {
		return nil, StatusError(http.StatusBadRequest, errUnknownInterruptMode)
	}
	// Steer is stored as the empty value, so a chat that never picked and one that
	// picked steer have one spelling.
	stored := p.Mode
	if stored == marotte.InterruptSteer {
		stored = ""
	}
	if _, err := chats.Mutate(ctx, cmd.ChatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			c.Name = marotte.DefaultChatName
		} else if c.InterruptMode == stored {
			return false
		}
		c.InterruptMode = stored
		return true
	}); err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusNotFound, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("interrupt mode set", "chat", cmd.ChatID, "mode", p.Mode)
	return responseWith(map[string]any{"mode": p.Mode}), nil
}
