package translate

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/runesafe/v3"
)

// approvalTypeTurn is the `_meta.kiro.type` marking a turn approval.
const approvalTypeTurn = "turn_approval"

// ToolIDUpdateWorkflow is the `_meta.kiro.toolId` of a workflow plan approval, on the live ask
// and on the re-ask KAS restores after a restart.
const ToolIDUpdateWorkflow = "update_workflow"

type permOptionWire struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// HandlePermissionRequest processes session/request_permission. The v3 params are FLAT
// ({sessionId, toolCall, options}) and the correlation id is the envelope's msg.ID: a
// params-wrapped decode reads all zeros and answers on id 0, wedging the tool call.
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID marotte.ChatID, origin AskOrigin, msg *marotte.RPCResponse) {
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
		// verdict (see acpPermissionMeta).
		Meta acpPermissionMeta `json:"_meta"`
	}
	reqID := *msg.ID
	req, err := decodeParams[permReq](msg)
	if err != nil {
		// CANCELLED names no option: the only safe answer when options[] could not be read.
		refuseAsk(ctx, chatID, origin, marotte.MethodRequestPermission, reqID, marotte.PermissionOutcomeCancelled(), err)
		return
	}

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

	// A step's ask is attributed to its run whichever lane it arrived on; the node id says WHO asks.
	step, watch := t.askStep(req.SessionID, &req.Meta.Kiro)
	var locations []string
	for _, l := range req.ToolCall.Locations {
		if l.Path != "" {
			locations = append(locations, displayText(t.relPath(l.Path)))
		}
	}
	mcpTool := mcpToolIdentity(&req.Meta.Kiro)
	title := displayText(req.ToolCall.Title)
	consent := permissionConsent(&req.Meta.Kiro.Consent)

	evt := marotte.NewEvent(marotte.EventPermissionNeeded, chatID, marotte.PermissionNeededPayload{
		MCPTool:    mcpTool,
		Consent:    consent,
		Command:    permissionCommand(req.Meta.Kiro.Command),
		ToolCallID: req.ToolCall.ToolCallID,
		// Every shown string here is a decision surface, defused: a Bidi override in the title could
		// render `rm -rf /workspace` as a harmless command (see displayText).
		Title:              title,
		Kind:               toolKindFromWire(req.ToolCall.Kind),
		RunID:              step.WorkflowID,
		NodeID:             step.NodeID,
		Watch:              watch,
		Locations:          locations,
		ConsentRound:       req.Meta.Kiro.ConsentRound,
		Options:            options,
		Files:              files,
		AlwaysAllowBlocked: alwaysAllowBlocked,
		AdminRequired:      req.Meta.Kiro.Consent.Scope == consentScopeAdministration,
		ClassifierNote:     classifierNote(req.Meta.Kiro.TrustClassifier),
		RejectionNote:      rejectionNote(&req.Meta.Kiro, options),
	})
	evt, filed := t.pendingPerms.PendingPermsAdd(ctx, reqID, evt, origin)
	if !filed {
		return
	}
	t.bus.Broadcast(ctx, evt)
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{Label: marotte.WorkingLabelApproval}))
	n := notice.Permission(t.push.NoticeTarget(ctx, chatID, step.WorkflowID),
		step.NodeID, title, len(files))
	n.Ask = notice.AskKey(marotte.DecisionRequestID(evt.Payload))
	t.push.Notify(ctx, chatID, &n)
}

// rejectionNote names the note a card offers with reject_once. KAS reads a note only on a tool
// approval (an ask carrying toolId, never a turn approval); keying the plan kind on toolId holds
// where KAS could not attach workflowPlan, and on the restored re-ask, which carries no consent.
func rejectionNote(k *acpPermissionKiroBlock, options []marotte.PermissionOption) marotte.RejectionNote {
	if k.ToolID == "" || k.Type == approvalTypeTurn ||
		!slices.ContainsFunc(options, func(o marotte.PermissionOption) bool { return o.Kind == "reject_once" }) {
		return ""
	}
	if k.ToolID == ToolIDUpdateWorkflow {
		return marotte.RejectionNotePlanEdits
	}
	return marotte.RejectionNoteReason
}

// nil on any other ask.
func (t *Translator) approvalFiles(k *acpPermissionKiroBlock) []marotte.ApprovalFile {
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
func (t *Translator) askStep(sessionID string, k *acpPermissionKiroBlock) (stepRef, *marotte.PermissionWatch) {
	step := t.steps.refFor(sessionID)
	w := k.WorkflowWatch
	if w == nil || w.WorkflowID == "" {
		return step, nil
	}
	if step.WorkflowID == "" {
		step = stepRef{WorkflowID: w.WorkflowID, NodeID: w.NodeID}
	}
	return step, &marotte.PermissionWatch{WorkflowID: w.WorkflowID, NodeID: w.NodeID}
}

// mcpToolIdentity is the MCP server and tool an ask names, nil unless both are.
func mcpToolIdentity(k *acpPermissionKiroBlock) *marotte.MCPToolIdentity {
	serverName := displayText(k.MCPTool.Identity.ServerName)
	toolName := displayText(k.MCPTool.Identity.ToolName)
	if serverName == "" || toolName == "" {
		return nil
	}
	return &marotte.MCPToolIdentity{ServerName: serverName, ToolName: toolName}
}

// permissionConsent is what an ask is about, nil when KAS named no capability. The subject
// and folder are decision surfaces like the title, so the shown copies are defused the same
// way while the raw ones stay the rule keys; the capability is an identifier the answer echoes.
func permissionConsent(c *acpConsentMeta) *marotte.PermissionConsent {
	if c.Capability == "" {
		return nil
	}
	subject := c.subject()
	out := &marotte.PermissionConsent{Capability: c.Capability, Subject: displayText(subject), Resource: subject}
	if i := strings.LastIndex(subject, "/"); strings.HasPrefix(c.Capability, "fs_") && i > 0 {
		// A folder whose shown copy reads the same as the subject's would be two identical
		// choices that save different rules.
		if folder := subject[:i] + "/**"; displayText(folder) != out.Subject {
			out.Folder, out.FolderResource = displayText(folder), folder
		}
	}
	return out
}

// maxApprovalCommandBytes is above any command a person vets in the card's box and matches
// what the record keeps of one input member; every pending ask rides the SSE connect's one
// pending_snapshot frame, under the hub's 1 MiB frame cap.
const maxApprovalCommandBytes = 8 << 10

// permissionCommand is the command an ask shows, nil when it names none. Past the cap the head
// and the tail both stay, so a suffix such as `; rm -rf ~` is never what the cut hides.
func permissionCommand(raw string) *marotte.PermissionCommand {
	if raw == "" {
		return nil
	}
	clean := displayLines(raw)
	if len(clean) <= maxApprovalCommandBytes {
		return &marotte.PermissionCommand{Text: clean}
	}
	head := runesafe.CapBytes(clean, maxApprovalCommandBytes/2)
	tail := runesafe.CapBytesTail(clean, maxApprovalCommandBytes/2)
	return &marotte.PermissionCommand{Text: head, Tail: tail, OmittedBytes: len(clean) - len(head) - len(tail)}
}

const consentScopeAdministration = "administration"
