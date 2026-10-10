package marotte

import (
	"encoding/json"
	"slices"
	"strings"
)

// ToolKind identifies the category of a tool invocation, assigned by kiro-cli and
// flowing through ACP unchanged.
type ToolKind string

// ToolKindExecute and the following constants define the valid ToolKind values. KAS
// emits only read/edit/delete/move/search/execute/think/fetch/switch_mode/other; the rest
// back WorkingLabelForKind's table. ToolKindHook is marotte's own `Hook fired` card.
const (
	ToolKindExecute    ToolKind = "execute"
	ToolKindShell      ToolKind = "shell"
	ToolKindRead       ToolKind = "read"
	ToolKindSearch     ToolKind = "search"
	ToolKindFetch      ToolKind = "fetch"
	ToolKindEdit       ToolKind = "edit"
	ToolKindThink      ToolKind = "think"
	ToolKindHook       ToolKind = "hook"
	ToolKindWrite      ToolKind = "write"
	ToolKindDelete     ToolKind = "delete"
	ToolKindMove       ToolKind = "move"
	ToolKindCommand    ToolKind = "command"
	ToolKindBrowser    ToolKind = "browser"
	ToolKindSwitchMode ToolKind = "switch_mode"
	ToolKindMCP        ToolKind = "mcp"
	ToolKindOther      ToolKind = "other"
)

// ToolStatus is the lifecycle state of a tool invocation.
type ToolStatus string

// ToolPending and the following constants define the ToolStatus lifecycle states for a tool invocation.
const (
	ToolPending    ToolStatus = "pending"
	ToolInProgress ToolStatus = "in_progress"
	ToolCompleted  ToolStatus = "completed"
	ToolFailed     ToolStatus = "failed"
	// ToolAborted is a call the reader's stop ended or its closed turn left unsettled;
	// `failed` means the tool ran and errored. marotte mints it; it never arrives on the wire.
	ToolAborted ToolStatus = "aborted"
)

// Terminal reports whether nothing can still change s.
func (s ToolStatus) Terminal() bool {
	return s == ToolCompleted || s == ToolFailed || s == ToolAborted
}

// ACPUpdateKind identifies the subtype of an ACP session/update notification.
type ACPUpdateKind string

// ACPUpdateAgentChunk and the following constants define the valid ACPUpdateKind values for ACP session notifications.
const (
	ACPUpdateAgentChunk   ACPUpdateKind = "agent_message_chunk"
	ACPUpdateThoughtChunk ACPUpdateKind = "agent_thought_chunk"
	ACPUpdateToolCall     ACPUpdateKind = "tool_call"
	ACPUpdateToolUpdate   ACPUpdateKind = "tool_call_update"
	ACPUpdatePlan         ACPUpdateKind = "plan"
	ACPUpdateModeChange   ACPUpdateKind = "current_mode_update"
	// ACPUpdateSessionInfo and the three below are v3 (KAS) sub-kinds.
	ACPUpdateSessionInfo ACPUpdateKind = "session_info_update"
	// ACPUpdateAvailableCommands carries KAS's slash-command catalog.
	ACPUpdateAvailableCommands ACPUpdateKind = "available_commands_update"
	// ACPUpdateConfigOption carries the live model/mode/effort catalog;
	// ACPUpdateUsage carries context-window usage. Both v3-only.
	ACPUpdateConfigOption ACPUpdateKind = "config_option_update"
	ACPUpdateUsage        ACPUpdateKind = "usage_update"
)

