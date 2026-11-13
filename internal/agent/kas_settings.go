package agent

import (
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// Settings whose value must reach the agent, resolved into EngineOpts per spawn or SessionOpts per door. KAS's ACP path
// never reads kiro-cli's settings store (2.19.2), so these ride levers it does read.

// knowledgeEnabled reports whether a session gets the knowledge index in msg0 and KAS's Knowledge tool. On by default,
// as settings.Default() is. It gates the agent only, never `_kiro/knowledge` or the panel.
func knowledgeEnabled(d settings.Document) bool {
	var b bool
	if !d.Into(settings.KeyKnowledgeEnabled, &b) {
		return true
	}
	return b
}

// An absent, invalid or unrecognised value takes settings.DefaultMemoryMode.
func memoryPreference(d settings.Document) marotte.MemoryPreference {
	mode := settings.DefaultMemoryMode
	var v string
	if d.Into(settings.KeyMemoryMode, &v) {
		mode = v
	}
	return settings.MemoryPreferenceFor(mode)
}

// sessionFeatures resolves the stored Agent-capabilities settings a session door sends KAS; an absent or invalid value
// takes its default. readSessionSettings applies the administrator locks.
func sessionFeatures(d settings.Document) marotte.SessionFeatures {
	var f marotte.SessionFeatures
	// An unset value is the default, off: ask-first is read only while planning is on.
	if plan := settingString(d, settings.KeySpecPlanning, settings.ValidSpecPlanning); plan != "" && plan != settings.SpecPlanningOff {
		f.SpecPlan = plan
		f.SpecAskClarification = settingBool(d, settings.KeySpecPlanningAskFirst)
	}
	f.Workflows = settings.DefaultWorkflowsEnabled
	var workflows bool
	if d.Into(settings.KeyWorkflowsEnabled, &workflows) {
		f.Workflows = workflows
	}
	// Read only while workflows are on, so a change with them off reloads no chat.
	if f.Workflows {
		f.WorkflowLaunch = settingString(d, settings.KeyWorkflowLaunch, settings.ValidWorkflowLaunch)
		if f.WorkflowLaunch == "" {
			f.WorkflowLaunch = settings.DefaultWorkflowLaunch
		}
		f.WorkflowCreatorEffort = settingString(d, settings.KeyWorkflowCreatorEffort, settings.ValidWorkflowCreatorEffort)
	}
	f.Messaging = settingBool(d, settings.KeyChatMessaging)
	f.Awareness = settingBool(d, settings.KeyChatAwareness)
	f.WorkValidation = settingString(d, settings.KeyWorkValidation, settings.ValidFeatureChoice)
	return f
}

// engineFeatures resolves the Agent-capabilities settings the engine is spawned with.
func engineFeatures(d settings.Document) marotte.EngineFeatures {
	return marotte.EngineFeatures{
		SteeringReminders:        settingBool(d, settings.KeySteeringReminders),
		InfraSafety:              settingString(d, settings.KeyCloudFormationSafety, settings.ValidCloudFormationChoice),
		AutoRouting:              settingString(d, settings.KeyAutoRouting, settings.ValidFeatureChoice),
		AutoDelegation:           settingString(d, settings.KeyAutoDelegation, settings.ValidFeatureChoice),
		ModelFallback:            modelFallbackEnabled(d),
		TerminalCommandTimeoutMs: terminalCommandTimeoutMs(d),
		MessageDeliveryLimit:     messageDeliveryLimit(d),
		Knowledge:                knowledgeTuning(d),
	}
}

// knowledgeTuning reads the Knowledge base indexing settings. Each absent, invalid or default value is the zero
// member, which withholds it, so saving kiro-cli's own value changes no engine fingerprint. An empty include list is
// withheld too: KAS includes nothing for one, where an absent list includes every file it indexes.
func knowledgeTuning(d settings.Document) marotte.KnowledgeTuning {
	t := marotte.KnowledgeTuning{
		IndexType:       settingString(d, settings.KeyKnowledgeIndexType, settings.ValidKnowledgeIndexType),
		IncludePatterns: knowledgePatterns(d, settings.KeyKnowledgeIncludePatterns),
		ExcludePatterns: knowledgePatterns(d, settings.KeyKnowledgeExcludePatterns),
	}
	var n int
	if d.Into(settings.KeyKnowledgeMaxFiles, &n) && settings.ValidKnowledgeMaxFiles(n) && n != settings.DefaultKnowledgeMaxFiles {
		t.MaxFiles = n
	}
	if c, ok := settings.KnowledgeChunkingIn(d); ok && c != settings.DefaultKnowledgeChunking() {
		t.Chunking = c
	}
	return t
}

// knowledgePatterns reads a knowledge glob list; nil for absent, invalid or empty.
func knowledgePatterns(d settings.Document, key string) []string {
	var p []string
	if d.Into(key, &p) && len(p) > 0 && settings.ValidKnowledgePatterns(p) {
		return p
	}
	return nil
}

// messageDeliveryLimit reads the peer-message delivery limit; 0, which withholds it, for absent, invalid or kiro-cli's
// own default, so saving the default changes no engine fingerprint.
func messageDeliveryLimit(d settings.Document) int {
	var n int
	if d.Into(settings.KeyChatMessageDeliveryLimit, &n) && settings.ValidChatMessageDeliveryLimit(n) &&
		n != settings.DefaultChatMessageDeliveryLimit {
		return n
	}
	return 0
}

// modelFallbackEnabled reads the default-on Model fallback switch; absent or invalid is on.
func modelFallbackEnabled(d settings.Document) bool {
	var b bool
	if !d.Into(settings.KeyModelFallback, &b) {
		return true
	}
	return b
}

// terminalCommandTimeoutMs reads the shell tool's default timeout; 0 is unset.
func terminalCommandTimeoutMs(d settings.Document) int {
	var ms int
	if d.Into(settings.KeyTerminalCommandTimeoutMs, &ms) && settings.ValidTerminalCommandTimeoutMs(ms) {
		return ms
	}
	return 0
}

// settingBool reads a default-false bool setting.
func settingBool(d settings.Document, key string) bool {
	var b bool
	return d.Into(key, &b) && b
}

// settingString reads a string setting, the zero value for absent or invalid.
func settingString(d settings.Document, key string, valid func(string) bool) string {
	var v string
	if d.Into(key, &v) && valid(v) {
		return v
	}
	return ""
}

// Invalid values read as the defaults (on, 80).
func autoCompactionPolicy(d settings.Document) (enabled bool, pct int) {
	enabled, pct = settings.DefaultAutoCompactionEnabled, settings.DefaultAutoCompactPct
	var b bool
	if d.Into(settings.KeyAutoCompactionEnabled, &b) {
		enabled = b
	}
	var p int
	if d.Into(settings.KeyAutoCompactPct, &p) && settings.ValidAutoCompactPct(p) {
		pct = p
	}
	return enabled, pct
}

// kiroTelemetryKey is the kiro-cli setting the Data sharing switch writes. Nothing seeds it, so
// unset reads kiroTelemetryDefault, kiro-cli's own default, as the switch shows it.
const (
	kiroTelemetryKey     = "telemetry.enabled"
	kiroTelemetryDefault = true
)

// telemetryDisabled resolves the engine's telemetryEnabled: kiro-cli's own setting, overridden by an
// organization's telemetry lock. KAS reads neither, so the engine spawn carries it.
func (lt *lifetime) telemetryDisabled(locks func() map[string]marotte.GovernanceLock) bool {
	return !lockedBool(currentLocks(locks), marotte.LockTelemetry, lt.kiroTelemetry.get())
}
