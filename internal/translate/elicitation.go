package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
)

// HandleElicitationCreate processes a _kiro/mcp/elicitation request: an MCP server asked for
// structured input mid-tool-call. The form is surfaced and CmdElicitationResponse answers on
// msg.ID. On v3 the body is NESTED under "elicitation". The pending-permissions tracker
// replays it on reconnect. KAS signals no upstream cancel.
func (t *Translator) HandleElicitationCreate(ctx context.Context, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
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
		refuseAsk(ctx, chatID, origin, marotte.MethodElicitationCreate, reqID,
			marotte.ElicitationResult{Action: marotte.ElicitationActionCancel}, err)
		return
	}

	subSessionID := t.deriveSubSession(chatID, p.SessionID)

	step := t.steps.refFor(p.SessionID)
	message := displayText(p.Elicitation.Message)
	evt := marotte.NewEvent(marotte.EventElicitationNeeded, chatID, marotte.ElicitationNeededPayload{
		Mode: p.Elicitation.Mode,
		// The message is what the user accepts or declines, so it gets the permission title's
		// treatment. Mode, URL and schema are not display text.
		Message:         message,
		URL:             p.Elicitation.URL,
		ToolCallID:      p.ToolCallID,
		SubSessionID:    subSessionID,
		RunID:           step.WorkflowID,
		NodeID:          step.NodeID,
		RequestedSchema: p.Elicitation.RequestedSchema,
	})
	t.bus.Broadcast(ctx, t.pendingPerms.PendingPermsAdd(reqID, evt, origin))
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelInput}))
	n := notice.Question(t.push.NoticeTarget(ctx, chatID, step.WorkflowID),
		step.NodeID, message)
	t.push.Notify(ctx, chatID, &n)
}
