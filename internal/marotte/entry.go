package marotte

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// EntryKind is the discriminator of an Entry's payload: the seventeen kinds a turn
// log holds. The chat log and the run log share the vocabulary.
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
	EntryKindPlan             EntryKind = "plan"
	EntryKindCompaction       EntryKind = "compaction"
	EntryKindCompactionFailed EntryKind = "compaction_failed"
	EntryKindSafetyBlocked    EntryKind = "safety_blocked"
	EntryKindModelSwitched    EntryKind = "model_switched"
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
	// Lane is "" for the agent that owns the log's turn (the chat's agent, or
	// the step's own agent in a run log), or a delegate's uuid. There is no
	// other lane value.
	Lane string    `json:"lane,omitempty"`
	Kind EntryKind `json:"kind"`
	// Payload is one of the seventeen payload types, chosen by Kind.
	Payload json.RawMessage `json:"payload"`
	// Seq is per turn, contiguous from 0 (turn_open), assigned at append.
	Seq uint64 `json:"seq"`
	// Ts is the appender's wall clock at append: metadata, never compared.
	Ts int64 `json:"ts"`
}

// OpenEntry is a text or thinking entry still coalescing deltas. It has no Seq:
// a position is assigned when it seals and becomes an Entry. It lives in the
// appender's memory and travels as entry_opened and as a GET's open_entries; it
// never reaches the log.
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

// TurnOpenSourceName is the wire spelling of what opened a turn. The five
// TurnOpenSource members map onto it through Name; `event` and `revert` are the
// two names with no int member behind them — one for a turn an event row opens,
// one for the headerless carrier a revert opens when no turn survives it — and
// `workflow_step` is set on a run log's turns.
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
	case TurnSourceWorkflowStep:
		return TurnOpenNameWorkflowStep
	}
	return ""
}

// EntryPrompt is the prompt a turn_open carries when the reader opened the turn:
// the client-minted message id, the text and the files staged beside it. Resends
// names the steer rows (their dock keys) whose words this prompt carries; absent
// means not a resend. The record owns it and the projection never synthesises it.
type EntryPrompt struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
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
	// Truncated is what the STORE dropped to bound this result on disk, nil on
	// every result that fit; OutputBytes and HasFull are the served PREVIEW's
	// markers, as on EntryToolCall. The three sit on the result as well as on the
	// call because the result is where the settled output and diffs live.
	Truncated *ToolTruncation `json:"truncated,omitempty"`
	Title     string          `json:"title,omitempty"`
	Kind      ToolKind        `json:"kind,omitempty"`
	// Status keeps its closed enum ONLY because every value is marotte's own: the
	// close rule mints aborted, and a result is otherwise written solely from a
	// TERMINAL upstream status the projection and the live path normalise. The
	// client decoder validates it with reqOneOf, so a value copied verbatim off the
	// wire (a status-less or unknown replayed update) would fail the whole window.
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

// SteerReason is why a steer was never read. An enum rather than a free string
// because the client WORDS it — the note reads "Not read · <clause>" — so the
// wording table has to be total over the vocabulary: a reason with no clause
// renders the bare state, which reads as if nothing had gone wrong.
type SteerReason string

const (
	// SteerReasonRestart is the merge's reason on a projected steer the record
	// never held, because KAS persists a steer's text at send time while its
	// pending state dies with the process.
	SteerReasonRestart SteerReason = "restart"
	// SteerReasonBoundary is the reason on a steer whose turn ended first: a user
	// steer still unread when its chat's tab closed (the close cancels the turn and
	// no prompt carries the steer), or an agent note KAS's turn-end clear named.
	SteerReasonBoundary SteerReason = "boundary"
	// SteerReasonDeleted is the reason on a steer the reader deleted from the
	// dock before the agent read it.
	SteerReasonDeleted SteerReason = "deleted"
)

// EntrySteer is the steer payload. Text is the bare text, the `[notification/<sev>]`
// prefix stripped. Severity is KAS's vocabulary (`info`, `success`, `warning`, `error`),
// present on a note (a KAS notification or marotte's run note), never on a user steer.
// State is absent on a replay-projected steer no merge stamped. OriginRun and ProducedTs
// name the run that left a note and when it finished, so a late read can say so; absent
// on a user steer and a step's mid-run note. ProducedTs labels; it never orders. Resends
// names the steer rows (dock keys) this one re-sends under its own id, taken from the
// steer record at the read; the projection never synthesises it, so the merge keeps it.
type EntrySteer struct {
	Text       string      `json:"text"`
	Origin     SteerOrigin `json:"origin"`
	State      SteerState  `json:"state,omitempty"`
	Reason     SteerReason `json:"reason,omitempty"`
	Severity   string      `json:"severity,omitempty"`
	OriginRun  string      `json:"origin_run,omitempty"`
	Resends    []string    `json:"resends,omitempty"`
	ProducedTs int64       `json:"produced_ts,omitempty"`
}

// EntrySteerAck is the steer_ack payload: KAS's acknowledgement of a steer,
// stripped from the chunk it rode.
type EntrySteerAck struct {
	SteerID string `json:"steer_id"`
	Text    string `json:"text"`
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
	// Effort is the reasoning tier in force, omitempty so every persisted entry
	// written before this field existed still decodes. A plain string rather than
	// EffortLevel for Chat.Effort's reason: a level another build wrote must decode
	// rather than throw, which a closed wire enum would not.
	Effort string `json:"effort,omitempty"`
	// Reason is set only on a switch KAS made by itself; absent on the reader's
	// own picks.
	Reason ModelSwitchReason `json:"reason,omitempty"`
}

// ModelSwitchReason says why KAS moved a chat off its model. A closed enum
// because the client words it, so the wording table must be total.
type ModelSwitchReason string

