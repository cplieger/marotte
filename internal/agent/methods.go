package agent

import "github.com/cplieger/marotte/internal/marotte"

// ACP method name constants, centralised so a protocol rename is one edit.

// v3 (KAS) `_kiro/*` notification names; the unhandled ones are noopMethods.
const (
	methodV3MCPStatus            = "_kiro/mcp/status"                        // consolidated MCP server state (connected/failed/oauth)
	methodV3MCPResetServer       = "_kiro/mcp/resetServer"                   // C→A request: reconnect a named server; {serverName, startOAuth?} → {success}
	methodV3MCPGetPrompt         = "_kiro/mcp/getPrompt"                     // C→A request: resolve a prompt; {serverName, promptName, arguments} → {messages[]}
	methodV3MCPGetResource       = marotte.MethodMCPGetResource              // C→A request: read a resource; {serverName, uri} → {contents[]}
	methodV3SessionsChanged      = "_kiro/sessions/changed"                  // session inventory diff (no client consumer on v3)
	methodV3SpecPhaseCheckpoint  = "_kiro/spec/phaseCheckpoint"              // A→C notification: {sessionId, featureName, phase, artifactPath} per accepted in-process spec-document write (spec_checkpoint.go)
	methodV3RateLimit            = "_kiro/error/rate_limit"                  // {sessionId, message}
	methodV3CustomAgentNotFound  = "_kiro/customAgent/not_found"             // {sessionId, requestedAgent, fallbackAgent}
	methodV3CustomAgentConfigErr = "_kiro/customAgent/config_error"          // {sessionId, path, error}
	methodV3SystemNotify         = "_kiro/system/notify"                     // {level, message} → system_notice (see init_errors.go)
	methodV3Governance           = "_kiro/governance/state"                  // org/account feature-flag policy → governance_state SSE + GET /api/governance (HandleGovernanceState)
	methodV3ToolsDidChange       = "_kiro/tools/didChange"                   // recognised-ignored (tool catalog; marotte fetches via REST)
	methodV3SteeringDocs         = "_kiro/steering/documents_changed"        // consumed for configIssues (GET /api/steering/issues)
	methodV3ProgressiveContext   = "_kiro/progressive_context/items_changed" // recognised-ignored (skills/steering list; REST-sourced)
	methodV3Powers               = "_kiro/powers/items_changed"              // installed Powers moved: re-render the legacy block, broadcast powers_changed
	methodV3CodeReferences       = "_kiro/code_references"                   // licensed-code attributions → per-turn chip (HandleCodeReferences)
)

// KAS native Cedar policy methods: list/explain are utility-bridge requests for the read-only
// view; changed/error are notifications. policy/check is never used: it raises a real
// session/request_permission. explain raises none.
const (
	methodV3PermissionsList    = "_kiro/permissions/list"    // C→A request: {sessionId, scope?} → {rules[]}
	methodV3PermissionsExplain = "_kiro/permissions/explain" // C→A request: {sessionId, capability|toolId, resource?} → {effect, matchedRule?, scope, source, isExplicitAsk}
	methodV3PolicyChanged      = "_kiro/policy/changed"      // A→C notification: {sessionId, status, errors?}
	methodV3PolicyError        = "_kiro/policy/error"        // A→C notification: {sessionId, errors[]}
)

// v3 request names outside the notification set: openExternalUrl is A→C (bridge_v3_hostreq.go), the others C→A on the utility bridge.
const (
	methodKiroOpenExternalURL = "_kiro/openExternalUrl"  // A→C request: {url} — open a URL for the user (MCP OAuth); needs the openExternalUrl client capability
	methodKiroGetUsage        = "_kiro/account/getUsage" // C→A request: account/subscription usage; needs the authenticated profile
	methodKiroCodeIntel       = "_kiro/codeIntelligence" // C→A request: code-intelligence status/init (subcommand param); needs the session opted in via initialize _meta.kiro.settings
)

