package marotte

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// EntryKind is the discriminator of an Entry's payload, shared by the chat log and the
// run log.
type EntryKind string

// EntryKindTurnOpen and the following constants are the valid EntryKind values.
const (
	EntryKindTurnOpen         EntryKind = "turn_open"
	EntryKindTurnBind         EntryKind = "turn_bind"
	EntryKindText             EntryKind = "text"
	EntryKindThinking         EntryKind = "thinking"
	EntryKindToolCall         EntryKind = "tool_call"
	EntryKindToolResult       EntryKind = "tool_result"
	EntryKindSteer            EntryKind = "steer"
	EntryKindSteerAck         EntryKind = "steer_ack"
	EntryKindSteerDelivered   EntryKind = "steer_delivered"
	EntryKindPlan             EntryKind = "plan"
	EntryKindCompaction       EntryKind = "compaction"
	EntryKindCompactionFailed EntryKind = "compaction_failed"
	EntryKindSafetyBlocked    EntryKind = "safety_blocked"
	EntryKindModelSwitched    EntryKind = "model_switched"
	EntryKindModelRouted      EntryKind = "model_routed"
	EntryKindModeSwitched     EntryKind = "mode_switched"
	EntryKindTurnRevert       EntryKind = "turn_revert"
	EntryKindReconciled       EntryKind = "reconciled"
	EntryKindTurnClose        EntryKind = "turn_close"
)

// Entry is one sealed row of a turn's log. Every field but Payload is the
// envelope. An Entry exists on disk and on the wire only after its payload is
// frozen and its Seq assigned; open state is OpenEntry.
type Entry struct {
	ID   string `json:"id"`
	Turn string `json:"turn"`
	// Lane is "" for the agent owning the log's turn (the chat's, or the step's in a
	// run log), else a delegate's uuid.
	Lane string    `json:"lane,omitempty"`
	Kind EntryKind `json:"kind"`
	// Payload is the payload type Kind selects.
	Payload json.RawMessage `json:"payload"`
	// Seq is per turn, contiguous from 0 (turn_open), assigned at append.
	Seq uint64 `json:"seq"`
	// Ts is the appender's wall clock at append: metadata, never compared.
	Ts int64 `json:"ts"`
}

// OpenEntry is a text or thinking entry still coalescing deltas. It has no Seq until it
// seals into an Entry, and travels as entry_opened and a GET's open_entries, never the log.
type OpenEntry struct {
	Turn string `json:"turn"`
	ID   string `json:"id"`
	Lane string `json:"lane,omitempty"`
	// Kind is text or thinking.
	Kind EntryKind `json:"kind"`
	// Text is the content coalesced so far.
	Text string `json:"text"`
	// N is the count of deltas applied so far.
	N uint64 `json:"n"`
}

// TurnOpenSourceName is the wire spelling of what opened a turn; TurnOpenSource maps onto
// it through Name. `event` (a turn an event row opens) and `revert` (the carrier a revert
// opens when no turn survives it) have no TurnOpenSource member.
type TurnOpenSourceName string

// TurnOpenNamePrompt and the following constants are the valid TurnOpenSourceName
// values.
const (
	TurnOpenNamePrompt        TurnOpenSourceName = "prompt"
	TurnOpenNameLocalShell    TurnOpenSourceName = "local_shell"
	TurnOpenNameWireTurnStart TurnOpenSourceName = "wire_turn_start"
	TurnOpenNameEmptyRetry    TurnOpenSourceName = "empty_retry"
	TurnOpenNameEvent         TurnOpenSourceName = "event"
	TurnOpenNameRevert        TurnOpenSourceName = "revert"
	TurnOpenNameWorkflowStep  TurnOpenSourceName = "workflow_step"
)

// Name is s spelled for the wire. Total over the declared TurnOpenSource members;
// an out-of-range value answers "" rather than a name it does not have.
func (s TurnOpenSource) Name() TurnOpenSourceName {
	switch s {
	case TurnSourcePrompt:
		return TurnOpenNamePrompt
	case TurnSourceLocalShell:
		return TurnOpenNameLocalShell
	case TurnSourceWireTurnStart:
		return TurnOpenNameWireTurnStart
	case TurnSourceEmptyRetry:
		return TurnOpenNameEmptyRetry
	case turnSourceWorkflowStep:
		return TurnOpenNameWorkflowStep
	}
	return ""
}

