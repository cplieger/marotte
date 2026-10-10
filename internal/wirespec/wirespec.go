// Package wirespec is the single source of truth for marotte's wire contract: the
// registered wire types, the enums, the TS-name and path-name overrides, and the SSE
// event-to-decoder table cmd/wire-codegen feeds into wiregen to emit
// static-src/wire/{types,decoders,registry}.gen.ts.
//
// Build-time only: imported by cmd/wire-codegen and tests, never the server, or
// go/packages would enter the binary. No endpoint table: nothing generates a typed client
// or path constants, so one would be an unverified copy of the routing.
package wirespec

import (
	"maps"
	"slices"

	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/wiregen/v3"
)

// wireTypes is every Go type the generator emits a TypeScript declaration for.
// ORDER IS SIGNIFICANT: a type must be declared before any type referencing it.
var wireTypes = []wiregen.WireType{
	wiregen.TypeRef[marotte.ToolLocation](),
	wiregen.TypeRef[marotte.ToolDiff](),
	wiregen.TypeRef[marotte.ToolCheckpoint](),
	// Before ToolCall, which references both.
	wiregen.TypeRef[marotte.ToolDisclosed](),
	wiregen.TypeRef[marotte.ToolDenialRule](),
	wiregen.TypeRef[marotte.ToolDenial](),
	wiregen.TypeRef[marotte.ToolOffload](),
	wiregen.TypeRef[marotte.ToolInteraction](),
	wiregen.TypeRef[marotte.TextSpan](),
	wiregen.TypeRef[marotte.ToolTruncation](),
	wiregen.TypeRef[marotte.ToolCall](),
	// A REST response, after ToolDiff and TextSpan. No `Payload` suffix, so the SSE-binding
	// test exempts it.
	wiregen.TypeRef[marotte.ToolCallBulk](),
	wiregen.TypeRef[marotte.PlanEntry](),
	wiregen.TypeRef[marotte.CodeReference](),
	wiregen.TypeRef[marotte.RefusalInfo](),
	wiregen.TypeRef[marotte.Attachment](),
	// The turn entry log: the envelope, the open-entry shape and the payloads, each after the
	// types it references (EntryTurnClose after FileChange below). No `Payload` suffix.
	wiregen.TypeRef[marotte.Entry](),
	wiregen.TypeRef[marotte.OpenEntry](),
	wiregen.TypeRef[marotte.EntryPrompt](),
	wiregen.TypeRef[marotte.EntryTurnOpen](),
	wiregen.TypeRef[marotte.EntryTurnBind](),
	wiregen.TypeRef[marotte.EntryText](),
	wiregen.TypeRef[marotte.EntryThinking](),
	wiregen.TypeRef[marotte.EntryToolCall](),
	wiregen.TypeRef[marotte.EntryToolResult](),
	wiregen.TypeRef[marotte.EntrySteer](),
	wiregen.TypeRef[marotte.EntrySteerAck](),
	wiregen.TypeRef[marotte.EntryPlan](),
	wiregen.TypeRef[marotte.EntryCompaction](),
	wiregen.TypeRef[marotte.EntryCompactionFailed](),
	wiregen.TypeRef[marotte.EntrySafetyBlocked](),
	wiregen.TypeRef[marotte.EntryModelSwitched](),
	wiregen.TypeRef[marotte.EntryModeSwitched](),
	wiregen.TypeRef[marotte.EntryTurnRevert](),
	wiregen.TypeRef[marotte.EntryReconciled](),
	wiregen.TypeRef[marotte.MeteringItem](),
	wiregen.TypeRef[marotte.Usage](),
	wiregen.TypeRef[marotte.SessionMode](),
	wiregen.TypeRef[marotte.SessionModel](),
	wiregen.TypeRef[marotte.SessionEffortLevel](),
	// Registered so the pre-session catalog fetch reads a generated decoder, not a cast.
	wiregen.TypeRef[marotte.ConfigTemplateResponse](),
	// Before ChatHeader, which holds it.
	wiregen.TypeRef[marotte.QueuedPrompt](),
	wiregen.TypeRef[marotte.ChatHeader](),
	wiregen.TypeRef[marotte.PermissionOption](),
	wiregen.TypeRef[marotte.ApprovalFile](),
	wiregen.TypeRef[marotte.PermissionWatch](),
	wiregen.TypeRef[marotte.FileChange](),
	wiregen.TypeRef[marotte.TurnThroughput](),
	wiregen.TypeRef[marotte.EntryTurnClose](),
	wiregen.TypeRef[marotte.ConnectedPayload](),
	wiregen.TypeRef[marotte.SubjectStamp](),
	wiregen.TypeRef[marotte.PendingSnapshotPayload](),
	wiregen.TypeRef[marotte.StatusRow](),
	wiregen.TypeRef[marotte.StatusSnapshotPayload](),
	wiregen.TypeRef[marotte.TurnOpenedPayload](),
	wiregen.TypeRef[marotte.EntryOpenedPayload](),
	wiregen.TypeRef[marotte.EntryDeltaPayload](),
	wiregen.TypeRef[marotte.EntrySealedPayload](),
	wiregen.TypeRef[marotte.EntryAppendedPayload](),
	wiregen.TypeRef[marotte.ToolProgressPayload](),
	wiregen.TypeRef[marotte.TurnClosedPayload](),
	wiregen.TypeRef[marotte.SteerQueuedPayload](),
	wiregen.TypeRef[marotte.AgentNoticePayload](),
	wiregen.TypeRef[marotte.SystemNoticePayload](),
	// Before the two types that reference it.
	wiregen.TypeRef[marotte.TabSubject](),
	wiregen.TypeRef[marotte.TabsChangedPayload](),
	wiregen.TypeRef[marotte.TabList](),
	wiregen.TypeRef[marotte.PreviewGrantRequest](),
	wiregen.TypeRef[marotte.PreviewHint](),
	wiregen.TypeRef[marotte.PreviewGrant](),
	wiregen.TypeRef[marotte.PreviewStamp](),
	// Leaves before the documents that hold them; SpecTaskNode references itself.
	wiregen.TypeRef[marotte.SpecProgress](),
	wiregen.TypeRef[marotte.SpecTruncated](),
	wiregen.TypeRef[marotte.SpecTaskNode](),
	wiregen.TypeRef[marotte.SpecDoc](),
	wiregen.TypeRef[marotte.SpecApproval](),
	wiregen.TypeRef[marotte.Spec](),
	wiregen.TypeRef[marotte.SpecChangedPayload](),
	wiregen.TypeRef[marotte.SpecApprovedPayload](),
	wiregen.TypeRef[marotte.MCPToolIdentity](),
	wiregen.TypeRef[marotte.PermissionConsent](),
	wiregen.TypeRef[marotte.PermissionNeededPayload](),
	wiregen.TypeRef[marotte.ErrorPayload](),
	wiregen.TypeRef[marotte.MCPConnectedPayload](),
	wiregen.TypeRef[marotte.MCPOAuthPayload](),
	wiregen.TypeRef[marotte.MCPFailedPayload](),
	wiregen.TypeRef[marotte.MCPDisconnectedPayload](),
	wiregen.TypeRef[marotte.ChatDeletedPayload](),
	wiregen.TypeRef[marotte.DraftChangedPayload](),
	wiregen.TypeRef[marotte.ElicitationPropertySchema](),
	wiregen.TypeRef[marotte.ElicitationRequestSchema](),
	wiregen.TypeRef[marotte.ElicitationNeededPayload](),
	wiregen.TypeRef[marotte.UserInputSubOption](),
	wiregen.TypeRef[marotte.UserInputOption](),
	wiregen.TypeRef[marotte.UserInputNeededPayload](),
	wiregen.TypeRef[marotte.DecisionSettledPayload](),
	wiregen.TypeRef[marotte.OpenExternalURLPayload](),
	wiregen.TypeRef[marotte.CodeReferencesPayload](),
	wiregen.TypeRef[marotte.AccountUsageBreakdown](),
	wiregen.TypeRef[marotte.AccountUsage](),
	wiregen.TypeRef[marotte.PolicyRuleCore](),
	wiregen.TypeRef[marotte.PolicyRule](),
	wiregen.TypeRef[marotte.SecurityProfile](),
	wiregen.TypeRef[marotte.PolicyView](),
	wiregen.TypeRef[marotte.PolicyExplainResult](),
	wiregen.TypeRef[marotte.PolicyErrorItem](),
	wiregen.TypeRef[marotte.PermissionsChangedPayload](),
	wiregen.TypeRef[marotte.PolicyErrorPayload](),
	wiregen.TypeRef[marotte.SafetyProperty](),
	wiregen.TypeRef[marotte.SafetyStatusPayload](),
	wiregen.TypeRef[marotte.KnowledgeIndexingPayload](),
	wiregen.TypeRef[marotte.SafetyPropertiesPayload](),
	wiregen.TypeRef[marotte.GovernanceFeatures](),
	wiregen.TypeRef[marotte.GovernanceStatePayload](),
	wiregen.TypeRef[marotte.GovernanceLock](),
	wiregen.TypeRef[marotte.GovernanceMCPRegistry](),
	wiregen.TypeRef[marotte.GovernanceRegistryServer](),
	wiregen.TypeRef[marotte.ToolJob](),
	wiregen.TypeRef[marotte.ToolRateLimit](),
	wiregen.TypeRef[marotte.ToolInfo](),
	wiregen.TypeRef[marotte.SystemTool](),
	wiregen.TypeRef[marotte.AptPackage](),
	wiregen.TypeRef[marotte.ToolsList](),
	wiregen.TypeRef[marotte.ToolCatalogHit](),
	wiregen.TypeRef[marotte.ToolsSearchResponse](),
	wiregen.TypeRef[marotte.ToolJobAccepted](),
	wiregen.TypeRef[marotte.ToolRemoveResponse](),
	wiregen.TypeRef[marotte.ToolsJobsResponse](),
	wiregen.TypeRef[marotte.ToolCatalogInfo](),
	// GET /api/slash-commands and GET /api/steering/issues.
	wiregen.TypeRef[marotte.SlashArgument](),
	wiregen.TypeRef[marotte.SlashCommand](),
	wiregen.TypeRef[marotte.SlashCommandsResponse](),
	wiregen.TypeRef[marotte.SteeringIssue](),
	wiregen.TypeRef[marotte.SteeringIssuesResponse](),
	wiregen.TypeRef[marotte.Recipe](),
	wiregen.TypeRef[marotte.RecipesResponse](),
	// GET /api/sessions, so the History picker can tell "nothing to resume" from a failed read.
	wiregen.TypeRef[marotte.ResumableSession](),
	wiregen.TypeRef[marotte.WorkflowRun](),
	wiregen.TypeRef[marotte.SessionListResponse](),
	// Before LiveRunsResponse, which references it.
	wiregen.TypeRef[marotte.LiveRun](),
	wiregen.TypeRef[marotte.LiveRunsResponse](),
	// REST replies; no `Payload` suffix, so the SSE-binding test exempts them.
	wiregen.TypeRef[marotte.RunControlsResponse](),
	wiregen.TypeRef[marotte.RunRetriedResponse](),
	wiregen.TypeRef[marotte.RunLaunchRequest](),
	wiregen.TypeRef[marotte.RunLaunchedResponse](),
	// A request the client composes, generated so a rename cannot land on one side only.
	wiregen.TypeRef[marotte.RunAnswerRequest](),
	wiregen.TypeRef[marotte.RunExtendRequest](),
	wiregen.TypeRef[marotte.RunFinishLoopRequest](),
	wiregen.TypeRef[marotte.RunStartedPayload](),
	wiregen.TypeRef[marotte.RunProgressPayload](),
	wiregen.TypeRef[marotte.RunFinishedPayload](),
	wiregen.TypeRef[marotte.RunInputNeededPayload](),
	wiregen.TypeRef[marotte.RunInputSettledPayload](),
	// GET /api/runs/{id}'s `open_asks`, a read reply, so the SSE-binding test exempts it.
	wiregen.TypeRef[marotte.RunOpenAsk](),
	// GET /api/runs/{id}'s `step_ends`, a read reply like `open_asks`.
	wiregen.TypeRef[marotte.RunStepEnd](),
	wiregen.TypeRef[marotte.RunStepStart](),
	// GET /api/runs/{id}/steps/{path}. No omitempty, so `state` is REQUIRED in TypeScript.
	wiregen.TypeRef[marotte.RunStepTranscript](),
	wiregen.TypeRef[marotte.ToolJobChangedPayload](),
	wiregen.TypeRef[marotte.ToolJobOutputPayload](),
	wiregen.TypeRef[marotte.TerminalCreatedPayload](),
	wiregen.TypeRef[marotte.TerminalOutputPayload](),
	wiregen.TypeRef[marotte.TerminalExitedPayload](),
	// GET /api/settings. No omitempty, so the client holds no defaults of its own.
	wiregen.TypeRef[marotte.EffectiveSettings](),
	wiregen.TypeRef[forges.ConfiguredForge](),
	// The list envelopes after the rows and parts they hold.
	wiregen.TypeRef[forges.PartialResult](),
	wiregen.TypeRef[forges.RepoSuccessor](),
	wiregen.TypeRef[forges.Affordance](),
	wiregen.TypeRef[forges.RepoAffordances](),
	// GET /api/forges/{id}/capabilities, both scopes keyed by capability.
	wiregen.TypeRef[forges.Capabilities](),
	wiregen.TypeRef[forges.Repo](),
	wiregen.TypeRef[forges.RepoList](),
	wiregen.TypeRef[forges.PRAction](),
	wiregen.TypeRef[forges.FieldFill](),
	wiregen.TypeRef[forges.PR](),
	wiregen.TypeRef[forges.PRList](),
	wiregen.TypeRef[forges.Issue](),
	wiregen.TypeRef[forges.IssueList](),
	wiregen.TypeRef[forges.Check](),
	wiregen.TypeRef[forges.CommitChecks](),
	wiregen.TypeRef[forges.PRDetail](),
	wiregen.TypeRef[forges.MergeOutcome](),
	wiregen.TypeRef[forges.MergeResult](),
	wiregen.TypeRef[forges.PRChanged](),
	wiregen.TypeRef[forges.MergeStatus](),
	wiregen.TypeRef[forges.CloneRepo](),
	wiregen.TypeRef[forges.InventoryError](),
	wiregen.TypeRef[forges.InventoryBudget](),
	wiregen.TypeRef[forges.InventoryScope](),
	wiregen.TypeRef[forges.InventoryEntry](),
	wiregen.TypeRef[forges.InventoryList](),
	wiregen.TypeRef[forges.InventoryRefresh](),
	wiregen.TypeRef[forges.OwnerScopes](),
	wiregen.TypeRef[forges.ProbeResult](),
	wiregen.TypeRef[forges.InventoryChangedPayload](),
	wiregen.TypeRef[forges.Release](),
	wiregen.TypeRef[forges.ReleaseList](),
	wiregen.TypeRef[forges.Label](),
	wiregen.TypeRef[forges.LabelList](),
	wiregen.TypeRef[forges.DeviceFlowResponse](),
	wiregen.TypeRef[forges.PollResult](),
	wiregen.TypeRef[forges.Detection](),
	wiregen.TypeRef[auth.WhoamiResponse](),
	wiregen.TypeRef[auth.LoginOptions](),
	// The search replies embed textsearch.Tally, which wiregen flattens, so
	// `scanned`/`matched`/`truncated` are REQUIRED on every one.
	wiregen.TypeRef[chat.Hit](),
	wiregen.TypeRef[chat.SearchResult](),
	wiregen.TypeRef[chat.Match](),
	wiregen.TypeRef[chat.SearchAllResult](),
	// GET /api/files/search, the third search reply on the same tally.
	wiregen.TypeRef[filebrowse.MatchRange](),
	wiregen.TypeRef[filebrowse.FileMatch](),
	wiregen.TypeRef[filebrowse.FileSearchResult](),
	// GET /api/mcp/registry/search, the entry family before the reply that holds it.
	wiregen.TypeRef[mcp.RegistryEnvVar](),
	wiregen.TypeRef[mcp.RegistryHeader](),
	wiregen.TypeRef[mcp.RegistryPackage](),
	wiregen.TypeRef[mcp.RegistryRemote](),
	wiregen.TypeRef[mcp.RegistryEntry](),
	wiregen.TypeRef[mcp.RegistrySearchResult](),
	wiregen.TypeRef[mcp.RegistrySearchFailure](),
	// GET /api/workspace/kiro-docs.
	wiregen.TypeRef[server.KiroDoc](),
	wiregen.TypeRef[server.KiroDocsResponse](),
}

