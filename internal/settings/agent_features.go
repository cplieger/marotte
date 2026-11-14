package settings

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
)

// The values of KeySpecPlanning: KAS's own specPlan workflow vocabulary, plus off.
const (
	SpecPlanningOff   = "off"
	SpecPlanningQuick = "quick"
	specPlanningFull  = "full"
)

// The three values of a setting that follows kiro-cli until the user chooses
// (KeyWorkValidation, KeyCloudFormationSafety, KeyAutoRouting, KeyAutoDelegation). FeatureFollowKiro sends nothing,
// so KAS's own experiment decides.
const (
	FeatureFollowKiro = ""
	FeatureOn         = "on"
	FeatureOff        = "off"
)

// FeatureEnforce is KeyCloudFormationSafety's fourth value: KAS's infraSafety enforce, which blocks a change that breaks
// a rule until the user allows it.
const FeatureEnforce = "enforce"

// KAS's sendMessageDeliveryLimit default and bounds (Koi, Tgt and xgt in 3.2.0). The default withholds the key.
const (
	DefaultChatMessageDeliveryLimit = 50
	minChatMessageDeliveryLimit     = 1
	maxChatMessageDeliveryLimit     = 500
)

// workflowCreatorEfforts is KAS's workflowsCreatorEffort vocabulary (gce in 3.2.0); KAS drops any other value.
var workflowCreatorEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// KAS's sessionToolsAwarenessMode vocabulary, unset reading as direct: direct gives the list, search and summary
// tools, consult gives consult_sessions only.
const (
	ChatAwarenessDirect  = "direct"
	ChatAwarenessConsult = "consult"
)

var chatAwarenessModes = []string{ChatAwarenessDirect, ChatAwarenessConsult}

// OutputStyleDefault is KAS's default output style id, the one prompts never send.
const OutputStyleDefault = "default"

// maxOutputStyleLen bounds a stored output style id; KAS's ids are short lower-case words.
const maxOutputStyleLen = 64

// minTerminalCommandTimeoutMs and MaxTerminalCommandTimeoutMs are KAS's own
// bounds for settings.terminal.commandTimeoutMs; 0 means unset (KAS's 120 s).
const (
	minTerminalCommandTimeoutMs = 1000
	maxTerminalCommandTimeoutMs = 1_800_000
)

// Defaults for the agent-capability settings, matching kiro-cli's own except
// DefaultWorkflowsEnabled, on by the user's choice (the TUI's is off).
const (
	defaultSpecPlanning             = SpecPlanningOff
	defaultSpecPlanningAskFirst     = false
	defaultSteeringReminders        = false
	DefaultWorkflowsEnabled         = true
	defaultChatMessaging            = false
	defaultChatAwareness            = false
	defaultWorkflowCreatorEffort    = ""
	defaultChatAwarenessMode        = ""
	defaultOutputStyle              = OutputStyleDefault
	defaultWorkValidation           = FeatureFollowKiro
	defaultCloudFormationSafety     = FeatureFollowKiro
	defaultAutoRouting              = FeatureFollowKiro
	defaultAutoDelegation           = FeatureFollowKiro
	defaultModelFallback            = true
	DefaultWorkflowLaunch           = marotte.WorkflowLaunchWhenAsked
	defaultTerminalCommandTimeoutMs = 0
)

// ValidSpecPlanning reports whether v is one of the three spec-planning values.
func ValidSpecPlanning(v string) bool {
	return v == SpecPlanningOff || v == SpecPlanningQuick || v == specPlanningFull
}

// ValidWorkflowLaunch reports whether v is one of KAS's workflowLaunch values.
func ValidWorkflowLaunch(v string) bool {
	return v == marotte.WorkflowLaunchWhenAsked || v == marotte.WorkflowLaunchLargeTasks || v == marotte.WorkflowLaunchDelegateAll
}

// ValidFeatureChoice reports whether v is a follow-kiro-cli choice.
func ValidFeatureChoice(v string) bool {
	return v == FeatureFollowKiro || v == FeatureOn || v == FeatureOff
}

// ValidCloudFormationChoice reports whether v is a CloudFormation check value: a follow-kiro-cli choice or enforce.
func ValidCloudFormationChoice(v string) bool {
	return ValidFeatureChoice(v) || v == FeatureEnforce
}

// ValidChatMessageDeliveryLimit reports whether n is inside KAS's sendMessageDeliveryLimit bounds.
func ValidChatMessageDeliveryLimit(n int) bool {
	return n >= minChatMessageDeliveryLimit && n <= maxChatMessageDeliveryLimit
}

// ValidWorkflowCreatorEffort reports whether v is unset ("") or one of KAS's creator effort levels.
func ValidWorkflowCreatorEffort(v string) bool {
	return v == "" || slices.Contains(workflowCreatorEfforts, v)
}