// EntryPrompt is the prompt a turn_open carries when the reader opened the turn; ID is
// the client-minted message id. Resends names the steer rows (their dock keys) whose words
// this prompt carries; the record owns it and the projection never synthesises it. Label is
// the PromptCommand.DisplayText the prompt was sent with, which KAS's replay gives as the text.
type EntryPrompt struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Label       string       `json:"label,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Resends     []string     `json:"resends,omitempty"`
}

// EntryTurnOpen is the turn_open payload. Prompt is present for a prompt,
// local_shell or empty_retry turn and absent otherwise. Run, NodePath and
// SessionID are set on a run log's turns only.
type EntryTurnOpen struct {
	Prompt    *EntryPrompt       `json:"prompt,omitempty"`
	Source    TurnOpenSourceName `json:"source"`
	Run       string             `json:"run,omitempty"`
	NodePath  string             `json:"node_path,omitempty"`
	SessionID string             `json:"session_id,omitempty"`
	// N is the transcript ordinal the appender assigns at open, from 1.
	N uint64 `json:"n"`
}

// EntryTurnBind is the turn_bind payload: the id KAS holds the prompt under and
// the session it went out on.
type EntryTurnBind struct {
	KASMessageID string `json:"kas_message_id"`
	SessionID    string `json:"session_id"`
}

// RewindTarget is what a rewind cuts at and what it tells KAS to revert to: the
// turn the addressed prompt opened, and that turn's turn_bind id, "" when no bind
// arrived.
type RewindTarget struct {
	Turn         string
	KASMessageID string
	// LaunchedRuns are the workflow ids the entries inside the cut launched: the
	// runs whose launching tool call the rewind un-says.
	LaunchedRuns []string
}

// PromptReceipt is what a chat's log holds of one prompt id: whether any turn opened under it,
// whether KAS bound any of those turns (on any session), and the words and label it went out with.
type PromptReceipt struct {
	Text   string
	Label  string
	Opened bool
	Bound  bool
}

// EntryText is the text payload.
type EntryText struct {
	Text string `json:"text"`
}

// EntryThinking is the thinking payload.
type EntryThinking struct {
	Text string `json:"text"`
}

// EntryToolCall is the tool_call payload: the call as created. Status holds the
// initial value only; the settled value is the tool_result's. AgentSubtaskID is
// the delegate uuid on a subagent invocation and the merge's pairing key for it.
type EntryToolCall struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Kind           ToolKind        `json:"kind"`
	Status         ToolStatus      `json:"status"`
	Output         string          `json:"output,omitempty"`
	AgentSubtaskID string          `json:"agent_subtask_id,omitempty"`
	WorkflowID     string          `json:"workflow_id,omitempty"`
	TerminalID     string          `json:"terminal_id,omitempty"`
	SourcePath     string          `json:"source_path,omitempty"`
	Checkpoint     *ToolCheckpoint `json:"checkpoint,omitempty"`
	Disclosed      *ToolDisclosed  `json:"disclosed,omitempty"`
	Denial         *ToolDenial     `json:"denial,omitempty"`
	Truncated      *ToolTruncation `json:"truncated,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Locations      []ToolLocation  `json:"locations,omitempty"`
	Diffs          []ToolDiff      `json:"diffs,omitempty"`
	OutputSpans    []TextSpan      `json:"output_spans,omitempty"`
	Ts             int64           `json:"ts"`
	DurationMs     int             `json:"duration_ms,omitempty"`
	OutputBytes    int             `json:"output_bytes,omitempty"`
	HasFull        bool            `json:"has_full,omitempty"`
	Declined       bool            `json:"declined,omitempty"`
}

