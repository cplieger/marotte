package command

// Staged attachments mirror draft.go: no bridge call, no move of the retention clock, a no-op on a
// chat that is not a server record yet.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// cmdSetAttachments records the paths staged beside the chat's draft. An
// empty Paths is a legitimate value (how a sent or emptied pill row clears);
// the whole list arrives every time, so nothing needs reconciling.
func cmdSetAttachments(ctx context.Context, chats chatStore, bus broadcaster, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SetAttachmentsCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if len(p.Paths) > marotte.MaxAttachments {
		return nil, StatusError(http.StatusRequestEntityTooLarge, errTooManyAttachments)
	}
	for _, path := range p.Paths {
		// A 400 rather than a silent drop: dropping one member of a list the
		// client believes it replaced wholesale would leave the two sides
		// disagreeing with nothing saying so. No UTF-8 check: encoding/json
		// already replaced any invalid byte sequence in the decoded string.
		if path == "" || len(path) > marotte.MaxAttachmentPathBytes {
			return nil, StatusError(http.StatusBadRequest, errBadAttachmentPath)
		}
	}

	state, err := chats.SetAttachments(ctx, cmd.ChatID, p.Paths)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	broadcastComposer(ctx, bus, cmd.ChatID, state)

	// Debug and by count, matching the draft's line: a workspace path can
	// name a file the user would not want in a log.
	slog.Debug("attachments set", "chat", cmd.ChatID, "count", len(p.Paths))
	return responseWith(map[string]any{"count": len(p.Paths)}), nil
}
