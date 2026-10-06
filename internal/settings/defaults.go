// Defaults and the known-keys set for marotte's `<configDir>/config.json`. Unknown keys are
// warned about, never rejected, because a preference file gains keys per release.

package settings

import "log/slog"

// Settings key names. Reference these instead of bare string literals.
const (
	KeyAgentIgnoreFiles = "agent_ignore_files"

	// KeyChatRetentionDays is three states: -1 keeps every chat, 0 deletes at close, N keeps N
	// days. A dropped key falls back to the 7-day default, hence PATCH merges, never replaces.
	KeyChatRetentionDays = "chat_retention_days"

	KeyDebugLogs            = "debug_logs"
	KeyLastModel            = "last_model"
	KeyNotificationsEnabled = "notifications_enabled"

	// KeyLastEffortByModel maps model id to the effort level last picked under it: the level a
	// NEW chat on that model opens on. A seed, never written onto the chat record (Chat.Effort
	// owns the level); both readers (effortSeedFor, getLastEffortFor) look up the chat's model.
	KeyLastEffortByModel = "last_effort_by_model"

	// KeyLastMergeMethod is the PR merge method last picked (forge spelling), the merge dialog's
	// next default when the repository offers it. Empty means none picked.
	KeyLastMergeMethod = "last_merge_method"

	KeyNotifyAgentFinished = "notify_agent_finished"
	KeyNotifyPRStatus      = "notify_pr_status"
	KeyNotifyRunOutcome    = "notify_run_outcome"
	KeySupervisedDefault   = "supervised_default"

	// KeySecurityProfile is the security posture every session opens with (a policyfile profile
	// id sent as policy presets). Global: KAS cannot change a live session's policy. An unset or
	// unknown value resolves to policyfile.DefaultProfile, never to Custom (no presets).
	KeySecurityProfile = "security_profile"

	// KeyScheduledAutoApprove lets a SCHEDULED run's tool requests be approved instead of refused
	// after the unattended budget. Off by default, and its own switch: unattended consent must be
	// chosen, not inherited.
	KeyScheduledAutoApprove = "scheduled_auto_approve"

	// KeyToolSearchEnabled and KeyKnowledgeEnabled reach the agent per spawn (knowledge under
	// _meta.kiro.settings, tool search as KIRO_FEATURE_TOOL_LOAD_ENABLED). Knowledge defaults TRUE.
	KeyToolSearchEnabled = "tool_search_enabled"
	KeyKnowledgeEnabled  = "knowledge_enabled"
	KeyGuardPayloadLinks = "guard_payload_links"

	// KeyContentCollectionEnabled lets model requests be used by AWS for service
	// improvement. Default off. internal/agent asserts it on every kiro-cli process
	// and pushes a change live, because KAS holds it per process and persists none
	// of it; an organization's lock overrides it.
	KeyContentCollectionEnabled = "content_collection_enabled"

	// KeyMemoryMode is the Memory dropdown (memory.go's Memory* values), resolved per spawn by
	// MemoryPreferenceFor. KAS freezes the mode per session; the reflection half is re-sent on
	// session/load.
	KeyMemoryMode = "memory_mode"

	// KeyMCPWaitForReady makes KAS hold each prompt until every enabled MCP server
	// has settled. It reaches the agent through the rendered KAS MCP file, not the
	// spawn, so a flip is live at the next prompt of every open chat. Default off.
	KeyMCPWaitForReady = "mcp_wait_for_ready" //nolint:gosec // G101: a settings key ("MCPWait" reads as "pw"), not a credential

	// KeyAutoCompactionEnabled and KeyAutoCompactPct are the chat compaction
	// policy internal/agent's autoCompactionPolicy reads. The door value they
	// imply is frozen by KAS per session, so a flip reaches a chat at its next
	// session/load; the pct itself acts at once because marotte drives it.
	KeyAutoCompactionEnabled = "auto_compaction_enabled"
	KeyAutoCompactPct        = "auto_compact_pct"

	// The Agent capabilities that ride KAS's doors, resolved per spawn by
	// internal/agent's agentFeatures (values in agent_features.go).
	// KeyWorkValidation and KeyCloudFormationSafety are three-state: empty sends
	// nothing, so kiro-cli's own experiment decides.
	KeySpecPlanning         = "spec_planning"
	KeySpecPlanningAskFirst = "spec_planning_ask_first"
	KeyInlineAgents         = "inline_agents_enabled"
	KeySteeringReminders    = "steering_reminders_enabled"
	KeyWorkflowsEnabled     = "workflows_enabled"
	KeyWorkValidation       = "work_validation"
	KeyCloudFormationSafety = "cloudformation_safety_check"

	// KeyOutputStyle rides each chat prompt as _meta.kiro.outputStyle.
	// KeyTerminalCommandTimeoutMs is the shell tool's default timeout (0 = KAS's
	// 120 s), sent at initialize and pushed live to every bridge.
	KeyOutputStyle              = "output_style"
	KeyTerminalCommandTimeoutMs = "terminal_command_timeout_ms"

	// KeyTheme is "dark" | "light" | "system" ("system" is a stored choice; absent resolves to
	// system at the client). The browser's localStorage copy is a pre-paint hint this value
	// overwrites. KeyFBPath is the file browser's last directory, per workspace.
	KeyTheme  = "theme"
	KeyFBPath = "fb_path"
)

// There is no notify_permission key: a permission ask blocks the turn, so its notice cannot
// be silenced (push.TestPermissionKindHasNoSettingsKey). The master switch still covers it.

// DefaultChatRetentionDays is the default for chat_retention_days: -1 forever, 0 delete on
// close (History hidden), N purge after N days. The purge treats <= 0 as no purge. GET
// /api/settings resolves it, so the client carries no copy.
const DefaultChatRetentionDays = 7

// DefaultAgentIgnoreFiles is the default for agent_ignore_files: EMPTY (other Kiro clients
// apply no workspace ignore file; AgentIgnoreFloor is sent regardless). Non-nil, so it marshals [].
func DefaultAgentIgnoreFiles() []string {
	return []string{}
}

// KnownKeys is the set of marotte-managed config.json keys; PATCH warns about others but
// does not reject them. kiro-cli's own settings (cli.json) are not in it. There is no
// model_effort key: effort is per chat (Chat.Effort), seeded by KeyLastEffortByModel.
var KnownKeys = map[string]struct{}{
	KeyAgentIgnoreFiles:         {},
	KeyAutoCompactionEnabled:    {},
	KeyAutoCompactPct:           {},
	KeyChatRetentionDays:        {},
	KeyCloudFormationSafety:     {},
	KeyDebugLogs:                {},
	KeyInlineAgents:             {},
	KeyOutputStyle:              {},
	KeySpecPlanning:             {},
	KeySpecPlanningAskFirst:     {},
	KeySteeringReminders:        {},
	KeyTerminalCommandTimeoutMs: {},
	KeyWorkValidation:           {},
	KeyWorkflowsEnabled:         {},
	KeyFBPath:                   {},
	KeyGuardPayloadLinks:        {},
	KeyKnowledgeEnabled:         {},
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
	KeyTheme:                    {},
	KeyToolSearchEnabled:        {},
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
