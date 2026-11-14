package agent

import (
	"reflect"
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
		"cloudformation_safety_check": "monitor",
		"auto_routing": "on",
		"auto_delegation": true,
		"model_fallback_enabled": true,
		"terminal_command_timeout_ms": 500
	}`)

	session := sessionFeatures(docAt(t, dir))
	wantSession := marotte.SessionFeatures{
		SpecPlan:             settings.SpecPlanningQuick,
		SpecAskClarification: true,
		WorkValidation:       settings.FeatureOff,
		// Absent: workflows default on, at kiro-cli's own workflow use.
		Workflows:      true,
		WorkflowLaunch: marotte.WorkflowLaunchWhenAsked,
	}
	if session != wantSession {
		t.Errorf("sessionFeatures() = %+v, want %+v", session, wantSession)
	}
	engine := engineFeatures(docAt(t, dir))
	wantEngine := marotte.EngineFeatures{
		AutoRouting:   settings.FeatureOn,
		ModelFallback: true,
		// Invalid values fall back, so KAS's own experiment and 120 s decide.
	}
	if !reflect.DeepEqual(engine, wantEngine) {
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
		if got := sessionFeatures(docAt(t, dir)).Workflows; got != tc.want {
			t.Errorf("sessionFeatures(%s).Workflows = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// Messages between chats is off unless the user turns it on, kiro-cli's own sessionToolsMessaging default.
func TestFeatures_MessagingFollowsTheToggle(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"absent", `{}`, false},
		{"on", `{"chat_messaging_enabled": true}`, true},
		{"off", `{"chat_messaging_enabled": false}`, false},
		{"invalid", `{"chat_messaging_enabled": "on"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := sessionFeatures(docAt(t, dir)).Messaging; got != tc.want {
				t.Errorf("sessionFeatures(%s).Messaging = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// Model fallback defaults on, as in kiro-cli, so an absent or invalid value spawns the engine with it on.
func TestEngineFeatures_ModelFallbackDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{}`, true},
		{`{"model_fallback_enabled": false}`, false},
		{`{"model_fallback_enabled": "off"}`, true},
	} {
		dir := t.TempDir()
		writeAgentSettings(t, dir, tc.body)
		if got := engineFeatures(docAt(t, dir)).ModelFallback; got != tc.want {
			t.Errorf("engineFeatures(%s).ModelFallback = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// Search other chats is off unless the user turns it on, kiro-cli's own sessionToolsAwareness default.
func TestFeatures_AwarenessFollowsTheToggle(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"absent", `{}`, false},
		{"on", `{"chat_awareness_enabled": true}`, true},
		{"off", `{"chat_awareness_enabled": false}`, false},
		{"invalid", `{"chat_awareness_enabled": "on"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := sessionFeatures(docAt(t, dir)).Awareness; got != tc.want {
				t.Errorf("sessionFeatures(%s).Awareness = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// The planning effort reaches a session only while workflows are on, so a change with them off reloads no chat.
func TestSessionFeatures_CreatorEffortOnlyWithWorkflows(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"absent", `{}`, ""},
		{"stated", `{"workflow_creator_effort": "high"}`, "high"},
		{"workflows off", `{"workflow_creator_effort": "high", "workflows_enabled": false}`, ""},
		{"invalid", `{"workflow_creator_effort": "extreme"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := sessionFeatures(docAt(t, dir)).WorkflowCreatorEffort; got != tc.want {
				t.Errorf("sessionFeatures(%s).WorkflowCreatorEffort = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// The search method reaches a session only while Search other chats is on, and only from KAS's vocabulary.
func TestSessionFeatures_AwarenessModeOnlyWithAwareness(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"unset", `{"chat_awareness_enabled": true}`, ""},
		{"stated", `{"chat_awareness_enabled": true, "chat_awareness_mode": "consult"}`, "consult"},
		{"awareness off", `{"chat_awareness_mode": "consult"}`, ""},
		{"invalid", `{"chat_awareness_enabled": true, "chat_awareness_mode": "ask"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := sessionFeatures(docAt(t, dir)).AwarenessMode; got != tc.want {
				t.Errorf("sessionFeatures(%s).AwarenessMode = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// The delivery limit reaches the engine only away from kiro-cli's own 50, so saving the default restarts nothing.
func TestEngineFeatures_MessageDeliveryLimitWithholdsTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"absent", `{}`, 0},
		{"kiro-cli's default", `{"chat_message_delivery_limit": 50}`, 0},
		{"set", `{"chat_message_delivery_limit": 12}`, 12},
		{"ceiling", `{"chat_message_delivery_limit": 500}`, 500},
		{"over the ceiling", `{"chat_message_delivery_limit": 501}`, 0},
		{"invalid", `{"chat_message_delivery_limit": "12"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := engineFeatures(docAt(t, dir)).MessageDeliveryLimit; got != tc.want {
				t.Errorf("engineFeatures(%s).MessageDeliveryLimit = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// The Knowledge base indexing rows reach the engine only away from kiro-cli's own values (best, every file, 10000
// files, 512/128 words), and a value KAS would not build with is withheld rather than sent.
func TestEngineFeatures_KnowledgeTuningWithholdsDefaultsAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want marotte.KnowledgeTuning
	}{
		{"absent", `{}`, marotte.KnowledgeTuning{}},
		{"kiro-cli's defaults", `{"knowledge_index_type": "", "knowledge_include_patterns": [],
			"knowledge_exclude_patterns": [], "knowledge_max_files": 10000,
			"knowledge_chunking": {"size": 512, "overlap": 128}}`, marotte.KnowledgeTuning{}},
		{"set", `{"knowledge_index_type": "fast", "knowledge_include_patterns": ["**/*.md"],
			"knowledge_exclude_patterns": ["drafts/**", "**/*.{tmp,bak}"], "knowledge_max_files": 200,
			"knowledge_chunking": {"size": 256, "overlap": 0}}`, marotte.KnowledgeTuning{
			IndexType:       settings.KnowledgeIndexFast,
			IncludePatterns: []string{"**/*.md"},
			ExcludePatterns: []string{"drafts/**", "**/*.{tmp,bak}"},
			MaxFiles:        200,
			Chunking:        marotte.KnowledgeChunking{Size: 256, Overlap: 0},
		}},
		{"best stated", `{"knowledge_index_type": "best"}`, marotte.KnowledgeTuning{IndexType: settings.KnowledgeIndexBest}},
		{"legacy accurate", `{"knowledge_index_type": "accurate"}`, marotte.KnowledgeTuning{}},
		{"empty pattern", `{"knowledge_include_patterns": ["**/*.md", ""]}`, marotte.KnowledgeTuning{}},
		{"padded pattern", `{"knowledge_exclude_patterns": [" drafts/**"]}`, marotte.KnowledgeTuning{}},
		{"no files", `{"knowledge_max_files": 0}`, marotte.KnowledgeTuning{}},
		{"chunk under 64 words", `{"knowledge_chunking": {"size": 63, "overlap": 0}}`, marotte.KnowledgeTuning{}},
		{"overlap as large as the chunk", `{"knowledge_chunking": {"size": 64, "overlap": 64}}`, marotte.KnowledgeTuning{}},
		{"overlap missing", `{"knowledge_chunking": {"size": 256}}`, marotte.KnowledgeTuning{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := engineFeatures(docAt(t, dir)).Knowledge; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("engineFeatures(%s).Knowledge = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// The CloudFormation check's fourth choice, enforce, reaches the engine as itself.
func TestEngineFeatures_CloudFormationTakesEnforce(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"cloudformation_safety_check": "enforce"}`)
	if got := engineFeatures(docAt(t, dir)).InfraSafety; got != settings.FeatureEnforce {
		t.Errorf("engineFeatures().InfraSafety = %q, want %q", got, settings.FeatureEnforce)
	}
}

func TestSessionFeatures_WorkflowLaunchFollowsTheSetting(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"absent", `{}`, marotte.WorkflowLaunchWhenAsked},
		{"stored", `{"workflow_launch": "delegate-all"}`, marotte.WorkflowLaunchDelegateAll},
		{"invalid", `{"workflow_launch": "sometimes"}`, marotte.WorkflowLaunchWhenAsked},
		{"workflows off", `{"workflows_enabled": false, "workflow_launch": "large-tasks"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAgentSettings(t, dir, tc.body)
			if got := sessionFeatures(docAt(t, dir)).WorkflowLaunch; got != tc.want {
				t.Errorf("sessionFeatures(%s).WorkflowLaunch = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestFeatures_SpecPlanningOffSendsNoWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"spec_planning": "off"}`)

	if got := sessionFeatures(docAt(t, dir)).SpecPlan; got != "" {
		t.Errorf("sessionFeatures().SpecPlan = %q, want empty for off", got)
	}
}
