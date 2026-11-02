package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// writeAgentSettings replaces config.json by rename, as the settings writer does: an in-place rewrite of the same
// size inside one file-timestamp tick keeps the identity the read cache keys on.
func writeAgentSettings(t *testing.T, dir, body string) {
	t.Helper()
	rewriteConfigByHand(t, dir, body)
}

func TestFeatures_TakesValidValuesAndDefaultsTheRest(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{
		"spec_planning": "quick",
		"spec_planning_ask_first": true,
		"work_validation": "off",
		"cloudformation_safety_check": "enforce",
		"auto_routing": "on",
		"auto_delegation": true,
		"model_fallback_enabled": true,
		"terminal_command_timeout_ms": 500
	}`)

	session := sessionFeatures(t.Context(), dir, nil)
	wantSession := marotte.SessionFeatures{
		SpecPlan:             settings.SpecPlanningQuick,
		SpecAskClarification: true,
		WorkValidation:       settings.FeatureOff,
		// Absent: workflows default on.
		Workflows: true,
	}
	if session != wantSession {
		t.Errorf("sessionFeatures() = %+v, want %+v", session, wantSession)
	}
	engine := engineFeatures(t.Context(), dir)
	wantEngine := marotte.EngineFeatures{
		AutoRouting:   settings.FeatureOn,
		ModelFallback: true,
		// Invalid values fall back, so KAS's own experiment and 120 s decide.
	}
	if engine != wantEngine {
		t.Errorf("engineFeatures() = %+v, want %+v", engine, wantEngine)
	}
}

func TestFeatures_WorkflowsFollowsTheToggle(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{}`, true},
		{`{"workflows_enabled": false}`, false},
		{`{"workflows_enabled": "off"}`, true},
	} {
		dir := t.TempDir()
		writeAgentSettings(t, dir, tc.body)
		if got := sessionFeatures(t.Context(), dir, nil).Workflows; got != tc.want {
			t.Errorf("sessionFeatures(%s).Workflows = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestFeatures_SpecPlanningOffSendsNoWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"spec_planning": "off"}`)

	if got := sessionFeatures(t.Context(), dir, nil).SpecPlan; got != "" {
		t.Errorf("sessionFeatures().SpecPlan = %q, want empty for off", got)
	}
}
