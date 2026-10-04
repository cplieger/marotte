// Package wirespec is the single source of truth for marotte's wire contract: the
// registered wire types, the enums, the TS-name and path-name overrides, and the
// SSE event→decoder table cmd/wire-codegen feeds into wiregen to emit
// static-src/wire/{types,decoders,registry}.gen.ts.
//
// wiregen is a BUILD-TIME-ONLY dependency: this package is imported by
// cmd/wire-codegen and by tests, never by the server runtime, or go/packages and
// golang.org/x/tools would enter the server binary.
//
// There is deliberately NO endpoint table: marotte generates neither a typed client
// nor Go path constants, so one here would be an unverified copy of the routing.
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
	wiregen.TypeRef[marotte.TextSpan](),
	wiregen.TypeRef[marotte.ToolTruncation](),
	wiregen.TypeRef[marotte.ToolCall](),
	// A REST response, after ToolDiff and TextSpan, which it references. No
	// `Payload` suffix, so the SSE-binding test exempts it by construction.
	wiregen.TypeRef[marotte.ToolCallBulk](),
	wiregen.TypeRef[marotte.PlanEntry](),
	wiregen.TypeRef[marotte.CodeReference](),
	wiregen.TypeRef[marotte.RefusalInfo](),
	wiregen.TypeRef[marotte.Attachment](),
	// The turn entry log: the envelope, the open-entry shape and the sixteen
	// payloads. Each payload after the types it references (ToolCall's sub-types,
	// PlanEntry, RefusalInfo, CodeReference, Attachment above; FileChange is
	// registered below, so EntryTurnClose sits after it). No `Payload` suffix: these
	// are entry payloads, not SSE payloads, and the binding test keys on the suffix.
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
	// Registered so the pre-session catalog fetch reads a GENERATED decoder rather
	// than an unchecked cast: `modes` was read as `d.modes.length` with nothing
	// behind the claim, so `modes: null` was a TypeError inside the boot path.
	wiregen.TypeRef[marotte.ConfigTemplateResponse](),
	wiregen.TypeRef[marotte.ChatHeader](),
	wiregen.TypeRef[marotte.PermissionOption](),
	wiregen.TypeRef[marotte.ApprovalFile](),
	wiregen.TypeRef[marotte.FileChange](),
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
	wiregen.TypeRef[marotte.SafetyPropertiesPayload](),
	wiregen.TypeRef[marotte.GovernanceFeatures](),
	wiregen.TypeRef[marotte.GovernanceStatePayload](),
	wiregen.TypeRef[marotte.ToolJob](),
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
	wiregen.TypeRef[marotte.Recipe](),
	wiregen.TypeRef[marotte.RecipesResponse](),
	// GET /api/sessions. Registered so the History picker reads the per-list
	// verdicts through a decoder: they had no client reader at all, so "nothing to
	// resume" and "the read failed" rendered identically.
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
	// A request shape the client composes: generated rather than hand-mirrored, so
	// a field rename cannot land on one side only.
	wiregen.TypeRef[marotte.RunAnswerRequest](),
	wiregen.TypeRef[marotte.RunStartedPayload](),
	wiregen.TypeRef[marotte.RunProgressPayload](),
	wiregen.TypeRef[marotte.RunFinishedPayload](),
	wiregen.TypeRef[marotte.RunInputNeededPayload](),
	wiregen.TypeRef[marotte.RunInputSettledPayload](),
	// GET /api/runs/{id}'s `open_asks`. No `Payload` suffix, so the SSE-binding test
	// exempts it: it is a read reply rather than an event payload.
	wiregen.TypeRef[marotte.RunOpenAsk](),
	// GET /api/runs/{id}/steps/{path...}. No field carries omitempty, so `state` is
	// a REQUIRED TypeScript field and a reader cannot invent "assume ready".
	wiregen.TypeRef[marotte.RunStepTranscript](),
	wiregen.TypeRef[marotte.ToolJobChangedPayload](),
	wiregen.TypeRef[marotte.ToolJobOutputPayload](),
	wiregen.TypeRef[marotte.TerminalCreatedPayload](),
	wiregen.TypeRef[marotte.TerminalOutputPayload](),
	wiregen.TypeRef[marotte.TerminalExitedPayload](),
	// GET /api/settings. Every field is required on both sides (no omitempty),
	// which is what lets the client hold no defaults of its own.
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
	// The search replies. Each embeds textsearch.Tally, which wiregen flattens
	// into the reply's own fields, so `scanned`/`matched`/`truncated` are REQUIRED
	// on every one and a client cannot read an absent count as zero.
	wiregen.TypeRef[chat.Hit](),
	wiregen.TypeRef[chat.SearchResult](),
	wiregen.TypeRef[chat.Match](),
	wiregen.TypeRef[chat.SearchAllResult](),
	// GET /api/files/search, the third search reply on the same tally.
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