// ValidChatAwarenessMode reports whether v is unset ("") or one of KAS's awareness modes.
func ValidChatAwarenessMode(v string) bool {
	return v == "" || slices.Contains(chatAwarenessModes, v)
}

// ThreeStateChoice answers a follow-kiro-cli setting's effective choice by key, false for a key that
// is not one.
func ThreeStateChoice(e *marotte.EffectiveSettings, key string) (string, bool) {
	switch key {
	case KeyWorkValidation:
		return e.WorkValidation, true
	case KeyCloudFormationSafety:
		return e.CloudFormationSafetyCheck, true
	case KeyAutoRouting:
		return e.AutoRouting, true
	case KeyAutoDelegation:
		return e.AutoDelegation, true
	}
	return "", false
}

// ValidOutputStyle reports whether v has the shape of an output style id: 1 to 64 bytes of
// [a-z0-9_-]. The vocabulary is KAS's (GET /api/config-template output_styles), so membership is
// not checked: KAS reads an id it does not serve as default.
func ValidOutputStyle(v string) bool {
	if v == "" || len(v) > maxOutputStyleLen {
		return false
	}
	for i := range len(v) {
		c := v[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ValidTerminalCommandTimeoutMs reports whether ms is unset (0) or inside KAS's
// bounds; KAS ignores the whole terminal object for a value outside them.
func ValidTerminalCommandTimeoutMs(ms int) bool {
	return ms == 0 || (ms >= minTerminalCommandTimeoutMs && ms <= maxTerminalCommandTimeoutMs)
}

// decodeChecked is decodeInto plus a value check, so a well-typed value the UI
// cannot produce keeps the default in the effective view and at spawn alike.
func decodeChecked[T any](dst *T, raw json.RawMessage, key string, valid func(T) bool) error {
	var v T
	if err := decodeInto(&v, raw); err != nil {
		return err
	}
	if !valid(v) {
		return fmt.Errorf("settings: %s %v is not a value the setting accepts", key, v)
	}
	*dst = v
	return nil
}

var agentPatchChecks = map[string]func(json.RawMessage) error{
	KeySpecPlanning: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeySpecPlanning, ValidSpecPlanning)
	},
	KeyWorkValidation: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyWorkValidation, ValidFeatureChoice)
	},
	KeyCloudFormationSafety: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyCloudFormationSafety, ValidCloudFormationChoice)
	},
	KeyAutoRouting: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyAutoRouting, ValidFeatureChoice)
	},
	KeyAutoDelegation: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyAutoDelegation, ValidFeatureChoice)
	},
	KeyOutputStyle: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyOutputStyle, ValidOutputStyle)
	},
	KeyWorkflowLaunch: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyWorkflowLaunch, ValidWorkflowLaunch)
	},
	KeyTerminalCommandTimeoutMs: func(r json.RawMessage) error {
		var v int
		return decodeChecked(&v, r, KeyTerminalCommandTimeoutMs, ValidTerminalCommandTimeoutMs)
	},
	KeyChatMessageDeliveryLimit: func(r json.RawMessage) error {
		var v int
		return decodeChecked(&v, r, KeyChatMessageDeliveryLimit, ValidChatMessageDeliveryLimit)
	},
	KeyWorkflowCreatorEffort: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyWorkflowCreatorEffort, ValidWorkflowCreatorEffort)
	},
	KeyChatAwarenessMode: func(r json.RawMessage) error {
		var v string
		return decodeChecked(&v, r, KeyChatAwarenessMode, ValidChatAwarenessMode)
	},
	KeySpecPlanningAskFirst: func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeySteeringReminders:    func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyWorkflowsEnabled:     func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyChatMessaging:        func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyChatAwareness:        func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyModelFallback:        func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyInterruptMode: func(r json.RawMessage) error {
		var m marotte.InterruptMode
		return decodeInterruptMode(&m, r)
	},
}

// validateAgentPatch refuses a patch whose agent-capability keys carry a value
// the UI cannot produce. A patch carrying none of them passes.
func validateAgentPatch(patch map[string]json.RawMessage) error {
	if raw, ok := patch[KeyEngineHeapMiB]; ok {
		if err := validateEngineHeapMiB(raw, nodeOptionsEnv(), containerLimit); err != nil {
			return err
		}
	}
	for key, check := range agentPatchChecks {
		raw, ok := patch[key]
		if !ok {
			continue
		}
		if check(raw) != nil {
			return fmt.Errorf("%s has a value the setting does not accept", key)
		}
	}
	for key, check := range knowledgePatchChecks {
		raw, ok := patch[key]
		if !ok {
			continue
		}
		if err := check(raw); err != nil {
			return err
		}
	}
	return nil
}