// wireEnums names the string enums to emit; values are auto-discovered from each type's
// const block. Each is a vocabulary a client branch or wording table must cover TOTALLY,
// so one generated union makes a missing arm a compile error in both languages.
var wireEnums = map[string]wiregen.EnumDef{
	"ToolKind": {}, "ToolStatus": {},
	"PlanStatus": {},
	"StopReason": {}, "ErrorCode": {}, "Kind": {}, // forges.Kind → ForgeKind
	// The rule producing it is implemented in BOTH languages.
	"TurnOutcome": {},
	// turn_close.failure_kind; the turn notice's remedy button branches on it.
	"FailureKind": {},
	// Five client surfaces branch on it.
	"TurnSeverity": {},
	// The entry dispatcher's switch; the decoder rejects a kind with no arm.
	"EntryKind": {},
	// The revert's cause, keying the client's wording record.
	"TurnRevertCause": {},
	// The banner says who switched the mode.
	"ModeSwitchSource": {},
	// The client words a KAS repin's reason, so its wording table must be total.
	"ModelSwitchReason": {},
	// turn_open.source, spelled once for both languages.
	"TurnOpenSourceName":      {},
	"SafetyStatus":            {},
	"KnowledgeIndexingPhase":  {},
	"KnowledgeIndexingStatus": {},
	// The client's label switch over it must be TOTAL.
	"SteerOrigin": {},
	// The renderer branches on it, and absence is a third answer.
	"SteerState": {},
	// Worded, not branched on: `Record<SteerReason, string>` fails on an unworded reason.
	"SteerReason": {},
	// The dock's row controls branch on it, so the branch must be total.
	"SteerRowState": {},
	// The toast colour is chosen by it, so the client fold must be total.
	"NoticeLevel":     {},
	"RunProgressKind": {},
	// The client folds over both status vocabularies.
	"RunStatus":     {},
	"RunNodeStatus": {},
	// Registered for CatalogState's reason below.
	"RunStepTranscriptState": {},
	"DecisionKind":           {},
	"SettledBy":              {},
	"AlwaysAllowBlock":       {},
	// One definition of the tab kinds; TabSubject.kind fails the decoder on an unknown one.
	"TabKind": {},
	// The composer's placeholder and the + menu's radio branch on it.
	"InterruptMode": {},
	// The spec page labels a segment by its role.
	"SpecDocRole": {},
	// The client branches on the verdict to decide whether to retry and what to say.
	"CatalogState":  {},
	"CatalogReason": {},
	// Registered for CatalogState's reason: the History picker branches on it.
	"ReadState": {},
	// "marotte could not ask" must render a retry, not a sign-in prompt.
	"WhoamiState": {},
	"Transport":   {},
	// Each kind resolves to a different rendered surface; the decoder is strict.
	"SegmentKind": {},
	// The client branches on it to say "wait" or "narrow the query".
	"RegistryFailureReason": {},
	// Directory, name and content rows open differently; the decoder is strict.
	"FileMatchKind": {},
	// The MCP panel treats "user" as marotte's own row and words every other origin.
	"Origin": {}, // marotte.Origin → MCPOrigin
	// The preview toolbar maps a hint onto its width radios, so the map is total.
	"PreviewPreset":     {},
	"PreviewHintSource": {},
	// The slash menu disables prompt and steering rows mid-turn.
	"SlashCommandKind": {},
}

