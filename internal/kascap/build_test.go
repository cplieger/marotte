package kascap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// Golden fixtures for the two projections. No agent-server bundle is required, so
// these run in CI as they stand.
// The bundle-dependent census is census_test.go and skips when it is absent.
const (
	initializeGoldenPath  = "testdata/initialize.golden"
	sessionGoldenPath     = "testdata/session.golden"
	environmentGoldenPath = "testdata/environment.golden"
	promptGoldenPath      = "testdata/prompt.golden"
	botRoleGoldenPath     = "testdata/bot_role.golden"
	updateGoldenCmd       = "UPDATE_GOLDEN=1 go test ./internal/kascap/ -run 'TestInitializeDeclaresExactly|TestSessionDoorDeclaresExactly|TestEnvironmentDoorDeclaresExactly|TestPromptDoorDeclaresExactly|TestBotRoleDeclaresExactly'"
)

// Exhaustive is what makes the golden a contract rather than a sample. One matrix per spawn type: a door reads only
// its own.
var engineMatrix = []struct {
	name  string
	spawn EngineSpawn
}{
	{"gates off", EngineSpawn{}},
	{"secret storage", EngineSpawn{SecretStorage: true}},
	// Knowledge is value-gated across TWO keys, the capability (`=== true`) and settings.knowledge
	// (isSettingEnabled).
	{"knowledge on", EngineSpawn{Knowledge: true}},
	{"steering reminders on", EngineSpawn{SteeringReminders: true}},
	{"safety check on", EngineSpawn{InfraSafetyMonitor: "on"}},
	{"safety check off", EngineSpawn{InfraSafetyMonitor: "off"}},
	{"auto routing and smart helpers on", EngineSpawn{AutoRouting: "on", AutoDelegation: "on"}},
	{"auto routing and smart helpers off", EngineSpawn{AutoRouting: "off", AutoDelegation: "off"}},
	{"shell timeout set", EngineSpawn{TerminalCommandTimeoutMs: 300000}},
	{"model fallback on", EngineSpawn{ModelFallback: true}},
	{"telemetry off", EngineSpawn{DisableTelemetry: true}},
	{"every gate on", EngineSpawn{
		SecretStorage: true, Knowledge: true, InfraSafetyMonitor: "on",
		AutoRouting: "on", AutoDelegation: "on",
		TerminalCommandTimeoutMs: 300000, SteeringReminders: true,
		DisableTelemetry: true, ModelFallback: true,
	}},
}

var sessionMatrix = []struct {
	name  string
	spawn SessionSpawn
}{
	{"gates off", SessionSpawn{}},
	{"a minted id", SessionSpawn{OverrideSessionID: "sess_00000000-0000-4000-8000-000000000000"}},
	// Presets need both states: an empty set withholds the policyPreset key entirely (the Custom
	// profile).
	{"one preset", SessionSpawn{Presets: []string{"read-workspace"}}},
	{"several presets", SessionSpawn{Presets: []string{"read-workspace", "read-only-shell", "read-all"}}},
	{"memory read only", SessionSpawn{MemoryMode: "read_only"}},
	{"memory read write", SessionSpawn{MemoryMode: "read_write"}},
	{"memory learn", SessionSpawn{MemoryMode: "read_write", MemoryReflection: true}},
	{"auto compaction off", SessionSpawn{DisableAutoCompaction: true}},
	{"spec planning quick", SessionSpawn{SpecPlan: "quick"}},
	{"spec planning full, ask first", SessionSpawn{SpecPlan: "full", SpecAskClarification: true}},
	{"work validation on", SessionSpawn{WorkValidation: "on"}},
	{"work validation off", SessionSpawn{WorkValidation: "off"}},
	{"workflows on", SessionSpawn{Workflows: true}},
	{"messages between chats on", SessionSpawn{Messaging: true}},
	{"a chat session", SessionSpawn{ProfileAgentTurns: true}},
	{"a Bot's chat session", SessionSpawn{ProfileAgentTurns: true, CustomAgents: []marotte.ClientCustomAgent{probeAgent}}},
	{"every gate on", SessionSpawn{
		OverrideSessionID: "sess_00000000-0000-4000-8000-000000000000",
		Presets:           []string{"read-workspace"},
		MemoryMode:        "read_write", MemoryReflection: true,
		DisableAutoCompaction: true, SpecPlan: "full", SpecAskClarification: true,
		WorkValidation: "on", Workflows: true, Messaging: true, ProfileAgentTurns: true,
		CustomAgents: []marotte.ClientCustomAgent{probeAgent},
	}},
}

