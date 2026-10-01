package marotte

// Chat domain types: the persisted and over-the-wire shapes for session state,
// tool calls, plans, usage and session modes/models.

import (
	"encoding/json"
	"slices"
)

// ToolKind identifies the category of a tool invocation, assigned by kiro-cli and
// flowing through ACP unchanged.
type ToolKind string

// ToolKindExecute and the following constants define the valid ToolKind values.
// KAS v3 emits only read/edit/delete/move/search/execute/think/fetch/switch_mode/
// other; the rest are retained because they back WorkingLabelForKind's label table
// and keep older persisted chat files renderable. KAS's own hook ASK arrives as kind
// "other" tagged _meta.kiro.hookAsk; ToolKindHook is minted by marotte for the
// synthetic `Hook fired` card (internal/translate/hook_status.go).
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
	// ToolAborted is a call nothing can still settle because its turn closed: the
	// reader's cancel, or a run whose step end never arrived. Its own value rather
	// than `failed`, which means the tool ran and reported an error. marotte mints
	// it at turn close; it never arrives on the wire.
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
	// ACPUpdateSessionInfo and the two below are v3 (KAS) sub-kinds.
	// available_commands_update arrives too and is deliberately NOT decoded: an
	// unhandled sub-kind falls through handleSessionUpdate silently, and the
	// slash-command catalog has no consumer.
	ACPUpdateSessionInfo ACPUpdateKind = "session_info_update"
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
	// AgentSubtaskID is set from a tool call's _meta.kiro.agentSubtaskId. On v3 a
	// subagent surfaces as an ordinary tool_call with _meta.kiro.kind agent-subtask;
	// this id links the card to its nested deltas, which carry the same id.
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	// WorkflowID names the run a `run_workflow` invocation started, from the terminal
	// update's `rawOutput.workflowId`. Empty on every other tool call, and on this one
	// until the run is created. It makes the invocation the RUN's card: the client
	// keys a run card on it, and a step's blocks arrive carrying the same id in their
	// `agent_subtask_id`, so the two sides join with no guessing.
	WorkflowID string `json:"workflow_id,omitempty"`
	// TerminalID links an execute tool call to the agent terminal running it, from
	// the ACP type:"terminal" content block. It makes the CARD the terminal's
	// rendering surface. Empty on every tool call that spawned no process.
	TerminalID string `json:"terminal_id,omitempty"`
	// Checkpoint is KAS's snapshot mapping for a tool call that wrote a file, from
	// _meta.kiro.checkpoint; nil when it touched no file. Ahead of the slices below
	// for govet fieldalignment: a trailing pointer would extend the GC scan region.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	// Disclosed names the skill or steering document a `disclose_context` call
	// loaded, from _meta.kiro.disclosedContext. The only signal that a skill's body
	// reached the model, so the transcript renders it, not a generic tool card.
	Disclosed *ToolDisclosed `json:"disclosed,omitempty"`
	// Denial is KAS's structured reason for a call the Cedar policy refused, from
	// _meta.kiro.policyDenial. Present so a refusal reads as a refusal rather than a
	// tool failure, and names the rule, since the user owns the policy.
	Denial *ToolDenial `json:"denial,omitempty"`
	// Truncated is what the STORE dropped to bound this call on disk, nil on every
	// call that fitted. Grouped with the pointers above for govet fieldalignment.
	Truncated *ToolTruncation `json:"truncated,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Locations []ToolLocation  `json:"locations,omitempty"`
	Diffs     []ToolDiff      `json:"diffs,omitempty"`
	// OutputSpans styles ranges of Output. Parsed once server-side by
	// internal/ansitext, so Output stays plain searchable text and the client never
	// builds HTML from agent-controlled bytes.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	Ts          int64      `json:"ts"`
	DurationMs  int        `json:"duration_ms,omitempty"`
	// OutputBytes is the PERSISTED output's length: what the reveal will fetch, and
	// what says the bulk holds output at all. Set ONLY alongside HasFull; where the
	// store also cut the call, Truncated carries the size before THAT cut.
	OutputBytes int `json:"output_bytes,omitempty"`
	// HasFull says Input, Output and Diffs here are a PREVIEW, and the whole of what
	// the record kept is at GET /api/chats/{id}/tools/{id}. Set by the transcript
	// read path alone, because only a page load or scroll-up reads a preview.
	HasFull bool `json:"has_full,omitempty"`
	// Declined says the tool RAN CORRECTLY AND REFUSED — a fifth card outcome, and a
	// distinct fact from Status. `failed` overstates it (nothing broke, so the card
	// must not offer "Explain this error") and `completed` mislabels it outright,
	// which is what a refused `update_workflow` reads as today.
	//
	// A field rather than a sixth ToolStatus member, following Denial's precedent for
	// the same reason: a status member lands wrong at eleven predicates that ask only
	// whether a call is over (`isToolDone`, the ToolFailed reason gates), and a refusal
	// IS over. The REASON is not duplicated here — it is the
	// tool's own output and already on Output, which a declined card auto-expands.
	Declined bool `json:"declined,omitempty"`
}

// ToolTruncation is what the store DROPPED to bound what one tool call costs the
// record, each cut field carrying its size BEFORE the cut so a reader renders
// "truncated, N bytes" instead of showing less than happened. A zero field was
// not cut.
type ToolTruncation struct {
	// OutputBytes and InputBytes are each field's original length.
	OutputBytes int `json:"output_bytes,omitempty"`
	InputBytes  int `json:"input_bytes,omitempty"`
	// DiffBytes is the original diff total and DiffCount the original count. Diffs
	// are dropped WHOLE: a cut before/after pair would describe an edit nobody made.
	DiffBytes int `json:"diff_bytes,omitempty"`
	DiffCount int `json:"diff_count,omitempty"`
}

// ToolCallBulk is GET /api/chats/{id}/tools/{toolCallID}: the whole of one tool
// call's PERSISTED content, for a card whose preview said HasFull. Only the three
// fields the transcript previews; title, kind and status are on the card already.
type ToolCallBulk struct {
	// Strings before slices, and no field order here carries meaning: this is
	// betteralign's answer for the smallest GC scan region (govet fieldalignment).
	Output      string          `json:"output,omitempty"`
	ID          string          `json:"id"`
	Diffs       []ToolDiff      `json:"diffs,omitempty"`
	OutputSpans []TextSpan      `json:"output_spans,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// TextSpan styles the half-open range [Start,End) of a sibling text field. It
