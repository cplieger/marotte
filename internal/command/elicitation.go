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

// cmdElicitationResponse answers the pending _kiro/mcp/elicitation request
// with the user's choice: the filled form, or accept or decline for a URL-mode
// link, which carries no content. A form answer its schema forbids is a 400
// elicitation_content_invalid and the ask stays pending.
func cmdElicitationResponse(ctx context.Context, perms pendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.ElicitationResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validElicitationAction(p.Action) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	var content map[string]any
	if p.Action == marotte.ElicitationActionAccept {
		// Validated before the claim, so a refused answer leaves the card live. A peek miss is a
		// 409, never a fall-through to the take: that would claim an answer nothing checked.
		ask, ok := perms.PendingElicitation(cmd.ChatID, p.RequestID)
		if !ok {
			return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
		}
		var err error
		if content, err = elicitationContent(&ask, p.Content); err != nil {
			return nil, StatusError(http.StatusBadRequest, errElicitationContentInvalid)
		}
	}
	// Take before responding, for the same reason cmdPermission does: an
	// elicitation form open in two tabs can be submitted twice, and the
	// second ElicitResult is silently dropped.
	reply, ok := perms.TakePendingPerm(cmd.ChatID, p.RequestID, marotte.DecisionKindElicitation, marotte.SettledByUser)
	if !ok {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	result := marotte.ElicitationResult{Action: p.Action, Content: content}
	return answerResponse(ctx, "elicitation", cmd.ChatID, reply, result)
}