// probeAgent is a minimal client custom agent: the session door's bytes for it, not the Bot role's, which
// TestBotRoleDeclaresExactly pins.
var probeAgent = marotte.ClientCustomAgent{ID: "probe-agent", Prompt: "probe", Tools: "*"}

func engineNames() []string {
	names := make([]string, len(engineMatrix))
	for i := range engineMatrix {
		names[i] = engineMatrix[i].name
	}
	return names
}

func sessionNames() []string {
	names := make([]string, len(sessionMatrix))
	for i := range sessionMatrix {
		names[i] = sessionMatrix[i].name
	}
	return names
}

// renderLines marshals one value per line; encoding/json sorts map keys, so the output is deterministic.
func renderLines[T any](t *testing.T, names []string, build func(i int) T) string {
	t.Helper()
	var out strings.Builder
	for i, name := range names {
		raw, err := json.Marshal(build(i))
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		out.Write(raw)
		out.WriteString("\n")
	}
	return out.String()
}

// checkGolden writes the fixture under UPDATE_GOLDEN=1 and compares in every
// case, so one code path both regenerates and asserts; `names` labels each line.
func checkGolden(t *testing.T, path, got string, names []string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with: %s): %v", path, updateGoldenCmd, err)
	}
	if string(want) == got {
		return
	}
	wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(wantLines) != len(gotLines) {
		t.Fatalf(`%s holds %d payload(s), the table produced %d.
A row or a spawnMatrix case changed. Regenerate with: %s`,
			path, len(wantLines), len(gotLines), updateGoldenCmd)
	}
	for i := range wantLines {
		if wantLines[i] == gotLines[i] {
			continue
		}
		name := "case " + strconv.Itoa(i)
		if i < len(names) {
			name = names[i]
		}
		t.Errorf(`%s changed for %q.
A capability was added, removed, renamed, or reshaped. If that is deliberate,
regenerate with:
  %s
--- want
%s
+++ got
%s`, path, name, updateGoldenCmd, wantLines[i], gotLines[i])
	}
}

// TestInitializeDeclaresExactly pins the exact _meta.kiro payload of the initialize handshake for
// every spawn combination.
func TestInitializeDeclaresExactly(t *testing.T) {
	got := renderLines(t, engineNames(), func(i int) map[string]any { return Capabilities(&engineMatrix[i].spawn) })
	checkGolden(t, initializeGoldenPath, got, engineNames())
}

// TestSessionDoorDeclaresExactly pins the payload session/new and session/load carry for every
// session combination.
func TestSessionDoorDeclaresExactly(t *testing.T) {
	got := renderLines(t, sessionNames(), func(i int) map[string]any { return SessionMeta(&sessionMatrix[i].spawn) })
	checkGolden(t, sessionGoldenPath, got, sessionNames())
}

// TestEnvironmentDoorDeclaresExactly pins the KIRO_* assignments ChildEnv hands
// the engine spawn for every engine combination, one JSON array per line.
func TestEnvironmentDoorDeclaresExactly(t *testing.T) {
	got := renderLines(t, engineNames(), func(i int) []string { return ChildEnv(&engineMatrix[i].spawn) })
	checkGolden(t, environmentGoldenPath, got, engineNames())
}

// promptMatrix is every per-prompt state; its order is prompt.golden's line order.
var promptMatrix = []struct {
	name   string
	prompt Prompt
}{
	{"default style", Prompt{Tag: "t-0000000000000001"}},
	{"concise", Prompt{OutputStyle: "concise", Tag: "t-0000000000000002"}},
	{"step message", Prompt{StepMessage: "continue with the plan"}},
	{"labelled chat prompt", Prompt{Label: "Merging findings from a tangent", Tag: "t-0000000000000003"}},
	{"caller-sent chat prompt", Prompt{Caller: "grelot", Tag: "t-0000000000000004"}},
}