// wireEnums names the string enums to emit; values are auto-discovered from each
// type's const block in the registered types' packages.
var wireEnums = map[string]wiregen.EnumDef{
	"ToolKind": {}, "ToolStatus": {},
	"PlanStatus": {},
	"StopReason": {}, "ErrorCode": {}, "Kind": {}, // forges.Kind → ForgeKind
	// The rule producing it is implemented in BOTH languages, so a hand-written
	// client union would be a second enumeration of one vocabulary.
	"TurnOutcome": {},
	// Five client surfaces BRANCH on it, and those branches must be total over the
	// vocabulary.
	"TurnSeverity": {},
	// The entry dispatcher's switch over the seventeen kinds must be total, and the
	// decoder rejects a kind the client has no arm for.
	"EntryKind": {},
	// The revert's cause, so the client's wording record is keyed on the generated
	// union and a second cause is a compile error on both sides.
	"TurnRevertCause": {},
	// The banner says who switched the mode, so the client's copy switch over it
	// must be total.
	"ModeSwitchSource": {},
	// turn_open.source, spelled once for both languages.
	"TurnOpenSourceName": {},
	"SafetyStatus":       {},
	// The client's label switch over it must be TOTAL.
	"SteerOrigin": {},
	// The renderer BRANCHES on it — a not-delivered steer renders as a different
	// note from a delivered one — and absence is a third answer that branch has to
	// keep, so a hand-written union would be a second enumeration of one vocabulary.
	"SteerState": {},
	// The client WORDS it rather than branching on it, and the wording table has
	// to be TOTAL over the vocabulary: a reason with no clause renders the bare
	// state, so the note reads as if nothing had gone wrong. A generated union is
	// what makes `Record<SteerReason, string>` fail to compile on a reason nobody
	// worded, which a hand-written union of one language cannot do.
	"SteerReason": {},
	// The dock's row controls branch on it, so the branch must be total.
	"SteerRowState":   {},
	"RunProgressKind": {},
	// Registered for the same reason: the client folds over both status
	// vocabularies, and every fold must stay total.
	"RunStatus":     {},
	"RunNodeStatus": {},
	// Registered for CatalogState's reason below.
	"RunStepTranscriptState": {},
	"DecisionKind":           {},
	"SettledBy":              {},
	"AlwaysAllowBlock":       {},
	// So the ten kinds have ONE definition across both languages. It was a
	// hand-written union in tabs.ts, so a kind added server-side reached a client
	// switch with no case for it and no build error anywhere, and TabSubject.kind
	// now fails the generated decoder at the boundary instead.
	"TabKind": {},
	// The spec page labels a segment by its role, so an unknown one fails the
	// decoder at the boundary rather than rendering an unlabelled segment.
	"SpecDocRole": {},
	// The client BRANCHES on the verdict to decide whether to retry and what to
	// say, so a value it has no case for is the failure the type prevents.
	"CatalogState":  {},
	"CatalogReason": {},
	// Registered for CatalogState's reason: the History picker branches on it.
	"ReadState": {},
	// The client's branch over it must be TOTAL: "marotte could not ask" has to
	// render a retry rather than a sign-in prompt.
	"WhoamiState": {},
	"Transport":   {},
	// A hit names the span it landed in, and the client resolves each kind to a
	// different rendered surface, so the decoder is strict: a kind the client has
	// no arm for fails the reply rather than resolving to no element.
	"SegmentKind": {},
	// The client branches on it to say "wait" or "narrow the query".
	"RegistryFailureReason": {},
	// The client BRANCHES on it — a directory row navigates the browser, a name
	// row opens the editor, a content row opens it at a line — and the decoder is
	// strict, so a kind the client has no arm for fails the reply rather than
	// rendering a row nothing can open.
	"FileMatchKind": {},
	// The preview toolbar maps a hint onto its width radios, so the map is total.
	"PreviewPreset":     {},
	"PreviewHintSource": {},
}

// enumTSNames renames an enum on the TypeScript side.
var enumTSNames = map[string]string{
	"Kind": "ForgeKind", // forges.Kind → ForgeKind in TS
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
	{EventType: "tool_job_changed", TypeName: "ToolJobChangedPayload"},
	{EventType: "tool_job_output", TypeName: "ToolJobOutputPayload"},
	{EventType: "steer_queued", TypeName: "SteerQueuedPayload"},
	{EventType: "agent_notice", TypeName: "AgentNoticePayload"},
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

// Registry returns the fully-populated wiregen registry: the generator options plus
// the declarative tables above.
//
// The tables are CLONED rather than aliased: the generator is free to reorder or
// extend what it is given, and this package's own tests call Registry() twice.
func Registry() *wiregen.Registry {
	r := wiregen.NewRegistry(
		wiregen.WithValidatorsImport("../validators.js"),
		// Library-owned generated output: Generate rewrites it next to the
		// hand-written source on every run. Never hand-edit it.
		wiregen.WithValidatorsFile("../validators.ts"),
		wiregen.WithBusImport("../bus.js"),
		// The property test iterates the emitted ARBITRARY_BY_TYPE, so a wire
		// type added here cannot reach the wire without one.
		wiregen.WithArbitrariesFile("arbitraries.gen.ts"),
		wiregen.WithHeaderComment("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n"),
	)
	r.Types = slices.Clone(wireTypes)
	r.Enums = maps.Clone(wireEnums)
	r.EnumTSName = maps.Clone(enumTSNames)
	r.PathNameOverride = maps.Clone(pathNameOverrides)
	r.SSEEvents = slices.Clone(sseEvents)
	return r
}