// ToolCall is a tool invocation inside an assistant message. Each can be updated
// in place as status changes (pending → in_progress → completed/failed/aborted).
type ToolCall struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Kind   ToolKind   `json:"kind"`
	Status ToolStatus `json:"status"`
	Output string     `json:"output,omitempty"`
	// AgentSubtaskID is _meta.kiro.agentSubtaskId on a subagent's tool_call; it links
	// the card to its nested deltas, which carry the same id.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// WorkflowID names the run a `run_workflow` invocation started (rawOutput.workflowId).
	// The client keys the run card on it, and a step's blocks carry the same id in
	// `agent_subtask_id`.
	WorkflowID string `json:"workflow_id,omitempty"`
	// TerminalID links an execute call to its agent terminal (the ACP type:"terminal"
	// block), making the card the terminal's rendering surface.
	TerminalID string `json:"terminal_id,omitempty"`
	// SourcePath is the workspace-relative file that DEFINES the call (a hook card's
	// hook file), which a click opens. Not in Locations, whose readers treat a path as
	// one the call touched.
	SourcePath string `json:"source_path,omitempty"`
	// Checkpoint is KAS's snapshot mapping (_meta.kiro.checkpoint) for a call that
	// wrote a file. Pointers sit ahead of the slices for govet fieldalignment.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	// Disclosed names the skill or steering doc a `disclose_context` call loaded
	// (_meta.kiro.disclosedContext), the only signal that its body reached the model.
	Disclosed *ToolDisclosed `json:"disclosed,omitempty"`
	// Denial is KAS's reason for a call the Cedar policy refused (_meta.kiro.policyDenial),
	// so a refusal reads as one rather than as a tool failure.
	Denial *ToolDenial `json:"denial,omitempty"`
	// Offload is set once KAS offloaded the full output to a file; one-way.
	Offload *ToolOffload `json:"offload,omitempty"`
	// Interaction is how the call's approval or question was answered; one-way.
	Interaction *ToolInteraction `json:"interaction,omitempty"`
	// Truncated is what the STORE dropped to bound this call on disk, nil when it fit.
	Truncated *ToolTruncation `json:"truncated,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Locations []ToolLocation  `json:"locations,omitempty"`
	Diffs     []ToolDiff      `json:"diffs,omitempty"`
	// OutputSpans styles ranges of Output, parsed server-side so Output stays plain
	// text and the client never builds HTML from agent-controlled bytes.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	Ts          int64      `json:"ts"`
	DurationMs  int        `json:"duration_ms,omitempty"`
	// OutputBytes is the PERSISTED output's length, set ONLY alongside HasFull; where
	// the store also cut the call, Truncated carries the size before that cut.
	OutputBytes int `json:"output_bytes,omitempty"`
	// HasFull says Input, Output and Diffs are a PREVIEW of GET
	// /api/chats/{id}/tools/{id}. Set by the transcript read path alone.
	HasFull bool `json:"has_full,omitempty"`
	// Declined says the tool RAN CORRECTLY AND REFUSED, a card outcome distinct from
	// Status. A field rather than a ToolStatus member because the predicates asking
	// whether a call is over (`isToolDone`, the ToolFailed gates) must read it as over.
	// The reason is the tool's own Output.
	Declined bool `json:"declined,omitempty"`
}

// ToolTruncation is what the store DROPPED to bound one tool call on disk, each cut field
// carrying its size BEFORE the cut; a zero field was not cut.
type ToolTruncation struct {
	// OutputBytes and InputBytes are each field's original length.
	OutputBytes int `json:"output_bytes,omitempty"`
	InputBytes  int `json:"input_bytes,omitempty"`
	// DiffBytes and DiffCount are the original totals. Diffs are dropped WHOLE: a cut
	// before/after pair would describe an edit nobody made.
	DiffBytes int `json:"diff_bytes,omitempty"`
	DiffCount int `json:"diff_count,omitempty"`
}

// ToolCallBulk is GET /api/chats/{id}/tools/{toolCallID}: the whole PERSISTED content of
// the three fields a HasFull preview cut.
type ToolCallBulk struct {
	// Field order is govet fieldalignment's and carries no meaning.
	Output      string          `json:"output,omitempty"`
	ID          string          `json:"id"`
	Diffs       []ToolDiff      `json:"diffs,omitempty"`
	OutputSpans []TextSpan      `json:"output_spans,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// TextSpan styles the half-open range [Start,End) of a sibling text field, mirroring
