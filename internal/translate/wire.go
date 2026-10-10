// Package translate decodes ACP notifications from kiro-cli bridges and dispatches them
// as domain events (SSE broadcasts, chat-store mutations); Runtime stays the coordinator.
package translate

import (
	"cmp"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// acpChunkWire is the wire shape for agent_message_chunk and agent_thought_chunk
// session updates. A nested subagent's chunks ride the parent session id and carry
// _meta.kiro.agentSubtaskId naming the tool call they belong to.
type acpChunkWire struct {
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Meta acpKiroMeta `json:"_meta"`
}

// acpToolCallContentBlock is one element in a tool_call or tool_call_update's content
// array. On a diff block, KAS's edit tools send whole-file OldText/NewText (others a hunk
// pair), so a line count must diff the two sides.
type acpToolCallContentBlock struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	// TerminalID is set on a type:"terminal" block: this tool call's output is that
	// terminal's stream, rendered on the tool CARD.
	TerminalID string `json:"terminalId"`
	Content    struct {
		Text string `json:"text"`
	} `json:"content"`
}

// acpKiroMeta is the top-level `_meta` carrying a `kiro` block. Kind=="agent-subtask"
// marks a subagent card, AgentSubtaskID links it to its nested chunk deltas. Kiro.HookAsk
// marks a pre-tool-use hook's ask card (KAS has no ToolKind "hook"); it gates nothing.
type acpKiroMeta struct {
	Kiro acpKiroBlock `json:"kiro"`
}

// acpKiroBlock is the `kiro` object inside an `_meta`, named so it can carry the census.
type acpKiroBlock struct {
	// Refusal is present only on the agent_message_chunk carrying a model-refusal
	// explanation. The turn then ends with core stopReason "refusal".
	Refusal *acpRefusalMeta `json:"refusal"`
	// DisclosedContext identifies the skill or steering document a `disclose_context` call
	// loaded. KAS persists it, so the activation renders as itself after a reload.
	DisclosedContext *acpDisclosedContext `json:"disclosedContext,omitempty"`
	// PolicyDenial is KAS's persisted reason for a tool call the Cedar policy refused, so a
	// refusal is not mistaken for a broken command.
	PolicyDenial *acpPolicyDenial   `json:"policyDenial,omitempty"`
	Checkpoint   *acpCheckpointMeta `json:"checkpoint"`
	// OutputTransformation rides the terminal tool_call_update, live and replayed, when KAS
	// offloaded an output of 30,000+ characters to a file.
	OutputTransformation *acpOutputTransformation `json:"outputTransformation,omitempty"`
	Kind                 string                   `json:"kind"`
	AgentSubtaskID       string                   `json:"agentSubtaskId"`
	// ToolID is KAS's machine name for a tool call (`execute_bash`, `user_input`). Not model-
	// or locale-composed, so the internal-tool suppression keys on it.
	ToolID string `json:"toolId"`
	// WorkflowID names the run a `run_workflow` invocation started. On a REPLAY it is the only
	// channel carrying it: replayed `rawOutput` is prose (kiro-cli 2.21.4).
	WorkflowID string `json:"workflowId"`
	// ReplayID is the `messageId` KAS reports for this message on REPLAY; it arrives on the
	// LIVE frame only (kiro-cli 2.21.4: `<uuid>-say`). The agent's id space, never marotte's.
	ReplayID string `json:"replayId"`
	// UserMessageTag marks a user row KAS filed under a PROMPT rather than as steering. Read
	// for PRESENCE only: the value can be client-supplied text, so no rule keys on its prefix.
	UserMessageTag string `json:"userMessageTag"`
	// MessageID and Timestamp (RFC3339 ms) are KAS's identity for the frame's message record.
	// Without them the replay projection fabricates ids and dates all history to now.
	MessageID string `json:"messageId"`
	Timestamp string `json:"timestamp"`
	// Source marks the whole steering CHANNEL (progress rows, step notices, boundaries). The
	// replay builder stamps it here, not on the update object.
	Source string `json:"source"`
	// Notification tags a row KAS wrote onto a chat on something else's behalf;
	// "workflow-progress" is a step's progress, a user_message_chunk carrying JSON.
	Notification struct {
		Kind string `json:"kind"`
		// Status is KAS's severity for a notification-kind row; only a REPLAY carries it here (the
		// live frame uses notificationSeverity), so a replayed step note keeps its label.
		Status string `json:"status"`
		// Sender is "parent" or "step" on a workflow message's row (send_message).
		Sender string `json:"sender"`
	} `json:"notification"`
	// Workflow is on every frame of a workflow STEP's session and is the only mark of one.
	// Nested at `params.update._meta.kiro.workflow`, not `params._meta`.
	Workflow *ACPWorkflowMeta `json:"workflow"`
	HookAsk  json.RawMessage  `json:"hookAsk,omitempty"`
	// AgentInitiated marks a turn the ENGINE started. It rides CONTENT frames, never the
	// bracket, and a zero-content auto-wake sends none.
	AgentInitiated bool `json:"agentInitiated"`
}

