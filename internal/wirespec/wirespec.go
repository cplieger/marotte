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
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/server"
	"github.com/cplieger/marotte/internal/marotte"
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
	wiregen.TypeRef[marotte.Block](),
	wiregen.TypeRef[marotte.CodeReference](),
	wiregen.TypeRef[marotte.RefusalInfo](),
	// Before Message, which references it.
	wiregen.TypeRef[marotte.Attachment](),
	wiregen.TypeRef[marotte.Message](),
	// After Message, which it carries. Registered so the single-chat GET's `live_turn`
	// reads a GENERATED decoder rather than a hand-mirrored one — the required block_base
	// cannot then be optional on one side only. No `Payload` suffix, so the SSE-binding
	// test exempts it by construction, like ToolCallBulk.
	wiregen.TypeRef[marotte.LiveTurn](),
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
	wiregen.TypeRef[marotte.ConnectedPayload](),
	wiregen.TypeRef[marotte.SubjectStamp](),
	wiregen.TypeRef[marotte.PendingSnapshotPayload](),
	wiregen.TypeRef[marotte.StatusRow](),
	wiregen.TypeRef[marotte.StatusSnapshotPayload](),
	wiregen.TypeRef[marotte.MessageChunkPayload](),
	wiregen.TypeRef[marotte.TurnEndedPayload](),
	wiregen.TypeRef[marotte.SteerQueuedPayload](),
	wiregen.TypeRef[marotte.SteerInjectedPayload](),
	wiregen.TypeRef[marotte.SteerClearedPayload](),
	wiregen.TypeRef[marotte.AgentNoticePayload](),
	// Before the two types that reference it.
	wiregen.TypeRef[marotte.TabSubject](),
	wiregen.TypeRef[marotte.TabsChangedPayload](),
	wiregen.TypeRef[marotte.TabList](),
	wiregen.TypeRef[marotte.MCPToolIdentity](),
	wiregen.TypeRef[marotte.PermissionNeededPayload](),
	wiregen.TypeRef[marotte.ErrorPayload](),
	wiregen.TypeRef[marotte.MCPConnectedPayload](),
	wiregen.TypeRef[marotte.MCPOAuthPayload](),
	wiregen.TypeRef[marotte.MCPFailedPayload](),
	wiregen.TypeRef[marotte.MCPDisconnectedPayload](),
	wiregen.TypeRef[marotte.ChatDeletedPayload](),
	wiregen.TypeRef[marotte.DraftChangedPayload](),
	wiregen.TypeRef[marotte.ToolCallPayload](),
	wiregen.TypeRef[marotte.ToolCallUpdatePayload](),
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
	wiregen.TypeRef[marotte.RunStepPayload](),
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
	wiregen.TypeRef[forges.Repo](),
	wiregen.TypeRef[forges.PR](),
	wiregen.TypeRef[forges.Issue](),
	wiregen.TypeRef[forges.Check](),
	wiregen.TypeRef[forges.Release](),
	wiregen.TypeRef[forges.Label](),
	wiregen.TypeRef[forges.User](),
	wiregen.TypeRef[forges.DeviceFlowResponse](),
	wiregen.TypeRef[forges.PollResult](),
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
	"Role": {}, "EventKind": {}, "ToolKind": {}, "ToolStatus": {},
	"PlanStatus": {},
	"StopReason": {}, "ErrorCode": {}, "Kind": {}, // forges.Kind → ForgeKind
	// The rule producing it is implemented in BOTH languages, so a hand-written
	// client union would be a second enumeration of one vocabulary.
	"TurnOutcome": {},
	// The client BRANCHES on it — a steer renders as a note rather than a system
	// row — and that branch must be total.
	"UserKind": {},
	// Five client surfaces BRANCH on it, and those branches must be total over the
	// vocabulary.
	"TurnSeverity": {},
	"SafetyStatus": {},
	// The client's label switch over it must be TOTAL.
	"SteerOrigin": {},
	// The renderer BRANCHES on it — a not-delivered steer renders as a different
	// note from a delivered one — and absence is a third answer that branch has to
	// keep, so a hand-written union would be a second enumeration of one vocabulary.
	"SteerState":      {},
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
	// So the nine kinds have ONE definition across both languages. It was a
	// hand-written union in tabs.ts, so a kind added server-side reached a client
	// switch with no case for it and no build error anywhere, and TabSubject.kind
	// now fails the generated decoder at the boundary instead.
	"TabKind": {},
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

// typeMessage is named because 3 SSE events decode to marotte.Message.
const typeMessage = "Message"

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
	{EventType: "message_appended", TypeName: typeMessage},
	{EventType: "message_chunk", TypeName: "MessageChunkPayload"},
	{EventType: "message_created", TypeName: typeMessage},
	{EventType: "message_updated", TypeName: typeMessage},
	{EventType: "open_external_url", TypeName: "OpenExternalURLPayload"},
	{EventType: "permission_needed", TypeName: "PermissionNeededPayload"},
	{EventType: "permissions_changed", TypeName: "PermissionsChangedPayload"},
	{EventType: "policy_error", TypeName: "PolicyErrorPayload"},
	{EventType: "run_started", TypeName: "RunStartedPayload"},
	{EventType: "run_progress", TypeName: "RunProgressPayload"},
	{EventType: "run_finished", TypeName: "RunFinishedPayload"},
	{EventType: "run_step", TypeName: "RunStepPayload"},
	{EventType: "run_input_needed", TypeName: "RunInputNeededPayload"},
	{EventType: "run_input_settled", TypeName: "RunInputSettledPayload"},
	{EventType: "safety_properties", TypeName: "SafetyPropertiesPayload"},
	{EventType: "safety_status", TypeName: "SafetyStatusPayload"},
	{EventType: "tool_call", TypeName: "ToolCallPayload"},
	{EventType: "tool_call_update", TypeName: "ToolCallUpdatePayload"},
	{EventType: "tool_job_changed", TypeName: "ToolJobChangedPayload"},
	{EventType: "tool_job_output", TypeName: "ToolJobOutputPayload"},
	{EventType: "steer_queued", TypeName: "SteerQueuedPayload"},
	{EventType: "steer_injected", TypeName: "SteerInjectedPayload"},
	{EventType: "steer_cleared", TypeName: "SteerClearedPayload"},
	{EventType: "agent_notice", TypeName: "AgentNoticePayload"},
	// The agent-terminal trio.
	{EventType: "terminal_created", TypeName: "TerminalCreatedPayload"},
	{EventType: "terminal_output", TypeName: "TerminalOutputPayload"},
	{EventType: "terminal_exited", TypeName: "TerminalExitedPayload"},
	{EventType: "turn_ended", TypeName: "TurnEndedPayload"},
	{EventType: "tabs_changed", TypeName: "TabsChangedPayload"},
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
		wiregen.WithHeaderComment("// CODE-GENERATED by cmd/wire-codegen, DO NOT EDIT.\n\n"),
	)
	r.Types = slices.Clone(wireTypes)
	r.Enums = maps.Clone(wireEnums)
	r.EnumTSName = maps.Clone(enumTSNames)
	r.PathNameOverride = maps.Clone(pathNameOverrides)
	r.SSEEvents = slices.Clone(sseEvents)
	return r
}