// enumTSNames renames an enum on the TypeScript side.
var enumTSNames = map[string]string{
	"Kind":   "ForgeKind", // forges.Kind → ForgeKind in TS
	"Origin": "MCPOrigin", // a bare Origin reads ambiguously beside SteerOrigin
}

// pathNameOverrides pins the snake_case path for a name whose acronym cluster
// cannot be split unambiguously.
var pathNameOverrides = map[string]string{
	"MCPOAuthPayload": "mcp_oauth_payload",
	// URL acronym cluster can't be split unambiguously; pin the path.
	"OpenExternalURLPayload": "open_external_url_payload",
}

// sseEvents binds each SSE event type to the registered struct its payload decodes
// as. Both directions are asserted by
// TestRegistry_EveryRegisteredPayloadHasAnSSEBinding.
var sseEvents = []wiregen.SSERegEntry{
	{EventType: "chat_created", TypeName: "ChatHeader"},
	{EventType: "chat_deleted", TypeName: "ChatDeletedPayload"},
	{EventType: "chat_updated", TypeName: "ChatHeader"},
	{EventType: "code_references", TypeName: "CodeReferencesPayload"},
	{EventType: "connected", TypeName: "ConnectedPayload"},
	{EventType: "decision_settled", TypeName: "DecisionSettledPayload"},
	{EventType: "draft_changed", TypeName: "DraftChangedPayload"},
	{EventType: "pending_snapshot", TypeName: "PendingSnapshotPayload"},
	{EventType: "status_snapshot", TypeName: "StatusSnapshotPayload"},
	{EventType: "elicitation_needed", TypeName: "ElicitationNeededPayload"},
	{EventType: "user_input_needed", TypeName: "UserInputNeededPayload"},
	{EventType: "error", TypeName: "ErrorPayload"},
	{EventType: "governance_state", TypeName: "GovernanceStatePayload"},
	{EventType: "mcp_connected", TypeName: "MCPConnectedPayload"},
	{EventType: "mcp_disconnected", TypeName: "MCPDisconnectedPayload"},
	{EventType: "mcp_failed", TypeName: "MCPFailedPayload"},
	{EventType: "mcp_oauth_needed", TypeName: "MCPOAuthPayload"},
	{EventType: "open_external_url", TypeName: "OpenExternalURLPayload"},
	{EventType: "permission_needed", TypeName: "PermissionNeededPayload"},
	{EventType: "permissions_changed", TypeName: "PermissionsChangedPayload"},
	{EventType: "policy_error", TypeName: "PolicyErrorPayload"},
	{EventType: "run_started", TypeName: "RunStartedPayload"},
	{EventType: "run_progress", TypeName: "RunProgressPayload"},
	{EventType: "run_finished", TypeName: "RunFinishedPayload"},
	{EventType: "run_input_needed", TypeName: "RunInputNeededPayload"},
	{EventType: "run_input_settled", TypeName: "RunInputSettledPayload"},
	{EventType: "safety_properties", TypeName: "SafetyPropertiesPayload"},
	{EventType: "safety_status", TypeName: "SafetyStatusPayload"},
	{EventType: "knowledge_indexing", TypeName: "KnowledgeIndexingPayload"},
	{EventType: "tool_job_changed", TypeName: "ToolJobChangedPayload"},
	{EventType: "tool_job_output", TypeName: "ToolJobOutputPayload"},
	{EventType: "steer_queued", TypeName: "SteerQueuedPayload"},
	{EventType: "agent_notice", TypeName: "AgentNoticePayload"},
	{EventType: "system_notice", TypeName: "SystemNoticePayload"},
	// The agent-terminal trio.
	{EventType: "terminal_created", TypeName: "TerminalCreatedPayload"},
	{EventType: "terminal_output", TypeName: "TerminalOutputPayload"},
	{EventType: "terminal_exited", TypeName: "TerminalExitedPayload"},
	// The entry-log set: one turn, its lanes' entries, and the turn_close.
	{EventType: "turn_opened", TypeName: "TurnOpenedPayload"},
	{EventType: "entry_opened", TypeName: "EntryOpenedPayload"},
	{EventType: "entry_delta", TypeName: "EntryDeltaPayload"},
	{EventType: "entry_sealed", TypeName: "EntrySealedPayload"},
	{EventType: "entry_appended", TypeName: "EntryAppendedPayload"},
	{EventType: "tool_progress", TypeName: "ToolProgressPayload"},
	{EventType: "turn_closed", TypeName: "TurnClosedPayload"},
	{EventType: "tabs_changed", TypeName: "TabsChangedPayload"},
	{EventType: "spec_changed", TypeName: "SpecChangedPayload"},
	{EventType: "spec_approved", TypeName: "SpecApprovedPayload"},
	{EventType: "forge_inventory", TypeName: "InventoryChangedPayload"},
}

// Registry returns the fully-populated wiregen registry: the generator options plus the
// tables above, CLONED because the generator may reorder or extend what it is given.
func Registry() *wiregen.Registry {
	r := wiregen.NewRegistry(
		wiregen.WithValidatorsImport("../validators.js"),
		// Library-owned generated output, rewritten on every run. Never hand-edit it.
		wiregen.WithValidatorsFile("../validators.ts"),
		wiregen.WithBusImport("../bus.js"),
		// The property test iterates ARBITRARY_BY_TYPE, so a new wire type needs an arbitrary.
		wiregen.WithArbitrariesFile("arbitraries.gen.ts"),
		wiregen.WithHeaderComment("// Code generated by cmd/wire-codegen. DO NOT EDIT.\n\n"),
	)
	r.Types = slices.Clone(wireTypes)
	r.Enums = maps.Clone(wireEnums)
	r.EnumTSName = maps.Clone(enumTSNames)
	r.PathNameOverride = maps.Clone(pathNameOverrides)
	r.SSEEvents = slices.Clone(sseEvents)
	return r
}
