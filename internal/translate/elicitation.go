package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// HandleElicitationCreate processes a _kiro/mcp/elicitation request: an MCP server asked for
// structured input mid-tool-call. The form is surfaced and CmdElicitationResponse answers on
// msg.ID. On v3 the body is NESTED under "elicitation". The pending-permissions tracker
// replays it on reconnect. KAS signals no upstream cancel.
func (t *Translator) HandleElicitationCreate(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if msg.ID == nil {
		// No id means no route for the answer: drop rather than show an unanswerable dialog.
		slog.Warn("elicitation missing id", "chat_id", chatID)
		return
	}
	type elicitBody struct {
		RequestedSchema *marotte.ElicitationRequestSchema `json:"requestedSchema"`
		Mode            string                            `json:"mode"`
		Message         string                            `json:"message"`
		URL             string                            `json:"url"`
	}
	type elicitParams struct {
		SessionID   string     `json:"sessionId"`
		ToolCallID  string     `json:"toolCallId"`
		Elicitation elicitBody `json:"elicitation"`
	}
	reqID := *msg.ID
	p, err := decodeParams[elicitParams](msg)
	if err != nil {
		t.refuseAsk(ctx, chatID, marotte.MethodElicitationCreate, reqID,
			marotte.ElicitationResult{Action: marotte.ElicitationActionCancel}, err)
		return
	}

	subSessionID := t.deriveSubSession(chatID, p.SessionID)

	step := t.steps.refFor(p.SessionID)
	evt := marotte.NewEvent(marotte.EventElicitationNeeded, chatID, marotte.ElicitationNeededPayload{
		RequestID: reqID,
		Mode:      p.Elicitation.Mode,
		// The message is what the user accepts or declines, so it gets the permission title's
		// treatment. Mode, URL and schema are not display text.
		Message:         displayText(p.Elicitation.Message),
		URL:             p.Elicitation.URL,
		ToolCallID:      p.ToolCallID,
		SubSessionID:    subSessionID,
		RunID:           step.WorkflowID,
		NodeID:          step.NodeID,
		RequestedSchema: p.Elicitation.RequestedSchema,
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelInput}))
	t.push.NotifyPush(ctx, "Input needed", marotte.PushKindPermission, chatID)
}
