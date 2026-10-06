package translate

import (
	"context"
	"log/slog"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
)

// approvalTypeTurn is the `_meta.kiro.type` marking a turn approval.
const approvalTypeTurn = "turn_approval"

type permOptionWire struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// HandlePermissionRequest processes session/request_permission. The v3 params are FLAT
// ({sessionId, toolCall, options}) and the correlation id is the envelope's msg.ID: a
// params-wrapped decode reads all zeros and answers on id 0, wedging the tool call.
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	if msg.ID == nil {
		// No id, no route for the outcome: drop rather than show an unanswerable dialog.
		slog.Warn("permission request missing id", "chat_id", chatID)
		return
	}
	// Field order is fieldalignment's rather than the wire's.
	type permReq struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string           `json:"toolCallId"`
			Title      string           `json:"title"`
			Kind       marotte.ToolKind `json:"kind"`
			Locations  []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"toolCall"`
		Options []permOptionWire `json:"options"`
		// Meta carries the turn-approval discriminator, its file list and the persistability
		// verdict (see ACPPermissionMeta).
		Meta ACPPermissionMeta `json:"_meta"`
	}
	reqID := *msg.ID
	req, err := decodeParams[permReq](msg)
	if err != nil {
		// CANCELLED names no option: the only safe answer when options[] could not be read.
		t.refuseAsk(ctx, chatID, marotte.MethodRequestPermission, reqID, marotte.PermissionOutcomeCancelled(), err)
		return
	}

	subSessionID := t.deriveSubSession(chatID, req.SessionID)

	options := make([]marotte.PermissionOption, len(req.Options))
	for i, o := range req.Options {
		// Name is the button text, a decision surface like the title; OptionID and Kind are echoed
		// identifiers and stay exactly as received.
		options[i] = marotte.PermissionOption{OptionID: o.OptionID, Name: displayText(o.Name), Kind: o.Kind}
	}

	// Workspace-relative, like every other path marotte puts on the wire.
	files := t.approvalFiles(&req.Meta.Kiro)

	// Persistability as a marotte CODE, not KAS's reason string. Absent means persistable, so only
	// present-and-false blocks the offer (a plain bool would block every pre-2.19.1 frame).
	var alwaysAllowBlocked marotte.AlwaysAllowBlock
	if c := req.Meta.Kiro.Consent.PersistableConsent; c != nil && !*c {
		alwaysAllowBlocked = marotte.AlwaysAllowBlockUnparseable
	}

	// A step's ask is attributed to its run whichever bridge it arrived on; the node id says WHO asks.
	step, watch := t.askStep(req.SessionID, &req.Meta.Kiro)
	var locations []string
	for _, l := range req.ToolCall.Locations {
		if l.Path != "" {
			locations = append(locations, t.relPath(l.Path))
		}
	}
	mcpTool := mcpToolIdentity(&req.Meta.Kiro)

	evt := marotte.NewEvent(marotte.EventPermissionNeeded, chatID, marotte.PermissionNeededPayload{
		MCPTool:    mcpTool,
		RequestID:  reqID,
		ToolCallID: req.ToolCall.ToolCallID,
		// THE TITLE IS A DECISION SURFACE, the one string here that is defused: a Bidi override in it
		// could render `rm -rf /workspace` as a harmless command (see displayText).
		Title:              displayText(req.ToolCall.Title),
		Kind:               toolKindFromWire(req.ToolCall.Kind),
		SubSessionID:       subSessionID,
		RunID:              step.WorkflowID,
		NodeID:             step.NodeID,
		Watch:              watch,
		Locations:          locations,
		ConsentRound:       req.Meta.Kiro.ConsentRound,
		Options:            options,
		Files:              files,
		AlwaysAllowBlocked: alwaysAllowBlocked,
		AdminRequired:      req.Meta.Kiro.Consent.Scope == consentScopeAdministration,
		// KAS reads a reason only at the ordinary tool approval, the one ask carrying toolId.
		AcceptsRejectionReason: req.Meta.Kiro.ToolID != "" && req.Meta.Kiro.Type != approvalTypeTurn &&
			slices.ContainsFunc(options, func(o marotte.PermissionOption) bool { return o.Kind == "reject_once" }),
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelApproval}))
	t.push.NotifyPush(ctx, "Permission needed", marotte.PushKindPermission, chatID)
}

// approvalFiles is a turn approval's file list, workspace-relative; nil on any
// other ask.
func (t *Translator) approvalFiles(k *ACPPermissionKiroBlock) []marotte.ApprovalFile {
	if k.Type != approvalTypeTurn {
		return nil
	}
	files := make([]marotte.ApprovalFile, 0, len(k.Files))
	for _, f := range k.Files {
		files = append(files, marotte.ApprovalFile{
			Path:        t.relPath(f.Path),
			SnapshotURI: f.SnapshotURI,
			ActionID:    f.ToolCallID,
		})
	}
	return files
}

// askStep attributes an ask to its workflow step, falling back to the watch a
// workflow WATCH raised when the step registry names none.
func (t *Translator) askStep(sessionID string, k *ACPPermissionKiroBlock) (StepRef, *marotte.PermissionWatch) {
	step := t.steps.refFor(sessionID)
	w := k.WorkflowWatch
	if w == nil || w.WorkflowID == "" {
		return step, nil
	}
	if step.WorkflowID == "" {
		step = StepRef{WorkflowID: w.WorkflowID, NodeID: w.NodeID}
	}
	return step, &marotte.PermissionWatch{WorkflowID: w.WorkflowID, NodeID: w.NodeID}
}

// mcpToolIdentity is the MCP server and tool an ask names, nil unless both are.
func mcpToolIdentity(k *ACPPermissionKiroBlock) *marotte.MCPToolIdentity {
	serverName := displayText(k.MCPTool.Identity.ServerName)
	toolName := displayText(k.MCPTool.Identity.ToolName)
	if serverName == "" || toolName == "" {
		return nil
	}
	return &marotte.MCPToolIdentity{ServerName: serverName, ToolName: toolName}
}

// consentScopeAdministration is the consent scope of an ask the administrator's
// managed-settings rules raise.
const consentScopeAdministration = "administration"
