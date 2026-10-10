package command

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cplieger/marotte/internal/marotte"
)

func validElicitationAction(a string) bool {
	switch a {
	case marotte.ElicitationActionAccept, marotte.ElicitationActionDecline, marotte.ElicitationActionCancel:
		return true
	}
	return false
}

// cmdElicitationResponse forwards the user's elicitation form answer to
// kiro-cli as the elicitation/create response.
func cmdElicitationResponse(ctx context.Context, perms pendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ElicitationResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validElicitationAction(p.Action) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Take before responding, for the same reason cmdPermission does: an
	// elicitation form open in two tabs can be submitted twice, and the
	// second ElicitResult is silently dropped.
	reply, ok := perms.TakePendingPerm(cmd.ChatID, p.RequestID, marotte.SettledByUser)
	if !ok {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := marotte.ElicitationResult{Action: p.Action}
	// Content only travels on accept; decline/cancel carry no values.
	if p.Action == marotte.ElicitationActionAccept {
		result.Content = p.Content
	}
	return answerResponse(ctx, "elicitation", cmd.ChatID, reply, result)
}
