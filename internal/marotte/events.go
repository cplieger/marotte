package marotte

// Server events: the wire shapes broadcast over SSE (/api/events) +
// per-type payloads. Event dispatch lives in agent/sse.go and the
// per-method translation lives in agent/translate*.go.
//
// Payload structs live in events_payloads.go; this file contains the
// envelope types, event-type constants, and working-label logic.

// EventType identifies the kind of SSE event broadcast to clients.
// Using a typed string instead of bare literals makes typos a compile
// error and the full event vocabulary discoverable via IDE completion.
type EventType string

// ServerEvent is the server→client event envelope broadcast to every SSE
// client: {type, chat_id, payload}. Payload is the event-specific JSON
// object; handlers type-assert based on Type. Construct it through NewEvent,
// which keeps the payload typed at the emit site.
type ServerEvent struct {
	Payload any `json:"payload,omitempty"`
	// Subject is the version stamp of the projection this frame COMPLETES, set by
	// the writer inside the critical section that minted it. Nil on a frame that
	// completes no certified projection; the client's version map ignores those.
	Subject *SubjectStamp `json:"subject,omitempty"`
	Type    EventType     `json:"type"`
	ChatID  ChatID        `json:"chat_id,omitempty"`
}

// SubjectStamp names one digest subject at one version. Kind and Ref spell the
// subject the way internal/subject does; Version is opaque to the client and
// compared by equality only. Epoch is filled by REST responses alone, so a
// response issued under a previous hub epoch is refused by the client's map.
type SubjectStamp struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Version string `json:"version"`
	Epoch   string `json:"epoch,omitempty"`
}

// NewSubjectStamp builds the stamp a frame carries, or nil when version is
// empty: a mutation that moved no counter certifies nothing, and a frame with no
// Subject leaves the client's map untouched.
func NewSubjectStamp(kind, ref, version string) *SubjectStamp {
	if version == "" {
		return nil
	}
	return &SubjectStamp{Kind: kind, Ref: ref, Version: version}
}

// NewEvent constructs a ServerEvent with a typed payload, providing
// compile-time type safety at the construction site.
func NewEvent[T any](t EventType, chatID ChatID, payload T) ServerEvent {
	return ServerEvent{Type: t, ChatID: chatID, Payload: payload}
}

// StopReason identifies why a turn ended. The underlying string is
// the wire value sent over SSE; typed constants prevent typos at
// construction sites.
type StopReason string

// StopReasonEndTurn and the following constants define the valid StopReason values for turn termination.
const (
	StopReasonEndTurn     StopReason = "end_turn"
	StopReasonCancelled   StopReason = "cancelled"
	StopReasonInterrupted StopReason = "interrupted"
	// StopReasonRefusal is kiro-cli 2.13+'s core ACP stop reason for a model
	// refusal (content_filtered): the refusal explanation streamed as the last
	// assistant chunk, tagged with _meta.kiro.refusal (marotte.RefusalInfo).
	StopReasonRefusal StopReason = "refusal"
	// StopReasonUnknown is a turn whose end marotte had to infer. Its one producer
	// is a wire turn_start arriving with the previous turn still open: that turn's
	// own end never came, so nothing on the wire says why it stopped.
	StopReasonUnknown StopReason = "unknown"
	// StopReasonError is KAS's own value for a turn the backend failed, and it is
	// one of the two reasons this enum is OPEN: ACP spec v1's union is closed at
	// five values and KAS exceeds it.
	StopReasonError StopReason = "error"
	// StopReasonContentFiltered is the raw spelling of a refusal. It reaches the
	// same outcome as StopReasonRefusal; both are the model declining rather than
	// anything malfunctioning.
	StopReasonContentFiltered StopReason = "content_filtered"
	// StopReasonMaxTokens and StopReasonMaxTurnRequests are a turn stopped at a
	// bound. The work that was allowed COMPLETED, and the answer is cut off, which
	// is what TurnConclusion.Truncated carries.
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	// StopReasonUnterminated is marotte's OWN value, never KAS's: the entry log's
	// store-open closer stamps it on a turn no process could have closed, and the
	// merge's synthesized closer stamps it on a turn neither side closed. It is a
	// member of this enum rather than a literal at the closer, because the value
	// reaches the generated client decoder, which rejects a stop reason outside
	// this union.
	StopReasonUnterminated StopReason = "unterminated"
)

