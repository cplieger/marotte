package command

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// validElicitationAction reports whether a is an accepted MCP
// ElicitResult action.
func validElicitationAction(a string) bool {
	switch a {
	case marotte.ElicitationActionAccept, marotte.ElicitationActionDecline, marotte.ElicitationActionCancel:
		return true
	}
	return false
}

// CmdElicitationResponse forwards the user's elicitation form answer to
// kiro-cli as the elicitation/create response.
func CmdElicitationResponse(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p marotte.ElicitationResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validElicitationAction(p.Action) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Take before responding, for the same reason CmdPermission does: an
	// elicitation form open in two tabs can be submitted twice, and the
	// second ElicitResult is silently dropped.
	if !perms.TakePendingPerm(cmd.ChatID, p.RequestID, marotte.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := marotte.ElicitationResult{Action: p.Action}
	// Content only travels on accept; decline/cancel carry no values.
	if p.Action == marotte.ElicitationActionAccept {
		result.Content = p.Content
	}
	// Claimed, so it must be answered whether or not the client is still there.
	if err := sb.Respond(durable.Context(ctx), p.RequestID, result, nil); err != nil {
		slog.Error("elicitation response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
