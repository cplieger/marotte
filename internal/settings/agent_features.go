package settings

import (
	"encoding/json"
	"fmt"

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

// The values of KeyOutputStyle, KAS's outputStyle option ids.
const (
	OutputStyleDefault = "default"
	OutputStyleConcise = "concise"
)

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
	defaultInlineAgents             = false
	defaultSteeringReminders        = false
	DefaultWorkflowsEnabled         = true
	defaultOutputStyle              = OutputStyleDefault
	defaultWorkValidation           = FeatureFollowKiro
	defaultCloudFormationSafety     = FeatureFollowKiro
	defaultAutoRouting              = FeatureFollowKiro
	defaultAutoDelegation           = FeatureFollowKiro
	defaultTerminalCommandTimeoutMs = 0
)

// ValidSpecPlanning reports whether v is one of the three spec-planning values.
func ValidSpecPlanning(v string) bool {
	return v == SpecPlanningOff || v == SpecPlanningQuick || v == specPlanningFull
}

// ValidFeatureChoice reports whether v is a follow-kiro-cli choice.
func ValidFeatureChoice(v string) bool {
	return v == FeatureFollowKiro || v == FeatureOn || v == FeatureOff
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

// ValidOutputStyle reports whether v is an output style id.
func ValidOutputStyle(v string) bool {
	return v == OutputStyleDefault || v == OutputStyleConcise
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
		return decodeChecked(&v, r, KeyCloudFormationSafety, ValidFeatureChoice)
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
	KeyTerminalCommandTimeoutMs: func(r json.RawMessage) error {
		var v int
		return decodeChecked(&v, r, KeyTerminalCommandTimeoutMs, ValidTerminalCommandTimeoutMs)
	},
	KeySpecPlanningAskFirst: func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyInlineAgents:         func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeySteeringReminders:    func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
	KeyWorkflowsEnabled:     func(r json.RawMessage) error { var b bool; return decodeInto(&b, r) },
}

// ValidateAgentPatch refuses a patch whose agent-capability keys carry a value
// the UI cannot produce. A patch carrying none of them passes.
func ValidateAgentPatch(patch map[string]json.RawMessage) error {
	for key, check := range agentPatchChecks {
		raw, ok := patch[key]
		if !ok {
			continue
		}
		if check(raw) != nil {
			return fmt.Errorf("%s has a value the setting does not accept", key)
		}
	}
	return nil
}