// methodKiroSessionNotify is the notification KAS's `send_message` builtin raises:
// `{sessionId, callerSessionId, message, severity, workflowId?, nodeId?}`. `message` is the only
// carrier of a step's question (KAS's pause detail is empty); a `session/prompt` to
// callerSessionId reroutes into the run.
const methodKiroSessionNotify = "_kiro/session/notify"

// KAS's filesystem verbs, each gated on `clientCapabilities.fs._meta.kiro.<name>`. Undeclared,
// KAS runs the same operation unconfined. Read/write stay undeclared so
// fs/{read,write}_text_file keeps its guardrails.
const (
	methodKiroFSStat          = "_kiro/fs/stat"
	methodKiroFSReadDirectory = "_kiro/fs/read_directory"
	methodKiroFSDelete        = "_kiro/fs/delete"
)

// KAS credential-storage requests, A→C only (client→agent returns -32603), built only when
// initialize declares `_meta.kiro.secretStorage`. KAS swallows a get failure as absent but rethrows
// a store or delete failure into MCP connect.
const (
	methodKiroSecretGet    = "_kiro/secret/get"
	methodKiroSecretStore  = "_kiro/secret/store"  //nolint:gosec // G101: ACP method name, not a credential
	methodKiroSecretDelete = "_kiro/secret/delete" //nolint:gosec // G101: ACP method name, not a credential
)

// `_kiro/spec/*` is deliberately unwired: its invoke verbs drive fire-and-forget turns with no turn-end signal.

// KAS memory verbs, utility-bridge requests with no sessionId: list {limit 1..1000, cursor?,
// scopes?} → {memories, nextCursor?}; get/update → {memory}; delete → {id, title}. Ineligible
// accounts get -32602.
const (
	methodKiroMemoryList   = "_kiro/memory/list"
	methodKiroMemoryGet    = "_kiro/memory/get"
	methodKiroMemoryUpdate = "_kiro/memory/update"
	methodKiroMemoryDelete = "_kiro/memory/delete"
)

// KAS knowledge methods. _kiro/knowledge is dispatched by `subcommand` with no sessionId (the
// global store). The two indexing notifications fire only for a custom agent's own bases.
const (
	methodKiroKnowledge                  = "_kiro/knowledge"                   // C→A request: {subcommand, ...} → {success, entries?/message?}
	methodKiroKnowledgeIndexingStarted   = "_kiro/knowledge/indexingStarted"   // A→C: {sessionId, name, fileCount}
	methodKiroKnowledgeIndexingCompleted = "_kiro/knowledge/indexingCompleted" // A→C: {sessionId, name, status: success|failed, itemCount?}
	methodKiroConfigTemplate             = "_kiro/config/template"             // C→A request (2.14+): {} → {modes:{availableModes,currentModeId}, configOptions[]} — session-less catalog
	// workspacePaths is a required array; anything else fails -32603 "not iterable".
	methodKiroWorkflowList    = "_kiro/workflow/list"    // C→A request: {workspacePaths[]} → {runs[]}
	methodKiroWorkflowInspect = "_kiro/workflow/inspect" // C→A request: {workflowId} → {workflowId, state, nodePlan}
)

// The nine workflow lifecycle notifications, KAS's KIND_TO_METHOD table. Payloads carry
// `parentSessionId` when parented and no top-level `sessionId` except node_start (the step's).
// They arrive on the launching chat's bridge.
const (
	methodWFRunStart     = "_kiro/workflow/run_start"     // {workflowId, workflowName, inputs, nodeTree[], parentSessionId?}
	methodWFRunComplete  = "_kiro/workflow/run_complete"  // {workflowId, status, finalState}
	methodWFNodeStart    = "_kiro/workflow/node_start"    // {workflowId, nodeId, nodePath[], type, agentName?, sessionId?, iteration?, branchId?}
	methodWFNodeComplete = "_kiro/workflow/node_complete" // {workflowId, nodeId, nodePath[], status, artifacts?, capturedOutput?}
	methodWFNodePaused   = "_kiro/workflow/node_paused"   // {workflowId, nodeId, nodePath[], reason} — note `reason`, not `pauseReason`
	// The heal reads `pauseDetail` ({class, code, occurredAt}), not the reason prose KAS re-renders
	// for parallel branches. Absent for an interruption, permanent failure or need-input park.
	methodWFPaused        = "_kiro/workflow/paused"         // {workflowId, pauseReason, pauseDetail?}
	methodWFLoopIteration = "_kiro/workflow/loop_iteration" // {workflowId, loopId, iteration, stopConditionMet}
	methodWFWatchPoll     = "_kiro/workflow/watch_poll"     // {workflowId, nodeId, nodePath[], outcome, at}
	methodWFStepsQueued   = "_kiro/workflow/steps_queued"   // {workflowId, pendingSteps[], resolution?}
)