// mirrors internal/ansitext.Span, and the wire type lives here because that package
// stays a stdlib-only leaf that knows nothing about the wire.
//
// Attrs matches web-terminal-engine's vt.WireRun.A so both renderers share one
// attribute vocabulary. The COLOUR encoding deliberately differs: a palette INDEX
// survives into a persisted chat file without baking today's theme into it.
type TextSpan struct {
	// Start is the inclusive offset in UTF-16 CODE UNITS, not bytes, because the
	// consumer indexes with JavaScript string offsets: a byte offset would point
	// mid-character the moment output carried a box-drawing glyph or accented name.
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

// ToolDenial is the policy verdict that refused a tool call. Rule is the
// load-bearing field — a denial naming its rule is one click from editing it — and
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

// ToolCheckpoint is KAS's pre/post-image mapping for one file write, persisted
// verbatim so a diff is a snapshot read plus a file read. Original and Modified are
// opaque `kiro-snapshot-v2://` handles, NOT filesystem paths, deliberately unparsed.
//
// ALL THREE FIELDS ARE INDEPENDENTLY OPTIONAL and a consumer must tolerate any
// subset: a file CREATE has no pre-image, so code treating this as a fixed triplet
// breaks on the first file the agent creates. Granularity is per-file-write, so
// multi-file attribution must not be inferred from it.
type ToolCheckpoint struct {
	// Original is the pre-image snapshot URI. Empty on a file creation.
	Original string `json:"original,omitempty"`
	// Modified is the post-image snapshot URI.
	Modified string `json:"modified,omitempty"`
	// Local is the `file://` URI of the live file on disk.
	Local string `json:"local,omitempty"`
}

// ToolLocation is a file path, and optional line, the agent is working with. The
// editor scrolls to it.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ToolDiff is a before/after text change from a write tool call. Path is
// workspace-relative; agent.relPath normalises it on the way in.
//
// OldText/NewText carry the WHOLE FILE for KAS's edit tools (325 of 413 persisted
// fragments measured whole-file shaped), and a hunk pair is also accepted, so a
// consumer must DIFF the two sides rather than count newlines — counting reported a
// one-line edit as the entire file removed and re-added.
type ToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

// CodeReference is one licensed-code attribution, emitted when a completion
// reproduces a recognizable chunk of a referenced open-source file.
//
// KAS drops CodeWhisperer's recommendationContentSpan upstream, so there is no span
// to map a reference to a message region: attributions are TURN-scoped.
type CodeReference struct {
	LicenseName string `json:"license_name"`
	Repository  string `json:"repository,omitempty"`
	URL         string `json:"url,omitempty"`
}

// RefusalInfo is the refusal metadata KAS attaches when the model declines to
// continue a conversation, and the turn then ends with stopReason "refusal".
//
// Explanation is the SERVICE's own sentence about why, which KAS also streams as
// an ordinary text chunk. It belongs here rather than in the assistant bubble:
// folded into the open text entry it renders as prose, appended to the end of a
// real reply with no separator, where no error surface can reach it. Keeping it on
// the record is what lets the refusal callout own the words; empty when the wire
// supplied none, and the callout keeps its own wording then. Persisted with the
// classification so the callout survives a reload.
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

// Usage is a chat's last-known context and billing snapshot, plus the percentages at
// which the SESSION summarizes and truncates its own context. Both thresholds are
// omitempty because 0 means UNKNOWN — a chat that has never resumed receives neither,
// and the client applies its own fallback.
type Usage struct {
	MeteringItems             []MeteringItem `json:"metering_items,omitempty"`
	ContextPct                float64        `json:"context_pct"`
	SummarizationThresholdPct float64        `json:"summarization_threshold_pct,omitempty"`
	TruncationThresholdPct    float64        `json:"truncation_threshold_pct,omitempty"`
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
// `modes.availableModes` on session/new or session/load; kept on the chat so the
// mode pill renders without re-querying the bridge.
//
// The v3 list is unified — bundled workflow modes AND every workspace custom
// agent, all switchable via session/set_mode — so Source ("bundled" vs
// "workspace") is what lets the picker group them.
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
	// DefaultEffortLevel is the level this MODEL defaults to, from
	// `_meta.kiro.defaultEffortLevel`; used for a chat with no session yet.
	//
	// NOT persisted onto Chat.Effort: seeding a chat's CHOICE from a service
	// default would pin it to every later session through StartOpts.Effort.
	DefaultEffortLevel string  `json:"default_effort_level,omitempty"`
	RateMultiplier     float64 `json:"rate_multiplier,omitempty"`
	// HasEffort reports whether this model offers reasoning-effort tiers, from
	// `_meta.kiro.hasEffort`. `auto` carries false, so a chat on it hides the
	// effort row: KAS builds no effortLevel option for a tierless model and
	// silently drops a level sent to one. Chat.EffortLevels answers the same
	// question from the other side.
	HasEffort bool `json:"has_effort,omitempty"`
}

// ModelChoiceMeta is the `_meta` block KAS stamps on one CHOICE of the `model`
// config option, wherever that option arrives: session/new, session/load,
// `_kiro/config/template`, or a live `config_option_update`.
//
// ONE type for all four doors, because it used to be four hand-written anonymous
// structs and they had DIVERGED — which is not a tidiness point, it is the defect
// this type exists to make unrepresentable. `translate`'s copy read
// `defaultEffortLevel` and `hasEffort` and not `rateMultiplier`, and since
// `handleConfigTemplate` prefers the LIVE catalog over the template's, every
// model reached the client with no rate and rendered `1x` — the picker's whole
// credit readout, wrong for every model, silently.
//
// It is named at each site rather than embedded: encoding/json PROMOTES an
// untagged embedded struct's fields to the outer object, so an embed would read
// `kiro` off the choice's own top level and decode nothing.
type ModelChoiceMeta struct {
	Kiro struct {
		// DefaultEffortLevel is the tier this MODEL defaults to.
		DefaultEffortLevel string `json:"defaultEffortLevel"`
		// RateMultiplier is the model's credit cost relative to the cheapest one.
		// Read by the picker's `Nx` readout and by cheapestModel's selection.
		RateMultiplier float64 `json:"rateMultiplier"`
		// HasEffort reports whether the model offers reasoning-effort tiers.
		HasEffort bool `json:"hasEffort"`
	} `json:"kiro"`
}

// SessionEffortLevel is one reasoning-effort tier the running session offers,
// from the `effortLevel` config option's own `options[]`.
//
// The tiers are NOT a fixed five and NOT a per-model list: kiro-cli 2.18.0 builds
// its picker from this option and errors when the list is empty, so the list IS
// the capability. A tier absent here is a level the service rejects.
type SessionEffortLevel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// CatalogState is GET /api/config-template's verdict on the pre-session model
// catalog.
//
// Three-valued, and the ceiling is KAS's: it omits the `model` option
// identically for an unresolved cache and for an unentitled account, so
// CatalogEmpty states no cause and copy over it must not invent one. That also
// bounds the retry — the template is a pure cache read, so retrying converges on
// CatalogUnavailable, never on CatalogEmpty.
type CatalogState string

const (
	// CatalogReady means a `model` config option was decoded. Its entry list may
	// still be empty after the [Deprecated]/[Legacy] filter — KAS answered with a
	// catalog either way, which is what separates this from CatalogEmpty.
	CatalogReady CatalogState = "ready"
	// CatalogEmpty means the reply decoded and carried no `model` option at all.
	CatalogEmpty CatalogState = "empty"
	// CatalogUnavailable means marotte never got an answer it could read.
	CatalogUnavailable CatalogState = "unavailable"
)

// CatalogReason discriminates the two ways a read fails, on CatalogUnavailable
// only. They are categorically different — one says try again, the other says the
// contract moved — and the flattened body could say neither.
type CatalogReason string

const (
	// CatalogReasonRPC means the read itself failed: retrying may succeed.
	CatalogReasonRPC CatalogReason = "rpc"
	// CatalogReasonDecode means the reply arrived unreadable: the contract moved.
	CatalogReasonDecode CatalogReason = "decode"
)

// ConfigTemplateResponse is the GET /api/config-template reply: the pre-session
// mode + model catalog, plus the verdict that says which outcome produced it.
//
// Named …Response rather than …Payload because it is bound to no SSE event;
// internal/wirespec's binding tests key on the Payload suffix, and
// auth.WhoamiResponse is the precedent.
type ConfigTemplateResponse struct {
	// Subject is the `catalog` digest stamp with the hub epoch, read under the
	// catalog's lock beside the two lists it vouches for.
	Subject      *SubjectStamp `json:"subject,omitempty"`
	DefaultModel string        `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue: the tier a
	// fresh session would run at. Pre-session, this is the only evidence of
	// a live level.
	EffortActive string `json:"effort_active,omitempty"`
	// Catalog carries NO omitempty deliberately: wiregen emits a required
	// TypeScript field for a field without it, so a client cannot invent a
	// fallback for the one value the whole retry policy reads.
	Catalog       CatalogState  `json:"catalog"`
	CatalogReason CatalogReason `json:"catalog_reason,omitempty"`
	// The three lists are non-null on every path, degrade branches included, so a
	// generated decoder requiring an array cannot fail on a failure body.
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
	// Effort is the chat's reasoning-effort level ("low".."max"), applied at
	// session/new through StartOpts.Effort and live through CmdSetEffort.
	//
	// A plain string, not EffortLevel: this is persisted state, so a value written
	// by a different build must decode rather than throw. The command boundary
	// validates with EffortLevel.Valid().
	Effort string `json:"effort,omitempty"`
	// Draft is the composer text typed but not sent, so switching chat tabs stops
	// bleeding one chat's half-written message into another. Server-side rather than
	// localStorage so it follows the user across devices.
	//
	// Deliberately NOT on ChatHeader: a debounced autosave would put the draft in a
	// chat_updated frame every 600ms of typing, which every client re-renders and
	// which races the caret of the tab that typed it. Retention rides the chat file,
	// so Store.SetDraft must not touch UpdatedAt, which the purge ages from.
	Draft               string `json:"draft,omitempty"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	ID                  string `json:"id"`
	// Attachments are the workspace paths staged beside the draft and not yet sent —
	// the DRAFT'S TWIN, saved on the same debounce, and absent from ChatHeader for
	// the same reason Draft is.
	//
	// Paths, not contents: the file is read at send time by BuildPromptBlocks, which
	// is also where the path is confined to the workspace. Storing bytes here would
	// put a 10 MiB image in the chat file for a prompt that may never be sent.
	// Store.SetAttachments keeps Draft's retention contract: no UpdatedAt stamp.
	Attachments []string `json:"attachments,omitempty"`
	// ServedModelIDs is every model id this chat's last session advertised,
	// UNFILTERED, unlike the picker's catalog, which drops end-of-life entries. The
	// `--model` launch flag is built BEFORE session/new returns a catalog, so at
	// spawn time the previous session's advertised set is the only evidence about
	// whether the stored model is still one the account can run. Empty means
	// unknowable, and ModelServed then allows the send.
	ServedModelIDs []string `json:"served_model_ids,omitempty"`
	// EffortLevels is the reasoning-effort vocabulary the last session advertised.
	// Per-chat rather than per-workspace because it is the vocabulary of THIS chat's
	// model: two chats on different models disagree about which tiers exist. EMPTY
	// means the current model has no tiers at all, which is how kiro-cli's own TUI
	// decides to refuse the command.
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// EffortActive is the level the session is RUNNING at, from that option's
	// `currentValue`. Distinct from Effort, which is what this chat CHOSE: a chat
	// that never picked has an empty Effort and still runs at a level.
	EffortActive string `json:"effort_active,omitempty"`
	// LastTurnOutcome is how the newest finished turn ended, written by the closer
	// that appends a turn_close in the same header rewrite as TurnCount.
	LastTurnOutcome TurnOutcome `json:"last_turn_outcome,omitempty"`
	// PendingModel is a model pick awaiting its apply between turns; empty when
	// none is pending.
	PendingModel string `json:"pending_model,omitempty"`
	// PriorACPSessionIDs are the KAS sessions this chat USED to run on, oldest
	// first, and a chat routinely changes session: a failed session/load blanks it,
	// a model switch fallback recreates it. Each of those sessions still holds that
	// period's transcript and pre-images on disk, so retention keys on the whole
	// CHAIN. Never trimmed: an entry here is a directory the reaper must spare.
	// Maintained by RecordSession.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	// TurnCount is the count of turns in the log: the newest turn_open's n, and
	// the one counter a window's n and has_more are read against.
	TurnCount      int  `json:"turn_count"`
	SupervisedMode bool `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id this chat has run on, current one
// last. The reaper's keep-set: any directory in it holds part of the history.
func (c *Chat) SessionChain() []string {
	return sessionChain(c.ACPSessionID, c.PriorACPSessionIDs)
}

// ComposerState is the pair a chat's composer holds between sends: the text typed
// and not sent, and the files staged beside it.
//
// Returned by Store.SetDraft and Store.SetAttachments so the draft_changed
// broadcast gets both halves without a second chat-file read.
//
// Not a wire type and not persisted: DraftChangedPayload crosses the wire.
type ComposerState struct {
	Text string
	// Version is the `chat` version the composer write minted, filled by the
	// store under the chat's lock so the draft_changed broadcast stamps from the
	// same critical section. Empty on a state nothing minted (a read).
	Version     string
	Attachments []string
}

// Composer returns the chat's current composer state.
func (c *Chat) Composer() ComposerState {
	return ComposerState{Text: c.Draft, Attachments: slices.Clone(c.Attachments)}
}

// sessionChain composes the current session id and the retired ones into the
// chat's full chain. Shared by Chat and ChatHeader so the two views cannot
// disagree about what a chat's retention set is.
//
// Freshly allocated on EVERY branch: returning PriorACPSessionIDs itself would
// let a caller's append rewrite the chat's retention set, and copying on only
// one branch is worse than never copying — a mutating caller would be correct
// with a session attached and corrupting without one.
func sessionChain(current string, prior []string) []string {
	chain := make([]string, 0, len(prior)+1)
	chain = append(chain, prior...)
	if current == "" {
		return chain
	}
	return append(chain, current)
}

// RecordSession points the chat at session id, retiring whatever it was on into
// the chain first. Pass "" to detach from the current session without forgetting
// it (a failed session/load). Idempotent: re-recording the current id, or one
// already in the chain, is a no-op.
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
		Usage:               c.Usage,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
		TurnCount:           c.TurnCount,
		PendingModel:        c.PendingModel,
		SupervisedMode:      c.SupervisedMode,
		CompactionWatermark: c.CompactionWatermark,
	}
}

// ChatHeader is the metadata-only view of a Chat. Field order is fieldalignment's,
// not Chat's; the two serialise independently, so the mismatch is harmless.
type ChatHeader struct {
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"`
	ACPSessionID  string `json:"acp_session_id,omitempty"`
	CurrentModeID string `json:"current_mode_id,omitempty"`
	// Effort mirrors Chat's, because the effort control reads the ACTIVE chat's
	// level and an empty chat never fetches its full record, so the header is the
	// only path that reaches every chat. Chat.Draft is deliberately NOT mirrored.
	Effort string `json:"effort,omitempty"`
	// LastTurnOutcome is how this chat's NEWEST finished turn ended, the stored
	// header field the turn_close writer keeps. Here because the header is the
	// only projection reaching every chat, which is what the tab dot needs.
	// Empty for a chat with no finished turn. Never `running`.
	LastTurnOutcome TurnOutcome `json:"last_turn_outcome,omitempty"`
	// EffortActive + EffortLevels mirror Chat's, for the same reason Effort does:
	// the control renders from the ACTIVE chat's header.
	EffortActive string `json:"effort_active,omitempty"`
	// PendingModel mirrors Chat's: the model badge reads it off the header.
	PendingModel string               `json:"pending_model,omitempty"`
	EffortLevels []SessionEffortLevel `json:"effort_levels,omitempty"`
	// The model and mode vocabulary is a WORKSPACE fact served once by
	// agent.Catalog, never mirrored per header.
	ID                  string `json:"id"`
	CompactionWatermark string `json:"compaction_watermark,omitempty"`
	// PriorACPSessionIDs mirrors Chat's, because the retention sweep derives its
	// keep-list from header reads rather than loading every chat in full.
	PriorACPSessionIDs []string `json:"prior_acp_session_ids,omitempty"`
	Usage              Usage    `json:"usage"`
	CreatedAt          int64    `json:"created_at"`
	UpdatedAt          int64    `json:"updated_at"`
	TurnCount          int      `json:"turn_count"`
	SupervisedMode     bool     `json:"supervised_mode,omitempty"`
}

// SessionChain returns every KAS session id the chat has run on, current one
// last. Same set as Chat.SessionChain.
func (h *ChatHeader) SessionChain() []string {
	return sessionChain(h.ACPSessionID, h.PriorACPSessionIDs)
}

// ResumableSession is one stored KAS session offered by the previous-session
// picker (GET /api/sessions). KAS owns the inventory and the transcript, so
// marotte keeps no archive of its own. Field order is fieldalignment's.
type ResumableSession struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	AgentMode string `json:"agent_mode,omitempty"`
	// Status is KAS's own session status: idle | failed | waiting_on_user.
	Status string `json:"status,omitempty"`
	// Description is the agent's self-declared focus for that session, present
	// on a minority of rows (88 of 399 measured).
	Description string `json:"description,omitempty"`
	// ChatID names the marotte chat that already owns this session, empty when none
	// does. A claimed session is one the user can simply open.
	ChatID    string `json:"chat_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// ReadState is one list read's outcome on GET /api/sessions.
//
// Two-valued where CatalogState is three: an empty list arrives as an empty
// array rather than an omitted field, so `empty` stays derivable from its own
// length and only the read's failure needs stating.
type ReadState string

const (
	// ReadReady means the list read succeeded; an empty list is a real answer.
	ReadReady ReadState = "ready"
	// ReadUnavailable means the read failed, so the list says nothing.
	ReadUnavailable ReadState = "unavailable"
)

// SessionListResponse is the GET /api/sessions reply.
//
// The two lists degrade INDEPENDENTLY — separate verbs on the same bridge, so
// one can fail alone — which is why each carries its own verdict rather than the
// response carrying one. Neither verdict carries omitempty: a reader must not be
// able to read an absent field as success.
type SessionListResponse struct {
	SessionsState ReadState          `json:"sessions_state"`
	RunsState     ReadState          `json:"runs_state"`
	Sessions      []ResumableSession `json:"sessions"`
	Runs          []WorkflowRun      `json:"runs"`
}

// WorkflowRun is one previous workflow run, listed beside previous chats in
// the history surface (GET /api/sessions) and reviewable read-only.
//
// Sourced from _kiro/workflow/list, NOT session/list: that verb's workflow rows
// are STEP sessions reporting idle whatever the run did, so they can be neither
// counted nor judged as runs.
type WorkflowRun struct {
	WorkflowID string `json:"workflow_id"`
	// Name is what to DISPLAY: upstream computes it as `runLabel ?? workflowName`,
	// so it stops being the recipe the moment anything stamps a label.
	Name string `json:"name"`
	// WorkflowName is the RECIPE, and the only field a per-recipe decision may
	// read: an agent launching via `run_workflow` passes a `<recipe>-<topic>`
	// label and KAS stamps `<workflowName>-<targetId>` on a watch node, so keying
	// the single-run rule on Name made that guard fail OPEN.
	WorkflowName string `json:"workflow_name,omitempty"`
	// Status is run-level: paused / completed / failed.
	Status RunStatus `json:"status,omitempty"`
	// ParentChatID is the marotte chat that launched the run, resolved through the
	// launching session's chain. Empty for a run with no marotte parent.
	ParentChatID string `json:"parent_chat_id,omitempty"`
	// EndReason says why something OTHER than the run stopped it: "overran" (a
	// slot or the backstop), "stalled" (the idle window) or "orphaned". Every bound cancels
	// through the verb the Cancel button reaches, so KAS reports `cancelled`
	// either way and only this separates a bound from a person; a user cancel
	// records nothing. In-memory for the runs THIS process stopped, so one
	// stopped before a restart falls back to the plain status.
	EndReason string `json:"end_reason,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	CreatedAt int64  `json:"created_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
}
