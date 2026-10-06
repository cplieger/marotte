package kascap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Golden fixtures for the two projections. No agent-server bundle is required, so
// these run in CI as they stand.
// The bundle-dependent census is census_test.go and skips when it is absent.
const (
	initializeGoldenPath  = "testdata/initialize.golden"
	sessionGoldenPath     = "testdata/session.golden"
	environmentGoldenPath = "testdata/environment.golden"
	updateGoldenCmd       = "UPDATE_GOLDEN=1 go test ./internal/kascap/ -run 'TestInitializeDeclaresExactly|TestSessionDoorDeclaresExactly|TestEnvironmentDoorDeclaresExactly'"
)

// spawnMatrix is the COMPLETE set of runtime inputs to either projection; exhaustive is what makes
// the golden a contract rather than a sample.
var spawnMatrix = []struct {
	name  string
	spawn Spawn
}{
	{"gates off", Spawn{}},
	{"secret storage only", Spawn{SecretStorage: true}},
	{"hooks only", Spawn{Hooks: true}},
	{"both gates on", Spawn{SecretStorage: true, Hooks: true}},
	// Presets need both states: an empty set withholds the policyPreset key entirely (the Custom
	// profile).
	{"one preset", Spawn{Presets: []string{"read-workspace"}}},
	{"several presets", Spawn{Presets: []string{"read-workspace", "read-only-shell", "read-all"}}},
	{"presets with both gates", Spawn{
		SecretStorage: true, Hooks: true,
		Presets: []string{"allow-all"},
	}},
	// Knowledge is value-gated across TWO keys, the capability (`=== true`) and settings.knowledge
	// (isSettingEnabled).
	{"knowledge on", Spawn{Knowledge: true}},
	{"memory read only", Spawn{MemoryMode: "read_only"}},
	{"memory read write", Spawn{MemoryMode: "read_write"}},
	{"memory learn", Spawn{MemoryMode: "read_write", MemoryReflection: true}},
	{"tool load on", Spawn{ToolLoad: true}},
	{"run bridge titles off", Spawn{DisableSessionTitles: true}},
	{"auto compaction off", Spawn{DisableAutoCompaction: true}},
	{"spec planning quick", Spawn{SpecPlan: "quick"}},
	{"spec planning full, ask first", Spawn{SpecPlan: "full", SpecAskClarification: true}},
	{"inline agents and steering reminders on", Spawn{InlineAgents: true, SteeringReminders: true}},
	{"work validation and safety check on", Spawn{WorkValidation: "on", InfraSafetyMonitor: "on"}},
	{"work validation and safety check off", Spawn{WorkValidation: "off", InfraSafetyMonitor: "off"}},
	{"shell timeout set", Spawn{TerminalCommandTimeoutMs: 300000}},
	{"workflows on", Spawn{Workflows: true}},
	{"every gate on", Spawn{
		SecretStorage: true, Hooks: true,
		Presets:   []string{"read-workspace"},
		Knowledge: true, MemoryMode: "read_write", MemoryReflection: true,
		DisableAutoCompaction: true, ToolLoad: true, DisableSessionTitles: true,
		SpecPlan: "full", SpecAskClarification: true,
		WorkValidation: "on", InfraSafetyMonitor: "on",
		TerminalCommandTimeoutMs: 300000,
		InlineAgents:             true, SteeringReminders: true,
		Workflows: true,
	}},
}

// renderMatrix marshals one projection across the matrix, one compact JSON object per line;
// encoding/json sorts map keys, so the output is deterministic.

func renderMatrix[T any](t *testing.T, build func(*Spawn) T) string {
	t.Helper()
	var out strings.Builder
	for _, tc := range spawnMatrix {
		raw, err := json.Marshal(build(&tc.spawn))
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		out.Write(raw)
		out.WriteString("\n")
	}
	return out.String()
}

