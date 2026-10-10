package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// errStoredNull marks a key whose stored value is the JSON literal null. It is
// package-private because no caller branches on it: EffectiveFrom reports the KEY,
// and why that key was refused is the same answer either way.
var errStoredNull = errors.New("settings: stored value is null")

// defaultKnowledgeEnabled is TRUE, shared with internal/agent's knowledgeEnabled: the
// knowledge tool predates the switch, so absent must not read as off.
const defaultKnowledgeEnabled = true

// DefaultContentCollectionEnabled is the content-collection switch's default: off,
// so nothing is offered for service improvement until the user opts in.
const DefaultContentCollectionEnabled = false

// defaultGuardPayloadLinks is TRUE because the guard closes the one channel that
// moves workspace data off the box on a single click with no ask; off is a choice
// the user makes, never the reading of an absent key.
const defaultGuardPayloadLinks = true

// DefaultAutoCompactPct is the point at which marotte adds nothing to KAS's own 80/95, which
// is why an invalid stored value reads as it.
const (
	DefaultAutoCompactionEnabled = true
	DefaultAutoCompactPct        = 80
)

// ValidAutoCompactPct reports whether pct is a value the slider can produce:
// 50..90 in steps of 5.
func ValidAutoCompactPct(pct int) bool {
	return pct >= 50 && pct <= 90 && pct%5 == 0
}

// DefaultNotifyPRStatus is OFF while its siblings are ON: a PR's CI verdict is already on the
// forge, where the other two report unobserved work.
const (
	DefaultNotifyAgentFinished = true
	DefaultNotifyPRStatus      = false
	DefaultNotifyRunOutcome    = true
)

// EffectiveDefaults is the ONE statement of every client-rendered preference's value when
// config.json is SILENT. It is not a consumer's answer for an UNREADABLE file, which differs
// per consumer (composition's chatRetention purges nothing then), so the two must not merge.
func EffectiveDefaults() marotte.EffectiveSettings {
	return marotte.EffectiveSettings{
		AgentIgnoreFiles:      DefaultAgentIgnoreFiles(),
		AutoCompactionEnabled: DefaultAutoCompactionEnabled,
		AutoCompactPct:        DefaultAutoCompactPct,
		ChatRetentionDays:     DefaultChatRetentionDays,
		GuardPayloadLinks:     defaultGuardPayloadLinks,
		KnowledgeEnabled:      defaultKnowledgeEnabled,
		ContentCollection:     DefaultContentCollectionEnabled,
		MemoryMode:            DefaultMemoryMode,
		// The agent-capability defaults are kiro-cli's own; "" on a follow-kiro
		// choice sends nothing, so kiro-cli's experiment decides.
		SpecPlanning:              defaultSpecPlanning,
		SpecPlanningAskFirst:      defaultSpecPlanningAskFirst,
		InlineAgents:              defaultInlineAgents,
		SteeringReminders:         defaultSteeringReminders,
		WorkflowsEnabled:          DefaultWorkflowsEnabled,
		WorkValidation:            defaultWorkValidation,
		CloudFormationSafetyCheck: defaultCloudFormationSafety,
		AutoRouting:               defaultAutoRouting,
		AutoDelegation:            defaultAutoDelegation,
		OutputStyle:               defaultOutputStyle,
		TerminalCommandTimeoutMs:  defaultTerminalCommandTimeoutMs,
		// Empty objects, not nil: nil marshals as null and no field here is optional.
		LastEffortByModel: map[string]string{},
		KiroDefaults:      map[string]marotte.KiroDefault{},
		// The per-kind push defaults are not uniform; no polarity is safe for a client to guess.
		NotifyAgentFinished: DefaultNotifyAgentFinished,
		NotifyPRStatus:      DefaultNotifyPRStatus,
		NotifyRunOutcome:    DefaultNotifyRunOutcome,
		// Everything else is its zero value deliberately (scheduled auto-approve fail-closed).
	}
}

