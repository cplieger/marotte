package settings

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestValidateAgentPatch_RefusesValuesTheUICannotProduce(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr bool
	}{
		{name: "spec quick", key: KeySpecPlanning, raw: `"quick"`},
		{name: "spec off", key: KeySpecPlanning, raw: `"off"`},
		{name: "spec unknown workflow", key: KeySpecPlanning, raw: `"deep"`, wantErr: true},
		{name: "validation follow kiro", key: KeyWorkValidation, raw: `""`},
		{name: "validation on", key: KeyWorkValidation, raw: `"on"`},
		{name: "validation bool", key: KeyWorkValidation, raw: `true`, wantErr: true},
		{name: "cfn off", key: KeyCloudFormationSafety, raw: `"off"`},
		{name: "auto routing follow kiro", key: KeyAutoRouting, raw: `""`},
		{name: "auto routing on", key: KeyAutoRouting, raw: `"on"`},
		{name: "auto routing bool", key: KeyAutoRouting, raw: `true`, wantErr: true},
		{name: "smart helpers off", key: KeyAutoDelegation, raw: `"off"`},
		{name: "smart helpers unknown", key: KeyAutoDelegation, raw: `"auto"`, wantErr: true},
		{name: "cfn enforce", key: KeyCloudFormationSafety, raw: `"enforce"`, wantErr: true},
		{name: "style concise", key: KeyOutputStyle, raw: `"concise"`},
		{name: "style verbose", key: KeyOutputStyle, raw: `"verbose"`, wantErr: true},
		{name: "timeout unset", key: KeyTerminalCommandTimeoutMs, raw: `0`},
		{name: "timeout floor", key: KeyTerminalCommandTimeoutMs, raw: `1000`},
		{name: "timeout ceiling", key: KeyTerminalCommandTimeoutMs, raw: `1800000`},
		{name: "timeout under floor", key: KeyTerminalCommandTimeoutMs, raw: `999`, wantErr: true},
		{name: "timeout over ceiling", key: KeyTerminalCommandTimeoutMs, raw: `1800001`, wantErr: true},
		{name: "timeout null", key: KeyTerminalCommandTimeoutMs, raw: `null`, wantErr: true},
		{name: "ask first bool", key: KeySpecPlanningAskFirst, raw: `false`},
		{name: "inline agents string", key: KeyInlineAgents, raw: `"yes"`, wantErr: true},
		{name: "reminders null", key: KeySteeringReminders, raw: `null`, wantErr: true},
		{name: "workflows off", key: KeyWorkflowsEnabled, raw: `false`},
		{name: "workflows string", key: KeyWorkflowsEnabled, raw: `"off"`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patch := map[string]json.RawMessage{tc.key: json.RawMessage(tc.raw)}
			if err := ValidateAgentPatch(patch); (err != nil) != tc.wantErr {
				t.Errorf("ValidateAgentPatch(%s: %s) = %v, want error %v", tc.key, tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestValidateAgentPatch_PassesAPatchWithNoAgentKeys(t *testing.T) {
	if err := ValidateAgentPatch(map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`)}); err != nil {
		t.Errorf("ValidateAgentPatch(theme only) = %v, want nil", err)
	}
}

func TestEffective_AgentCapabilityDefaults(t *testing.T) {
	d := EffectiveDefaults()
	checks := []struct {
		field     string
		got, want any
	}{
		{"SpecPlanning", d.SpecPlanning, defaultSpecPlanning},
		{"SpecPlanningAskFirst", d.SpecPlanningAskFirst, defaultSpecPlanningAskFirst},
		{"InlineAgents", d.InlineAgents, defaultInlineAgents},
		{"SteeringReminders", d.SteeringReminders, defaultSteeringReminders},
		{"WorkflowsEnabled", d.WorkflowsEnabled, DefaultWorkflowsEnabled},
		{"WorkValidation", d.WorkValidation, defaultWorkValidation},
		{"CloudFormationSafetyCheck", d.CloudFormationSafetyCheck, defaultCloudFormationSafety},
		{"AutoRouting", d.AutoRouting, FeatureFollowKiro},
		{"AutoDelegation", d.AutoDelegation, FeatureFollowKiro},
		{"OutputStyle", d.OutputStyle, defaultOutputStyle},
		{"TerminalCommandTimeoutMs", d.TerminalCommandTimeoutMs, defaultTerminalCommandTimeoutMs},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("EffectiveDefaults().%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestEffective_AgentCapabilityDefaultsAreKiroCLIs(t *testing.T) {
	// kiro-cli's own spec planning skips the clarifying questions by default.
	if defaultSpecPlanning != SpecPlanningOff || defaultSpecPlanningAskFirst {
		t.Errorf("spec planning defaults = (%q, ask %v), want (off, false)", defaultSpecPlanning, defaultSpecPlanningAskFirst)
	}
	if defaultWorkValidation != FeatureFollowKiro || defaultCloudFormationSafety != FeatureFollowKiro {
		t.Errorf("follow-kiro defaults = (%q, %q), want both empty", defaultWorkValidation, defaultCloudFormationSafety)
	}
	// Workflows is the one departure from kiro-cli: on, by the user's choice.
	if !DefaultWorkflowsEnabled {
		t.Error("DefaultWorkflowsEnabled = false, want true")
	}
	if defaultOutputStyle != OutputStyleDefault || defaultTerminalCommandTimeoutMs != 0 {
		t.Errorf("style/timeout defaults = (%q, %d), want (default, 0)", defaultOutputStyle, defaultTerminalCommandTimeoutMs)
	}
}

func TestEffective_AStoredValueOutsideTheVocabularyReadsAsTheDefault(t *testing.T) {
	got, rejected := EffectiveFrom(map[string]json.RawMessage{
		KeySpecPlanning:             json.RawMessage(`"deep"`),
		KeyWorkValidation:           json.RawMessage(`"maybe"`),
		KeyAutoRouting:              json.RawMessage(`true`),
		KeyAutoDelegation:           json.RawMessage(`"on"`),
		KeyOutputStyle:              json.RawMessage(`"concise"`),
		KeyTerminalCommandTimeoutMs: json.RawMessage(`500`),
	})
	if got.SpecPlanning != SpecPlanningOff {
		t.Errorf("SpecPlanning = %q, want the default off", got.SpecPlanning)
	}
	if got.WorkValidation != FeatureFollowKiro {
		t.Errorf("WorkValidation = %q, want the follow-kiro default", got.WorkValidation)
	}
	if got.AutoRouting != FeatureFollowKiro {
		t.Errorf("AutoRouting = %q, want the follow-kiro default for a bool", got.AutoRouting)
	}
	if got.AutoDelegation != FeatureOn {
		t.Errorf("AutoDelegation = %q, want the stored on", got.AutoDelegation)
	}
	if got.OutputStyle != OutputStyleConcise {
		t.Errorf("OutputStyle = %q, want the stored concise", got.OutputStyle)
	}
	if got.TerminalCommandTimeoutMs != 0 {
		t.Errorf("TerminalCommandTimeoutMs = %d, want 0 for a value under KAS's floor", got.TerminalCommandTimeoutMs)
	}
	for _, k := range []string{KeySpecPlanning, KeyWorkValidation, KeyAutoRouting, KeyTerminalCommandTimeoutMs} {
		if !slices.Contains(rejected, k) {
			t.Errorf("rejected = %v, want it to name %s", rejected, k)
		}
	}
	if slices.Contains(rejected, KeyOutputStyle) {
		t.Errorf("rejected = %v, want the valid output_style accepted", rejected)
	}
}
