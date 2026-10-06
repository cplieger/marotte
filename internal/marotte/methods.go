package marotte

// ACP protocol method name constants. The complete vocabulary lives here
// so a protocol rename is a single-line change with compile-time
// verification of all consumers across packages.

// Bridge lifecycle methods — used by the bridge package to drive the
// kiro-cli subprocess through initialize → session/new|load. On v3 (KAS)
// model/mode/effort switches all route through session/set_config_option
// (v3 has no session/set_model), and a rewind reverts the session in place at a
// message id (see command/rewind.go).
const (
	MethodInitialize  = "initialize"
	MethodSessionNew  = "session/new"
	MethodSessionLoad = "session/load"
	// MethodSessionList enumerates stored sessions. Scope it with {cwd} —
	// unscoped it returns every session on the box (measured: 399 rows across
	// 55 directories, against 2 for one workspace). `limit` is IGNORED.
	MethodSessionList = "session/list"
	// MethodSessionDelete is ACP's core delete. It cascades the session's
	// workflow runs, refuses (-32000 SessionOwnsLiveWorkflowsError) while one is
	// live, and answers {} for an id that never existed.
	MethodSessionDelete = "session/delete"
	MethodSetMode       = "session/set_mode"
	MethodCancel        = "session/cancel"
	// MethodCheckpointRevertMultiple reverts a session to a USER message, dropping it and
	// everything after and rolling the files back from KAS's snapshots. KAS appends a durable
	// `checkpoint_revert` tombstone that a later session/load replays (applyCheckpointReverts). KAS
	// itself refuses a mid-turn session and a concurrent revert.
	MethodCheckpointRevertMultiple = "_kiro/checkpoint/revertMultiple"
	// MethodSessionFork branches a session (`session/*`, not `_kiro/*`; 2.18.0 sidecar): KAS
	// creates one carrying the forked context and returns `{sessionId}`. Params `{sessionId, cwd,
	// _meta:{kiro:{…}}}`, the `_meta.kiro` block caller-supplied: `createdReason` (reported back on
	// session/load) and `title`. No `messageId` for a tangent: KAS's own /tangent sends none.
	MethodSessionFork = "session/fork"
	// MethodSessionCompact summarizes the conversation and replaces it with the summary, emitting
	// `summarization_completed`; typed `/compact` does nothing (no parser in KAS claims it).
	// `{success: false}` covers a turn in flight and a compaction already running with no
	// discriminator.
	MethodSessionCompact = "_kiro/session/compact"
	// MethodSessionRename sets a session's title as the USER's, latching
	// titleSetByUser so KAS stops emitting agent titles for it. Served for a
	// resident session and, on any other process, as a write to its stored metadata.
	MethodSessionRename = "_kiro/session/rename"
	// MethodSessionExport zips one persisted session ({sessionId} →
	// {success, filePath, error?}) into KAS's own TMPDIR/kiro-exports-* directory
	// under the fixed name kiro-session-<sessionId>.zip. It reads the session off
	// disk, so any process answers it; the caller deletes the file.
	MethodSessionExport = "_kiro/session/export"

	// MethodSessionSteer delivers a mid-turn steer (`_session/*`, KAS's spelling). Params
	// `{sessionId, message, messageId?}`; replies `{queued: true, messageId}` or `{queued: false,
	// messageId, dropped}`; a missing or unknown session and an empty message THROW. KAS buffers it
	// and the graph consumes it at the next node boundary as a human turn.
	// `message` is a plain STRING (no attachments), and KAS classifies text matching
	// `^\s*\[notification/(info|success|warning|error)\]` as a system notification;
	// command/steer.go handles both.
	MethodSessionSteer = "_session/steer"
	// MethodSessionSteerClear drops every steer still queued in the session's
	// buffer before the model reads it. Params `{sessionId}`, reply
	// `{messageIds: [...]}` naming what it dropped; clearing an empty buffer is a
	// no-op returning an empty list.
	//
	// Unlike `session/cancel` this does NOT abort the turn — the in-flight
	// execution keeps running, only the unread steers go away.
	MethodSessionSteerClear = "_session/steer/clear"
)