// acpKiroBlockShadow strips the UnmarshalJSON method so the real decode does not recurse.
type acpKiroBlockShadow acpKiroBlock

// UnmarshalJSON decodes the block and reports any member KAS sent that this type does not
// read. The census runs here because encoding/json hands this method just the
// `_meta.kiro` bytes. It contributes no error.
func (b *acpKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*acpKiroBlockShadow)(b)); err != nil {
		return err
	}
	// `preview` repeats the checkpoint URIs plus both file bodies: noise on every write.
	// `replay` is already read on ACPSessionUpdateBase's own kiro block, its single owner.
	censusMeta("_meta.kiro", data, reflect.TypeFor[acpKiroBlockShadow](), "preview", "replay")
	return nil
}

// acpDisclosedContext is _meta.kiro.disclosedContext on a disclose_context call.
type acpDisclosedContext struct {
	// Type is "skill" or "steering".
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
	URI         string `json:"uri"`
}

// acpOutputTransformation is _meta.kiro.outputTransformation; "offloaded" is the
// only kind KAS emits.
type acpOutputTransformation struct {
	Kind        string `json:"kind"`
	AbsFilePath string `json:"absFilePath"`
	TotalChars  int    `json:"totalChars"`
}

// acpPolicyDenial is _meta.kiro.policyDenial on a tool call Cedar refused. MatchedRule
// names the rule the user owns. The outer `effect` is always "deny" and not decoded; the
// inner rule's effect can be deny or ask.
type acpPolicyDenial struct {
	MatchedRule *acpPolicyRule `json:"matchedRule"`
	Capability  string         `json:"capability"`
	Resource    string         `json:"resource"`
	Scope       string         `json:"scope"`
	Source      string         `json:"source"`
}

// acpPolicyRule is the matched rule inside a policy denial.
type acpPolicyRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// ACPWorkflowMeta is the _meta.kiro.workflow block on a step session's frames. Per-step
// attribution keys on NodePath (two iterations share a NodeID). Its `iter-<n>` segment
// is the FRAME spelling; inspect's state tree names it `<repeatId>#<n>`.
type ACPWorkflowMeta struct {
	WorkflowID string   `json:"workflowId"`
	NodeID     string   `json:"nodeId"`
	NodePath   []string `json:"nodePath"`
}

// acpCheckpointMeta is the _meta.kiro.checkpoint object on a file-writing
// tool_call_update. Every field is independently optional, so it merges per field (see
// marotte.ToolCheckpoint).
type acpCheckpointMeta struct {
	Original string `json:"original"`
	Modified string `json:"modified"`
	Local    string `json:"local"`
}

// acpRefusalMeta is the _meta.kiro.refusal block on a refusal explanation chunk. The
// chunk's text is kept OUT of the assistant entry, so this block, flowing into
// marotte.RefusalInfo, is where the explanation survives.
type acpRefusalMeta struct {
	Category         string `json:"category"`
	Explanation      string `json:"explanation"`
	RecommendedModel string `json:"recommendedModel"`
}

// acpConsentMeta is the _meta.kiro.consent object on a session/request_permission: what the
// ask is about, present whenever KAS offers to persist an answer. PersistableConsent is a
// pointer because absent means yes. One compound command asks once per unapproved part, and
// TriggeringResource names that part; Resource is the whole command.
type acpConsentMeta struct {
	PersistableConsent       *bool  `json:"persistableConsent"`
	PersistableConsentReason string `json:"persistableConsentReason"`
	// Scope is the policy scope that asked; "administration" (managed settings) needs a person.
	Scope              string `json:"scope"`
	Capability         string `json:"capability"`
	Resource           string `json:"resource"`
	TriggeringResource string `json:"triggeringResource"`
}

type acpConsentMetaShadow acpConsentMeta

// UnmarshalJSON decodes the block and reports any member it does not read. askType needs no
// reader (KAS offers no Always allow on an explicit ask); matchedRule and source name the rule
// that asked, which the card does not show; KAS falls back to the request's own workspaceRoot
// when an answer omits it.
func (c *acpConsentMeta) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*acpConsentMetaShadow)(c)); err != nil {
		return err
	}
	censusMeta("session/request_permission._meta.kiro.consent", data, reflect.TypeFor[acpConsentMetaShadow](),
		"askType", "matchedRule", "source", "workspaceRoot")
	return nil
}

// subject is the part of the command this ask is about.
func (c *acpConsentMeta) subject() string {
	return cmp.Or(c.TriggeringResource, c.Resource)
}