// TestPromptDoorDeclaresExactly pins the _meta.kiro payload session/prompt carries for every
// per-prompt state, one JSON object per line.
func TestPromptDoorDeclaresExactly(t *testing.T) {
	var out strings.Builder
	names := make([]string, len(promptMatrix))
	for i, tc := range promptMatrix {
		raw, err := json.Marshal(PromptMeta(&tc.prompt))
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		out.Write(raw)
		out.WriteString("\n")
		names[i] = tc.name
	}
	checkGolden(t, promptGoldenPath, out.String(), names)
}

// TestInitialize_InactiveItemsRidesConfigurationState pins that the one engine declares both halves of u4's
// configuration view on every spawn: configurationState makes KAS push the view, inactiveItems adds the items another
// layer displaced or switched off. Neither is a session key.
func TestInitialize_InactiveItemsRidesConfigurationState(t *testing.T) {
	for _, tc := range engineMatrix {
		caps := Capabilities(&tc.spawn)
		for _, key := range []string{"configurationState", "inactiveItems"} {
			if got := caps[key]; got != true {
				t.Errorf("%s: Capabilities()[%q] = %#v, want true", tc.name, key, got)
			}
		}
	}
	for _, tc := range sessionMatrix {
		meta := SessionMeta(&tc.spawn)
		for _, key := range []string{"configurationState", "inactiveItems"} {
			if _, present := meta[key]; present {
				t.Errorf("%s: SessionMeta carries %s; KAS reads it at initialize only", tc.name, key)
			}
		}
	}
}

// TestSessionDoor_OverrideSessionIDOnlyWhenMinted pins that a minted id rides session/new's door and a load, which
// names its id itself, sends no override.
func TestSessionDoor_OverrideSessionIDOnlyWhenMinted(t *testing.T) {
	const id = "sess_00000000-0000-4000-8000-000000000000"
	if got := SessionMeta(&SessionSpawn{OverrideSessionID: id})["overrideSessionId"]; got != id {
		t.Errorf("SessionMeta(minted) overrideSessionId = %#v, want %q", got, id)
	}
	if got, present := SessionMeta(&SessionSpawn{})["overrideSessionId"]; present {
		t.Errorf("SessionMeta(load) carries overrideSessionId = %#v, want it absent", got)
	}
}

