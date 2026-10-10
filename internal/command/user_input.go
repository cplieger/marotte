package command

// Relays the answer to the agent's structured question (_kiro/userInput) on the request's JSON-RPC
// id, as cmdElicitationResponse does.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

// cmdUserInputResponse forwards the user's answer to kiro-cli as the
// _kiro/userInput response.
func cmdUserInputResponse(ctx context.Context, perms pendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
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
	// Take before responding, as cmdPermission does: the agent advances
	// on the first answer it receives, so a second tab's answer is both
	// discarded and invisible.
	reply, ok := perms.TakePendingPerm(cmd.ChatID, p.RequestID, marotte.SettledByUser)
	if !ok {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := marotte.UserInputResult{Action: p.Action}
	if p.Action == marotte.UserInputActionAnswered {
		result.Answer = p.Answer
	}
	return answerResponse(ctx, "user_input", cmd.ChatID, reply, result)
}
