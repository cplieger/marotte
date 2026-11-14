package kascap

import (
	"cmp"
	"encoding/json"
)

// enabledMember is the one member name KAS reads inside a settings entry (isSettingEnabled returns
// val.enabled). A misspelling is invisible on the wire: it resolves to undefined, neither true nor
// false.
const enabledMember = "enabled"

// enabled is the shape every _meta.kiro.settings entry marotte SENDS takes. A
// helper rather than inline literals because KAS reads each one through the same
// absent-key-means-false resolver, so the shape is a contract shared by all of
// them rather than a coincidence repeated at each row.
func enabled() map[string]any { return map[string]any{enabledMember: true} }

// enabledIf is enabled()'s runtime twin, for a row whose value comes from a spawn field.
// `{"enabled": false}` is how KAS is told a feature is OFF, a different statement from an absent
// key where a key must veto something or answer a resolver comparing a real boolean.
func enabledIf(on bool) map[string]any { return map[string]any{enabledMember: on} }

// chatTurnBreakdown is the contextBreakdown mode of every chat turn, the ones the user prompts (the contextBreakdown
// row) and the ones KAS starts on its own (automaticTurnProfiling), so both fill the same info panel.
const chatTurnBreakdown = "detailed"

// infraSafetyEnforce is EngineSpawn.InfraSafety's value for kiro-cli's infraSafety enforce mode, which sends both
// infraSafetyMonitor and infraSafetyEnforce.
const infraSafetyEnforce = "enforce"

// Not a bare true: KAS requires an object carrying a v2 member and then checks that member
// (resolverObject).
func hooksValue() map[string]any { return map[string]any{"enabled": true, "v2": true} }

// specPlanValue builds settings.specPlan: off sends {"enabled": false}; on adds
// KAS's workflow and the inverted clarification flag.
func specPlanValue(s *SessionSpawn) map[string]any {
	if s.SpecPlan == "" {
		return enabledIf(false)
	}
	return map[string]any{enabledMember: true, "workflow": s.SpecPlan, "skipClarification": !s.SpecAskClarification}
}

// choiceValue maps a follow-kiro-cli setting onto a row: empty withholds the
// key so KAS's own resolution decides.
func choiceValue(choice string) (any, bool) {
	switch choice {
	case "on":
		return enabledIf(true), true
	case "off":
		return enabledIf(false), true
	}
	return nil, false
}

// infraSafetyMonitor maps the CloudFormation check onto infraSafetyMonitor: enforce runs the check too.
func infraSafetyMonitor(choice string) (any, bool) {
	if choice == infraSafetyEnforce {
		return enabledIf(true), true
	}
	return choiceValue(choice)
}

// workflowsValue builds settings.workflows. creatorEffort rides it only when stated: KAS reads a workflows object
// without the member as no effort, which its creator agent's own level then fills.
func workflowsValue(s *SessionSpawn) map[string]any {
	v := enabledIf(s.Workflows)
	if s.Workflows && s.WorkflowCreatorEffort != "" {
		v["creatorEffort"] = s.WorkflowCreatorEffort
	}
	return v
}

// awarenessValue builds settings.sessionTools.awareness. mode rides it only when stated: KAS reads an awareness
// object without the member as direct.
func awarenessValue(s *SessionSpawn) map[string]any {
	v := enabledIf(s.Awareness)
	if s.Awareness && s.AwarenessMode != "" {
		v["mode"] = s.AwarenessMode
	}
	return v
}

// knowledgeValue builds settings.knowledge: enabled plus each stated member of KnowledgeTuning, under KAS's member
// names (the kiroAgent knowledge entries in 3.2.0), which ResolvedKnowledgeConfig reads whatever enabled says.
func knowledgeValue(s *EngineSpawn) map[string]any {
	v := enabledIf(s.Knowledge)
	k := s.KnowledgeTuning
	if k.IndexType != "" {
		v["indexType"] = k.IndexType
	}
	if len(k.IncludePatterns) > 0 {
		v["includePatterns"] = k.IncludePatterns
	}
	if len(k.ExcludePatterns) > 0 {
		v["excludePatterns"] = k.ExcludePatterns
	}
	if k.MaxFiles > 0 {
		v["maxFiles"] = k.MaxFiles
	}
	if k.Chunking.Size > 0 {
		v["chunkSize"], v["chunkOverlap"] = k.Chunking.Size, k.Chunking.Overlap
	}
	return v
}