// EntryToolResult is the tool_result payload: the settled value of every field a
// tool_call_update deltas. Output and Diffs are whole, never a delta.
type EntryToolResult struct {
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	Disclosed  *ToolDisclosed  `json:"disclosed,omitempty"`
	Denial     *ToolDenial     `json:"denial,omitempty"`
	Offload    *ToolOffload    `json:"offload,omitempty"`
	// Interaction is how the call's approval or question was answered.
	Interaction *ToolInteraction `json:"interaction,omitempty"`
	// Truncated is what the STORE dropped to bound this result on disk, nil when it
	// fit; OutputBytes and HasFull mark the served PREVIEW, as on EntryToolCall.
	Truncated *ToolTruncation `json:"truncated,omitempty"`
	Title     string          `json:"title,omitempty"`
	Kind      ToolKind        `json:"kind,omitempty"`
	// Status is a closed enum because every value is marotte's own (aborted, or a
	// normalised terminal status). The client validates it with reqOneOf, so a value
	// copied verbatim off the wire would fail the whole window.
	Status      ToolStatus     `json:"status"`
	Output      string         `json:"output,omitempty"`
	TerminalID  string         `json:"terminal_id,omitempty"`
	WorkflowID  string         `json:"workflow_id,omitempty"`
	OutputSpans []TextSpan     `json:"output_spans,omitempty"`
	Diffs       []ToolDiff     `json:"diffs,omitempty"`
	Locations   []ToolLocation `json:"locations,omitempty"`
	DurationMs  int            `json:"duration_ms,omitempty"`
	OutputBytes int            `json:"output_bytes,omitempty"`
	HasFull     bool           `json:"has_full,omitempty"`
	Declined    bool           `json:"declined,omitempty"`
}

// SteerReason is why a steer was never read. A closed enum because the client's
// "Not read · <clause>" wording table must be total; a reason with no clause reads as fine.
type SteerReason string

const (
	// SteerReasonRestart is the merge's reason on a projected steer the record never
	// held: KAS persists a steer's text at send, its pending state dies with the process.
	SteerReasonRestart SteerReason = "restart"
	// SteerReasonBoundary is a steer whose turn ended first: a user steer still unread when its
	// chat's tab closed (the close cancels the turn and no prompt carries the steer), or an agent
	// note KAS's turn-end clear named.
	SteerReasonBoundary SteerReason = "boundary"
	// SteerReasonDeleted is the reason on a steer the reader deleted from the
	// dock before the agent read it.
	SteerReasonDeleted SteerReason = "deleted"
)

// EntrySteer is the steer payload. Text has the `[notification/<sev>]` prefix stripped;
// Severity (KAS's vocabulary) is set on a note KAS's buffer carried. OriginRun is the run a note
// came from. ProducedTs is when the words were sent (a workflow message, a finished run's end) and
// ReadTs when its receiver took a workflow message up; both label and never order. A log read
// fills ReadTs (or the dropped state) from the message's steer_delivered. Step names the run step on
// the other side of a workflow message, in the launching chat's log only; StepPath is that step's
// node path on both sides' rows, and Chat the launching chat on the step's row. Resends names the
// steer rows (dock keys) this one re-sends, taken from the steer record at the read.
type EntrySteer struct {
	Text       string      `json:"text"`
	Origin     SteerOrigin `json:"origin"`
	State      SteerState  `json:"state"`
	Reason     SteerReason `json:"reason,omitempty"`
	Severity   string      `json:"severity,omitempty"`
	OriginRun  string      `json:"origin_run,omitempty"`
	Step       string      `json:"step,omitempty"`
	StepPath   string      `json:"step_path,omitempty"`
	Chat       ChatID      `json:"chat,omitempty"`
	Resends    []string    `json:"resends,omitempty"`
	ProducedTs int64       `json:"produced_ts,omitempty"`
	ReadTs     int64       `json:"read_ts,omitempty"`
}

// EntrySteerAck is the steer_ack payload: KAS's acknowledgement of a steer,
// stripped from the chunk it rode.
type EntrySteerAck struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
}

// EntrySteerDelivered is the steer_delivered payload: the receiver took up the workflow message
// SteerID names, recorded at send, at ReadTs; Dropped instead says KAS cleared it unread at a turn
// boundary. It renders on that message's row, not where it sits. KASID is the id KAS persisted the
// message under, which its replay row carries.
type EntrySteerDelivered struct {
	SteerID string `json:"steer_id"`
	KASID   string `json:"kas_id,omitempty"`
	ReadTs  int64  `json:"read_ts"`
	Dropped bool   `json:"dropped,omitempty"`
}

// SteerDeliveredID is the steer_delivered entry id for a steer: `<steer>:delivered`.
func SteerDeliveredID(steerID string) string {
	return steerID + ":delivered"
}

// EntryPlan is the plan payload, one per distinct plan state.
type EntryPlan struct {
	Entries []PlanEntry `json:"entries"`
}

// EntryCompaction is the compaction payload.
type EntryCompaction struct {
	Summary string `json:"summary"`
}

// EntryCompactionFailed is the compaction_failed payload.
type EntryCompactionFailed struct {
	Reason string `json:"reason"`
}

