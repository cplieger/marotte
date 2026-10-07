package agent

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// Settings whose value must reach the agent, resolved into StartOpts per spawn. KAS's ACP path
// never reads kiro-cli's settings store (2.19.2), so these ride levers it does read. KAS
// freezes both at session creation.

// toolSearchEnabled reports whether a session defers MCP tools behind KAS's tool_load/tool_call
// pair. Off by default, matching kiro-cli; an absent key reads false.
func toolSearchEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyToolSearchEnabled, &b) {
		return false
	}
	return b
}

// knowledgeEnabled reports whether a session gets the knowledge index in msg0 and KAS's
// Knowledge tool. On by default (settings.Default() matches) so existing installs keep it. It
// gates the agent only, never `_kiro/knowledge` or the panel.
func knowledgeEnabled(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyKnowledgeEnabled, &b) {
		return true
	}
	return b
}

// memoryPreference resolves the Memory dropdown for a spawn. An absent,
// unreadable or unrecognised value takes settings.DefaultMemoryMode.
func memoryPreference(ctx context.Context, configDir string) marotte.MemoryPreference {
	mode := settings.DefaultMemoryMode
	var v string
	if settings.FieldInto(ctx, configDir, settings.KeyMemoryMode, &v) {
		mode = v
	}
	return settings.MemoryPreferenceFor(mode)
}

// agentFeatures resolves the Agent-capabilities settings a spawn sends KAS; an absent or invalid
// value takes its default, and an administrator lock wins.
func agentFeatures(ctx context.Context, configDir string, locks map[string]marotte.GovernanceLock) marotte.AgentFeatures {
	var f marotte.AgentFeatures
	if plan := settingString(ctx, configDir, settings.KeySpecPlanning, settings.ValidSpecPlanning); plan != settings.SpecPlanningOff {
		f.SpecPlan = plan
	}
	f.SpecAskClarification = settingBool(ctx, configDir, settings.KeySpecPlanningAskFirst)
	f.InlineAgents = lockedBool(locks, marotte.LockInlineAgents, settingBool(ctx, configDir, settings.KeyInlineAgents))
	f.SteeringReminders = settingBool(ctx, configDir, settings.KeySteeringReminders)
	f.Workflows = settings.DefaultWorkflowsEnabled
	var workflows bool
	if settings.FieldInto(ctx, configDir, settings.KeyWorkflowsEnabled, &workflows) {
		f.Workflows = workflows
	}
	f.Workflows = lockedBool(locks, marotte.LockWorkflows, f.Workflows)
	f.WorkValidation = settingString(ctx, configDir, settings.KeyWorkValidation, settings.ValidFeatureChoice)
	f.InfraSafetyMonitor = settingString(ctx, configDir, settings.KeyCloudFormationSafety, settings.ValidFeatureChoice)
	f.TerminalCommandTimeoutMs = terminalCommandTimeoutMs(ctx, configDir)
	return f
}

// terminalCommandTimeoutMs reads the shell tool's default timeout; 0 is unset.
func terminalCommandTimeoutMs(ctx context.Context, configDir string) int {
	var ms int
	if settings.FieldInto(ctx, configDir, settings.KeyTerminalCommandTimeoutMs, &ms) && settings.ValidTerminalCommandTimeoutMs(ms) {
		return ms
	}
	return 0
}

// settingBool reads a default-false bool setting.
func settingBool(ctx context.Context, configDir, key string) bool {
	var b bool
	return settings.FieldInto(ctx, configDir, key, &b) && b
}

// settingString reads a string setting, the zero value for absent, unreadable or invalid; every
// caller's zero value is its default.
func settingString(ctx context.Context, configDir, key string, valid func(string) bool) string {
	var v string
	if settings.FieldInto(ctx, configDir, key, &v) && valid(v) {
		return v
	}
	return ""
}

// autoCompactionPolicy reads whether marotte may compact a chat and at what percentage;
// invalid values read as the defaults (on, 80).
func autoCompactionPolicy(ctx context.Context, configDir string) (enabled bool, pct int) {
	enabled, pct = settings.DefaultAutoCompactionEnabled, settings.DefaultAutoCompactPct
	var b bool
	if settings.FieldInto(ctx, configDir, settings.KeyAutoCompactionEnabled, &b) {
		enabled = b
	}
	var p int
	if settings.FieldInto(ctx, configDir, settings.KeyAutoCompactPct, &p) && settings.ValidAutoCompactPct(p) {
		pct = p
	}
	return enabled, pct
}

// sessionDisablesAutoCompaction is the session-door value: KAS's own compaction goes off when
// marotte must be the only compactor (point above 80, which KAS would pre-empt) or nothing may compact.
func sessionDisablesAutoCompaction(enabled bool, pct int) bool {
	return !enabled || pct > settings.DefaultAutoCompactPct
}

// kiroTelemetryKey is the kiro-cli setting the Data sharing switch writes; unset is kiro-cli's
// default, on.
const kiroTelemetryKey = "telemetry.enabled"

// telemetryDisabled resolves a spawn's telemetryEnabled: kiro-cli's own setting, overridden by an
// organization's telemetry lock. KAS reads neither, so the spawn carries it.
func (lt *lifetime) telemetryDisabled(locks func() map[string]marotte.GovernanceLock) bool {
	return !lockedBool(currentLocks(locks), marotte.LockTelemetry, lt.kiroTelemetry.get())
}
