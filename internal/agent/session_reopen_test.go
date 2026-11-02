package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

func reopenFixture(t *testing.T, opts ...Option) (h *Runtime, configDir string) {
	t.Helper()
	return reopenFixtureWith(t, nil, opts...)
}

func reopenFixtureWith(t *testing.T, seed func(*fakeBridge), opts ...Option) (h *Runtime, configDir string) {
	t.Helper()
	configDir = t.TempDir()
	return reopenFixtureIn(t, configDir, seed, opts...), configDir
}

// reopenFixtureIn builds the runtime over configDir as the caller left it, so a test can seed config.json before boot.
func reopenFixtureIn(t *testing.T, configDir string, seed func(*fakeBridge), opts ...Option) *Runtime {
	t.Helper()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession {
		br := newFakeBridge()
		if seed != nil {
			seed(br)
		}
		return br
	}, nil), cs, append([]Option{WithConfigDir(configDir)}, opts...)...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	return h
}

// fakeOf is the fake session sb holds, read through the entry, never by door order.
// unguarded is s past the run-work guard every engine-opened session carries (guardedSession).
func unguarded(s engineSession) engineSession {
	if g, ok := s.(guardedSession); ok {
		return g.engineSession
	}
	return s
}

func fakeOf(sb *sharedBridge) *fakeBridge {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	br, _ := unguarded(sb.bridge).(*fakeBridge)
	return br
}

func writeSetting(t *testing.T, configDir, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Setup: marshal %s: %v", key, err)
	}
	if _, err := settings.Update(t.Context(), configDir, func(doc map[string]json.RawMessage) error {
		doc[key] = raw
		return nil
	}, nil); err != nil {
		t.Fatalf("Setup: write %s: %v", key, err)
	}
}

func sessionClosed(br *fakeBridge) bool {
	br.mu.Lock()
	defer br.mu.Unlock()
	return br.stopped
}

func TestMarkSessionsForReload_AnIdleChatReloadsAtItsNextOpen(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")

	h.markSessionsForReload("test")

	if h.bridge.mgr.get("c1") != first || sessionClosed(fakeOf(first)) {
		t.Fatal("the mark closed an idle chat's session; it must wait for the chat's next open")
	}
	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the mark: %v", err)
	}
	if second == first || !sessionClosed(fakeOf(first)) {
		t.Fatal("the next open kept the marked session instead of closing and reloading it")
	}
	opts := fakeOf(second).lastSessionOpts()
	if opts.SessionID != string(first.SessionID()) {
		t.Errorf("the reload loaded %q, want the chat's own session %q", opts.SessionID, first.SessionID())
	}
	if !slices.Equal(opts.Presets, []string{"allow-all"}) {
		t.Errorf("the reloaded session's presets = %v, want [allow-all] from the new profile", opts.Presets)
	}
}

// A host session keeps the presets its run launched with, so a mark never reaches it.
func TestMarkSessionsForReload_LeavesHostSessionsUnmarked(t *testing.T) {
	h, _ := reopenFixture(t)
	sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
	if !h.bridge.mgr.insert(runChatID("wf-1"), sb) {
		t.Fatal("Setup: insert host session = false")
	}

	h.markSessionsForReload("test")

	if h.bridge.mgr.claimReload(runChatID("wf-1"), sb).due {
		t.Error("the mark reached a run's host session, whose presets only its lease decides")
	}
}

func TestSecurityProfileChanged_ReopensOpenChats(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")

	h.SecurityProfileChanged(t.Context())

	if second, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || second == first {
		t.Errorf("after a profile change the chat's next open kept its bridge (err %v)", err)
	}
}

func TestSecurityProfileChanged_KeepsTheBridgeWhenTheProfileInForceIsUnchanged(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	h.SecurityProfileChanged(t.Context())

	if second, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || second != first {
		t.Errorf("re-selecting the profile in force replaced the chat's bridge (err %v)", err)
	}
}

// openBridge reconciles before the profile write's own callback runs; the callback must not
// retire the session already opened under the new profile.
func TestSecurityProfileChanged_KeepsABridgeAlreadyOpenedUnderTheNewProfile(t *testing.T) {
	h, configDir := reopenFixture(t)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")
	fresh, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the write: %v", err)
	}

	h.SecurityProfileChanged(t.Context())

	if again, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || again != fresh {
		t.Errorf("the profile callback replaced a session already opened under the new profile (err %v)", err)
	}
}