// jsonChoice maps a follow-kiro-cli setting onto a resolverEnvJSON row: empty withholds the
// variable. encoding/json sorts map keys, so the bytes are stable for the goldens.
func jsonChoice(choice string, on, off map[string]any) (any, bool) {
	var v map[string]any
	switch choice {
	case "on":
		v = on
	case "off":
		v = off
	default:
		return nil, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return string(b), true
}

// envBool writes an env row as "true" or "false" in both positions: KAS reads an absent row as
// the flag's default, so a switch over a default-on flag must spell its off out.
func envBool(on bool) (any, bool) {
	if on {
		return "true", true
	}
	return "false", true
}

// table is every capability key marotte knows about, sent or withheld.
//
// Row order is presentation only. Both builders emit maps, and encoding/json
// sorts map keys, so no wire byte depends on this order.
var table = []decl{
	{
		key:      "resolvesSteeringCommands",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "notifications",
		door:     doorConnection,
		resolver: resolverObject,
		send:     false,
	},
	{
		key:      "sessionScopedMcpStatus",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "openExternalUrl",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "infrastructureSafety",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "userInput",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "backgroundProcesses",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "textSearch",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "findFiles",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Value-gated, always present: resolveCapabilities compares `=== true`, so
		// the key has to carry a real boolean either way. Gated on the SAME field
		// as the knowledge SETTING row below, because the two are two thirds of one
		// gate and turning one off alone breaks it.
		engineGate: func(s *EngineSpawn) (any, bool) { return s.Knowledge, true },
	},
	{
		key:      "telemetryEnabled",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Value-gated, always present: KAS's own default is on, so only an explicit
		// false turns its agent telemetry off.
		engineGate: func(s *EngineSpawn) (any, bool) { return !s.DisableTelemetry, true },
	},
	{
		key:      "secretStorage",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     true,
		// Always present, value from the spawn: a false is a real declaration
		// here, not an omission. See because.
		engineGate: func(s *EngineSpawn) (any, bool) { return s.SecretStorage, true },
	},
	{
		key:      "configurationState",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "inactiveItems",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "hooks",
		door:     doorConnection,
		resolver: resolverObject,
		value:    hooksValue(),
		send:     true,
	},
	{
		key:      "codeIntelligence",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
	},
	{
		key:      "knowledge",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     true,
		// Value-gated on the same field as the capability row above. enabledIf
		// rather than enabled(), because the resolver needs the object shape in
		// both states: isSettingEnabled reads .enabled unchecked, so {"enabled":
		// false} is the off state and an absent member would be undefined.
		engineGate: func(s *EngineSpawn) (any, bool) { return knowledgeValue(s), true },
	},
	{
		key:      "toolSearch",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "workflows",
		door:     doorSession,
		resolver: resolverSetting,
		// Value-gated, always present: on load KAS takes the client value over
		// the persisted workflowsEnabled, so an explicit false is what turns a
		// resumed chat's workflow tools off.
		sessionGate: func(s *SessionSpawn) (any, bool) { return workflowsValue(s), true },
		send:        true,
	},
	{
		// KAS's sessionToolsMessaging and sessionToolsAwareness switches, read as settings.sessionTools.<switch>.enabled
		// (record fields sessionMessagingEnabled, sessionAwarenessEnabled). Both always present: on load a stated value
		// replaces the persisted field, so an explicit false is what turns a resumed chat's switch off. awareness.mode
		// is fixed at session/new: on load KAS keeps the session record's mode and reads the member only with no record.
		key:      "sessionTools",
		door:     doorSession,
		resolver: resolverSettingObject,
		sessionGate: func(s *SessionSpawn) (any, bool) {
			return map[string]any{"messaging": enabledIf(s.Messaging), "awareness": awarenessValue(s)}, true
		},
		send: true,
	},
	{
		key:      "sendMessage",
		door:     doorConnection,
		resolver: resolverSettingObject,
		engineGate: func(s *EngineSpawn) (any, bool) {
			return map[string]any{"deliveryLimit": s.MessageDeliveryLimit}, s.MessageDeliveryLimit > 0
		},
		send: true,
	},
	{
		key:      "backgroundExecution",
		door:     doorSession,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
	},
	{
		key:      "shellType",
		door:     doorSession,
		resolver: resolverEnum,
		value:    "bash",
		send:     true,
	},
	{
		key:      "goal",
		door:     doorConnection,
		resolver: resolverSetting,
		value:    enabled(),
		send:     true,
	},
	{
		key:      "workspaceTrusted",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "session_title_llm",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "sessionEviction",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:         "specPlan",
		door:        doorSession,
		resolver:    resolverSetting,
		sessionGate: func(s *SessionSpawn) (any, bool) { return specPlanValue(s), true },
		send:        true,
	},
	{
		key:        "steeringReminders",
		door:       doorConnection,
		resolver:   resolverSetting,
		engineGate: func(s *EngineSpawn) (any, bool) { return enabledIf(s.SteeringReminders), true },
		send:       true,
	},
	{
		key:         "validation",
		door:        doorSession,
		resolver:    resolverSetting,
		sessionGate: func(s *SessionSpawn) (any, bool) { return choiceValue(s.WorkValidation) },
		send:        true,
	},
	{
		key:        "infraSafetyMonitor",
		door:       doorConnection,
		resolver:   resolverSetting,
		engineGate: func(s *EngineSpawn) (any, bool) { return infraSafetyMonitor(s.InfraSafety) },
		send:       true,
	},
	{
		key:      "steeringSupervisor",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "_providerPowers",
		door:     doorSession,
		resolver: resolverSetting,
		value:    enabledIf(false),
		send:     true,
	},
	{
		key:      "fta",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:        "infraSafetyEnforce",
		door:       doorConnection,
		resolver:   resolverSetting,
		engineGate: func(s *EngineSpawn) (any, bool) { return enabledIf(true), s.InfraSafety == infraSafetyEnforce },
		send:       true,
	},
	{
		key:      "terminal",
		door:     doorConnection,
		resolver: resolverSettingObject,
		engineGate: func(s *EngineSpawn) (any, bool) {
			if s.TerminalCommandTimeoutMs <= 0 {
				return nil, false
			}
			return map[string]any{enabledMember: true, "commandTimeoutMs": s.TerminalCommandTimeoutMs}, true
		},
		send: true,
	},
	{
		key:        "contentCollection",
		door:       doorConnection,
		resolver:   resolverSettingObject,
		engineGate: func(s *EngineSpawn) (any, bool) { return enabledIf(s.ContentCollection), true },
		send:       true,
	},
	{
		key:      "specPhaseCheckpoints",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "requirementsAnalysis",
		door:     doorConnection,
		resolver: resolverCapability,
		value:    true,
		send:     true,
	},
	{
		key:      "policyPreset",
		door:     doorSession,
		resolver: resolverEnumList,
		// Gated on the active security profile. Present only for a NON-EMPTY set, which is the
		// Custom profile's whole implementation: no key makes the permissions files the entire
		// policy. resolvePresetIds treats an empty array as absent anyway.
		sessionGate: func(s *SessionSpawn) (any, bool) { return s.Presets, len(s.Presets) > 0 },
		send:        true,
	},
	{
		key:         "customAgents",
		door:        doorSession,
		resolver:    resolverObjectList,
		sessionGate: func(s *SessionSpawn) (any, bool) { return s.CustomAgents, len(s.CustomAgents) > 0 },
		send:        true,
	},
	{
		key:      "overrideSessionId",
		door:     doorSession,
		resolver: resolverText,
		// Present only on session/new: KAS takes it as the new session's id, so every frame it sends for the session,
		// even before the reply, names an id marotte already routes.
		sessionGate: func(s *SessionSpawn) (any, bool) { return s.OverrideSessionID, s.OverrideSessionID != "" },
		send:        true,
	},
	{
		key:      "nonInteractive",
		door:     doorSession,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "shellEnvironment",
		door:     doorSession,
		resolver: resolverObject,
		send:     false,
	},
	{
		key:      "disableAutoCompaction",
		door:     doorSession,
		resolver: resolverSetting,
		// Always present: KAS persists the resolved value, so a session once
		// opened with true (by another client or an earlier marotte setting)
		// stays off unless every load sends false explicitly.
		sessionGate: func(s *SessionSpawn) (any, bool) { return enabledIf(s.DisableAutoCompaction), true },
		send:        true,
	},
	{
		// KAS's own compaction threshold; withheld because disableAutoCompaction turns KAS's compaction off on every
		// chat session, marotte's compactor running at a turn's end instead.
		key:      "compaction",
		door:     doorSession,
		resolver: resolverSettingObject,
		send:     false,
	},
	{
		key:      "memory",
		door:     doorSession,
		resolver: resolverSettingObject,
		// Always present: an absent key leaves the chat's memory to KAS's
		// fallback config, not the user's choice.
		sessionGate: func(s *SessionSpawn) (any, bool) {
			return map[string]any{"mode": cmp.Or(s.MemoryMode, "disabled"), "reflection": s.MemoryReflection}, true
		},
		send: true,
	},
	{
		key:      "userMemoryOptIn",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "memoryEnable",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "streamingShellContent",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "agentHostsForegroundShell",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "diagrams",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "approveForMeOption",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "c2s",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "c2sViews",
		door:     doorConnection,
		resolver: resolverCapability,
		send:     false,
	},
	{
		key:      "semanticReview",
		door:     doorConnection,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_AUTH_EXPIRY_RETRY_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_BACKGROUND_EXECUTION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_CGS_DELEGATION_V2_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_CONSOLIDATE_DISCLOSURES_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_KIRO_INFRA_SAFETY_MONITOR_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_KUTS_TELEMETRY_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_IMPORT_FEATURE_EXTERNAL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_IMPORT_FEATURE_INTERNAL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_QUALITY_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MEMORY_SYNC_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_MODEL_FALLBACK_AUTO_REFUSAL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:          "KIRO_FEATURE_MODEL_FALLBACK_AVAILABILITY_ENABLED",
		door:         doorEnvironment,
		resolver:     resolverEnv,
		engineGate:   func(s *EngineSpawn) (any, bool) { return envBool(s.ModelFallback) },
		send:         true,
		settingOwned: true,
	},
	{
		key:          "KIRO_FEATURE_MODEL_FALLBACK_REFUSAL_ENABLED",
		door:         doorEnvironment,
		resolver:     resolverEnv,
		engineGate:   func(s *EngineSpawn) (any, bool) { return envBool(s.ModelFallback) },
		send:         true,
		settingOwned: true,
	},
	{
		key:      "KIRO_FEATURE_SESSION_TITLE_LLM_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_SKILL_NAME_QUALIFIERS_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_STALL_TOOL_CONTINUATION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_STEERING_SUPERVISOR_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_STREAM_IDLE_WATCHDOG_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_UNIFIED_AGENT_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_UNIVERSAL_BACKGROUND_TOOL_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_USER_AGENT_REFACTORING_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_VALIDATION_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_VIBE_TO_DEFAULT_V2_ROUTING_ENABLED",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_DISABLE_EXPERIMENT_CONFIG",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_DISABLE_RECAP",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_DISABLE_SUGGESTED_PROMPTS",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
	{
		key:      "KIRO_TRUST_CLASSIFIER_MODE",
		door:     doorEnvironment,
		resolver: resolverEnvMode,
		value:    "enforce",
		send:     true,
	},
	{
		key:      "KIRO_FEATURE_CASCADE_CONFIG",
		door:     doorEnvironment,
		resolver: resolverEnvJSON,
		engineGate: func(s *EngineSpawn) (any, bool) {
			return jsonChoice(s.AutoRouting,
				map[string]any{"cascadeEnabled": true, "cascadeClientNotificationEnabled": true},
				map[string]any{"cascadeEnabled": false})
		},
		send:         true,
		settingOwned: true,
	},
	{
		key:      "cascade",
		door:     doorSession,
		resolver: resolverSetting,
		send:     false,
	},
	{
		key:      "KIRO_FEATURE_DYNAMIC_DELEGATION_CONFIG",
		door:     doorEnvironment,
		resolver: resolverEnvJSON,
		engineGate: func(s *EngineSpawn) (any, bool) {
			return jsonChoice(s.AutoDelegation,
				map[string]any{"delegationEnabled": true},
				map[string]any{"delegationEnabled": false})
		},
		send:         true,
		settingOwned: true,
	},
	{
		key:      "contextBreakdown",
		door:     doorPrompt,
		resolver: resolverEnum,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			return chatTurnBreakdown, p.StepMessage == ""
		},
	},
	{
		key:      "ttft",
		door:     doorPrompt,
		resolver: resolverCapability,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			return true, p.StepMessage == ""
		},
	},
	{
		key:      "automaticTurnProfiling",
		door:     doorSession,
		resolver: resolverObject,
		sessionGate: func(s *SessionSpawn) (any, bool) {
			return map[string]any{"ttft": true, "contextBreakdown": chatTurnBreakdown}, s.ProfileAgentTurns
		},
		send: true,
	},
	{
		key:      "displayText",
		door:     doorPrompt,
		resolver: resolverText,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			label := cmp.Or(p.Label, p.StepMessage)
			return label, label != ""
		},
	},
	{
		key:      "userMessageTag",
		door:     doorPrompt,
		resolver: resolverText,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			return p.Tag, p.Tag != ""
		},
	},
	{
		key:      "callerLabel",
		door:     doorPrompt,
		resolver: resolverText,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			return p.Caller, p.Caller != ""
		},
	},
	{
		key:      "outputStyle",
		door:     doorPrompt,
		resolver: resolverEnum,
		send:     true,
		promptGate: func(p *Prompt) (any, bool) {
			return p.OutputStyle, p.OutputStyle != ""
		},
	},
	{
		key:      "KIRO_DISABLE_SESSION_TITLE_LLM",
		door:     doorEnvironment,
		resolver: resolverEnv,
		send:     false,
	},
}
