package translate

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/turnlog"
)

// errElicitationURLNotWeb is the refusal cause for a URL-mode ask whose link is no web address.
var errElicitationURLNotWeb = errors.New("elicitation link is not an http or https URL")

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
	// Decline is KAS's own answer when its URL gate refuses a link; an app-scheme or file link
	// on a loopback host passes that gate.
	if p.Elicitation.Mode == marotte.ElicitationModeURL && !IsWebURL(p.Elicitation.URL) {
		refuseAsk(ctx, chatID, origin, marotte.MethodElicitationCreate, reqID,
			marotte.ElicitationResult{Action: marotte.ElicitationActionDecline}, errElicitationURLNotWeb)
		return
	}

	step := t.steps.refFor(p.SessionID)
	message := displayText(p.Elicitation.Message)
	evt := marotte.NewEvent(marotte.EventElicitationNeeded, chatID, marotte.ElicitationNeededPayload{
		Mode:            p.Elicitation.Mode,
		Message:         message,
		URL:             p.Elicitation.URL,
		ToolCallID:      p.ToolCallID,
		MCPTool:         t.askingTool(chatID, step, p.ToolCallID),
		RunID:           step.WorkflowID,
		NodeID:          step.NodeID,
		RequestedSchema: displaySchema(p.Elicitation.RequestedSchema),
	})
	evt, filed := t.pendingPerms.PendingPermsAdd(ctx, reqID, evt, origin)
	if !filed {
		return
	}
	t.bus.Broadcast(ctx, evt)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelInput}))
	n := notice.Question(t.push.NoticeTarget(ctx, chatID, step.WorkflowID),
		step.NodeID, message)
	n.Ask = notice.AskKey(marotte.DecisionRequestID(evt.Payload))
	t.push.Notify(ctx, chatID, &n)
}

// askingTool is the MCP identity of the open call an elicitation names, from the step's run turn
// or the chat's own; KAS's request carries no server name of its own.
func (t *Translator) askingTool(chatID marotte.ChatID, step stepRef, toolCallID string) *marotte.MCPToolIdentity {
	if toolCallID == "" {
		return nil
	}
	var turn *turnlog.Turn
	var ok bool
	if step.WorkflowID != "" {
		turn, ok = t.runs.RunOpenTurn(step.WorkflowID, step.NodePath)
	} else {
		turn, ok = t.turns.OwnTurn(chatID)
	}
	if !ok {
		return nil
	}
	call, ok := turn.OpenCallFor(toolCallID)
	if !ok || call.Call.MCP == nil {
		return nil
	}
	id := *call.Call.MCP
	return &id
}

// displaySchema passes every string the card shows through displayText, in place. Values the
// answer carries (property names, enum values, option consts, defaults) stay as sent, so a
// property with no title gets its sanitized name as one: the card labels it by name.
func displaySchema(s *marotte.ElicitationRequestSchema) *marotte.ElicitationRequestSchema {
	if s == nil {
		return nil
	}
	s.Title = displayText(s.Title)
	s.Description = displayText(s.Description)
	for name := range s.Properties {
		prop := s.Properties[name]
		if prop.Title == "" {
			prop.Title = name
		}
		prop.Title = displayText(prop.Title)
		prop.Description = displayText(prop.Description)
		for i := range prop.EnumNames {
			prop.EnumNames[i] = displayText(prop.EnumNames[i])
		}
		displayOptions(prop.OneOf)
		if prop.Items != nil {
			displayOptions(prop.Items.AnyOf)
		}
		s.Properties[name] = prop
	}
	return s
}

func displayOptions(opts []marotte.ElicitationEnumOption) {
	for i := range opts {
		opts[i].Title = displayText(opts[i].Title)
	}
}