// EntrySafetyBlocked is the safety_blocked payload: the safety properties an
// enforce-mode block violated.
type EntrySafetyBlocked struct {
	Properties []string `json:"properties"`
}

// EntryModelSwitched is the model_switched payload. From == To means the model did
// not move and only the reasoning effort did, so the renderer needs no second entry
// kind and no trigger field to tell the two apart.
type EntryModelSwitched struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Effort is the reasoning tier in force; a plain string, as Chat.Effort, so a
	// level another build wrote decodes rather than throws.
	Effort string `json:"effort,omitempty"`
	// Reason is set only on a switch KAS made by itself.
	Reason ModelSwitchReason `json:"reason,omitempty"`
}

// EntryModelRouted is the model_routed payload: KAS's Auto routing moved the chat's requests up a
// model category inside a turn. It names no model, because KAS keeps reporting Auto; Message is
// KAS's own notice text, treated at the decode door.
type EntryModelRouted struct {
	Message string `json:"message"`
}

// ModelSwitchReason says why KAS moved a chat off its model. A closed enum
// because the client words it, so the wording table must be total.
type ModelSwitchReason string

// ModelSwitchReasonUnavailable is KAS repinning a model the account cannot
// use, seen as an unsolicited config_option_update moving the chat's model.
const ModelSwitchReasonUnavailable ModelSwitchReason = "unavailable"

// ModeSwitchSource says who applied a mode switch; a closed enum because the renderer
// branches on it.
type ModeSwitchSource string

// The two sources. Each string is the wire value AND the client's
// ModeSwitchSource union member, so a rename here is a cross-language change.
const (
	// ModeSwitchSourceUser is a switch the reader asked for, from set_mode.
	ModeSwitchSourceUser ModeSwitchSource = "user"
	// ModeSwitchSourceAgent is a switch KAS applied on its own (current_mode_update):
	// the Plan-to-Execute flip at turn end, or an approved switch_mode tool call.
	ModeSwitchSourceAgent ModeSwitchSource = "agent"
)

// EntryModeSwitched is the mode_switched payload. From and To are mode ids, so a mode the
// catalog no longer offers still renders; From is empty when no mode was recorded.
type EntryModeSwitched struct {
	From   string           `json:"from"`
	To     string           `json:"to"`
	Source ModeSwitchSource `json:"source"`
}

// EntryTurnRevert is the turn_revert payload, the record that makes a reverted range
// unreadable rather than absent. From names the reverted turn and FromN its turn_open.n,
// so a partial window can place the cut. KASMessageID ties it to KAS's checkpoint_revert.
type EntryTurnRevert struct {
	From string `json:"from"`
	// Through is the NEWEST turn when the revert landed: the upper bound STATED, never
	// implied by this entry's offset. groupByTurn relocates this carrier-owned record
	// above the reverted turns, so an offset bound would un-revert them on the next open.
	Through      string          `json:"through"`
	KASMessageID string          `json:"kas_message_id"`
	Cause        TurnRevertCause `json:"cause"`
	FromN        uint64          `json:"from_n"`
}

// TurnRevertCause is why a range was reverted. The client's wording is total over the
// type, so a second member cannot compile without wording of its own.
type TurnRevertCause string

// TurnRevertCauseRewind is the only cause: a rewind the reader asked for.
const TurnRevertCauseRewind TurnRevertCause = "rewind"

// EntryReconciled records that a merge looked and had nothing to add, the only fact that
// stops a reconcile signal. EXACTLY ONE field is set: Turn for a per-turn signal (a
// synthesized closer, an empty steer), Session for "this record adopted that history".
type EntryReconciled struct {
	Turn    string `json:"turn,omitempty"`
	Session string `json:"session,omitempty"`
}