// File-system protocol method names (ACP fs/* namespace). Exported so
// test files can reference the canonical string without bare literals.
const (
	MethodFSRead  = "fs/read_text_file"
	MethodFSWrite = "fs/write_text_file"
)

// MCP elicitation method name. On v3 (KAS) an MCP server's structured-input
// request is forwarded to us as the extension request _kiro/mcp/elicitation
// (a JSON-RPC request with an id, delivered like fs/*). The params nest the
// elicitation body under an "elicitation" object with sessionId/toolCallId
// at the top level; we surface a form and reply with {action, content} on
// the request id. Gated by the clientCapabilities.elicitation capability
// advertised in bridge.initialize(). v3 has no elicitation-complete method.
const (
	MethodElicitationCreate = "_kiro/mcp/elicitation"
)

// MethodMCPGetResource reads one resource from the MCP pool of the process it
// is sent to: {serverName, uri} → {contents[]}. Each bridge holds its own pool.
const MethodMCPGetResource = "_kiro/mcp/getResource"

// CreatedReasonTangent labels a forked session in KAS's roster, sent as `_meta.kiro.createdReason`
// on MethodSessionFork and reported back on session/load; KAS's own /tangent sends this exact
// string.
const CreatedReasonTangent = "tangent"

// Agent user-input method name. On v3 the agent's user_input tool reaches us as the request
// _kiro/userInput carrying {sessionId, toolCallId, question, options[…]}; we reply
// {action:"answered", answer:"<text>"} on the request id, any other action advancing the phase.
// Gated by `_meta.kiro.userInput:true`, without which KAS flattens it into a permission request and
// skips free-form questions.
const (
	MethodKiroUserInput = "_kiro/userInput"
)

// Session-level ACP method name for prompts.
const (
	MethodPrompt = "session/prompt"
)

// MethodPolicyIgnoreFilesChanged is the whole door for which ignore FILES KAS enforces (no
// `_meta.kiro` key exists). Connection-scope and hot. `{files: []}` CLEARS the list, so an empty
// list is never sent (StartOpts.IgnoreFiles); a malformed payload is a no-op on KAS's side.
const (
	MethodPolicyIgnoreFilesChanged = "_kiro/policy/ignore_files_changed"

	// ParamIgnoreFiles is the notification's one params key, carrying []string.
	ParamIgnoreFiles = "files"
)

// MethodTerminalSettingsChanged updates a live KAS process's shell-tool default
// timeout. Params are {terminal: {enabled, commandTimeoutMs?}}, strict on KAS's
// side; {enabled: false} drops back to KAS's 120 s.
const MethodTerminalSettingsChanged = "_kiro/terminal/settings_changed"

// TerminalSettingsParams builds MethodTerminalSettingsChanged's params for a
// timeout in ms. KAS's schema is strict, so the unset case carries no
// commandTimeoutMs member at all.
func TerminalSettingsParams(ms int) map[string]any {
	terminal := map[string]any{"enabled": ms > 0}
	if ms > 0 {
		terminal["commandTimeoutMs"] = ms
	}
	return map[string]any{"terminal": terminal}
}

// Session-level ACP method names — streaming updates, permissions, config.
const (
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
	MethodSetConfigOption   = "session/set_config_option"
)

// ContentTypeText is the ACP content-block type discriminator for plain text content.
// Used across agent, command, and translate packages; declared here as a single source
// of truth so a protocol rename is one edit.
const ContentTypeText = "text"

// ModelAuto is the sentinel model value meaning "keep current / use
// task-based selection". Used by bridge, agent, and model-switch logic.
const ModelAuto = "auto"