// internal/ansitext.Span. Attrs matches web-terminal-engine's vt.WireRun.A; colour stays
// a palette INDEX so a persisted chat does not bake in today's theme.
type TextSpan struct {
	// Start is the inclusive offset in UTF-16 CODE UNITS, because the consumer indexes
	// JavaScript strings; a byte offset would point mid-character.
	Start int `json:"start"`
	// End is the exclusive offset into the styled text, in UTF-16 code units.
	End int `json:"end"`
	// FG is the foreground colour: -1 default, 0-255 a palette index, or
	// 0x1000000|RGB for 24-bit.
	FG int32 `json:"fg"`
	// BG is the background colour, encoded like FG.
	BG int32 `json:"bg"`
	// Attrs is a bitfield: 1 bold, 2 italic, 4 underline, 8 inverse, 16 strike,
	// 32 dim, 64 hidden, 128 blink, 256 overline, 512 double-underline.
	Attrs uint16 `json:"attrs"`
}

// ToolDisclosed identifies a skill or steering document loaded into context by
// the agent's own `disclose_context` call. Type is "skill" or "steering".
type ToolDisclosed struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	URI         string `json:"uri"`
}

// ToolOffload is where KAS wrote a tool's full output when it was too large for the
// model's context (`_meta.kiro.outputTransformation` "offloaded"); Output is then KAS's
// preview. Path is absolute, under the session's tool-outputs directory.
type ToolOffload struct {
	Path       string `json:"path"`
	TotalChars int    `json:"total_chars"`
}

// ToolInteraction is how a tool call's ask was answered, from KAS's interaction_resolved.
// Type is `tool_approval` or `user_input`; Outcome is `selected`, `answered` or
// `dismissed`. Choice is the chosen option's KIND for an approval (`allow_once`, …) and
// the answer text for a question; empty when the outcome carries none.
type ToolInteraction struct {
	Type    string `json:"type"`
	Outcome string `json:"outcome"`
	Choice  string `json:"choice,omitempty"`
}

// ToolDenial is the policy verdict that refused a tool call. Rule names what to edit and
// Scope plus Source say which `permissions.yaml` to open.
type ToolDenial struct {
	Rule       *ToolDenialRule `json:"rule,omitempty"`
	Capability string          `json:"capability"`
	Resource   string          `json:"resource"`
	Scope      string          `json:"scope"`
	Source     string          `json:"source"`
}