// checkGolden writes the fixture under UPDATE_GOLDEN=1 and compares in every
// case, so one code path both regenerates and asserts.
func checkGolden(t *testing.T, path, got string) {
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
		if i < len(spawnMatrix) {
			name = spawnMatrix[i].name
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
	checkGolden(t, initializeGoldenPath, renderMatrix(t, Capabilities))
}

// TestSessionDoorDeclaresExactly pins the payload session/new and session/load carry for every
// spawn combination.
func TestSessionDoorDeclaresExactly(t *testing.T) {
	checkGolden(t, sessionGoldenPath, renderMatrix(t, SessionMeta))
}

// TestEnvironmentDoorDeclaresExactly pins the KIRO_* assignments ChildEnv hands
// the bridge for every spawn combination, one JSON array per line.
func TestEnvironmentDoorDeclaresExactly(t *testing.T) {
	checkGolden(t, environmentGoldenPath, renderMatrix(t, ChildEnv))
}

// TestMemoryRow_FailsClosedAndNeverVetoes pins the memory preference's two
// load-bearing facts: a zero Spawn (the utility bridge) sends mode disabled on
// the session door, and the legacy userMemoryOptIn veto is on no door, because
// sending it refuses the sessionless _kiro/memory/* calls the Memories tab makes.
func TestMemoryRow_FailsClosedAndNeverVetoes(t *testing.T) {
	settings, _ := SessionMeta(&Spawn{})[settingsKey].(map[string]any)
	want := map[string]any{"mode": "disabled", "reflection": false}
	if got := settings["memory"]; !reflect.DeepEqual(got, want) {
		t.Errorf("SessionMeta(&Spawn{}) memory = %#v, want %#v", got, want)
	}
	for _, build := range []func(*Spawn) map[string]any{Capabilities, SessionMeta} {
		for _, tc := range spawnMatrix {
			s, _ := build(&tc.spawn)[settingsKey].(map[string]any)
			if _, present := s["userMemoryOptIn"]; present {
				t.Errorf("%s: userMemoryOptIn is sent; want it withheld on every door", tc.name)
			}
		}
	}
	if _, present := Capabilities(&Spawn{MemoryMode: "read_write"})[settingsKey].(map[string]any)["memory"]; present {
		t.Error("memory rides initialize; want the session door only (only it upgrades a legacy session)")
	}
}

// TestChildEnv_WritesToolLoadInBothStates pins that the tool_load arm is always
// explicit and that no other KIRO_FEATURE_* arm is pinned: the rest follow the
// experiment ramp.
func TestChildEnv_WritesToolLoadInBothStates(t *testing.T) {
	for _, on := range []bool{false, true} {
		got := ChildEnv(&Spawn{ToolLoad: on})
		want := []string{"KIRO_FEATURE_TOOL_LOAD_ENABLED=" + strconv.FormatBool(on)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ChildEnv(ToolLoad=%v) = %q, want %q", on, got, want)
		}
	}
	got := ChildEnv(&Spawn{DisableSessionTitles: true})
	want := []string{"KIRO_DISABLE_SESSION_TITLE_LLM=true", "KIRO_FEATURE_TOOL_LOAD_ENABLED=false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChildEnv(DisableSessionTitles) = %q, want %q", got, want)
	}
}

// TestSessionDoor_WorkflowsFollowsTheSetting pins that the workflows row is
// value-gated on both states: a resumed chat drops its workflow tools only when
// false is SENT, because an absent key falls back to KAS's persisted value.
func TestSessionDoor_WorkflowsFollowsTheSetting(t *testing.T) {
	for _, on := range []bool{false, true} {
		settings, _ := SessionMeta(&Spawn{Workflows: on})[settingsKey].(map[string]any)
		if got, want := settings["workflows"], enabledIf(on); !reflect.DeepEqual(got, want) {
			t.Errorf("SessionMeta(Workflows=%v) workflows = %#v, want %#v", on, got, want)
		}
		connection, _ := Capabilities(&Spawn{Workflows: on})[settingsKey].(map[string]any)
		if _, present := connection["workflows"]; present {
			t.Errorf("Capabilities(Workflows=%v) carries workflows; KAS reads it on the session door only", on)
		}
	}
}

// TestSessionDoor_BackgroundExecutionAndShellTypeAreUngated pins the two rows
// every session carries whatever the spawn: the control_process class as an
// {enabled} object (a bare true resolves undefined) and the shell type.
func TestSessionDoor_BackgroundExecutionAndShellTypeAreUngated(t *testing.T) {
	for _, tc := range spawnMatrix {
		meta := SessionMeta(&tc.spawn)
		settings, _ := meta[settingsKey].(map[string]any)
		if got, want := settings["backgroundExecution"], enabled(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: backgroundExecution = %#v, want %#v", tc.name, got, want)
		}
		if got := meta["shellType"]; got != "bash" {
			t.Errorf("%s: shellType = %#v, want \"bash\"", tc.name, got)
		}
	}
	if _, present := Capabilities(&Spawn{})["backgroundProcesses"]; !present {
		t.Error("backgroundProcesses left initialize; it is the fallback for sessions resolving backgroundExecution false")
	}
}

// TestDisableAutoCompaction_ValueGatedOnSessionDoorOnly pins the three door facts
// the compaction policy depends on: false is sent (absence would keep a
// persisted true), true is sent as an object (a bare true resolves undefined),
// and the key never reaches initialize, where step sessions would inherit it.
func TestDisableAutoCompaction_ValueGatedOnSessionDoorOnly(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		settings, _ := SessionMeta(&Spawn{DisableAutoCompaction: disabled})[settingsKey].(map[string]any)
		if got, want := settings["disableAutoCompaction"], enabledIf(disabled); !reflect.DeepEqual(got, want) {
			t.Errorf("SessionMeta(DisableAutoCompaction=%v) disableAutoCompaction = %#v, want %#v", disabled, got, want)
		}
		connection, _ := Capabilities(&Spawn{DisableAutoCompaction: disabled})[settingsKey].(map[string]any)
		if _, present := connection["disableAutoCompaction"]; present {
			t.Errorf("Capabilities(DisableAutoCompaction=%v) carries disableAutoCompaction; want it on the session door only", disabled)
		}
	}
}