func TestSpecPlanningAskFirst_ReopensOnlyWhilePlanningIsOn(t *testing.T) {
	for _, tc := range []struct {
		planning string
		want     bool
	}{
		{planning: settings.SpecPlanningOff, want: false},
		{planning: settings.SpecPlanningQuick, want: true},
	} {
		t.Run(tc.planning, func(t *testing.T) {
			h, configDir := reopenFixture(t)
			writeSetting(t, configDir, settings.KeySpecPlanning, tc.planning)

			reopened := reloadsAfter(t, h, func() { writeSetting(t, configDir, settings.KeySpecPlanningAskFirst, true) })

			if reopened != tc.want {
				t.Errorf("spec_planning=%s: writing spec_planning_ask_first reopened = %v, want %v", tc.planning, reopened, tc.want)
			}
		})
	}
}

type settingReach string

const (
	reachLive    settingReach = "live"
	reachReopen  settingReach = "reopen"
	reachNewChat settingReach = "new chats only"
	reachNoChat  settingReach = "no chat reads it"
	// reachEngine rides the engine's spawn: an idle engine restarts at quiescence, which reloads every session.
	reachEngine settingReach = "engine restart"
)

var configWrites = []struct {
	value any
	from  any
	key   string
	reach settingReach
}{
	{key: settings.KeyMemoryMode, value: "learn", reach: reachReopen},
	{key: settings.KeySecurityProfile, value: policyfile.ProfileTrusted, reach: reachReopen},
	{key: settings.KeyWorkflowsEnabled, value: false, reach: reachReopen},
	{key: settings.KeySpecPlanning, value: settings.SpecPlanningQuick, reach: reachReopen},
	// Read only while planning is on: TestSpecPlanningAskFirst_ReopensOnlyWhilePlanningIsOn.
	{key: settings.KeySpecPlanningAskFirst, value: true, reach: reachNoChat},
	{key: settings.KeyWorkValidation, value: settings.FeatureOff, reach: reachReopen},
	{key: settings.KeySteeringReminders, value: true, reach: reachEngine},
	{key: settings.KeyCloudFormationSafety, value: settings.FeatureOn, reach: reachEngine},
	{key: settings.KeyAutoRouting, value: settings.FeatureOff, reach: reachEngine},
	{key: settings.KeyAutoDelegation, value: settings.FeatureOff, reach: reachEngine},
	{key: settings.KeyModelFallback, value: true, reach: reachEngine},
	{key: settings.KeyKnowledgeEnabled, value: false, reach: reachEngine},
	{key: settings.KeyAutoCompactionEnabled, value: false, reach: reachReopen},
	// sessionDisablesAutoCompaction draws the session door at 80, so only a move across it reopens.
	{key: settings.KeyAutoCompactPct, value: 60, reach: reachLive},
	{key: settings.KeyAutoCompactPct, value: 85, reach: reachReopen},
	{key: settings.KeyAutoCompactPct, from: 85, value: 90, reach: reachLive},
	{key: settings.KeyAutoCompactPct, from: 85, value: 60, reach: reachReopen},
	{key: settings.KeyTerminalCommandTimeoutMs, value: 300000, reach: reachLive},
	{key: settings.KeyContentCollectionEnabled, value: true, reach: reachLive},
	{key: settings.KeyAgentIgnoreFiles, value: []string{".gitignore"}, reach: reachLive},
	{key: settings.KeyMCPWaitForReady, value: true, reach: reachLive},
	{key: settings.KeyOutputStyle, value: settings.OutputStyleConcise, reach: reachLive},
	{key: settings.KeySupervisedDefault, value: true, reach: reachNewChat},
	{key: settings.KeyLastModel, value: "claude-sonnet-4", reach: reachNewChat},
	{key: settings.KeyLastEffortByModel, value: map[string]string{"claude-sonnet-4": "high"}, reach: reachNewChat},
	{key: settings.KeyScheduledAutoApprove, value: true, reach: reachNoChat},
	{key: settings.KeyGuardPayloadLinks, value: false, reach: reachNoChat},
	{key: settings.KeyNotificationsEnabled, value: false, reach: reachNoChat},
	{key: settings.KeyNotifyAgentFinished, value: false, reach: reachNoChat},
	{key: settings.KeyNotifyPRStatus, value: false, reach: reachNoChat},
	{key: settings.KeyNotifyRunOutcome, value: false, reach: reachNoChat},
	{key: settings.KeyDebugLogs, value: true, reach: reachNoChat},
	{key: settings.KeyChatRetentionDays, value: 30, reach: reachNoChat},
	{key: settings.KeyTheme, value: "light", reach: reachNoChat},
	{key: settings.KeyFBPath, value: "/workspace", reach: reachNoChat},
	{key: settings.KeyLastMergeMethod, value: "squash", reach: reachNoChat},
	// Update writes both on every write, so a session door reading them would reopen every chat on any change.
	{key: settings.KeySettingsEpoch, value: "e", reach: reachNoChat},
	{key: settings.KeySettingsRevision, value: 42, reach: reachNoChat},
	{key: settings.KeyEngineHeapMiB, value: 1024, reach: reachEngine},
	{key: settings.KeyAdditionalDirectories, value: grantableDirList, reach: reachReopen},
}