// C→A workflow verbs beyond list/inspect. listRecipes' `source` is the launch key
// (`bundled://<name>` or a *.workflow.json path). new requires `parentSessionId` (kiro-cli 2.21.4);
// marotte sends the run bridge's own session, which then routes all nine lifecycle frames and
// supplies the workspace roots and model/effort fallbacks. invoke is fire-and-forget; cancel
// and resume act at node boundaries.
const (
	methodKiroWorkflowListRecipes = "_kiro/workflow/listRecipes"
	methodKiroWorkflowNew         = "_kiro/workflow/new"
	methodKiroWorkflowInvoke      = "_kiro/workflow/invoke"
	methodKiroWorkflowCancel      = "_kiro/workflow/cancel"
	// The only verb that removes a run from `_kiro/workflow/list`; cancel only settles a status.
	methodKiroWorkflowDelete = "_kiro/workflow/delete"
	methodKiroWorkflowResume = "_kiro/workflow/resume"
	// Stops the run at the next node boundary. cancel's `targetStatus` is never sent, so a stop
	// records the same status whichever door was used.
	methodKiroWorkflowPause = "_kiro/workflow/pause"

	// Resets a failed or aborted run's failed and aborted nodes plus ancestors; KAS loads an
	// unregistered run itself (0.66.22). Resetting zero nodes succeeds.
	methodKiroWorkflowRetry = "_kiro/workflow/retry"

	// marotte narrows this to a step-status update; `replace_remaining` is not wired. A `nodeId`
	// targets only a repeat paused at maxIterations.
	methodKiroWorkflowUpdate = "_kiro/workflow/update"

	// The recipe catalog changed; GET /api/recipes stays the one reader.
	methodKiroWorkflowRecipesChanged = "_kiro/workflow/recipes_changed"
)

// KAS hook methods: list/setEnabled are utility-bridge requests gated on v2Hooks, didChange a
// notification. triggerHook and executeHook are deliberately absent: naming one makes it reachable.
const (
	methodKiroHooksList       = "_kiro/hooks/list"       // C→A request: {workspacePaths?,trigger?,toolId?,includeDisabled?} → {hooks[]}
	methodKiroHooksSetEnabled = "_kiro/hooks/setEnabled" // C→A request: {hookId, enabled} → {success, code?, error?}
	methodKiroHooksDidChange  = "_kiro/hooks/didChange"  // A→C notification: {hooks[]}
)

// KAS Infrastructure-Safety methods: getProperties returns [] by default; the two notifications
// fire only with the infrastructureSafety capability and an AWS governance flag. No set RPC exists.
const (
	methodV3SafetyGetProperties = "_kiro/safety/getProperties"     // C→A request (reachable; not wired — inert by default)
	methodV3SafetyPropertiesChg = "_kiro/safety/propertiesChanged" // A→C notification → safety_properties SSE
	methodV3SafetyStatusChanged = "_kiro/safety/statusChanged"     // A→C notification → safety_status SSE
)

// Terminal methods: v3 uses snake_case terminal/wait_for_exit.
const (
	methodTermPrefix      = "terminal/"
	methodTermCreate      = "terminal/create"
	methodTermOutput      = "terminal/output"
	methodTermRelease     = "terminal/release"
	methodTermWaitForExit = "terminal/wait_for_exit"
	methodTermKill        = "terminal/kill"
)
