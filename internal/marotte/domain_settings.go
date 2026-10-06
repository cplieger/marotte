package marotte

// EffectiveSettings is what GET /api/settings answers: every marotte-owned preference the client
// renders, resolved against config.json rather than echoed from it. The PATCH body is its partial.
// NO FIELD CARRIES omitempty: wiregen then emits a REQUIRED TypeScript field, so the client cannot
// keep a fallback default of its own. Field order is fieldalignment's (strings, then the slice,
// then non-pointers), not topical.
type EffectiveSettings struct {
	// LastEffortByModel maps a model id to the reasoning-effort level last picked
	// under it, so the seed applies to a chat running THAT model and to no other.
	// One level for the whole app cannot express that: a pick on any chat retracts
	// every other model's remembered level (settings.KeyLastEffortByModel).
	LastEffortByModel map[string]string `json:"last_effort_by_model"`
	// Theme is "", "dark", "light" or "system". The empty string is a REAL value
	// meaning nothing has been chosen, which the client resolves to the OS
	// preference; it is deliberately not normalised to "system" here, because the
	// client's one-time paint-cache carry-across is reachable only while the
	// server says nothing.
	Theme string `json:"theme"`
	// FBPath is the file-browser path to restore, "" to list the granted mounts.
	FBPath string `json:"fb_path"`
	// LastModel and LastEffortByModel are what a NEW chat opens on. Both are pure
	// memory: the value in force for an existing chat lives on that chat's record.
	LastModel string `json:"last_model"`
	// LastMergeMethod is the PR merge method picked last, in the forge's own
	// spelling, the merge dialog's default where the repository offers it. Empty
	// means nothing picked yet.
	LastMergeMethod string `json:"last_merge_method"`
	// MemoryMode is the Memory dropdown's value (settings.MemoryOff and its three
	// siblings); it defaults to settings.DefaultMemoryMode, kiro-cli's own.
	MemoryMode string `json:"memory_mode"`
	// SpecPlanning is "off", "quick" or "full"; default off, matching kiro-cli.
	SpecPlanning string `json:"spec_planning"`
	// WorkValidation and CloudFormationSafetyCheck are "", "on" or "off". The
	// empty string is a real value: marotte sends nothing and kiro-cli decides.
	WorkValidation            string `json:"work_validation"`
	CloudFormationSafetyCheck string `json:"cloudformation_safety_check"`
	// OutputStyle is "default" or "concise".
	OutputStyle string `json:"output_style"`
	// AgentIgnoreFiles is the ignore-FILE basename list marotte sends kiro-cli,
	// which is what enforces it; marotte runs no matcher of its own. The default is
	// EMPTY (settings.DefaultAgentIgnoreFiles), and an absent key must still not
	// read as the zero value: the panel row is authoritative on write, so a client
	// falling back to an empty list persists that emptiness on the next edit.
	AgentIgnoreFiles []string `json:"agent_ignore_files"`
	// ChatRetentionDays is -1 (keep forever), 0 (delete on close) or a day count.
	// Zero is the most destructive value in the document, so absent must never
	// resolve to it.
	ChatRetentionDays int `json:"chat_retention_days"`
	// AutoCompactPct is 50..90 in steps of 5; any other stored value reads as
	// settings.DefaultAutoCompactPct (80).
	AutoCompactPct int `json:"auto_compact_pct"`
	// TerminalCommandTimeoutMs is the shell tool's default timeout in ms; 0 is
	// unset (kiro-cli's 120 s).
	TerminalCommandTimeoutMs int `json:"terminal_command_timeout_ms"`
	// AutoCompactionEnabled defaults TRUE: off means nothing compacts a chat
	// until the reader presses Compact.
	AutoCompactionEnabled bool `json:"auto_compaction_enabled"`
	// KnowledgeEnabled defaults TRUE, so it is the other key whose zero value is
	// the wrong answer: the index, its REST surface and its UI all predate the
	// switch, so an absent key read as false takes the knowledge tool away from
	// every existing install.
	KnowledgeEnabled bool `json:"knowledge_enabled"`
	// ContentCollection is the stored content-collection choice, default off. An
	// organization's lock overrides what marotte sends, not this value.
	ContentCollection bool `json:"content_collection_enabled"`
	// GuardPayloadLinks defaults to settings.DefaultGuardPayloadLinks; no server path
	// reads it.
	GuardPayloadLinks bool `json:"guard_payload_links"`
	// ToolSearchEnabled defaults off, matching kiro-cli.
	ToolSearchEnabled bool `json:"tool_search_enabled"`
	// MCPWaitForReady renders waitForReady on every server in KAS's MCP file, so
	// a prompt waits for the servers to settle. Default off.
	MCPWaitForReady bool `json:"mcp_wait_for_ready"`
	// NotificationsEnabled is the push master switch, default off. The three per-kind
	// switches below take their defaults from settings.Default*, and those are not
	// uniform either — pr_status is OFF where its two siblings are ON. So there are
	// three polarities across these four fields, and that asymmetry is exactly why
	// the client must not guess any of them.
	NotificationsEnabled bool `json:"notifications_enabled"`
	NotifyAgentFinished  bool `json:"notify_agent_finished"`
	NotifyPRStatus       bool `json:"notify_pr_status"`
	NotifyRunOutcome     bool `json:"notify_run_outcome"`
	// SupervisedDefault seeds newly created chats; ScheduledAutoApprove decides an
	// unattended run's permission ask at its deadline and is fail-closed by
	// decision. DebugLogs raises the log level. All three default off.
	// SpecPlanningAskFirst, InlineAgents and SteeringReminders default off,
	// matching kiro-cli. WorkflowsEnabled defaults ON (user's choice, where the
	// TUI's default is off).
	SpecPlanningAskFirst bool `json:"spec_planning_ask_first"`
	InlineAgents         bool `json:"inline_agents_enabled"`
	SteeringReminders    bool `json:"steering_reminders_enabled"`
	WorkflowsEnabled     bool `json:"workflows_enabled"`
	SupervisedDefault    bool `json:"supervised_default"`
	ScheduledAutoApprove bool `json:"scheduled_auto_approve"`
	DebugLogs            bool `json:"debug_logs"`
}