// RetentionEnabled reports whether chat retention keeps a closed chat's record. It FAILS
// TOWARD KEEPING: unreadable answers ON, absent takes the default, only 0 answers OFF. It must
// never share a reader with the purge, whose 0-sentinel's safe direction is the inverse.
func RetentionEnabled(ctx context.Context, configDir string) bool {
	days, ok, err := FieldStrict[int](ctx, configDir, KeyChatRetentionDays)
	if err != nil {
		slog.Error("chat retention: config.json is present but unreadable; treating retention as ON so this close deletes nothing",
			"key", KeyChatRetentionDays, "error", err)
		return true
	}
	if !ok {
		days = DefaultChatRetentionDays
	}
	return days != 0
}

// MCPWaitForReady reports whether KAS's rendered MCP file carries waitForReady; anything but a
// stored true is off. readable is false over an unreadable config.json, where off is no answer.
func MCPWaitForReady(ctx context.Context, configDir string) (on, readable bool) {
	return FieldOr(ctx, configDir, KeyMCPWaitForReady, false)
}

// DebugLogs reports whether the process logs at debug level; anything but a readable true is
// info, so a broken settings file never drops into debug.
func DebugLogs(ctx context.Context, configDir string) bool {
	on, ok := Field[bool](ctx, configDir, KeyDebugLogs)
	return ok && on
}

// EffectiveFrom resolves the stored document into the client's view: EffectiveDefaults with
// every stored value that FITS ITS TYPE overlaid, plus the keys that did not fit. Value
// validity is the client's (theme "purple" passes). A nil or empty map yields the defaults.
func EffectiveFrom(stored map[string]json.RawMessage) (effective marotte.EffectiveSettings, rejected []string) {
	out := EffectiveDefaults()
	for key, set := range effectiveSetters(&out) {
		raw, ok := stored[key]
		if !ok {
			continue
		}
		if err := set(raw); err != nil {
			rejected = append(rejected, key)
		}
	}
	return out, rejected
}