// acpPermissionMeta is the `_meta` on a session/request_permission, named because it
// multiplexes three concerns on the one human APPROVAL surface.
type acpPermissionMeta struct {
	Kiro acpPermissionKiroBlock `json:"kiro"`
}

// acpPermissionKiroBlock is the `kiro` object inside a permission request's `_meta`.
// Field order is fieldalignment's: Consent leads as the only pointer.
type acpPermissionKiroBlock struct {
	// WorkflowWatch names the run and node a watch command polls for; the ask arrives on the
	// LAUNCHING session, so the step registry cannot name them.
	WorkflowWatch *acpWorkflowWatch `json:"workflowWatch"`
	// Consent names what the ask is about and whether an always answer can persist.
	Consent acpConsentMeta `json:"consent"`
	// MCPTool carries the identity KAS verified for an MCP-backed tool.
	MCPTool acpmcpToolWire `json:"mcpTool"`
	// Type marks a TURN APPROVAL ("turn_approval"), the only thing distinguishing it from a
	// tool approval on session/request_permission.
	Type string `json:"type"`
	// ToolID is set on the ordinary tool approval alone, whose reject reads a rejectionReason.
	ToolID string `json:"toolId"`
	// Files is the turn approval's staged file list: ABSOLUTE paths and a `toolCallId` action
	// id, both renamed on the way out.
	Files []acpApprovalFile `json:"files"`
	// ConsentRound counts KAS's asks for one tool call, from 1.
	ConsentRound int `json:"consentRound"`
}

// acpWorkflowWatch is `_meta.kiro.workflowWatch` on a watch command's ask.
type acpWorkflowWatch struct {
	WorkflowID string `json:"workflowId"`
	NodeID     string `json:"nodeId"`
}

type acpPermissionKiroBlockShadow acpPermissionKiroBlock

// UnmarshalJSON decodes the block and reports any member KAS sent that it does not read;
// the declined ones are read elsewhere or belong to asks this frame does not render.
func (b *acpPermissionKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*acpPermissionKiroBlockShadow)(b)); err != nil {
		return err
	}
	censusMeta("session/request_permission._meta.kiro", data, reflect.TypeFor[acpPermissionKiroBlockShadow](),
		"command", "agentManagesTrust", "disclosedContext", "hookName", "executionId", "kind",
		"requirementText", "question", "requirementRange", "requirementsFilePath")
	return nil
}

// acpmcpToolWire is the verified MCP identity attached to a permission request.
type acpmcpToolWire struct {
	Identity struct {
		ServerName string `json:"serverName"`
		ToolName   string `json:"toolName"`
	} `json:"identity"`
}

// acpApprovalFile is one entry of a turn approval's `files` array.
type acpApprovalFile struct {
	Path        string `json:"path"`
	SnapshotURI string `json:"snapshotUri"`
	ToolCallID  string `json:"toolCallId"`
}

// acpToolCallWire is the wire shape for tool_call session updates.
type acpToolCallWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       marotte.ToolKind          `json:"kind"`
	Status     marotte.ToolStatus        `json:"status"`
	RawInput   json.RawMessage           `json:"rawInput"`
	Locations  []marotte.ToolLocation    `json:"locations"`
	Content    []acpToolCallContentBlock `json:"content"`
	// Meta trails: acpKiroBlock ends in a bool and fieldalignment counts leading pointer bytes.
	Meta acpKiroMeta `json:"_meta"`
}

// acpToolCallUpdateWire is the wire shape for tool_call_update session updates. rawOutput
// stays opaque except for the workflow link and the narrow text fallbacks.
type acpToolCallUpdateWire struct {
	ToolCallID string                    `json:"toolCallId"`
	Title      string                    `json:"title"`
	Kind       marotte.ToolKind          `json:"kind"`
	Status     marotte.ToolStatus        `json:"status"`
	RawOutput  json.RawMessage           `json:"rawOutput"`
	Locations  []marotte.ToolLocation    `json:"locations"`
	Content    []acpToolCallContentBlock `json:"content"`
	// Meta trails: acpKiroBlock ends in a bool and fieldalignment counts leading pointer bytes.
	Meta acpKiroMeta `json:"_meta"`
}