// TestMemoryRow_FailsClosedAndNeverVetoes pins the memory preference's two
// load-bearing facts: a zero SessionSpawn (an observer pass) sends mode disabled on
// the session door, and the legacy userMemoryOptIn veto is on no door, because
// sending it refuses the sessionless _kiro/memory/* calls the Memories tab makes.
func TestMemoryRow_FailsClosedAndNeverVetoes(t *testing.T) {
	settings, _ := SessionMeta(&SessionSpawn{})[settingsKey].(map[string]any)
	want := map[string]any{"mode": "disabled", "reflection": false}
	if got := settings["memory"]; !reflect.DeepEqual(got, want) {
		t.Errorf("SessionMeta(&SessionSpawn{}) memory = %#v, want %#v", got, want)
	}
	for _, tc := range sessionMatrix {
		s, _ := SessionMeta(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := s["userMemoryOptIn"]; present {
			t.Errorf("session %s: userMemoryOptIn is sent; want it withheld on every door", tc.name)
		}
	}
	for _, tc := range engineMatrix {
		s, _ := Capabilities(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := s["userMemoryOptIn"]; present {
			t.Errorf("engine %s: userMemoryOptIn is sent; want it withheld on every door", tc.name)
		}
		if _, present := s["memory"]; present {
			t.Errorf("engine %s: memory rides initialize; want the session door only (only it upgrades a legacy session)", tc.name)
		}
	}
}

// classifierEnv is on every engine whatever the spawn: KAS reads it once per process, and it is inert
// without an evaluate rule.
const classifierEnv = "KIRO_TRUST_CLASSIFIER_MODE=enforce"

// TestChildEnv_PinsNoArmByDefault pins that a zero EngineSpawn writes no KIRO_FEATURE_* or KIRO_DISABLE_*
// variable, so every arm follows the experiment ramp unless a Settings control decides it.
func TestChildEnv_PinsNoArmByDefault(t *testing.T) {
	if got := ChildEnv(&EngineSpawn{}); !reflect.DeepEqual(got, []string{classifierEnv}) {
		t.Errorf("ChildEnv(&EngineSpawn{}) = %q, want only %q", got, classifierEnv)
	}
	for _, tc := range engineMatrix {
		for _, kv := range ChildEnv(&tc.spawn) {
			if strings.HasPrefix(kv, "KIRO_DISABLE_SESSION_TITLE_LLM=") {
				t.Errorf("%s: ChildEnv writes %q; the switch has no per-session seat on one engine", tc.name, kv)
			}
		}
	}
}

// TestChildEnv_TrustClassifierEnforce pins the classifier gate on every engine: withheld, a profile's
// approve-for-me preset and every evaluate rule are skipped with a warning.
func TestChildEnv_TrustClassifierEnforce(t *testing.T) {
	spawns := map[string]*EngineSpawn{"zero": {}}
	for i := range engineMatrix {
		spawns[engineMatrix[i].name] = &engineMatrix[i].spawn
	}
	for name, spawn := range spawns {
		got := ChildEnv(spawn)
		n := 0
		for _, kv := range got {
			if strings.HasPrefix(kv, "KIRO_TRUST_CLASSIFIER_MODE=") {
				n++
			}
		}
		if n != 1 || !strings.Contains(strings.Join(got, "\n"), classifierEnv) {
			t.Errorf("engine %s: ChildEnv = %q, want %q exactly once", name, got, classifierEnv)
		}
	}
	if OwnsEnv(classifierEnv) {
		t.Errorf("OwnsEnv(%q) = true; no setting decides the gate", classifierEnv)
	}
}

// TestChildEnv_AutoModelChoicesFollowTheirSwitches pins the two _CONFIG arms: unset withholds the
// variable so the experiment decides, and each explicit choice writes the JSON KAS parses.
func TestChildEnv_AutoModelChoicesFollowTheirSwitches(t *testing.T) {
	for _, tc := range []struct {
		choice string
		want   []string
	}{
		{"", []string{classifierEnv}},
		{"on", []string{
			`KIRO_FEATURE_CASCADE_CONFIG={"cascadeClientNotificationEnabled":true,"cascadeEnabled":true}`,
			`KIRO_FEATURE_DYNAMIC_DELEGATION_CONFIG={"delegationEnabled":true}`,
			classifierEnv,
		}},
		{"off", []string{
			`KIRO_FEATURE_CASCADE_CONFIG={"cascadeEnabled":false}`,
			`KIRO_FEATURE_DYNAMIC_DELEGATION_CONFIG={"delegationEnabled":false}`,
			classifierEnv,
		}},
	} {
		got := ChildEnv(&EngineSpawn{AutoRouting: tc.choice, AutoDelegation: tc.choice})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ChildEnv(AutoRouting=AutoDelegation=%q) = %q, want %q", tc.choice, got, tc.want)
		}
	}
	if got := ChildEnv(&EngineSpawn{AutoRouting: "on"}); len(got) != 2 || got[0] != `KIRO_FEATURE_CASCADE_CONFIG={"cascadeClientNotificationEnabled":true,"cascadeEnabled":true}` {
		t.Errorf("ChildEnv(AutoRouting=on alone) = %q, want the cascade arm and the classifier gate", got)
	}
	for _, tc := range sessionMatrix {
		if settings, _ := SessionMeta(&tc.spawn)[settingsKey].(map[string]any); settings["cascade"] != nil {
			t.Errorf("%s: SessionMeta sends settings.cascade = %#v; the env arm carries the choice", tc.name, settings["cascade"])
		}
	}
}

// TestChildEnv_ModelFallbackSendsBothFlagsOnlyWhenOn pins that off writes neither flag: an explicit
// false would override KAS's default and any server-side ramp.
func TestChildEnv_ModelFallbackSendsBothFlagsOnlyWhenOn(t *testing.T) {
	for _, tc := range []struct {
		want []string
		on   bool
	}{
		{on: false, want: []string{classifierEnv}},
		{on: true, want: []string{
			"KIRO_FEATURE_MODEL_FALLBACK_AVAILABILITY_ENABLED=true",
			"KIRO_FEATURE_MODEL_FALLBACK_REFUSAL_ENABLED=true",
			classifierEnv,
		}},
	} {
		if got := ChildEnv(&EngineSpawn{ModelFallback: tc.on}); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ChildEnv(ModelFallback=%v) = %q, want %q", tc.on, got, tc.want)
		}
	}
}