// AgentEngineV3 is the only agent engine marotte speaks. The relay owns
// authentication, while marotte answers the shell-type request and the
// reshaped extension set. The legacy engines are unsupported.
const AgentEngineV3 = "v3"

// Session config-option ids for session/set_config_option (v3/KAS). Model
// and reasoning-effort switches route through set_config_option with one of
// these configId values plus a matching value string. (Verified against the
// KAS 2.12 acp-server bundle: MODEL_CONFIG_ID / EFFORT_LEVEL_CONFIG_ID.)
// Mode switches use the dedicated session/set_mode method, so there is
// deliberately no "mode" configId constant here.
const (
	ConfigOptionModel  = "model"
	ConfigOptionEffort = "effortLevel"
	// ConfigOptionAutopilot is supervised mode on v3. `autopilot: false` makes KAS
	// request a TURN APPROVAL before applying a file-touching turn's writes;
	// `true` (its default at session creation) applies them as they happen.
	//
	// It persists into KAS's own session metadata, so it survives session/load and
	// never needs re-asserting — which is what lets marotte pass it once at
	// session/new instead of policing every write.
	ConfigOptionAutopilot = "autopilot"
	// ConfigOptionMemoryReflection is background memory learning. Its value is the
	// STRING "on" or "off" (a JSON boolean is a silent no-op), and it is the one
	// half of the memory preference KAS lets a client change on a live session.
	ConfigOptionMemoryReflection = "memoryReflection"
	// ConfigOptionThinking is extended thinking, present only for a model whose
	// choice carries `_meta.kiro.thinkingToggleable`. Its value is the STRING
	// ThinkingOn or ThinkingOff; turning it off caps a high effort tier.
	ConfigOptionThinking = "thinking"
	// ConfigOptionContentCollection drives the opt-out header on every model request
	// of the PROCESS, not the session, and KAS persists none of it. The value is the
	// STRING ConfigValueContentCollectionEnabled or ...Disabled; a JSON boolean is
	// ignored.
	ConfigOptionContentCollection = "contentCollection"
)

// The two values KAS's contentCollection option accepts.
const (
	ConfigValueContentCollectionEnabled  = "enabled"
	ConfigValueContentCollectionDisabled = "disabled"
)

// The thinking option's two values, also Chat.Thinking's choices.
const (
	ThinkingOn  = "on"
	ThinkingOff = "off"
)

// The two values KAS's autopilot option accepts: a SELECT over these strings. A bare boolean
// satisfies neither arm of zSetSessionConfigOptionRequest and answers -32602 (kiro-cli 2.20.0),
// leaving the session in autopilot. A pair so both senders spell it alike.
const (
	ConfigValueAutopilotOn  = "on"
	ConfigValueAutopilotOff = "off"
)

// ACP content-block JSON field name constants. These are the wire-format
// keys inside a content block object (distinct from ContentTypeText which
// is the field VALUE). Single source of truth for agent, command, and
// translate packages.
const (
	ContentKeyType = "type"
	ContentKeyText = "text"
)

// TextBlock returns a canonical ACP text content block:
//
//	{"type": "text", "text": content}
//
// Eliminates ad-hoc map construction across agent, command, and translate.
func TextBlock(content string) map[string]any {
	return map[string]any{ContentKeyType: ContentTypeText, ContentKeyText: content}
}

// KeySessionID is the ACP wire key for the session identifier in
// parameter maps. Single source of truth; agent and command packages
// reference this constant instead of bare "sessionId" literals.
const KeySessionID = "sessionId"

// KeyPrompt is the ACP wire key for a `session/prompt` call's content-block
// array. KeySessionID's sibling, for the same reason: three senders build that
// call — a chat's prompt, the utility bridge's own turn, and the answer to a
// parked workflow step — and a bare literal at each is three chances to disagree
// about the spelling of a key KAS validates.
const KeyPrompt = "prompt"