// ToolDenialRule is the Cedar rule that produced a denial. Effect is "deny" or
// "ask" (an unanswered ask that timed out reaches here as a denial).
type ToolDenialRule struct {
	Capability string   `json:"capability"`
	Effect     string   `json:"effect"`
	Match      []string `json:"match,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
}

// ToolCheckpoint is KAS's pre/post-image mapping for one file write, persisted verbatim.
// Original and Modified are opaque `kiro-snapshot-v2://` handles, not paths. ALL THREE
// FIELDS ARE INDEPENDENTLY OPTIONAL: a file CREATE has no pre-image. Granularity is one
// file write, so multi-file attribution must not be inferred from it.
type ToolCheckpoint struct {
	// Original is the pre-image snapshot URI. Empty on a file creation.
	Original string `json:"original,omitempty"`
	// Modified is the post-image snapshot URI.
	Modified string `json:"modified,omitempty"`
	// Local is the `file://` URI of the live file on disk.
	Local string `json:"local,omitempty"`
}

// ToolLocation is a file path, and optional line, the agent is working with.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff is a before/after text change from a write tool call; Path is
// workspace-relative. OldText/NewText may be the WHOLE FILE or a hunk pair, so a consumer
// must DIFF them: counting newlines reports a one-line edit as the whole file rewritten.
type ToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

// CodeReference is one licensed-code attribution. KAS drops the upstream content span,
// so attributions are TURN-scoped.
type CodeReference struct {
	LicenseName string `json:"license_name"`
	Repository  string `json:"repository,omitempty"`
	URL         string `json:"url,omitempty"`
}

// RefusalInfo is the refusal metadata KAS attaches when the turn ends with stopReason
// "refusal". Explanation is the service's own sentence, which KAS also streams as a text
// chunk; it lives here so the refusal callout owns the words rather than the reply's prose.
type RefusalInfo struct {
	Category         string `json:"category,omitempty"`
	Explanation      string `json:"explanation,omitempty"`
	RecommendedModel string `json:"recommended_model,omitempty"`
}

// PlanStatus is the lifecycle state of a plan entry.
type PlanStatus string

// PlanPending and the following constants define the PlanStatus lifecycle states for a plan entry.
const (
	PlanPending    PlanStatus = "pending"
	PlanInProgress PlanStatus = "in_progress"
	PlanCompleted  PlanStatus = "completed"
)

// PlanEntry is one item in an agent-authored plan.
type PlanEntry struct {
	Content  string     `json:"content"`
	Priority string     `json:"priority"`
	Status   PlanStatus `json:"status"`
}

// Usage is a chat's last-known context and billing snapshot. SummarizationThresholdPct 0
// means UNKNOWN, and the client applies its own fallback.
type Usage struct {
	MeteringItems             []MeteringItem `json:"metering_items,omitempty"`
	ContextPct                float64        `json:"context_pct"`
	SummarizationThresholdPct float64        `json:"summarization_threshold_pct,omitempty"`
	ContextSize               int            `json:"context_size"`
	Credits                   float64        `json:"credits"`
	LastTurnMs                float64        `json:"last_turn_ms"`
	HasRealData               bool           `json:"has_real_data"`
}

// MeteringItem is one usage dimension from kiro-cli's meteringUsage array.
// UnitPlural is the canonical identifier ("credits", "tokens", "requests").
type MeteringItem struct {
	UnitSingular string  `json:"unit_singular"`
	UnitPlural   string  `json:"unit_plural"`
	Value        float64 `json:"value"`
}

// SessionMode describes one mode the running agent supports, from
// `modes.availableModes`. The list holds bundled modes AND workspace custom agents;
// Source is what lets the picker group them.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // "bundled" | "workspace" (v3 _meta.kiro.source)
}

// SessionModel describes one model the running agent can swap to, as declared by
// kiro-cli's session/new response.
type SessionModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// DefaultEffortLevel is this MODEL's default level. Never seed Chat.Effort from it:
	// that would pin the default to every later session through StartOpts.Effort.
	DefaultEffortLevel string  `json:"default_effort_level,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier,omitempty"`
	// HasEffort reports whether this model offers effort tiers. KAS silently drops a
	// level sent to a tierless model (`auto`), so the effort row hides.
	HasEffort bool `json:"has_effort,omitempty"`
	// ThinkingToggleable reports whether thinking can be turned off for this model;
	// ThinkingDefaultOff, whether that is the model's default.
	ThinkingToggleable bool `json:"thinking_toggleable,omitempty"`
	ThinkingDefaultOff bool `json:"thinking_default_off,omitempty"`
}

// ModelChoiceMeta is the `_meta` block KAS stamps on one CHOICE of the `model` config
// option at every door (session/new, session/load, `_kiro/config/template`,
// `config_option_update`). One type so the doors cannot decode different fields. Name it
// as a field, never embed it: encoding/json promotes an embed's fields and decodes nothing.
type ModelChoiceMeta struct {
	Kiro struct {
		// DefaultThinkingEnabled is the model's thinking default; absent means on.
		DefaultThinkingEnabled *bool `json:"defaultThinkingEnabled"`
		// DefaultEffortLevel is the tier this MODEL defaults to.
		DefaultEffortLevel string `json:"defaultEffortLevel"`
		// RateMultiplier is the model's credit cost relative to the cheapest one.
		RateMultiplier float64 `json:"rateMultiplier"`
		// HasEffort reports whether the model offers reasoning-effort tiers.
		HasEffort bool `json:"hasEffort"`
		// ThinkingToggleable reports whether thinking can be turned off.
		ThinkingToggleable bool `json:"thinkingToggleable"`
	} `json:"kiro"`
}

// ThinkingDefaultOff reads a choice's defaultThinkingEnabled, absent meaning on.
func (m *ModelChoiceMeta) ThinkingDefaultOff() bool {
	return m.Kiro.DefaultThinkingEnabled != nil && !*m.Kiro.DefaultThinkingEnabled
}