// OwnsEnv claims the environment rows a Settings control decides, a withheld arm included, and
// leaves a row marotte never sends and every other name to the operator.
func TestOwnsEnv_ClaimsOnlyTheSettingOwnedRows(t *testing.T) {
	for _, tc := range []struct {
		kv   string
		want bool
	}{
		{kv: "KIRO_FEATURE_MODEL_FALLBACK_REFUSAL_ENABLED=true", want: true},
		{kv: "KIRO_FEATURE_MODEL_FALLBACK_AVAILABILITY_ENABLED=", want: true},
		{kv: `KIRO_FEATURE_CASCADE_CONFIG={"cascadeEnabled":true}`, want: true},
		{kv: `KIRO_FEATURE_DYNAMIC_DELEGATION_CONFIG={}`, want: true},
		{kv: "KIRO_DISABLE_SESSION_TITLE_LLM=true", want: false},
		{kv: "KIRO_FEATURE_VALIDATION_ENABLED=true", want: false},
		{kv: "KIRO_FEATURE_MODEL_FALLBACK_REFUSAL_ENABLED_X=true", want: false},
		{kv: "telemetryEnabled=true", want: false},
		{kv: "PATH=/usr/bin", want: false},
	} {
		if got := OwnsEnv(tc.kv); got != tc.want {
			t.Errorf("OwnsEnv(%q) = %v, want %v", tc.kv, got, tc.want)
		}
	}
}

// TestSessionDoor_WorkflowsFollowsTheSetting pins that the workflows row is
// value-gated on both states: a resumed chat drops its workflow tools only when
// false is SENT, because an absent key falls back to KAS's persisted value.
func TestSessionDoor_WorkflowsFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{false, true} {
		settings, _ := SessionMeta(&SessionSpawn{Workflows: on})[settingsKey].(map[string]any)
		if got, want := settings["workflows"], enabledIf(on); !reflect.DeepEqual(got, want) {
			t.Errorf("SessionMeta(Workflows=%v) workflows = %#v, want %#v", on, got, want)
		}
	}
	for _, tc := range engineMatrix {
		connection, _ := Capabilities(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := connection["workflows"]; present {
			t.Errorf("%s: Capabilities carries workflows; KAS reads it on the session door only", tc.name)
		}
	}
}

// TestSessionDoor_MessagingFollowsTheSetting pins settings.sessionTools.messaging in both states, the shape KAS's
// session-tools reader takes (`keys:["sessionTools"]`, then `.messaging.enabled` as a boolean): on load a stated
// false clears the persisted sessionMessagingEnabled, so off is sent too.
func TestSessionDoor_MessagingFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{false, true} {
		settings, _ := SessionMeta(&SessionSpawn{Messaging: on})[settingsKey].(map[string]any)
		want := map[string]any{"messaging": map[string]any{"enabled": on}}
		if got := settings["sessionTools"]; !reflect.DeepEqual(got, want) {
			t.Errorf("SessionMeta(Messaging=%v) sessionTools = %#v, want %#v", on, got, want)
		}
	}
	for _, tc := range engineMatrix {
		connection, _ := Capabilities(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := connection["sessionTools"]; present {
			t.Errorf("%s: Capabilities carries sessionTools; KAS reads it on session/new and session/load only", tc.name)
		}
	}
}