// SSE event type constants. Using these instead of bare string literals
// makes typos a compile error and the full event vocabulary discoverable.
const (
	EventChatCreated       EventType = "chat_created"
	EventChatUpdated       EventType = "chat_updated"
	EventChatDeleted       EventType = "chat_deleted"
	EventChatStatus        EventType = "chat_status"
	EventCodeReferences    EventType = "code_references"
	EventCompactionStarted EventType = "compaction_started"
	EventConnected         EventType = "connected"
	// EventDecisionSettled retires an ask on every surface that did NOT answer
	// it. The three *_needed events below are offered to every tab and to a
	// watching run tab at once, while only the first answer is accepted, so
	// something has to close the others — and it carries attribution, because a
	// card that collapses for no stated reason reads as a lost click.
	EventDecisionSettled EventType = "decision_settled"
	// EventDraftChanged carries a chat's parked composer state after a
	// set_draft or set_attachments write, so a device that is NOT typing in that
	// chat converges on it instead of holding whatever it saw last. Chat-scoped
	// and deliberately its own event rather than a header field: it fires on a
	// 600ms debounce while someone types, and chat_updated is re-rendered by
	// every client.
	EventDraftChanged      EventType = "draft_changed"
	EventError             EventType = "error"
	EventElicitationNeeded EventType = "elicitation_needed"
	EventUserInputNeeded   EventType = "user_input_needed"
	EventMCPConfigChanged  EventType = "mcp_config_changed"
	EventMCPConnected      EventType = "mcp_connected"
	EventMCPDisconnected   EventType = "mcp_disconnected"
	EventMCPFailed         EventType = "mcp_failed"
	EventMCPOAuthNeeded    EventType = "mcp_oauth_needed"
	// EventMCPPoolChanged is an empty refetch signal for GET /api/mcp/pool: a
	// bridge's pool was replaced or dropped.
	EventMCPPoolChanged  EventType = "mcp_pool_changed"
	EventMCPPrewarm      EventType = "mcp_prewarm"
	EventModeChanged     EventType = "mode_changed"
	EventOpenExternalURL EventType = "open_external_url"
	// EventPermissionNeeded carries a turn's verdict as well as an individual
	// tool ask. There are no pending_change_* or pending_trust_* events:
	// staged writes are KAS's.
	EventPermissionNeeded   EventType = "permission_needed"
	EventPermissionsChanged EventType = "permissions_changed"
	// EventPendingSnapshot is the whole pending set (permissions, run asks,
	// steers, across every chat) as ONE frame on a v3 connect, stamped with the
	// `pending` version. Possibly empty: an empty set is what clears a row that
	// was resolved elsewhere while the client was away.
	EventPendingSnapshot EventType = "pending_snapshot"
	EventPolicyError     EventType = "policy_error"
	// EventRunStarted and the two below are the workflow-run lifecycle. Three,
	// not six — see domain_workflow.go for what the other three would have been
	// and why none of them can exist. All ride the launching chat's topic.
	EventRunStarted  EventType = "run_started"
	EventRunProgress EventType = "run_progress"
	EventRunFinished EventType = "run_finished"
	// EventRunInputNeeded is a workflow STEP asking a person a question. It carries its payload
	// because the question is on no endpoint: KAS parks the run with a fixed pauseReason. Keyed to
	// the launching chat, else to `run:<workflowId>`.
	EventRunInputNeeded EventType = "run_input_needed"
	// EventRunInputSettled retires a run ask on every surface that did not answer
	// it, the run-shaped twin of decision_settled. Separate from that event
	// because a run ask is identified by a string, not by an int64 request id.
	EventRunInputSettled EventType = "run_input_settled"
	EventForgesChanged   EventType = "forges_changed"
	EventGovernanceState EventType = "governance_state"
	EventHooksChanged    EventType = "hooks_changed"
	// EventSlashCommandsChanged and EventSteeringIssuesChanged are empty refetch
	// signals for GET /api/slash-commands and GET /api/steering/issues.
	EventSlashCommandsChanged  EventType = "slash_commands_changed"
	EventSteeringIssuesChanged EventType = "steering_issues_changed"
	EventSafetyStatus          EventType = "safety_status"
	EventSafetyProperties      EventType = "safety_properties"
	// EventKnowledgeIndexing reports a custom agent's own knowledge base being
	// indexed, chat-scoped. The Settings list never shows these bases.
	EventKnowledgeIndexing EventType = "knowledge_indexing"
	// EventRecipesChanged is an empty refetch signal for GET /api/recipes.
	EventRecipesChanged EventType = "recipes_changed"
	// EventPowersChanged is an empty refetch signal for GET /api/powers.
	EventPowersChanged   EventType = "powers_changed"
	EventSettingsUpdated EventType = "settings_updated"
	// EventStatusSnapshot is every retained waiting_on_user row as ONE frame on a
	// v3 connect, stamped with the `status` version; the per-row chat_status
	// replay is the legacy-connect form. Possibly empty, for pending_snapshot's
	// reason.
	EventStatusSnapshot EventType = "status_snapshot"
	// EventSubjectChanged is a fetch instruction and nothing else: the frame that
	// should have carried this stamp was too large for the hub, so the client
	// refetches the subject through its action and observes on commit. The
	// payload is empty; the stamp rides the envelope's Subject.
	EventSubjectChanged EventType = "subject_changed"
	// EventForgeInventory carries one forge connection's pull-request inventory
	// entry each time a cycle writes it, stamped with its forge_inventory version.
	// Workspace-global; the payload type is forges.InventoryChangedPayload.
	EventForgeInventory EventType = "forge_inventory"
	// EventTabsChanged is ONE aggregate frame per committed mutation of the open-tab set (changed,
	// removed, the new order), stamped with that mutation's version. One type, so version and event
	// are one-to-one and a parent's close with children is one frame.
	EventTabsChanged EventType = "tabs_changed"
	// EventSpecChanged says a spec directory's files changed on disk. One frame
	// per coalescing window, workspace-global, payload names the directory.
	EventSpecChanged EventType = "spec_changed"
	// EventSpecApproved says a human approved one phase of a spec. Pure
	// invalidation, workspace-global, payload names the directory: the client
	// refetches that spec, exactly as forges_changed and subject_changed do.
	//
	// Deliberately NOT a reuse of EventSpecChanged. That one means the FILES
	// moved and is coalesced workspace-globally by the watcher, and conflating
	// the two would make an approval look like a document edit — which is the
	// one thing the badge exists to tell apart.
	EventSpecApproved EventType = "spec_approved"
	// EventSteerQueued says a steer reached KAS's buffer, and it is the only
	// steering EVENT: whether the model then read it or a boundary dropped it is
	// the `steer` entry's own `state`, which arrives as entry_appended and is
	// durable, where an event is not.
	EventSteerQueued EventType = "steer_queued"
	// EventAgentNotice is a notice the AGENT produced (a workflow step or subagent reporting into
	// the launching session), arriving on KAS's steering channel. Its own event rather than a
	// steer_queued, because the composer's chip row speaks only of the user's outbound messages;
	// severity is required.
	EventAgentNotice EventType = "agent_notice"
	// EventSystemNotice is KAS's _kiro/system/notify: a notice about the model
	// connection (high load, a paused or retried stream, a model switched for the
	// account), never an error and never an agent's own words.
	EventSystemNotice    EventType = "system_notice"
	EventTerminalCreated EventType = "terminal_created"
	EventTerminalExited  EventType = "terminal_exited"
	EventTerminalOutput  EventType = "terminal_output"
	EventToolJobChanged  EventType = "tool_job_changed"
	EventToolJobOutput   EventType = "tool_job_output"
	EventWorkingLabel    EventType = "working_label"
	// The entry log's live path (events_entries.go): every frame is an append to
	// the log a client can also GET, a delta into an open entry, or a live-only
	// replace of a turn-level value the turn_close will carry.
	EventTurnOpened    EventType = "turn_opened"
	EventEntryOpened   EventType = "entry_opened"
	EventEntryDelta    EventType = "entry_delta"
	EventEntrySealed   EventType = "entry_sealed"
	EventEntryAppended EventType = "entry_appended"
	EventToolProgress  EventType = "tool_progress"
	EventTurnClosed    EventType = "turn_closed"
)