// SessionEffortLevel is one reasoning-effort tier the running session offers, from the
// `effortLevel` option's `options[]`. The list IS the capability: a tier absent here is a
// level the service rejects.
type SessionEffortLevel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// CatalogState is GET /api/config-template's verdict on the pre-session model catalog.
// KAS omits the `model` option alike for an unresolved cache and an unentitled account,
// so copy over CatalogEmpty must not invent a cause. The template is a pure cache read,
// so a retry converges on CatalogUnavailable, never on CatalogEmpty.
type CatalogState string

const (
	// CatalogReady means a `model` option was decoded; its list may still be empty
	// after the [Deprecated]/[Legacy] filter.
	CatalogReady CatalogState = "ready"
	// CatalogEmpty means the reply decoded and carried no `model` option at all.
	CatalogEmpty CatalogState = "empty"
	// CatalogUnavailable means marotte never got an answer it could read.
	CatalogUnavailable CatalogState = "unavailable"
)

// CatalogReason discriminates the two ways a read fails, on CatalogUnavailable only.
type CatalogReason string

const (
	// CatalogReasonRPC means the read itself failed: retrying may succeed.
	CatalogReasonRPC CatalogReason = "rpc"
	// CatalogReasonDecode means the reply arrived unreadable: the contract moved.
	CatalogReasonDecode CatalogReason = "decode"
)

// ConfigTemplateResponse is the GET /api/config-template reply: the pre-session mode and
// model catalog plus its verdict. Not …Payload: wirespec's binding tests key on that
// suffix, and this is bound to no SSE event.
type ConfigTemplateResponse struct {
	// Subject is the `catalog` digest stamp with the hub epoch, read under the
	// catalog's lock beside the two lists it vouches for.
	Subject      *SubjectStamp `json:"subject,omitempty"`
	DefaultModel string        `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue: the tier a fresh
	// session would run at.
	EffortActive string `json:"effort_active,omitempty"`
	// Catalog has NO omitempty, so wiregen emits a required TypeScript field the
	// retry policy can rely on.
	Catalog       CatalogState  `json:"catalog"`
	CatalogReason CatalogReason `json:"catalog_reason,omitempty"`
	// The three lists are non-null on every path, so a generated decoder requiring an
	// array cannot fail on a failure body.
	Modes        []SessionMode        `json:"modes"`
	Models       []SessionModel       `json:"models"`
	EffortLevels []SessionEffortLevel `json:"effort_levels"`
}

// Chat is the persisted chat header, <dir>/<id>/chat.json; the transcript is the
// entry log beside it.
type Chat struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort is the chat's chosen effort level. A plain string so a value another build
	// wrote decodes rather than throws; the command boundary validates EffortLevel.
	Effort string `json:"effort,omitempty"`
	// Draft is the composer text typed but not sent. NOT on ChatHeader: the autosave
	// would put it in a chat_updated frame on every debounce, racing the typing tab's
	// caret. Store.SetDraft must not touch UpdatedAt, which the purge ages from.
	Draft               string `json:"draft,omitempty"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	ID                  string `json:"id"`
	// Attachments are the workspace paths staged beside the draft, under Draft's rules.
	// Paths, not contents: BuildPromptBlocks reads and confines them at send time.
	Attachments []string `json:"attachments,omitempty"`
	// ServedModelIDs is every model id the last session advertised, UNFILTERED: the
	// only evidence at spawn, before session/new answers, that the stored model still
	// runs. Empty means unknowable, and ModelServed then allows the send.
	ServedModelIDs []string `json:"served_model_ids,omitempty"`
	// EffortLevels is the effort vocabulary the last session advertised, per chat
	// because it is THIS chat's model's. EMPTY means the model has no tiers.
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// EffortActive is the level the session is RUNNING at; Effort is what the chat
	// CHOSE, empty when it never picked.
	EffortActive string `json:"effort_active,omitempty"`
	// Thinking is the chat's thinking CHOICE, empty for the model's default;
	// ThinkingActive is what the session last reported.
	Thinking       string `json:"thinking,omitempty"`
	ThinkingActive string `json:"thinking_active,omitempty"`
	// LastTurnOutcome is how the newest finished non-carrier turn ended, written with TurnCount.
	LastTurnOutcome TurnOutcome `json:"last_turn_outcome,omitempty"`
	// PendingModel is a model pick awaiting its apply between turns.
	PendingModel string `json:"pending_model,omitempty"`
	// PriorACPSessionIDs are the KAS sessions this chat USED to run on, oldest first.
	// Never trimmed: each still holds history on disk, so the reaper spares the whole
	// chain. Maintained by RecordSession.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	// InterruptMode is what Send does while a turn runs; empty reads as steer.
	InterruptMode InterruptMode `json:"interrupt_mode,omitempty"`
	// QueuedPrompts are follow-ups held for the running turn's end, oldest first,
	// carried rows ahead of user rows. drainAfterClose owns which row a close sends.
	QueuedPrompts []QueuedPrompt `json:"queued_prompts,omitempty"`
	// TangentMerges are this tangent's merge operations; internal/command owns their lifecycle.
	TangentMerges []TangentMerge `json:"tangent_merges,omitempty"`
	Usage         Usage          `json:"usage"`
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
	// TurnCount is the newest turn_open's n, the counter windows are read against.
	TurnCount      int  `json:"turn_count"`
	SupervisedMode bool `json:"supervised_mode,omitempty"`
	// NameSetByUser marks Name as the user's own: no other title overwrites it, and
	// every KAS session the chat opens is renamed to it.
	NameSetByUser bool `json:"name_set_by_user,omitempty"`
	// Tangent marks a chat whose session KAS forked from another chat's, which is what offers a
	// merge back; a fork that fell back to a fresh session is not one. Which chat it merges into
	// is KAS's parent session link, never this record.
	Tangent bool `json:"tangent,omitempty"`
}