// grantableDirList is an additional_directories value that Granted keeps: a stored entry it refuses
// never reaches the session door, so it could not prove the reopen.
func grantableDirList(t *testing.T) any { return []string{canonDir(t, t.TempDir())} }

var kiroCLIWrites = []struct {
	key   string
	reach settingReach
	value bool
}{
	{key: kiroTelemetryKey, value: false, reach: reachEngine},
	{key: "hooks.showStatus", value: false, reach: reachLive},
	{key: "chat.enableKnowledge", value: true, reach: reachNoChat},
	{key: "chat.enableSubagent", value: true, reach: reachNoChat},
	{key: "chat.enablePromptHints", value: true, reach: reachNoChat},
	{key: "chat.disableInheritingDefaultResources", value: true, reach: reachNoChat},
}

func TestConfigWrites_RouteEveryKnownKey(t *testing.T) {
	routed := map[string]struct{}{}
	for _, w := range configWrites {
		routed[w.key] = struct{}{}
	}
	if got, want := slices.Sorted(maps.Keys(routed)), slices.Sorted(maps.Keys(settings.KnownKeys)); !slices.Equal(got, want) {
		t.Errorf("config.json keys with a route = %v, want every settings.KnownKeys member %v", got, want)
	}
}

// assertReach checks one write's route: an engine-door write restarts the idle engine (the second open may land on
// either generation, so only the restart is asserted), any other reopens the chat exactly when its route says so.
func assertReach(t *testing.T, h *Runtime, reach settingReach, reopened bool, what string) {
	t.Helper()
	if reach == reachEngine {
		src := fakeSourceOf(t, h)
		deadline := time.Now().Add(5 * time.Second)
		for src.generations() < 2 {
			if time.Now().After(deadline) {
				t.Errorf("%s: the idle engine never restarted (generations = %d), want a restart at quiescence", what, src.generations())
				return
			}
			time.Sleep(time.Millisecond)
		}
		return
	}
	if want := reach == reachReopen; reopened != want {
		t.Errorf("%s (route %q): reopened = %v, want %v", what, reach, reopened, want)
	}
}

func reloadsAfter(t *testing.T, h *Runtime, write func()) bool {
	t.Helper()
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	write()
	h.ReconcileSessionSettings(t.Context())
	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the write: %v", err)
	}
	return second != first
}

func TestConfigWrites_ReachChatsByTheirRoute(t *testing.T) {
	for i, tc := range configWrites {
		t.Run(fmt.Sprintf("%02d_%s", i, tc.key), func(t *testing.T) {
			h, configDir := reopenFixture(t)
			if tc.from != nil {
				writeSetting(t, configDir, tc.key, tc.from)
			}

			value := tc.value
			if f, ok := value.(func(*testing.T) any); ok {
				value = f(t)
			}

			reopened := reloadsAfter(t, h, func() { writeSetting(t, configDir, tc.key, value) })

			assertReach(t, h, tc.reach, reopened, fmt.Sprintf("writing %s = %v (from %v)", tc.key, value, tc.from))
		})
	}
}