// acpRawOutput is the object shape read out of a tool call's `rawOutput` (KAS: `unknown`).
// WorkflowID is run_workflow's link to its run; Error and Message are failure fallbacks
// (Message also unwraps a stringified copy); Updated is update_workflow's own verdict and
// Saved save_workflow_definition's, pointers because absent means TAKEN. Kept narrow so
// success payloads stay off the card.
type acpRawOutput struct {
	Updated    *bool  `json:"updated"`
	Saved      *bool  `json:"saved"`
	WorkflowID string `json:"workflowId"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// rawOutputWorkflowID extracts the workflow id a `run_workflow` invocation
// reports, or "" when this update's rawOutput is absent, not an object, or
// carries no id.
func rawOutputWorkflowID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out acpRawOutput
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	return out.WorkflowID
}

// rawOutputStatesUpdate reports whether a workflow-update tool stated its own verdict. It is
// false for an absent, non-object or malformed rawOutput and for one with no `updated` key (only
// one tool's object carries it), so absent means TAKEN, as in agent's stepStatusRefusal.
func rawOutputStatesUpdate(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var out acpRawOutput
	return json.Unmarshal(raw, &out) == nil && out.Updated != nil
}

// rawOutputVerdict reports a workflow tool's own verdict (`updated`, else `saved`) and whether
// it stated one, with rawOutputStatesUpdate's absent-means-taken rule.
func rawOutputVerdict(raw json.RawMessage) (accepted, present bool) {
	if len(raw) == 0 {
		return false, false
	}
	var out acpRawOutput
	if json.Unmarshal(raw, &out) != nil {
		return false, false
	}
	switch {
	case out.Updated != nil:
		return *out.Updated, true
	case out.Saved != nil:
		return *out.Saved, true
	default:
		return false, false
	}
}

// rawOutputString extracts rawOutput only when it is a bare JSON string, KAS's shape when
// an edit's diff block is suppressed: the one non-content channel safe on any status.
func rawOutputString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// stringifiedRawOutputMessage returns message only when content encodes the same
// multi-field object as rawOutput; a different content block remains canonical.
func stringifiedRawOutputMessage(raw json.RawMessage, content string) string {
	var out acpRawOutput
	var rawObject map[string]any
	var contentObject map[string]any
	if json.Unmarshal(raw, &out) != nil || json.Unmarshal(raw, &rawObject) != nil || len(rawObject) < 2 {
		return ""
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &contentObject) != nil {
		return ""
	}
	if !reflect.DeepEqual(rawObject, contentObject) {
		return ""
	}
	return strings.TrimSpace(out.Message)
}

// mcpEnvelopeResponse returns the tool text from KAS's MCP result envelope
// {response, imageBase64Urls?} when content is that object stringified. Keys match
// exactly, so no other tool's object is unwrapped.
func mcpEnvelopeResponse(raw json.RawMessage, content string) string {
	var rawObject map[string]any
	var contentObject map[string]any
	if json.Unmarshal(raw, &rawObject) != nil {
		return ""
	}
	response, ok := rawObject["response"].(string)
	if !ok {
		return ""
	}
	for key := range rawObject {
		if key != "response" && key != "imageBase64Urls" {
			return ""
		}
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &contentObject) != nil ||
		!reflect.DeepEqual(rawObject, contentObject) {
		return ""
	}
	return strings.TrimSpace(response)
}

func rawOutputFailureText(raw json.RawMessage) string {
	if text := rawOutputString(raw); text != "" {
		return text
	}
	var out acpRawOutput
	if json.Unmarshal(raw, &out) != nil {
		return ""
	}
	if out.Error != "" {
		return strings.TrimSpace(out.Error)
	}
	return strings.TrimSpace(out.Message)
}

// acpPlanWire is the wire shape for plan session updates.
type acpPlanWire struct {
	Entries []marotte.PlanEntry `json:"entries"`
}

// acpModeUpdateWire is the current_mode_update sub-kind. The mode is `currentModeId`, NOT
// `modeId` (the outbound set_mode field): the wrong one drops agent mode changes.
type acpModeUpdateWire struct {
	ModeID string `json:"currentModeId"`
}

// ACPSessionUpdateEnvelope is the outer envelope for session/update
// notifications.
type ACPSessionUpdateEnvelope struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ACPSessionUpdateBase extracts the sessionUpdate kind and whether the frame is a replay.
// A session/load replays history tagged `_meta.kiro.replay: true` on the UPDATE object,
// not params. Absent means live, meaning "current state, not history": a rule, not a
// list, since the untagged set grows.
type ACPSessionUpdateBase struct {
	Kind marotte.ACPUpdateKind `json:"sessionUpdate"`
	Meta struct {
		Kiro struct {
			// Workflow is the discriminator the dispatcher classifies a step on.
			Workflow *ACPWorkflowMeta `json:"workflow"`
			Replay   bool             `json:"replay"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// contentTypeContent is the ACP content-block type discriminator value "content".
// Distinct from jsonFieldContent, which is the JSON field *name* "content".
const contentTypeContent = "content"

// contentTypeDiff is the ACP content-block type for file-change diffs.
const contentTypeDiff = "diff"

// contentTypeTerminal is the content-block type naming the terminal running an execute
// tool call; its terminalId finds the card's output stream.
const contentTypeTerminal = "terminal"
