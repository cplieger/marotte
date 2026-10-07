package command

// Relays the answer to the agent's structured question (_kiro/userInput) on the request's JSON-RPC
// id, as CmdElicitationResponse does.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// CmdUserInputResponse forwards the user's answer to kiro-cli as the
// _kiro/userInput response.
func CmdUserInputResponse(ctx context.Context, bridges BridgeAccess, perms PendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return nil, StatusError(http.StatusBadRequest, errNoBridge)
	}
	var p marotte.UserInputResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// KAS ignores an empty answer and advances anyway, so "answered" without text is rejected.
	switch p.Action {
	case marotte.UserInputActionAnswered:
		if p.Answer == "" {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	case marotte.UserInputActionDismissed:
	default:
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Take before responding, as CmdPermission does: the agent advances
	// on the first answer it receives, so a second tab's answer is both
	// discarded and invisible.
	if !perms.TakePendingPerm(cmd.ChatID, p.RequestID, marotte.SettledByUser) {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := marotte.UserInputResult{Action: p.Action}
	if p.Action == marotte.UserInputActionAnswered {
		result.Answer = p.Answer
	}
	// Claimed, so it must be answered whether or not the client is still there.
	if err := sb.Respond(durable.Context(ctx), p.RequestID, result, nil); err != nil {
		slog.Error("user input response failed", "chat_id", cmd.ChatID, keyError, err)
	}
	return responseOK, nil
}