func TestKiroCLIWrites_ReachChatsByTheirRoute(t *testing.T) {
	for _, tc := range kiroCLIWrites {
		t.Run(tc.key, func(t *testing.T) {
			cliJSON := withKiroTelemetry(t, "")
			h, _ := reopenFixture(t)

			reopened := reloadsAfter(t, h, func() {
				writeKiroCLISettings(t, cliJSON, fmt.Sprintf(`{%q:%t}`, tc.key, tc.value))
			})

			assertReach(t, h, tc.reach, reopened, fmt.Sprintf("writing kiro-cli %s = %t", tc.key, tc.value))
		})
	}
}

func broadcastTypes(h *Runtime, since uint64) []marotte.EventType {
	var out []marotte.EventType
	for _, e := range h.bus.fanout.Snapshot() {
		if e.Offset <= since {
			continue
		}
		var frame struct {
			Type    marotte.EventType `json:"type"`
			Payload struct {
				Status string `json:"status"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &frame); err != nil {
			continue
		}
		if frame.Type == marotte.EventPermissionsChanged && frame.Payload.Status != "success" {
			continue
		}
		out = append(out, frame.Type)
	}
	return out
}

func TestEnsureCustomProfile_SwitchesAGuardedProfileToCustom(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h, configDir := reopenFixture(t)
	// The preset rules are read on an observer pass.
	fakeSourceOf(t, h).setObserver(func() engineSession {
		br := newFakeBridge()
		br.callResults = map[string]json.RawMessage{methodV3PermissionsList: json.RawMessage(`{"rules":[` +
			`{"capability":"shell","effect":"allow","match":["ls *"],"scope":"session","source":"preset:read-workspace"}]}`)}
		return br
	})
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileGuarded)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	since := h.bus.fanout.Position().Head

	if err := h.EnsureCustomProfile(t.Context()); err != nil {
		t.Fatalf("EnsureCustomProfile on guarded = %v, want nil", err)
	}

	var active string
	if !settings.FieldInto(t.Context(), configDir, settings.KeySecurityProfile, &active) || active != policyfile.ProfileCustom {
		t.Errorf("security_profile after EnsureCustomProfile = %q, want %q", active, policyfile.ProfileCustom)
	}
	f, err := policyfile.Load(filepath.Join(home, ".kiro", "settings", "permissions.yaml"))
	if err != nil {
		t.Fatalf("Load the user permissions file: %v", err)
	}
	want := []policyfile.Rule{{Capability: "shell", Effect: "allow", Match: []string{"ls *"}}}
	if !reflect.DeepEqual(f.Rules, want) {
		t.Errorf("user permissions file rules = %+v, want Guarded's preset rules %+v", f.Rules, want)
	}
	if second, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || second == first {
		t.Errorf("after the switch to Custom the chat's next open kept its bridge (err %v)", err)
	}
	got := broadcastTypes(h, since)
	for _, typ := range []marotte.EventType{marotte.EventSettingsUpdated, marotte.EventPermissionsChanged} {
		if !slices.Contains(got, typ) {
			t.Errorf("broadcasts after the switch = %v, want %s among them", got, typ)
		}
	}
}

func TestEnsureCustomProfile_IsANoOpOnCustom(t *testing.T) {
	h, configDir := reopenFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeSetting(t, configDir, settings.KeySecurityProfile, "custom")
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if err := h.EnsureCustomProfile(t.Context()); err != nil {
		t.Fatalf("EnsureCustomProfile on custom = %v, want nil", err)
	}

	if second, _ := h.coord.openBridge(t.Context(), "c1", ""); second != first {
		t.Error("EnsureCustomProfile on custom reopened an open chat")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".kiro", "settings", "permissions.yaml")); !os.IsNotExist(err) {
		t.Errorf("EnsureCustomProfile on custom touched the user permissions file (stat err %v)", err)
	}
}

// wantTelemetryPending checks a busy engine's answer to a telemetry move: restart-pending for usage telemetry, the
// chat's session left in place, and no second generation while the turn runs.
func wantTelemetryPending(t *testing.T, h *Runtime, first, second *sharedBridge) {
	t.Helper()
	if !slices.Contains(h.engine.status().Restart.Reasons, reasonTelemetry) {
		t.Errorf("restart reasons = %v, want %q: telemetry is engine-wide", h.engine.status().Restart.Reasons, reasonTelemetry)
	}
	if second != first || sessionClosed(fakeOf(first)) {
		t.Error("a telemetry move reloaded the chat's session, which holds no engine-wide value")
	}
	if got := fakeSourceOf(t, h).generations(); got != 1 {
		t.Errorf("engine generations = %d, want 1: an open turn holds the restart", got)
	}
}

func TestReconcileSessionSettings_ATelemetrySwitchMarksTheEngineRestartPending(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	openTestTurn(t, h.coord.turns, "c1")
	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	h.ReconcileSessionSettings(t.Context())

	wantTelemetryPending(t, h, first, h.bridge.mgr.get("c1"))
}

func TestGovernanceLockChange_ReopensOpenChatsOnlyWhenAFingerprintMoves(t *testing.T) {
	orgAnalytics := func(on bool) func(context.Context, *Runtime) {
		return func(ctx context.Context, h *Runtime) { h.config.SetGovernance(ctx, orgTelemetry(on)) }
	}
	denySubagents := func(ctx context.Context, h *Runtime) {
		h.config.publishGovernance(ctx, h.config.governance.setAdmin(reduceAdminRules([]marotte.PolicyRule{adminRule("subagent", "deny")})))
	}
	for _, tc := range []struct {
		lock        func(context.Context, *Runtime)
		stored      map[string]any
		name        string
		wantReopen  bool
		wantPending bool
	}{
		{name: "organization_turns_usage_analytics_off", lock: orgAnalytics(false), wantPending: true},
		{name: "organization_on_matches_the_switch", lock: orgAnalytics(true)},
		{name: "denied_subagents_turn_workflows_off", lock: denySubagents, wantReopen: true},
		{
			name:   "denied_subagents_over_workflows_already_off",
			stored: map[string]any{settings.KeyWorkflowsEnabled: false},
			lock:   denySubagents,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withKiroTelemetry(t, `{"telemetry.enabled":true}`)
			settled := make(chan struct{}, 1)
			h, configDir := reopenFixture(t, WithGovernanceLocksHook(func(context.Context) { settled <- struct{}{} }))
			for key, value := range tc.stored {
				writeSetting(t, configDir, key, value)
			}
			first, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			// An open turn holds a restart, so a pending one stays observable.
			openTestTurn(t, h.coord.turns, "c1")

			tc.lock(t.Context(), h)
			select {
			case <-settled:
			case <-time.After(10 * time.Second):
				t.Fatal("the lock change never finished its hooks")
			}

			if pending := h.engine.restartPending(); pending != tc.wantPending {
				t.Errorf("%s over stored %v: engine restart pending = %v, want %v", tc.name, tc.stored, pending, tc.wantPending)
			}
			second, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge after the lock change: %v", err)
			}
			if reopened := second != first; reopened != tc.wantReopen {
				t.Errorf("%s over stored %v: reopened = %v, want %v", tc.name, tc.stored, reopened, tc.wantReopen)
			}
		})
	}
}

func presetPolicy(br *fakeBridge) {
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodV3PermissionsList {
			return nil, nil, false
		}
		var rules []marotte.PolicyRule
		for _, id := range br.lastSessionOpts().Presets {
			rules = append(rules, marotte.PolicyRule{
				Capability: "fs_read", Effect: "allow", Match: []string{id}, Scope: "session", Source: "preset:" + id,
			})
		}
		raw, err := json.Marshal(map[string]any{"rules": rules})
		return raw, nil, err == nil
	}
}

func TestEnsureCustomProfile_CopiesTheProfileAHandEditPicked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h, configDir := reopenFixtureWith(t, presetPolicy)
	fakeSourceOf(t, h).setObserver(func() engineSession {
		b := newFakeBridge()
		presetPolicy(b)
		return b
	})
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)

	rewriteConfigByHand(t, configDir, `{"`+settings.KeySecurityProfile+`":"`+policyfile.ProfileGuarded+`"}`)

	// The policy read is an observer pass opened with the presets the document names now.
	if err := h.EnsureCustomProfile(t.Context()); err != nil {
		t.Fatalf("EnsureCustomProfile after a hand edit to guarded = %v, want nil", err)
	}
	f, err := policyfile.Load(filepath.Join(home, ".kiro", "settings", "permissions.yaml"))
	if err != nil {
		t.Fatalf("Load the user permissions file: %v", err)
	}
	want := []policyfile.Rule{{Capability: "fs_read", Effect: "allow", Match: []string{policyfile.PresetReadWorkspace}}}
	if !reflect.DeepEqual(f.Rules, want) {
		t.Errorf("Customize after a hand edit to guarded saved %+v, want guarded's rules %+v", f.Rules, want)
	}
}

func rewriteConfigByHand(t *testing.T, configDir, body string) {
	t.Helper()
	tmp := filepath.Join(configDir, "config.json.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(configDir, settings.Filename)); err != nil {
		t.Fatalf("Setup: rename over config.json: %v", err)
	}
}

func TestOpenBridge_AHandEditOfConfigReopensTheChatAtItsNextOpen(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	session := first.bridge.SessionID()

	rewriteConfigByHand(t, configDir, `{"`+settings.KeySecurityProfile+`":"unrestricted"}`)

	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the hand edit: %v", err)
	}
	if second == first || !sessionClosed(fakeOf(first)) {
		t.Fatal("a hand edit of security_profile left the chat on its old session")
	}
	opts := fakeOf(second).lastSessionOpts()
	if !slices.Equal(opts.Presets, []string{"allow-all"}) || opts.SessionID != string(session) {
		t.Errorf("the reopened bridge started with presets %v loading %q, want [allow-all] loading %q", opts.Presets, opts.SessionID, session)
	}
}

func TestReconcileSessionSettings_ABusyChatSwitchesAfterItsTurn(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if !first.tryAcquireForPrompt() {
		t.Fatal("Setup: the chat's prompt slot was not free")
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")

	h.ReconcileSessionSettings(t.Context())

	if during, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || during != first || sessionClosed(fakeOf(first)) {
		t.Fatalf("an open during the turn replaced or stopped the busy bridge (err %v)", err)
	}
	first.releaseAfterPrompt()
	after, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the turn: %v", err)
	}
	if after == first || !sessionClosed(fakeOf(first)) {
		t.Fatal("the first open after the turn kept the session opened under the old profile")
	}
	opts := fakeOf(after).lastSessionOpts()
	if !slices.Equal(opts.Presets, []string{"allow-all"}) || opts.SessionID != string(first.bridge.SessionID()) {
		t.Errorf("the reopened bridge started with presets %v loading %q, want [allow-all] loading the chat's session", opts.Presets, opts.SessionID)
	}
}

func TestReconcileSessionSettings_AChatHostingALiveRunSwitchesAfterTheRun(t *testing.T) {
	h, configDir := reopenFixture(t)
	var runLive atomic.Bool
	runLive.Store(true)
	h.bridge.mgr.hostsLiveRun = func(chatID marotte.ChatID) bool { return chatID == "c1" && runLive.Load() }
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")

	h.ReconcileSessionSettings(t.Context())

	if during, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || during != first || sessionClosed(fakeOf(first)) {
		t.Fatalf("an open while the chat hosts a live run replaced or stopped its bridge (err %v)", err)
	}
	runLive.Store(false)
	after, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the run: %v", err)
	}
	if after == first || !sessionClosed(fakeOf(first)) {
		t.Fatal("the first open after the run kept the session opened under the old profile")
	}
	if got := fakeOf(after).lastSessionOpts().Presets; !slices.Equal(got, []string{"allow-all"}) {
		t.Errorf("the reopened bridge's presets = %v, want [allow-all]", got)
	}
}

func TestReconcileSessionSettings_ACancelledCallerReopensNothing(t *testing.T) {
	h, configDir := reopenFixture(t)
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
	h.ReconcileSessionSettings(t.Context())
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	h.ReconcileSessionSettings(cancelled)

	if h.engine.restartPending() {
		t.Error("a reconcile under a cancelled context marked the engine restart-pending though no setting moved")
	}
	if second, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || second != first {
		t.Errorf("a reconcile under a cancelled context reopened the open chat though no setting moved (err %v)", err)
	}
}

func TestOpenBridge_AShellEditOfTelemetryMarksTheEngineRestartPending(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	openTestTurn(t, h.coord.turns, "c1")

	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the edit: %v", err)
	}
	wantTelemetryPending(t, h, first, second)
}

// A reload's close went through and its load of the same session failed: the open falls back to a new session, and
// the closed handle is never put back.
func TestReset_CloseDoneLoadFailedKeepsReloadNeededAndNeverRestoresTheHandle(t *testing.T) {
	var doors atomic.Int32
	h, _ := reopenFixtureWith(t, func(br *fakeBridge) {
		if doors.Add(1) > 1 {
			br.loadErr = errors.New("load refused")
		}
	})
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	old := fakeOf(first)
	h.markSessionsForReload("test")

	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the mark: %v", err)
	}

	if !sessionClosed(old) {
		t.Fatal("Setup: the reload did not close the marked session")
	}
	if got := doors.Load(); got != 3 {
		t.Errorf("doors = %d, want 3: the first open, the reload's load, then its session/new fallback", got)
	}
	if cur := fakeOf(second); cur == old || h.bridge.mgr.get("c1") != second {
		t.Error("the closed handle was restored on the chat's entry after its load failed")
	}
	if id := fakeOf(second).lastSessionOpts().SessionID; id != "" {
		t.Errorf("the fallback door named session %q, want a new session", id)
	}
}

// A reload whose close KAS refused keeps the chat on its session and keeps the mark for the next open.
func TestReset_RefusedCloseKeepsTheHandleAndTheMark(t *testing.T) {
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	old := fakeOf(first)
	old.mu.Lock()
	old.closeErr = errors.New("refused")
	old.mu.Unlock()
	h.markSessionsForReload("test")

	during, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil || during != first || sessionClosed(old) {
		t.Fatalf("an open after a refused close left the chat's session (err %v)", err)
	}
	if !reloadMarked(h, "c1") {
		t.Fatal("a refused close dropped the reload mark")
	}
	old.mu.Lock()
	old.closeErr = nil
	old.mu.Unlock()
	after, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the refusal cleared: %v", err)
	}
	if after == first || !sessionClosed(old) {
		t.Error("the kept mark did not reload the session at the next open")
	}
}

// One chat's reload closes and loads only that chat's session: the engine and the other chat's session stay.
func TestSessionReset_LeavesEngineAndCoTenantUntouched(t *testing.T) {
	h, _ := reopenFixture(t)
	if _, err := h.chatStore.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true }); err != nil {
		t.Fatalf("Setup: seed c2: %v", err)
	}
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge(c1): %v", err)
	}
	coTenant, err := h.coord.openBridge(t.Context(), "c2", "")
	if err != nil {
		t.Fatalf("OpenBridge(c2): %v", err)
	}
	src := fakeSourceOf(t, h)
	engines := src.generations()
	first.mu.Lock()
	first.reload = true
	first.mu.Unlock()

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge(c1) after the mark: %v", err)
	}

	if !sessionClosed(fakeOf(first)) {
		t.Fatal("Setup: c1's session was not reset")
	}
	if got := src.generations(); got != engines {
		t.Errorf("engine generations = %d, want %d: a session reset never restarts the engine", got, engines)
	}
	if h.bridge.mgr.get("c2") != coTenant || sessionClosed(fakeOf(coTenant)) {
		t.Error("c1's reset closed or replaced c2's session")
	}
}

// A mark waits for the chat's turn to release its prompt slot and for the run step it hosts to close, so a session
// is never closed under a live turn or step.
func TestReloadIfMarked_WaitsForTheTurnAndTheHostedStep(t *testing.T) {
	for _, tc := range []struct {
		hold func(t *testing.T, h *Runtime, sb *sharedBridge) (release func())
		name string
	}{
		{name: "an open turn", hold: func(t *testing.T, _ *Runtime, sb *sharedBridge) func() {
			if !sb.tryAcquireForPrompt() {
				t.Fatal("Setup: the chat's prompt slot was not free")
			}
			return sb.releaseAfterPrompt
		}},
		{name: "a hosted run step", hold: func(_ *testing.T, h *Runtime, _ *sharedBridge) func() {
			var live atomic.Bool
			live.Store(true)
			h.bridge.mgr.hostsLiveRun = func(chatID marotte.ChatID) bool { return chatID == "c1" && live.Load() }
			return func() { live.Store(false) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := reopenFixture(t)
			first, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			release := tc.hold(t, h, first)
			h.markSessionsForReload("test")

			if during, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || during != first || sessionClosed(fakeOf(first)) {
				t.Fatalf("an open while held replaced or closed the session (err %v)", err)
			}
			release()
			after, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge after the hold: %v", err)
			}
			if after == first || !sessionClosed(fakeOf(first)) {
				t.Error("the first open after the hold kept the marked session")
			}
		})
	}
}