// InterruptMode is a chat's choice of what a message sent mid-turn does.
type InterruptMode string

const (
	// InterruptSteer joins the running turn (_session/steer).
	InterruptSteer InterruptMode = "steer"
	// InterruptQueue holds the message as a QueuedPrompt for the turn's end.
	InterruptQueue InterruptMode = "queue"
)

// Valid reports whether m is a member of the vocabulary; empty is not.
func (m InterruptMode) Valid() bool {
	return m == InterruptSteer || m == InterruptQueue
}

// MaxQueuedPrompts caps Chat.QueuedPrompts, which the header carries to every device. A
// user's queue_prompt stops short of it, so a shutdown's carried row always has a slot.
const MaxQueuedPrompts = 20

// carrySeparator joins the steers one carried row resends.
const carrySeparator = "\n\n"

// CarryCost is the bytes a steer of this text adds to its carried row, separator included.
func CarryCost(text string) int { return len(text) + len(carrySeparator) }

// QueuedPrompt is one follow-up waiting for a turn to end. A row with Resends is CARRIED,
// written at shutdown from unread steers. Held marks a row a previous process queued; it is
// never sent automatically. A list holds at most one carried row, and it is held. Label is the
// row's PromptCommand.DisplayText.
type QueuedPrompt struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Label       string       `json:"label,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Resends     []string     `json:"resends,omitempty"`
	Held        bool         `json:"held,omitempty"`
}

// Carried reports whether the row resends dropped steers.
func (q *QueuedPrompt) Carried() bool { return len(q.Resends) > 0 }

// CarriedRow is the carried row for unread steers, texts in the order given.
func CarriedRow(id string, texts, resends []string) QueuedPrompt {
	return QueuedPrompt{ID: id, Text: strings.Join(texts, carrySeparator), Resends: resends}
}

// Absorb appends more steers to q, keeping q's id.
func (q *QueuedPrompt) Absorb(more *QueuedPrompt) {
	q.Text += carrySeparator + more.Text
	q.Resends = append(q.Resends, more.Resends...)
}

// SessionChain returns every KAS session id this chat has run on, current one last: the
// reaper's keep-set.
func (c *Chat) SessionChain() []string {
	return sessionChain(c.ACPSessionID, c.PriorACPSessionIDs)
}

// ComposerState is a chat's unsent text and staged files, returned by Store.SetDraft and
// Store.SetAttachments for the draft_changed broadcast. Not a wire type.
type ComposerState struct {
	Text string
	// Version is the `chat` version the write minted under the chat's lock; empty
	// on a read.
	Version     string
	Attachments []string
}

// Composer returns the chat's current composer state.
func (c *Chat) Composer() ComposerState {
	return ComposerState{Text: c.Draft, Attachments: slices.Clone(c.Attachments)}
}

// sessionChain is the chat's full session chain, shared by Chat and ChatHeader. Freshly
// allocated on EVERY branch, so a caller's append can never rewrite the retention set.
func sessionChain(current string, prior []string) []string {
	chain := make([]string, 0, len(prior)+1)
	chain = append(chain, prior...)
	if current == "" {
		return chain
	}
	return append(chain, current)
}

// RecordSession points the chat at session id, retiring the current one into the chain.
// "" detaches without forgetting (a failed session/load). Idempotent.
func (c *Chat) RecordSession(id string) {
	if c.ACPSessionID == id {
		return
	}
	if c.ACPSessionID != "" && !slices.Contains(c.PriorACPSessionIDs, c.ACPSessionID) {
		c.PriorACPSessionIDs = append(c.PriorACPSessionIDs, c.ACPSessionID)
	}
	c.ACPSessionID = id
	// A revisited id lives in exactly one place: the current field.
	if id != "" {
		c.PriorACPSessionIDs = slices.DeleteFunc(c.PriorACPSessionIDs, func(s string) bool { return s == id })
	}
}

// Header returns the chat's metadata: the list endpoints' row and the
// chat_updated payload.
func (c *Chat) Header() ChatHeader {
	return ChatHeader{
		ID:                  c.ID,
		Name:                c.Name,
		Model:               c.Model,
		ACPSessionID:        c.ACPSessionID,
		PriorACPSessionIDs:  c.PriorACPSessionIDs,
		CurrentModeID:       c.CurrentModeID,
		Effort:              c.Effort,
		LastTurnOutcome:     c.LastTurnOutcome,
		EffortLevels:        c.EffortLevels,
		EffortActive:        c.EffortActive,
		Thinking:            c.Thinking,
		ThinkingActive:      c.ThinkingActive,
		Usage:               c.Usage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
		TurnCount:           c.TurnCount,
		PendingModel:        c.PendingModel,
		SupervisedMode:      c.SupervisedMode,
		CompactionWatermark: c.CompactionWatermark,
		InterruptMode:       c.InterruptMode,
		QueuedPrompts:       c.QueuedPrompts,
		Tangent:             c.Tangent,
	}
}

// ThinkingIsOff reports whether the chat's session runs, or will run, with thinking off:
// the session's report, else the chat's choice, else the model's default. Callers apply
// it only on a toggleable model.
func (c *Chat) ThinkingIsOff(modelDefaultOff bool) bool {
	if c.ThinkingActive != "" {
		return c.ThinkingActive == ThinkingOff
	}
	if c.Thinking != "" {
		return c.Thinking == ThinkingOff
	}
	return modelDefaultOff
}

// CapEffortForThinkingOff mirrors KAS's cap: with thinking off, xhigh and max run as the
// highest lower tier in the model's vocabulary. Anything else is returned unchanged.
func CapEffortForThinkingOff(level string, levels []SessionEffortLevel) string {
	if level != string(effortXHigh) && level != string(EffortMax) {
		return level
	}
	for _, l := range slices.Backward(levels) {
		if l.ID != string(effortXHigh) && l.ID != string(EffortMax) {
			return l.ID
		}
	}
	return level
}

// ChatHeader is the metadata-only view of a Chat; field order is fieldalignment's.
type ChatHeader struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort mirrors Chat's: the header is the only projection reaching every chat,
	// and the effort control reads the ACTIVE chat's.
	Effort string `json:"effort,omitempty"`
	// LastTurnOutcome mirrors Chat's for the tab dot; carrier turns are skipped. Never `running`.
	LastTurnOutcome TurnOutcome `json:"last_turn_outcome,omitempty"`
	// EffortActive and EffortLevels mirror Chat's, as Effort does.
	EffortActive string `json:"effort_active,omitempty"`
	// Thinking and ThinkingActive mirror Chat's for the effort slider's Off stop.
	Thinking       string `json:"thinking,omitempty"`
	ThinkingActive string `json:"thinking_active,omitempty"`
	// PendingModel mirrors Chat's: the model badge reads it off the header.
	PendingModel string               `json:"pending_model,omitempty"`
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// The model and mode vocabulary is a workspace fact (agent.catalog), never here.
	ID                  string `json:"id"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	// PriorACPSessionIDs mirrors Chat's: the retention sweep reads headers only.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	// InterruptMode and QueuedPrompts mirror Chat's for the composer and the dock.
	InterruptMode  InterruptMode  `json:"interrupt_mode,omitempty"`
	QueuedPrompts  []QueuedPrompt `json:"queued_prompts,omitempty"`
	Usage          Usage          `json:"usage"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	TurnCount      int            `json:"turn_count"`
	SupervisedMode bool           `json:"supervised_mode,omitempty"`
	// Tangent mirrors Chat's for the merge controls.
	Tangent bool `json:"tangent,omitempty"`
}

// SessionChain returns the same set as Chat.SessionChain.
func (h *ChatHeader) SessionChain() []string {
	return sessionChain(h.ACPSessionID, h.PriorACPSessionIDs)
}

// ResumableSession is one stored KAS session the previous-session picker offers (GET
// /api/sessions). Field order is fieldalignment's.
type ResumableSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	AgentMode string `json:"agent_mode,omitempty"`
	// Status is KAS's own session status: idle | failed | waiting_on_user.
	Status string `json:"status,omitempty"`
	// Description is the agent's self-declared focus, present on a minority of rows.
	Description string `json:"description,omitempty"`
	// ChatID names the marotte chat that already owns this session, if any.
	ChatID    string `json:"chat_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// ReadState is one list read's outcome on GET /api/sessions. Two-valued: an empty list is
