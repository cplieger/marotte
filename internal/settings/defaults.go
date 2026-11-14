// Defaults and the known-keys set for marotte's `<configDir>/config.json`. Unknown keys are
// warned about, never rejected, because a preference file gains keys per release.

package settings

import "log/slog"

// Settings key names. Reference these instead of bare string literals.
const (
	KeyAgentIgnoreFiles = "agent_ignore_files"

	// KeyAdditionalDirectories lists the directories beyond the workspace that agents may read
	// and write (AgentDirs owns which entries are granted). Default empty.
	KeyAdditionalDirectories = "additional_directories"

	// KeyChatRetentionDays is three states: -1 keeps every chat, 0 deletes at close, N keeps N
	// days. A dropped key falls back to the 7-day default, hence PATCH merges, never replaces.
	KeyChatRetentionDays = "chat_retention_days"

	KeyDebugLogs            = "debug_logs"
	KeyLastModel            = "last_model"
	KeyNotificationsEnabled = "notifications_enabled"

	// KeyLastEffortByModel maps model id to the effort level last picked under it: the preference
	// a chat holding no level for that model takes, then pinned onto Chat.Effort, which owns it
	// from there; both readers (effortSeedFor, getLastEffortFor) look up the chat's model.
	KeyLastEffortByModel = "last_effort_by_model"

	// KeyLastMergeMethod is the PR merge method last picked (forge spelling), the merge dialog's
	// next default when the repository offers it. Empty means none picked.
	KeyLastMergeMethod = "last_merge_method"

	KeyNotifyAgentFinished = "notify_agent_finished"
	KeyNotifyPRStatus      = "notify_pr_status"
	KeyNotifyRunOutcome    = "notify_run_outcome"
	KeySupervisedDefault   = "supervised_default"

	// KeySecurityProfile is the security profile a new chat starts on, and the one observer passes
	// and parentless run hosts open with (a policyfile profile id sent as policy presets). An
	// unset or unknown value resolves to policyfile.DefaultProfile, never to Custom (no presets).
	KeySecurityProfile = "security_profile"

	// KeyScheduledAutoApprove lets a scheduled or triggered run's tool requests be approved instead
	// of refused after the unattended budget; a triggered run can read text other people wrote. Off
	// by default, and its own switch: unattended consent must be chosen, not inherited. The key
	// keeps its name so a saved value keeps working.
	KeyScheduledAutoApprove = "scheduled_auto_approve"

	// keyTriggerMaxLiveRuns and keyTriggerMaxLaunchesPerHour cap every event rule's triggered runs together: the
	// runs live at once and the runs started in any rolling hour (TriggerCaps). Over a cap, events wait queued.
	keyTriggerMaxLiveRuns        = "trigger_max_live_runs"
	keyTriggerMaxLaunchesPerHour = "trigger_max_launches_per_hour"

	// KeyKnowledgeEnabled reaches the agent per spawn under _meta.kiro.settings; it defaults TRUE.
	KeyKnowledgeEnabled  = "knowledge_enabled"
	KeyGuardPayloadLinks = "guard_payload_links"

	// KeyContentCollectionEnabled lets model requests be used by AWS for service
	// improvement. Default on, as in kiro-cli; an organization's lock overrides it.
	// Sent at initialize and pushed live through _kiro/contentCollection/settings_changed.
	KeyContentCollectionEnabled = "content_collection_enabled"

	// KeyMemoryMode is the Memory dropdown (memory.go's Memory* values), resolved per session door by
	// MemoryPreferenceFor. KAS freezes the mode per session; the reflection half is re-sent on
	// session/load.
	KeyMemoryMode = "memory_mode"

	// KeyMCPWaitForReady makes KAS hold each prompt until every enabled MCP server
	// has settled. It reaches the agent through the rendered KAS MCP file, not the
	// spawn, so a flip is live at the next prompt of every open chat. Default off.
	KeyMCPWaitForReady = "mcp_wait_for_ready" //nolint:gosec // G101: a settings key ("MCPWait" reads as "pw"), not a credential

	// KeyAutoCompactionEnabled and KeyAutoCompactPct are the chat compaction
	// policy internal/agent's autoCompactionPolicy reads. Both ride the session
	// door, so a change reaches a chat at its next session/load.
	KeyAutoCompactionEnabled = "auto_compaction_enabled"
	KeyAutoCompactPct        = "auto_compact_pct"

	// The Agent capabilities that ride KAS's doors, resolved per session door by
	// internal/agent's agentFeatures (values in agent_features.go).
	// KeyWorkValidation, KeyCloudFormationSafety, KeyAutoRouting and
	// KeyAutoDelegation are three-state: empty sends nothing, so kiro-cli's own
	// experiment decides; KeyCloudFormationSafety also takes "enforce". KeyModelFallback
	// sends KAS's refusal and availability fallback flags as true or false; it defaults
	// on, as kiro-cli does.
	KeySpecPlanning         = "spec_planning"
	KeySpecPlanningAskFirst = "spec_planning_ask_first"
	KeySteeringReminders    = "steering_reminders_enabled"
	KeyWorkflowsEnabled     = "workflows_enabled"
	KeyChatMessaging        = "chat_messaging_enabled"
	KeyChatAwareness        = "chat_awareness_enabled"
	KeyWorkValidation       = "work_validation"
	KeyCloudFormationSafety = "cloudformation_safety_check"
	KeyAutoRouting          = "auto_routing"
	KeyAutoDelegation       = "auto_delegation"
	KeyModelFallback        = "model_fallback_enabled"
	// KeyWorkflowLaunch is asserted at the session doors as KAS's workflowLaunch option, and
	// read only while KeyWorkflowsEnabled is on.
	KeyWorkflowLaunch = "workflow_launch"
	// KeyWorkflowCreatorEffort is the effort of the step KAS runs to draft a workflow from a prose request, sent in
	// the session doors' workflows object and read only while KeyWorkflowsEnabled is on; empty sends nothing.
	KeyWorkflowCreatorEffort = "workflow_creator_effort"
	// KeyChatAwarenessMode is how a chat searches the others, sent in the session doors' sessionTools.awareness.mode
	// and read only while KeyChatAwareness is on; empty sends nothing. KAS fixes it at session/new, so a change
	// reaches new chats only.
	KeyChatAwarenessMode = "chat_awareness_mode"
	// KeyChatMessageDeliveryLimit caps one agent-to-agent exchange between two chats (KAS's
	// sendMessageDeliveryLimit), sent at initialize, so it applies when the engine restarts.
	KeyChatMessageDeliveryLimit = "chat_message_delivery_limit"
	// The Knowledge base indexing settings (knowledge_tuning.go), sent in the initialize block's knowledge object, so
	// each applies when the engine restarts.
	KeyKnowledgeIndexType       = "knowledge_index_type"
	KeyKnowledgeIncludePatterns = "knowledge_include_patterns"
	KeyKnowledgeExcludePatterns = "knowledge_exclude_patterns"
	KeyKnowledgeMaxFiles        = "knowledge_max_files"
	KeyKnowledgeChunking        = "knowledge_chunking"
	// KeyOutputStyle, a KAS output style id, rides each chat prompt as _meta.kiro.outputStyle.
	// KeyTerminalCommandTimeoutMs is the shell tool's default timeout (0 = KAS's
	// 120 s), sent at initialize and pushed live to the engine.
	KeyOutputStyle              = "output_style"
	KeyTerminalCommandTimeoutMs = "terminal_command_timeout_ms"

	// KeyEngineHeapMiB is the agent engine's JavaScript heap limit in MiB: 0 (the default) adds no flag and leaves
	// kiro-cli's own, otherwise ValidEngineHeapMiB's range. It applies when the engine restarts.
	KeyEngineHeapMiB = "engine_heap_mib"

	// KeyHighThroughput starts the agent server on kiro-cli's bundled Node.js with internal/kasnode's flags, default
	// ON (defaultHighThroughput); off is kiro-cli's stock engine. It applies when the engine restarts.
	KeyHighThroughput = "high_throughput_optimizations"

	// KeyTheme is "dark" | "light" | "system" ("system" is a stored choice; absent resolves to
	// system at the client). The browser's localStorage copy is a pre-paint hint this value
	// overwrites.
	KeyTheme = "theme"

	// KeySettingsEpoch and KeySettingsRevision stamp the document's version: Update owns both, and
	// a PATCH may set neither.
	KeySettingsEpoch    = "settings_epoch"
	KeySettingsRevision = "settings_revision"
)

