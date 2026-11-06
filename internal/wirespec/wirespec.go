// Package wirespec is the single source of truth for marotte's wire contract: the
// registered wire types, the enums, the TS-name and path-name overrides, and the SSE
// events cmd/wire-codegen feeds into wiregen to emit static-src/wire/*.gen.ts.
//
// Build-time only: imported by cmd/wire-codegen and tests, never the server, or
// go/packages would enter the binary. No endpoint table: the server's routes are not built
// from one, so a table here would be an unverified copy of the routing.
package wirespec

import (
	"github.com/cplieger/go/wiregen"
	"github.com/cplieger/marotte/internal/auth"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/forges"
	"github.com/cplieger/marotte/internal/git"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/mcp"
	"github.com/cplieger/marotte/internal/server"
)

// wireTypes is every Go type the generator emits a TypeScript declaration for.
var wireTypes = []wiregen.WireType{
	wiregen.TypeRef[marotte.ToolLocation](),
	wiregen.TypeRef[marotte.ToolDiff](),
	wiregen.TypeRef[marotte.ToolCheckpoint](),
	wiregen.TypeRef[marotte.ToolDisclosed](),
	wiregen.TypeRef[marotte.ToolDenialRule](),
	wiregen.TypeRef[marotte.ToolDenial](),
	wiregen.TypeRef[marotte.ToolOffload](),
	wiregen.TypeRef[marotte.ToolInteraction](),
	wiregen.TypeRef[marotte.TextSpan](),
	wiregen.TypeRef[marotte.ToolTruncation](),
	wiregen.TypeRef[marotte.ToolCall](),
	// A REST response. No `Payload` suffix, so the SSE-binding test exempts it.
	wiregen.TypeRef[marotte.ToolCallBulk](),
	wiregen.TypeRef[marotte.PlanEntry](),
	wiregen.TypeRef[marotte.CodeReference](),
	wiregen.TypeRef[marotte.RefusalInfo](),
	wiregen.TypeRef[marotte.Attachment](),
	// The turn entry log: the envelope, the open-entry shape and the payloads. No `Payload` suffix.
	wiregen.TypeRef[marotte.Entry](),
	wiregen.TypeRef[marotte.OpenEntry](),
	wiregen.TypeRef[marotte.Caller](),
	wiregen.TypeRef[marotte.EntryPrompt](),
	wiregen.TypeRef[marotte.EntryTurnOpen](),
	wiregen.TypeRef[marotte.EntryTurnBind](),
	wiregen.TypeRef[marotte.WakeSteer](),
	wiregen.TypeRef[marotte.EntryTurnWake](),
	wiregen.TypeRef[marotte.EntryRunNamed](),
	wiregen.TypeRef[marotte.EntryText](),
	wiregen.TypeRef[marotte.EntryThinking](),
	wiregen.TypeRef[marotte.EntryToolCall](),
	wiregen.TypeRef[marotte.EntryToolResult](),
	wiregen.TypeRef[marotte.EntrySteer](),
	wiregen.TypeRef[marotte.EntrySteerAck](),
	wiregen.TypeRef[marotte.EntrySteerDelivered](),
	wiregen.TypeRef[marotte.EntrySteerCleared](),
	wiregen.TypeRef[marotte.EntryPlan](),
	wiregen.TypeRef[marotte.EntryCompaction](),
	wiregen.TypeRef[marotte.EntryCompactionFailed](),
	wiregen.TypeRef[marotte.EntrySafetyBlocked](),
	wiregen.TypeRef[marotte.EntryModelSwitched](),
	wiregen.TypeRef[marotte.EntryModelRouted](),
	wiregen.TypeRef[marotte.EntryModelFallback](),
	wiregen.TypeRef[marotte.EntryModeSwitched](),
	wiregen.TypeRef[marotte.EntryTurnRevert](),
	wiregen.TypeRef[marotte.EntryContextReset](),
	wiregen.TypeRef[marotte.EntryReconciled](),
	wiregen.TypeRef[marotte.MeteringItem](),
	wiregen.TypeRef[marotte.Usage](),
	wiregen.TypeRef[marotte.SessionMode](),
	wiregen.TypeRef[marotte.SessionModel](),
	wiregen.TypeRef[marotte.OutputStyle](),
	wiregen.TypeRef[marotte.SessionEffortLevel](),
	// Registered so the pre-session catalog fetch reads a generated decoder, not a cast.
	wiregen.TypeRef[marotte.ConfigTemplateResponse](),
	wiregen.TypeRef[marotte.QueuedPrompt](),
	wiregen.TypeRef[marotte.ChatHeader](),
	wiregen.TypeRef[marotte.PermissionOption](),
	wiregen.TypeRef[marotte.ApprovalFile](),
	wiregen.TypeRef[marotte.PermissionWatch](),
	wiregen.TypeRef[marotte.FileChange](),
	wiregen.TypeRef[marotte.TurnFirstOutput](),
	wiregen.TypeRef[marotte.ContextPart](),
	wiregen.TypeRef[marotte.ContextItem](),
	wiregen.TypeRef[marotte.ContextMedia](),
	wiregen.TypeRef[marotte.ContextCategory](),
	wiregen.TypeRef[marotte.ContextBreakdown](),
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
	wiregen.TypeRef[marotte.TangentMergedPayload](),
	wiregen.TypeRef[marotte.SettingsUpdatedPayload](),
	wiregen.TypeRef[marotte.AgentNoticePayload](),
	wiregen.TypeRef[marotte.SystemNoticePayload](),
	wiregen.TypeRef[marotte.TabSubject](),
	wiregen.TypeRef[marotte.TabsChangedPayload](),
	wiregen.TypeRef[marotte.TabList](),
	// A close_tab or delete_chat confirmation, and the impact a close_impact_changed refusal answers.
	wiregen.TypeRef[marotte.ChatWork](),
	wiregen.TypeRef[marotte.CloseImpact](),
	wiregen.TypeRef[marotte.PreviewGrantRequest](),
	wiregen.TypeRef[marotte.PreviewHint](),
	wiregen.TypeRef[marotte.PreviewGrant](),
	wiregen.TypeRef[marotte.PreviewStamp](),
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
	wiregen.TypeRef[marotte.PermissionCommand](),
	wiregen.TypeRef[marotte.PermissionNeededPayload](),
	wiregen.TypeRef[marotte.ErrorPayload](),
	wiregen.TypeRef[marotte.MCPConnectedPayload](),
	wiregen.TypeRef[marotte.MCPOAuthPayload](),
	wiregen.TypeRef[marotte.MCPSignInOpenPayload](),
	wiregen.TypeRef[marotte.MCPSignInClosedPayload](),
	wiregen.TypeRef[marotte.MCPFailedPayload](),
	wiregen.TypeRef[marotte.MCPDisconnectedPayload](),
	wiregen.TypeRef[marotte.ChatDeletedPayload](),
	wiregen.TypeRef[marotte.DraftChangedPayload](),
	wiregen.TypeRef[marotte.ElicitationPropertySchema](),
	wiregen.TypeRef[marotte.ElicitationEnumOption](),
	wiregen.TypeRef[marotte.ElicitationItemsSchema](),
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
	wiregen.TypeRef[marotte.RunStanding](),
	wiregen.TypeRef[marotte.SessionListResponse](),
	wiregen.TypeRef[marotte.LiveRun](),
	wiregen.TypeRef[marotte.LiveRunsResponse](),
	// REST replies; no `Payload` suffix, so the SSE-binding test exempts them.
	wiregen.TypeRef[marotte.RunControlsResponse](),
	wiregen.TypeRef[marotte.RunRetriedResponse](),
	wiregen.TypeRef[marotte.RunLaunchRequest](),
	wiregen.TypeRef[marotte.RunLaunchedResponse](),
	// A request the client composes, generated so a rename cannot land on one side only.
	wiregen.TypeRef[marotte.RunAnswerRequest](),
	wiregen.TypeRef[marotte.RunStepMessageRequest](),
	wiregen.TypeRef[marotte.RunStepMessageResponse](),
	wiregen.TypeRef[marotte.RunStepSteerRequest](),
	wiregen.TypeRef[marotte.RunExtendRequest](),
	wiregen.TypeRef[marotte.RunFinishLoopRequest](),
	wiregen.TypeRef[marotte.RunStepEndRequest](),
	wiregen.TypeRef[marotte.RunStartedPayload](),
	wiregen.TypeRef[marotte.RunProgressPayload](),
	wiregen.TypeRef[marotte.RunFinishedPayload](),
	wiregen.TypeRef[marotte.NotificationPayload](),
	// One step waiting on a person: run_asks's element and GET /api/runs/{id}'s `open_asks`. No
	// `Payload` suffix, so the SSE-binding test exempts it.
	wiregen.TypeRef[marotte.RunAsk](),
	wiregen.TypeRef[marotte.RunAsksPayload](),
	// GET /api/runs/{id}'s `plan_update`, a read reply too.
	wiregen.TypeRef[marotte.RunPlanUpdate](),
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
	// GET /api/engine and the engine_status event.
	wiregen.TypeRef[marotte.EngineRestart](),
	wiregen.TypeRef[marotte.EngineBusyTurn](),
	wiregen.TypeRef[marotte.EngineBusyRun](),
	wiregen.TypeRef[marotte.EngineBusy](),
	wiregen.TypeRef[marotte.EngineExit](),
	wiregen.TypeRef[marotte.EngineStatusVersion](),
	wiregen.TypeRef[marotte.EngineStatus](),
	// GET /api/config/standing.
	wiregen.TypeRef[marotte.ConfigStanding](),
	wiregen.TypeRef[marotte.AgentLoadError](),
	wiregen.TypeRef[marotte.ConfigStandingResponse](),
	// GET /api/settings. No omitempty, so the client holds no defaults of its own.
	wiregen.TypeRef[marotte.KiroDefault](),
	wiregen.TypeRef[marotte.EffectiveSettings](),
	wiregen.TypeRef[forges.ForgeError](),
	wiregen.TypeRef[forges.ConfiguredForge](),
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
	// POST /api/forges/login's 200 answer.
	wiregen.TypeRef[forges.LoginResult](),
	wiregen.TypeRef[forges.Detection](),
	wiregen.TypeRef[auth.WhoamiResponse](),
	// POST /api/login's 200 answer: the attempt the login poll waits on.
	wiregen.TypeRef[auth.LoginResponse](),
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
	// The file viewer: stat, the whole read, the save reply and their refusal body.
	wiregen.TypeRef[filebrowse.FileStat](),
	wiregen.TypeRef[filebrowse.FileRead](),
	wiregen.TypeRef[filebrowse.FileWriteResult](),
	wiregen.TypeRef[filebrowse.FileRefusal](),
	// GET /api/git/show, the diff view's base side, and its refusal.
	wiregen.TypeRef[git.ShowResponse](),
	wiregen.TypeRef[git.ShowRefusal](),
	// GET /api/mcp/registry/search and its entry family.
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

// Members are the type's constants in its own package. Each is a vocabulary a client branch or
// wording table must cover TOTALLY, so one generated union makes a missing arm a compile error in
// both languages.
var wireEnums = []wiregen.EnumRef{
	wiregen.StringEnum[marotte.ToolKind](),
	wiregen.StringEnum[marotte.ToolStatus](),
	// The denial block words each basis, so its Record must be total.
	wiregen.StringEnum[marotte.DenialBasis](),
	wiregen.StringEnum[marotte.PlanStatus](),
	wiregen.StringEnum[marotte.StopReason](),
	wiregen.StringEnum[marotte.ErrorCode](),
	wiregen.StringEnum[forges.Kind](),
	// Sources, the PR tab and the merge dialog each switch over it.
	wiregen.StringEnum[forges.ForgeStatus](),
	// The rule producing it is implemented in BOTH languages.
	wiregen.StringEnum[marotte.TurnOutcome](),
	// turn_close.failure_kind; the turn notice's remedy button branches on it.
	wiregen.StringEnum[marotte.FailureKind](),
	// Five client surfaces branch on it.
	wiregen.StringEnum[marotte.TurnSeverity](),
	// The entry dispatcher's switch; the decoder rejects a kind with no arm.
	wiregen.StringEnum[marotte.EntryKind](),
	// The revert's cause, keying the client's wording record.
	wiregen.StringEnum[marotte.TurnRevertCause](),
	// The reset row words each cause, keying the client's wording record.
	wiregen.StringEnum[marotte.ContextResetCause](),
	// The turn header words each wake reason and note, so both tables must be total.
	wiregen.StringEnum[marotte.WakeReason](),
	wiregen.StringEnum[marotte.WakeNote](),
	// The banner says who switched the mode.
	wiregen.StringEnum[marotte.ModeSwitchSource](),
	// The client words a KAS repin's reason, so its wording table must be total.
	wiregen.StringEnum[marotte.ModelSwitchReason](),
	// turn_open.source, spelled once for both languages.
	wiregen.StringEnum[marotte.TurnOpenSourceName](),
	wiregen.StringEnum[marotte.SafetyStatus](),
	wiregen.StringEnum[marotte.KnowledgeIndexingPhase](),
	wiregen.StringEnum[marotte.KnowledgeIndexingStatus](),
	// The client's label switch over it must be TOTAL.
	wiregen.StringEnum[marotte.SteerOrigin](),
	// The renderer branches on it, and absence is a third answer.
	wiregen.StringEnum[marotte.SteerState](),
	// Worded, not branched on: `Record<SteerReason, string>` fails on an unworded reason.
	wiregen.StringEnum[marotte.SteerReason](),
	// The dock's row controls branch on it, so the branch must be total.
	wiregen.StringEnum[marotte.SteerRowState](),
	// The toast colour is chosen by it, so the client fold must be total.
	wiregen.StringEnum[marotte.NoticeLevel](),
	// A notification's kind picks its Settings switch on the page.
	wiregen.StringEnum[marotte.PushKind](),
	wiregen.StringEnum[marotte.RunProgressKind](),
	// The client folds over both status vocabularies.
	wiregen.StringEnum[marotte.RunStatus](),
	wiregen.StringEnum[marotte.RunNodeStatus](),
	// History words each standing, so the fold must be total.
	wiregen.StringEnum[marotte.RunStandingKind](),
	// The run composer words the delivery and each refusal, so both folds must be total.
	wiregen.StringEnum[marotte.RunStepMessageVerb](),
	wiregen.StringEnum[marotte.RunStepMessageRefusal](),
	// The run tab sends one of these, so the client cannot spell a status KAS refuses.
	wiregen.StringEnum[marotte.RunStepEndStatus](),
	wiregen.StringEnum[marotte.RunStepEndRefusal](),
	// A card labels its text and words its blocker from these, and the answer's refusal from the code.
	wiregen.StringEnum[marotte.RunAskText](),
	wiregen.StringEnum[marotte.RunAskBlocker](),
	wiregen.StringEnum[marotte.RunAskRefusal](),
	// Registered for CatalogState's reason below.
	wiregen.StringEnum[marotte.RunStepTranscriptState](),
	wiregen.StringEnum[marotte.DecisionKind](),
	wiregen.StringEnum[marotte.SettledBy](),
	wiregen.StringEnum[marotte.AlwaysAllowBlock](),
	wiregen.StringEnum[marotte.RejectionNote](),
	// One definition of the tab kinds; TabSubject.kind fails the decoder on an unknown one.
	wiregen.StringEnum[marotte.TabKind](),
	// The composer's placeholder and the chat-options menu's radio branch on it.
	wiregen.StringEnum[marotte.InterruptMode](),
	// The spec page labels a segment by its role.
	wiregen.StringEnum[marotte.SpecDocRole](),
	// The client branches on the verdict to decide whether to retry and what to say.
	wiregen.StringEnum[marotte.CatalogState](),
	wiregen.StringEnum[marotte.CatalogReason](),
	// Registered for CatalogState's reason: the History picker branches on it.
	wiregen.StringEnum[marotte.ReadState](),
	// "marotte could not ask" must render a retry, not a sign-in prompt.
	wiregen.StringEnum[auth.WhoamiState](),
	wiregen.StringEnum[mcp.Transport](),
	// The engine banner and the Settings restart control branch on it.
	wiregen.StringEnum[marotte.EngineState](),
	// Each kind resolves to a different rendered surface; the decoder is strict.
	wiregen.StringEnum[chat.SegmentKind](),
	// The client branches on it to say "wait" or "narrow the query".
	wiregen.StringEnum[mcp.RegistryFailureReason](),
	// Directory, name and content rows open differently; the decoder is strict.
	wiregen.StringEnum[filebrowse.FileMatchKind](),
	// The MCP panel treats "user" as marotte's own row and words every other origin.
	wiregen.StringEnum[marotte.Origin](),
	// The preview toolbar maps a hint onto its width radios, so the map is total.
	wiregen.StringEnum[marotte.PreviewPreset](),
	wiregen.StringEnum[marotte.PreviewHintSource](),
	// The slash menu disables prompt and steering rows mid-turn.
	wiregen.StringEnum[marotte.SlashCommandKind](),
	// The viewer's refusal arms and the stale-save surface branch on these.
	wiregen.StringEnum[filebrowse.RefusalCode](),
	wiregen.StringEnum[filebrowse.ContentKind](),
	// The diff view names the view a refused base gets from it.
	wiregen.StringEnum[git.ShowRefusalCode](),
}

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

// Both directions are asserted by TestRegistry_EveryRegisteredPayloadHasAnSSEBinding.
var wireEvents = []wiregen.Event{
	{Name: "chat_created", Data: wiregen.TypeRef[marotte.ChatHeader]()},
	{Name: "chat_deleted", Data: wiregen.TypeRef[marotte.ChatDeletedPayload]()},
	{Name: "chat_updated", Data: wiregen.TypeRef[marotte.ChatHeader]()},
	{Name: "code_references", Data: wiregen.TypeRef[marotte.CodeReferencesPayload]()},
	{Name: "connected", Data: wiregen.TypeRef[marotte.ConnectedPayload]()},
	{Name: "decision_settled", Data: wiregen.TypeRef[marotte.DecisionSettledPayload]()},
	{Name: "draft_changed", Data: wiregen.TypeRef[marotte.DraftChangedPayload]()},
	{Name: "pending_snapshot", Data: wiregen.TypeRef[marotte.PendingSnapshotPayload]()},
	{Name: "status_snapshot", Data: wiregen.TypeRef[marotte.StatusSnapshotPayload]()},
	{Name: "elicitation_needed", Data: wiregen.TypeRef[marotte.ElicitationNeededPayload]()},
	{Name: "user_input_needed", Data: wiregen.TypeRef[marotte.UserInputNeededPayload]()},
	{Name: "error", Data: wiregen.TypeRef[marotte.ErrorPayload]()},
	{Name: "governance_state", Data: wiregen.TypeRef[marotte.GovernanceStatePayload]()},
	{Name: "mcp_connected", Data: wiregen.TypeRef[marotte.MCPConnectedPayload]()},
	{Name: "mcp_disconnected", Data: wiregen.TypeRef[marotte.MCPDisconnectedPayload]()},
	{Name: "mcp_failed", Data: wiregen.TypeRef[marotte.MCPFailedPayload]()},
	{Name: "mcp_oauth_needed", Data: wiregen.TypeRef[marotte.MCPOAuthPayload]()},
	{Name: "mcp_sign_in_open", Data: wiregen.TypeRef[marotte.MCPSignInOpenPayload]()},
	{Name: "mcp_sign_in_closed", Data: wiregen.TypeRef[marotte.MCPSignInClosedPayload]()},
	{Name: "open_external_url", Data: wiregen.TypeRef[marotte.OpenExternalURLPayload]()},
	{Name: "notification", Data: wiregen.TypeRef[marotte.NotificationPayload]()},
	{Name: "permission_needed", Data: wiregen.TypeRef[marotte.PermissionNeededPayload]()},
	{Name: "policy_error", Data: wiregen.TypeRef[marotte.PolicyErrorPayload]()},
	{Name: "run_started", Data: wiregen.TypeRef[marotte.RunStartedPayload]()},
	{Name: "run_progress", Data: wiregen.TypeRef[marotte.RunProgressPayload]()},
	{Name: "run_finished", Data: wiregen.TypeRef[marotte.RunFinishedPayload]()},
	{Name: "run_asks", Data: wiregen.TypeRef[marotte.RunAsksPayload]()},
	{Name: "safety_properties", Data: wiregen.TypeRef[marotte.SafetyPropertiesPayload]()},
	{Name: "safety_status", Data: wiregen.TypeRef[marotte.SafetyStatusPayload]()},
	{Name: "knowledge_indexing", Data: wiregen.TypeRef[marotte.KnowledgeIndexingPayload]()},
	{Name: "tool_job_changed", Data: wiregen.TypeRef[marotte.ToolJobChangedPayload]()},
	{Name: "tool_job_output", Data: wiregen.TypeRef[marotte.ToolJobOutputPayload]()},
	{Name: "steer_queued", Data: wiregen.TypeRef[marotte.SteerQueuedPayload]()},
	{Name: "tangent_merged", Data: wiregen.TypeRef[marotte.TangentMergedPayload]()},
	{Name: "settings_updated", Data: wiregen.TypeRef[marotte.SettingsUpdatedPayload]()},
	{Name: "agent_notice", Data: wiregen.TypeRef[marotte.AgentNoticePayload]()},
	{Name: "system_notice", Data: wiregen.TypeRef[marotte.SystemNoticePayload]()},
	// The agent-terminal trio.
	{Name: "terminal_created", Data: wiregen.TypeRef[marotte.TerminalCreatedPayload]()},
	{Name: "terminal_output", Data: wiregen.TypeRef[marotte.TerminalOutputPayload]()},
	{Name: "terminal_exited", Data: wiregen.TypeRef[marotte.TerminalExitedPayload]()},
	// The entry-log set: one turn, its lanes' entries, and the turn_close.
	{Name: "turn_opened", Data: wiregen.TypeRef[marotte.TurnOpenedPayload]()},
	{Name: "entry_opened", Data: wiregen.TypeRef[marotte.EntryOpenedPayload]()},
	{Name: "entry_delta", Data: wiregen.TypeRef[marotte.EntryDeltaPayload]()},
	{Name: "entry_sealed", Data: wiregen.TypeRef[marotte.EntrySealedPayload]()},
	{Name: "entry_appended", Data: wiregen.TypeRef[marotte.EntryAppendedPayload]()},
	{Name: "tool_progress", Data: wiregen.TypeRef[marotte.ToolProgressPayload]()},
	{Name: "turn_closed", Data: wiregen.TypeRef[marotte.TurnClosedPayload]()},
	{Name: "tabs_changed", Data: wiregen.TypeRef[marotte.TabsChangedPayload]()},
	{Name: "spec_changed", Data: wiregen.TypeRef[marotte.SpecChangedPayload]()},
	{Name: "spec_approved", Data: wiregen.TypeRef[marotte.SpecApprovedPayload]()},
	{Name: "forge_inventory", Data: wiregen.TypeRef[forges.InventoryChangedPayload]()},
	{Name: "engine_status", Data: wiregen.TypeRef[marotte.EngineStatus]()},
}

// Registry returns marotte's wire contract. It holds no output layout: cmd/wire-codegen
// owns where the files go.
func Registry() *wiregen.Registry {
	return &wiregen.Registry{
		Types:            wireTypes,
		Enums:            wireEnums,
		EnumTSName:       enumTSNames,
		PathNameOverride: pathNameOverrides,
		Events:           wireEvents,
	}
}