// an empty array, so only a failure needs stating.
type ReadState string

const (
	// ReadReady means the list read succeeded; an empty list is a real answer.
	ReadReady ReadState = "ready"
	// ReadUnavailable means the read failed, so the list says nothing.
	ReadUnavailable ReadState = "unavailable"
)

// SessionListResponse is the GET /api/sessions reply. The two lists come from separate
// verbs and degrade independently; neither verdict has omitempty, so absence never reads
// as success.
type SessionListResponse struct {
	SessionsState ReadState          `json:"sessions_state"`
	RunsState     ReadState          `json:"runs_state"`
	Sessions      []ResumableSession `json:"sessions"`
	Runs          []WorkflowRun      `json:"runs"`
}

// WorkflowRun is one previous workflow run in GET /api/sessions. Sourced from
// _kiro/workflow/list, NOT session/list, whose workflow rows are STEP sessions that
// report idle whatever the run did.
type WorkflowRun struct {
	WorkflowID string `json:"workflow_id"`
	// Name is what to DISPLAY (`runLabel ?? workflowName`), not the recipe.
	Name string `json:"name"`
	// WorkflowName is the RECIPE, the only field a per-recipe decision may read:
	// labels rewrite Name, so a rule keyed on it fails OPEN.
	WorkflowName string `json:"workflow_name,omitempty"`
	// Status is run-level: paused / completed / failed.
	Status RunStatus `json:"status,omitempty"`
	// ParentChatID is the marotte chat that launched the run, via its session chain.
	ParentChatID string `json:"parent_chat_id,omitempty"`
	// EndReason says which bound stopped the run ("overran", "stalled", "orphaned"):
	// KAS reports `cancelled` for a bound and a person alike. In-memory, so a run
	// stopped before a restart falls back to the plain status.
	EndReason string `json:"end_reason,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}