// EntryTurnClose is the turn_close payload: TurnConclusion's persisted fields plus the
// turn's footer. StopReasonRaw is a plain string because KAS adds stop reasons (`tool_use`)
// and a closed enum would make such a chat undecodable; Outcome is derived, so closed.
type EntryTurnClose struct {
	ChangedFiles map[string]*FileChange `json:"changed_files,omitempty"`
	Refusal      *RefusalInfo           `json:"refusal,omitempty"`
	// ContextBreakdown is KAS's measure of the turn's last model request.
	ContextBreakdown *ContextBreakdown `json:"context_breakdown,omitempty"`
	Outcome          TurnOutcome       `json:"outcome"`
	StopReasonRaw    string            `json:"stop_reason_raw,omitempty"`
	FailureReason    string            `json:"failure_reason,omitempty"`
	// FailureKind classifies a failed turn the client offers a remedy for.
	FailureKind    FailureKind     `json:"failure_kind,omitempty"`
	Model          string          `json:"model,omitempty"`
	CodeReferences []CodeReference `json:"code_references,omitempty"`
	// EngineErrorClass is the engine's error class for a broken turn, set only
	// for a class rpcerr.KnownClass names: an unmapped error's class is minified.
	EngineErrorClass string `json:"engine_error_class,omitempty"`
	// Throughput is KAS's streaming estimate for the turn's model output.
	Throughput *TurnThroughput `json:"throughput,omitempty"`
	// RequestIDs are the backend request ids of the turn's model calls.
	RequestIDs []string `json:"request_ids,omitempty"`
	// Recoveries are KAS's names for the turn's recoveries; an open vocabulary.
	Recoveries []string `json:"recoveries,omitempty"`
	// Steering is the ids of the steering KAS added during the turn, first-seen order.
	Steering  []string `json:"steering,omitempty"`
	Credits   float64  `json:"credits,omitempty"`
	ElapsedMs float64  `json:"elapsed_ms,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	// Carrier marks the close of a turn the log minted to hold a record, which no agent ran. A turn's
	// source cannot say so: a replayed `event` turn can be one KAS ran.
	Carrier bool `json:"carrier,omitempty"`
}

// ContextBreakdown is what one model request carried, in characters as KAS measures them (the
// turn_completion contextBreakdown, kiro-cli 2.28). Categories are ordered by size; Key is KAS's
// category name, an open vocabulary.
type ContextBreakdown struct {
	Media      *ContextMedia     `json:"media,omitempty"`
	Categories []ContextCategory `json:"categories"`
	TotalChars int64             `json:"total_chars"`
	ModelCalls int               `json:"model_calls"`
	Compacted  bool              `json:"compacted,omitempty"`
}

// ContextCategory is one category's share. Parts are its named sub-totals (history's user and
// assistant text); Items are KAS's largest members, the rest counted in Omitted*.
type ContextCategory struct {
	Key          string        `json:"key"`
	Parts        []ContextPart `json:"parts,omitempty"`
	Items        []ContextItem `json:"items,omitempty"`
	Chars        int64         `json:"chars"`
	Percent      float64       `json:"percent"`
	OmittedChars int64         `json:"omitted_chars,omitempty"`
	Count        int           `json:"count,omitempty"`
	OmittedCount int           `json:"omitted_count,omitempty"`
}

// ContextItem is one member of a category: a steering document, a tool, an MCP server, an
// attached file. URI is set only for a file: link; Inclusion is KAS's open vocabulary (always,
// fileMatch, auto, …).
type ContextItem struct {
	Name      string  `json:"name"`
	URI       string  `json:"uri,omitempty"`
	Inclusion string  `json:"inclusion,omitempty"`
	Chars     int64   `json:"chars"`
	Percent   float64 `json:"percent"`
	Count     int     `json:"count,omitempty"`
}

// ContextPart is one named sub-total of a category.
type ContextPart struct {
	Key   string `json:"key"`
	Chars int64  `json:"chars"`
}

// ContextMedia counts the images and documents a request attached.
type ContextMedia struct {
	ImageBytes    int64 `json:"image_bytes"`
	DocumentBytes int64 `json:"document_bytes"`
	Images        int   `json:"images"`
	Documents     int   `json:"documents"`
}

// TurnThroughput is KAS's estimate of a turn's streamed model output: tokens
// estimated from characters, over the time chunks were actually arriving.
type TurnThroughput struct {
	EstimatedTokens   int64   `json:"estimated_tokens"`
	ActiveStreamingMs float64 `json:"active_streaming_ms"`
}

// FailureKind names a turn failure with a reader-facing remedy. A registered
// wire enum, so the client's branch over it is total.
type FailureKind string

// FailureKindContextLimit is a turn that overflowed the model's context window:
// the remedy is compacting the context and sending the prompt again.
const FailureKindContextLimit FailureKind = "context_limit"

// FailureKindModelCallLimit is a turn kiro-cli stopped at its per-turn model-call
// limit: the work may be unfinished, and the remedy is running it again or splitting it.
const FailureKindModelCallLimit FailureKind = "model_call_limit"

// SaySegmentID is the id of the k-th segment of a say a seal split: `<say>#k`.
func SaySegmentID(say string, k int) string {
	return say + "#" + strconv.Itoa(k)
}

// SayIDOf is the say a text entry id belongs to: entryID with a `#k` segment
// suffix removed, or entryID itself when it carries none.
func SayIDOf(entryID string) string {
	i := strings.LastIndexByte(entryID, '#')
	if i < 0 || i == len(entryID)-1 {
		return entryID
	}
	for _, c := range entryID[i+1:] {
		if c < '0' || c > '9' {
			return entryID
		}
	}
	return entryID[:i]
}

// ToolResultID is the tool_result entry id for a tool_call entry: `<call>:result`.
func ToolResultID(callID string) string {
	return callID + ":result"
}

// SteerAckID is the steer_ack entry id for a steer: `<steer>:ack`.
func SteerAckID(steerID string) string {
	return steerID + ":ack"
}

// CompactionEntryID derives a compaction's id from its summary so the live handler and the
// replay projection agree. An empty summary is `compaction-empty-<emptyOrdinal>`, counting
// the log's empty-summary compactions from 1.
func CompactionEntryID(summary []byte, emptyOrdinal int) string {
	if len(summary) == 0 {
		return "compaction-empty-" + strconv.Itoa(emptyOrdinal)
	}
	sum := sha256.Sum256(summary)
	return "compaction-" + hex.EncodeToString(sum[:8])
}

// EntryToolCallOf is the tool_call payload for a call as the wire created it: the card's
// fields minus Offload and Interaction, which only a result carries.
func EntryToolCallOf(tc *ToolCall) EntryToolCall {
	return EntryToolCall{
		ID:             tc.ID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Output:         tc.Output,
		AgentSubtaskID: tc.AgentSubtaskID,
		WorkflowID:     tc.WorkflowID,
		TerminalID:     tc.TerminalID,
		SourcePath:     tc.SourcePath,
		Checkpoint:     tc.Checkpoint,
		Disclosed:      tc.Disclosed,
		Denial:         tc.Denial,
		Truncated:      tc.Truncated,
		Input:          tc.Input,
		Locations:      tc.Locations,
		Diffs:          tc.Diffs,
		OutputSpans:    tc.OutputSpans,
		Ts:             tc.Ts,
		DurationMs:     tc.DurationMs,
		OutputBytes:    tc.OutputBytes,
		HasFull:        tc.HasFull,
		Declined:       tc.Declined,
	}
}

// ToolCallOfEntry is EntryToolCallOf's inverse, the card a tool_call payload
// describes before any result folded into it.
func ToolCallOfEntry(e *EntryToolCall) ToolCall {
	return ToolCall{
		ID:             e.ID,
		Title:          e.Title,
		Kind:           e.Kind,
		Status:         e.Status,
		Output:         e.Output,
		AgentSubtaskID: e.AgentSubtaskID,
		WorkflowID:     e.WorkflowID,
		TerminalID:     e.TerminalID,
		SourcePath:     e.SourcePath,
		Checkpoint:     e.Checkpoint,
		Disclosed:      e.Disclosed,
		Denial:         e.Denial,
		Truncated:      e.Truncated,
		Input:          e.Input,
		Locations:      e.Locations,
		Diffs:          e.Diffs,
		OutputSpans:    e.OutputSpans,
		Ts:             e.Ts,
		DurationMs:     e.DurationMs,
		OutputBytes:    e.OutputBytes,
		HasFull:        e.HasFull,
		Declined:       e.Declined,
	}
}

// EntryToolResultOf is the tool_result payload carrying a call's settled value:
// every field the progress frames folded, as it stands at the settle.
func EntryToolResultOf(tc *ToolCall) EntryToolResult {
	return EntryToolResult{
		Checkpoint:  tc.Checkpoint,
		Disclosed:   tc.Disclosed,
		Denial:      tc.Denial,
		Offload:     tc.Offload,
		Interaction: tc.Interaction,
		Truncated:   tc.Truncated,
		Title:       tc.Title,
		Kind:        tc.Kind,
		Status:      tc.Status,
		Output:      tc.Output,
		TerminalID:  tc.TerminalID,
		WorkflowID:  tc.WorkflowID,
		OutputSpans: tc.OutputSpans,
		Diffs:       tc.Diffs,
		Locations:   tc.Locations,
		DurationMs:  tc.DurationMs,
		OutputBytes: tc.OutputBytes,
		HasFull:     tc.HasFull,
		Declined:    tc.Declined,
	}
}