// TestSessionDoor_BackgroundExecutionAndShellTypeAreUngated pins the two rows
// every session carries whatever the spawn: the control_process class as an
// {enabled} object (a bare true resolves undefined) and the shell type.
func TestSessionDoor_BackgroundExecutionAndShellTypeAreUngated(t *testing.T) {
	for _, tc := range sessionMatrix {
		meta := SessionMeta(&tc.spawn)
		settings, _ := meta[settingsKey].(map[string]any)
		if got, want := settings["backgroundExecution"], enabled(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: backgroundExecution = %#v, want %#v", tc.name, got, want)
		}
		if got := meta["shellType"]; got != "bash" {
			t.Errorf("%s: shellType = %#v, want \"bash\"", tc.name, got)
		}
	}
	if _, present := Capabilities(&EngineSpawn{})["backgroundProcesses"]; !present {
		t.Error("backgroundProcesses left initialize; it is the fallback for sessions resolving backgroundExecution false")
	}
}

// TestDisableAutoCompaction_ValueGatedOnSessionDoorOnly pins the three door facts
// the compaction policy depends on: false is sent (absence would keep a
// persisted true), true is sent as an object (a bare true resolves undefined),
// and the key never reaches initialize, where step sessions would inherit it.
func TestDisableAutoCompaction_ValueGatedOnSessionDoorOnly(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		settings, _ := SessionMeta(&SessionSpawn{DisableAutoCompaction: disabled})[settingsKey].(map[string]any)
		if got, want := settings["disableAutoCompaction"], enabledIf(disabled); !reflect.DeepEqual(got, want) {
			t.Errorf("SessionMeta(DisableAutoCompaction=%v) disableAutoCompaction = %#v, want %#v", disabled, got, want)
		}
	}
	for _, tc := range engineMatrix {
		connection, _ := Capabilities(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := connection["disableAutoCompaction"]; present {
			t.Errorf("%s: Capabilities carries disableAutoCompaction; want it on the session door only", tc.name)
		}
	}
}

// TestCompactionThreshold_NeverSent pins the threshold row withheld on both doors: every chat session turns KAS's
// own compaction off, and a threshold beside it would only be KAS's to misread.
func TestCompactionThreshold_NeverSent(t *testing.T) {
	for _, tc := range sessionMatrix {
		settings, _ := SessionMeta(&tc.spawn)[settingsKey].(map[string]any)
		if got, present := settings["compaction"]; present {
			t.Errorf("%s: SessionMeta carries compaction %#v; want it withheld", tc.name, got)
		}
	}
	for _, tc := range engineMatrix {
		connection, _ := Capabilities(&tc.spawn)[settingsKey].(map[string]any)
		if _, present := connection["compaction"]; present {
			t.Errorf("%s: Capabilities carries compaction; want it withheld", tc.name)
		}
	}
}

// promptProfilingKeys are the session/prompt _meta.kiro keys KAS reads as a turn's profiling, the same three
// automaticTurnProfiling declares for the turns KAS starts on its own.
var promptProfilingKeys = []string{"ttft", "perf", "contextBreakdown"}

// TestAutomaticTurnProfiling_MatchesAChatPrompt pins that a turn KAS starts on its own asks for what a typed chat
// prompt asks for, so a change to one door's profiling cannot leave the other behind.
func TestAutomaticTurnProfiling_MatchesAChatPrompt(t *testing.T) {
	declared, ok := SessionMeta(&SessionSpawn{ProfileAgentTurns: true})["automaticTurnProfiling"].(map[string]any)
	if !ok {
		t.Fatal("a chat session's door carries no automaticTurnProfiling object")
	}
	prompt := PromptMeta(&Prompt{})
	want := map[string]any{}
	for _, k := range promptProfilingKeys {
		if v, ok := prompt[k]; ok {
			want[k] = v
		}
	}
	if !reflect.DeepEqual(declared, want) {
		t.Errorf("automaticTurnProfiling = %v, want a chat prompt's profiling %v", declared, want)
	}
	if got, ok := SessionMeta(&SessionSpawn{})["automaticTurnProfiling"]; ok {
		t.Errorf("a session that is not a chat's declared automaticTurnProfiling = %v, want none", got)
	}
}