// labelRunning is the working label shared by the running-process kinds
// (shell/execute commands, plain commands, and MCP tool calls).
const labelRunning = "Running"

// workingLabelByKind maps each tool kind with a fixed working label to that
// label. Kinds whose label depends on runtime data (ToolKindExecute and
// ToolKindShell incorporate the title) are handled in WorkingLabelForKind
// directly; ToolKindOther and any unrecognized kind fall back to
// WorkingLabelThinking. Keep this in sync with the ToolKind constants.
var workingLabelByKind = map[ToolKind]string{
	ToolKindRead:       "Reading",
	ToolKindSearch:     "Searching",
	ToolKindFetch:      "Fetching",
	ToolKindEdit:       "Writing",
	ToolKindWrite:      "Writing",
	ToolKindThink:      "Reasoning",
	ToolKindDelete:     "Deleting",
	ToolKindMove:       "Moving",
	ToolKindCommand:    labelRunning,
	ToolKindBrowser:    "Browsing",
	ToolKindSwitchMode: "Switching",
	ToolKindMCP:        labelRunning,
	ToolKindHook:       "Running hook",
}

// WorkingLabelForKind maps a tool kind to a human-readable label.
// Matches ASAI's VV() function from the frontend reducer.
func WorkingLabelForKind(kind ToolKind, title string) string {
	if kind == ToolKindExecute || kind == ToolKindShell {
		if title != "" {
			return labelRunning + " " + title
		}
		return labelRunning
	}
	if label, ok := workingLabelByKind[kind]; ok {
		return label
	}
	return WorkingLabelThinking
}

// Working-label constants. Centralised so agent callers reference these
// instead of bare string literals; a future label rename lands in one place.
const (
	WorkingLabelThinking = "Thinking"
	WorkingLabelApproval = "Waiting for approval"
	WorkingLabelInput    = "Waiting for input"
)