// effectiveSetters maps each key to the one decode-and-assign for its field, bound to the
// caller's struct.
func effectiveSetters(out *marotte.EffectiveSettings) map[string]func(json.RawMessage) error {
	return map[string]func(json.RawMessage) error{
		KeyAgentIgnoreFiles:      func(r json.RawMessage) error { return decodeInto(&out.AgentIgnoreFiles, r) },
		KeyAutoCompactionEnabled: func(r json.RawMessage) error { return decodeInto(&out.AutoCompactionEnabled, r) },
		KeyAutoCompactPct:        func(r json.RawMessage) error { return decodeAutoCompactPct(&out.AutoCompactPct, r) },
		KeyChatRetentionDays:     func(r json.RawMessage) error { return decodeInto(&out.ChatRetentionDays, r) },
		KeyTheme:                 func(r json.RawMessage) error { return decodeInto(&out.Theme, r) },
		KeyFBPath:                func(r json.RawMessage) error { return decodeInto(&out.FBPath, r) },
		KeyLastModel:             func(r json.RawMessage) error { return decodeInto(&out.LastModel, r) },
		KeyLastEffortByModel:     func(r json.RawMessage) error { return decodeInto(&out.LastEffortByModel, r) },
		KeyLastMergeMethod:       func(r json.RawMessage) error { return decodeInto(&out.LastMergeMethod, r) },
		KeyKnowledgeEnabled:      func(r json.RawMessage) error { return decodeInto(&out.KnowledgeEnabled, r) },
		KeyContentCollectionEnabled: func(r json.RawMessage) error {
			return decodeInto(&out.ContentCollection, r)
		},
		KeyGuardPayloadLinks:    func(r json.RawMessage) error { return decodeInto(&out.GuardPayloadLinks, r) },
		KeyToolSearchEnabled:    func(r json.RawMessage) error { return decodeInto(&out.ToolSearchEnabled, r) },
		KeyMemoryMode:           func(r json.RawMessage) error { return decodeMemoryMode(&out.MemoryMode, r) },
		KeyMCPWaitForReady:      func(r json.RawMessage) error { return decodeInto(&out.MCPWaitForReady, r) },
		KeyNotificationsEnabled: func(r json.RawMessage) error { return decodeInto(&out.NotificationsEnabled, r) },
		KeyNotifyAgentFinished:  func(r json.RawMessage) error { return decodeInto(&out.NotifyAgentFinished, r) },
		KeyNotifyPRStatus:       func(r json.RawMessage) error { return decodeInto(&out.NotifyPRStatus, r) },
		KeyNotifyRunOutcome:     func(r json.RawMessage) error { return decodeInto(&out.NotifyRunOutcome, r) },
		KeySupervisedDefault:    func(r json.RawMessage) error { return decodeInto(&out.SupervisedDefault, r) },
		KeyScheduledAutoApprove: func(r json.RawMessage) error { return decodeInto(&out.ScheduledAutoApprove, r) },
		KeyDebugLogs:            func(r json.RawMessage) error { return decodeInto(&out.DebugLogs, r) },
		KeySpecPlanning: func(r json.RawMessage) error {
			return decodeChecked(&out.SpecPlanning, r, KeySpecPlanning, ValidSpecPlanning)
		},
		KeySpecPlanningAskFirst: func(r json.RawMessage) error { return decodeInto(&out.SpecPlanningAskFirst, r) },
		KeyInlineAgents:         func(r json.RawMessage) error { return decodeInto(&out.InlineAgents, r) },
		KeySteeringReminders:    func(r json.RawMessage) error { return decodeInto(&out.SteeringReminders, r) },
		KeyWorkflowsEnabled:     func(r json.RawMessage) error { return decodeInto(&out.WorkflowsEnabled, r) },
		KeyWorkValidation: func(r json.RawMessage) error {
			return decodeChecked(&out.WorkValidation, r, KeyWorkValidation, ValidFeatureChoice)
		},
		KeyCloudFormationSafety: func(r json.RawMessage) error {
			return decodeChecked(&out.CloudFormationSafetyCheck, r, KeyCloudFormationSafety, ValidFeatureChoice)
		},
		KeyAutoRouting: func(r json.RawMessage) error {
			return decodeChecked(&out.AutoRouting, r, KeyAutoRouting, ValidFeatureChoice)
		},
		KeyAutoDelegation: func(r json.RawMessage) error {
			return decodeChecked(&out.AutoDelegation, r, KeyAutoDelegation, ValidFeatureChoice)
		},
		KeyOutputStyle: func(r json.RawMessage) error {
			return decodeChecked(&out.OutputStyle, r, KeyOutputStyle, ValidOutputStyle)
		},
		KeyTerminalCommandTimeoutMs: func(r json.RawMessage) error {
			return decodeChecked(&out.TerminalCommandTimeoutMs, r, KeyTerminalCommandTimeoutMs, ValidTerminalCommandTimeoutMs)
		},
	}
}

// decodeAutoCompactPct is decodeInto plus the value check the effective view
// otherwise leaves to the client: a well-typed pct the slider cannot produce
// keeps the default, because the agent's policy reads the same value.
func decodeAutoCompactPct(dst *int, raw json.RawMessage) error {
	var pct int
	if err := decodeInto(&pct, raw); err != nil {
		return err
	}
	if !ValidAutoCompactPct(pct) {
		return fmt.Errorf("settings: %s %d is not 50..90 in steps of 5", KeyAutoCompactPct, pct)
	}
	*dst = pct
	return nil
}

// decodeInto decodes raw into a scratch T and assigns it only on success. A stored JSON null
// is refused: encoding/json treats it as a no-op for most targets, which would assign the
// scratch's zero (0 retention days, "delete on close").
func decodeInto[T any](dst *T, raw json.RawMessage) error {
	if string(raw) == "null" {
		return errStoredNull
	}
	var scratch T
	if err := json.Unmarshal(raw, &scratch); err != nil {
		return err
	}
	*dst = scratch
	return nil
}