// There is no notify_permission key: a permission ask blocks the turn, so its notice cannot
// be silenced (push.TestPermissionKindHasNoSettingsKey). The master switch still covers it.

// DefaultChatRetentionDays is the default for chat_retention_days: -1 forever, 0 delete on
// close, N purge after N days. The purge treats <= 0 as no purge. GET
// /api/settings resolves it, so the client carries no copy.
const DefaultChatRetentionDays = 7

// DefaultAgentIgnoreFiles is the default for agent_ignore_files: EMPTY (other Kiro clients
// apply no workspace ignore file; AgentIgnoreFloor is sent regardless). Non-nil, so it marshals [].
func DefaultAgentIgnoreFiles() []string {
	return []string{}
}

// defaultAdditionalDirectories is the default for additional_directories: EMPTY, so agents
// reach only the workspace until the user grants more. Non-nil, so it marshals [].
func defaultAdditionalDirectories() []string {
	return []string{}
}

// KnownKeys is the set of marotte-managed config.json keys; PATCH warns about others but
// does not reject them. kiro-cli's own settings (cli.json) are not in it. There is no
// model_effort key: effort is per chat (Chat.Effort), seeded by KeyLastEffortByModel.
var KnownKeys = map[string]struct{}{
	KeyAgentIgnoreFiles:         {},
	KeyAdditionalDirectories:    {},
	KeyAutoCompactionEnabled:    {},
	KeyAutoCompactPct:           {},
	KeyAutoDelegation:           {},
	KeyAutoRouting:              {},
	KeyChatRetentionDays:        {},
	KeyCloudFormationSafety:     {},
	KeyDebugLogs:                {},
	KeyEngineHeapMiB:            {},
	KeyHighThroughput:           {},
	KeyInterruptMode:            {},
	KeyOutputStyle:              {},
	KeySpecPlanning:             {},
	KeySpecPlanningAskFirst:     {},
	KeySteeringReminders:        {},
	KeyTerminalCommandTimeoutMs: {},
	KeyWorkValidation:           {},
	KeyWorkflowsEnabled:         {},
	KeyChatMessaging:            {},
	KeyChatAwareness:            {},
	KeyChatAwarenessMode:        {},
	KeyChatMessageDeliveryLimit: {},
	KeyWorkflowLaunch:           {},
	KeyWorkflowCreatorEffort:    {},
	KeyGuardPayloadLinks:        {},
	KeyKnowledgeEnabled:         {},
	KeyKnowledgeIndexType:       {},
	KeyKnowledgeIncludePatterns: {},
	KeyKnowledgeExcludePatterns: {},
	KeyKnowledgeMaxFiles:        {},
	KeyKnowledgeChunking:        {},
	KeyModelFallback:            {},
	KeyContentCollectionEnabled: {},
	KeyLastEffortByModel:        {},
	KeyLastMergeMethod:          {},
	KeyLastModel:                {},
	KeyMCPWaitForReady:          {},
	KeyMemoryMode:               {},
	KeyNotificationsEnabled:     {},
	KeyNotifyAgentFinished:      {},
	KeyNotifyPRStatus:           {},
	KeyNotifyRunOutcome:         {},
	KeySupervisedDefault:        {},
	KeyScheduledAutoApprove:     {},
	KeySecurityProfile:          {},
	KeySettingsEpoch:            {},
	KeySettingsRevision:         {},
	KeyTheme:                    {},

	keyTriggerMaxLiveRuns:        {},
	keyTriggerMaxLaunchesPerHour: {},
}

// WarnUnknownKeys logs a warning for each key in keys outside KnownKeys and returns them
// sorted (nil when all are known). source identifies the call site.
func WarnUnknownKeys(keys []string, source string) []string {
	var unknown []string
	for _, k := range keys {
		if _, ok := KnownKeys[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		slog.Warn("settings: unknown keys in write",
			"source", source,
			"keys", unknown)
	}
	return unknown
}