// ModelSwitchReasonUnavailable is KAS repinning a model the account cannot
// use, seen as an unsolicited config_option_update moving the chat's model.
const ModelSwitchReasonUnavailable ModelSwitchReason = "unavailable"

// ModeSwitchSource says who applied a mode switch. The renderer BRANCHES on it —
// an agent-initiated switch is labelled as the agent's — so it is a closed enum
// rather than a plain string.
type ModeSwitchSource string

// The two sources. Each string is the wire value AND the client's
// ModeSwitchSource union member, so a rename here is a cross-language change.
const (
	// ModeSwitchSourceUser is a switch the reader asked for, from set_mode.
	ModeSwitchSourceUser ModeSwitchSource = "user"
	// ModeSwitchSourceAgent is a switch KAS applied on its own, arriving as
	// current_mode_update: the Plan-to-Execute flip at turn end, or a mode the
	// agent switched to through an approved switch_mode tool call.
	ModeSwitchSourceAgent ModeSwitchSource = "agent"
)

// EntryModeSwitched is the mode_switched payload. From and To are mode ids, not
// display names: the catalog resolves the name, and a mode the catalog no longer
// offers still renders as its id rather than as nothing. From is empty for a chat
// whose mode was never recorded.
type EntryModeSwitched struct {
	From   string           `json:"from"`
	To     string           `json:"to"`
	Source ModeSwitchSource `json:"source"`
}

// EntryTurnRevert is the turn_revert payload: the record that a rewind happened,
// which is what makes the reverted range unreadable rather than absent. From names
// the reverted turn — the addressed prompt's turn — and FromN its turn_open.n, so a
// client holding a partial window can place the cut without the carrier's position.
// KASMessageID is the id the revert was issued against, which is what makes this
// record and KAS's own checkpoint_revert tombstone one fact stated twice.
type EntryTurnRevert struct {
	From string `json:"from"`
	// Through is the id of the NEWEST turn in the log when the revert landed: the
	// window's upper bound STATED, never implied by this entry's own file offset.
	// The merge's rewrite regroups entries by turn (groupByTurn), and this record is
	// lane-less and belongs to the CARRIER, whose group sits BELOW the turns the
	// revert took — so a rewrite relocates the record above them, and a window
	// bounded by its offset would un-revert the range on the next open. A stated
	// bound cannot move.
	Through      string          `json:"through"`
	KASMessageID string          `json:"kas_message_id"`
	Cause        TurnRevertCause `json:"cause"`
	FromN        uint64          `json:"from_n"`
}

// TurnRevertCause is why a range was reverted. ONE member, and the client's wording
// is total over the TYPE rather than over a string, so a second member cannot compile
// without wording of its own.
type TurnRevertCause string

// TurnRevertCauseRewind is the only cause: a rewind the reader asked for.
const TurnRevertCauseRewind TurnRevertCause = "rewind"

// EntryReconciled records that a merge has looked at something and had nothing to add,
// which is the only fact that can stop a reconcile signal honestly. EXACTLY ONE field is
// set: Turn for the per-turn signals (a synthesized closer, an empty steer) and Session
// for the per-session one, because the fact it clears is "this record has adopted that
// session's history". One kind rather than two, because the reader is the same map build
// in the same scan arm.
type EntryReconciled struct {
	Turn    string `json:"turn,omitempty"`
	Session string `json:"session,omitempty"`
}

// EntryTurnClose is the turn_close payload: TurnConclusion's three persisted
// fields plus the turn's aggregate footer. StopReasonRaw is whatever the upstream
// said, a plain string on the wire: KAS may add a stop reason at any time (measured:
// `tool_use`), and a closed enum here made every chat holding one undecodable.
// Outcome is the closed enum, because ConcludeStopReason derives it.
type EntryTurnClose struct {
	ChangedFiles  map[string]*FileChange `json:"changed_files,omitempty"`
	Refusal       *RefusalInfo           `json:"refusal,omitempty"`
	Outcome       TurnOutcome            `json:"outcome"`
	StopReasonRaw string                 `json:"stop_reason_raw,omitempty"`
	FailureReason string                 `json:"failure_reason,omitempty"`
	// FailureKind classifies a failed turn the client offers a remedy for.
	// Empty for every other ending.
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
	// Recoveries are KAS's wire names for the recoveries the turn needed
	// (`empty`, `streamError`, `authExpiry`, …); an open vocabulary.
	Recoveries []string `json:"recoveries,omitempty"`
	// Steering is the ids (file URIs for documents on disk) of the steering KAS
	// added to the context during the turn, first-seen order.
	Steering  []string `json:"steering,omitempty"`
	Credits   float64  `json:"credits,omitempty"`
	ElapsedMs float64  `json:"elapsed_ms,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	// Carrier marks the close of a turn the log minted to hold a record, which no agent ran. A turn's
	// source cannot say so: a replayed `event` turn can be one KAS ran.
	Carrier bool `json:"carrier,omitempty"`
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

// CompactionEntryID is a compaction entry's id, derived from the summary bytes so
// the live handler and the replay projection mint one id for one compaction:
// `compaction-` plus the first 16 hex digits of SHA-256 over summary, or
// `compaction-empty-<emptyOrdinal>` for an empty summary, emptyOrdinal counting
// the log's empty-summary compactions from 1.
func CompactionEntryID(summary []byte, emptyOrdinal int) string {
	if len(summary) == 0 {
		return "compaction-empty-" + strconv.Itoa(emptyOrdinal)
	}
	sum := sha256.Sum256(summary)
	return "compaction-" + hex.EncodeToString(sum[:8])
}

// EntryToolCallOf is the tool_call payload for a call as the wire created it: the
// same fields as the card type minus the v2 sub-session attribution.
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
