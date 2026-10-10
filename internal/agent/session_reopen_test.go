package agent

import (
	"context"
	"encoding/json"
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
	h := New(context.Background(), t.TempDir(), func() ACPBridge {
		br := newFakeBridge()
		if seed != nil {
			seed(br)
		}
		return br
	}, cs, append([]Option{WithConfigDir(configDir)}, opts...)...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	return h
}

// fakeOf is the process sb holds. Read through the bridge, never by spawn order: a session load
// also starts the utility session on another goroutine.
func fakeOf(sb *sharedBridge) *fakeBridge {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	br, _ := sb.bridge.(*fakeBridge)
	return br
}

func acquireUtility(t *testing.T, h *Runtime) *fakeBridge {
	t.Helper()
	lease, err := h.utility.get().session.acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire the utility session: %v", err)
	}
	br, _ := lease.bridge.(*fakeBridge)
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
	}); err != nil {
		t.Fatalf("Setup: write %s: %v", key, err)
	}
}

func stopped(br *fakeBridge) bool {
	br.mu.Lock()
	defer br.mu.Unlock()
	return br.stopped
}

func TestReopenChatSessions_IdleChatKeepsItsProcessUntilItsNextOpen(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeSetting(t, configDir, settings.KeySecurityProfile, "unrestricted")

	h.reopenChatSessions("test")

	if h.bridge.mgr.get("c1") != first || stopped(fakeOf(first)) {
		t.Fatal("ReopenChatSessions stopped an idle chat's process; it must wait for the chat's next open")
	}
	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the mark: %v", err)
	}
	if second == first || !stopped(fakeOf(first)) {
		t.Fatal("the next open reused the marked bridge instead of replacing it")
	}
	if got := fakeOf(second).lastStartOpts().Presets; !slices.Equal(got, []string{"allow-all"}) {
		t.Errorf("the reopened session's presets = %v, want [allow-all] from the new profile", got)
	}
}

func TestReopenChatSessions_LeavesRunBridgesUntouched(t *testing.T) {
	h, _ := reopenFixture(t)
	sb := &sharedBridge{bridge: newFakeBridge(), state: bridgeIdle}
	if !h.bridge.mgr.insert(runChatID("wf-1"), sb) {
		t.Fatal("Setup: insert run bridge = false")
	}

	h.reopenChatSessions("test")

	if reopen, _ := h.bridge.mgr.closeIfRetired(runChatID("wf-1"), sb); reopen {
		t.Error("ReopenChatSessions marked a run bridge for retirement")
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
// retire the bridge that already spawned under the new profile.
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
		t.Errorf("the profile callback replaced a bridge already spawned under the new profile (err %v)", err)
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

			reopened := reopensAfter(t, h, func() { writeSetting(t, configDir, settings.KeySpecPlanningAskFirst, true) })

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
)

var configWrites = []struct {
	value any
	from  any
	key   string
	reach settingReach
}{
	{key: settings.KeyMemoryMode, value: settings.MemoryOff, reach: reachReopen},
	{key: settings.KeySecurityProfile, value: policyfile.ProfileTrusted, reach: reachReopen},
	{key: settings.KeyWorkflowsEnabled, value: false, reach: reachReopen},
	{key: settings.KeySpecPlanning, value: settings.SpecPlanningQuick, reach: reachReopen},
	// Read only while planning is on: TestSpecPlanningAskFirst_ReopensOnlyWhilePlanningIsOn.
	{key: settings.KeySpecPlanningAskFirst, value: true, reach: reachNoChat},
	{key: settings.KeyWorkValidation, value: settings.FeatureOff, reach: reachReopen},
	{key: settings.KeyInlineAgents, value: true, reach: reachReopen},
	{key: settings.KeySteeringReminders, value: true, reach: reachReopen},
	{key: settings.KeyCloudFormationSafety, value: settings.FeatureOn, reach: reachReopen},
	{key: settings.KeyAutoRouting, value: settings.FeatureOff, reach: reachReopen},
	{key: settings.KeyAutoDelegation, value: settings.FeatureOff, reach: reachReopen},
	{key: settings.KeyKnowledgeEnabled, value: false, reach: reachReopen},
	{key: settings.KeyToolSearchEnabled, value: true, reach: reachReopen},
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
}

var kiroCLIWrites = []struct {
	key   string
	reach settingReach
	value bool
}{
	{key: kiroTelemetryKey, value: true, reach: reachReopen},
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

func reopensAfter(t *testing.T, h *Runtime, write func()) bool {
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

func TestConfigWrites_ReachOpenChatsByTheirRoute(t *testing.T) {
	for i, tc := range configWrites {
		t.Run(fmt.Sprintf("%02d_%s", i, tc.key), func(t *testing.T) {
			h, configDir := reopenFixture(t)
			if tc.from != nil {
				writeSetting(t, configDir, tc.key, tc.from)
			}

			reopened := reopensAfter(t, h, func() { writeSetting(t, configDir, tc.key, tc.value) })

			if want := tc.reach == reachReopen; reopened != want {
				t.Errorf("writing %s = %v (from %v, route %q): reopened = %v, want %v", tc.key, tc.value, tc.from, tc.reach, reopened, want)
			}
		})
	}
}

func TestKiroCLIWrites_ReachOpenChatsByTheirRoute(t *testing.T) {
	for _, tc := range kiroCLIWrites {
		t.Run(tc.key, func(t *testing.T) {
			cliJSON := withKiroTelemetry(t, "")
			h, _ := reopenFixture(t)

			reopened := reopensAfter(t, h, func() {
				writeKiroCLISettings(t, cliJSON, fmt.Sprintf(`{%q:%t}`, tc.key, tc.value))
			})

			if want := tc.reach == reachReopen; reopened != want {
				t.Errorf("writing kiro-cli %s = %t (route %q): reopened = %v, want %v", tc.key, tc.value, tc.reach, reopened, want)
			}
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
	h, configDir := reopenFixtureWith(t, func(br *fakeBridge) {
		br.callResults = map[string]json.RawMessage{methodV3PermissionsList: json.RawMessage(`{"rules":[` +
			`{"capability":"shell","effect":"allow","match":["ls *"],"scope":"session","source":"preset:read-workspace"}]}`)}
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

func TestReconcileSessionSettings_TelemetrySwitchReopensOpenChats(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	h.ReconcileSessionSettings(t.Context())

	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the write: %v", err)
	}
	if second == first || !fakeOf(second).lastStartOpts().DisableTelemetry {
		t.Error("turning telemetry off left the open chat on a session spawned with telemetry on")
	}
}

func TestGovernanceLockChange_ReopensOpenChatsOnlyWhenASpawnMoves(t *testing.T) {
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
		wantRestart bool
	}{
		{name: "organization_turns_usage_analytics_off", lock: orgAnalytics(false), wantReopen: true, wantRestart: true},
		{name: "organization_on_matches_the_switch", lock: orgAnalytics(true)},
		{name: "denied_subagents_turn_workflows_off", lock: denySubagents, wantReopen: true},
		{
			name:   "denied_subagents_turn_inline_agents_off",
			stored: map[string]any{settings.KeyWorkflowsEnabled: false, settings.KeyInlineAgents: true},
			lock:   denySubagents, wantReopen: true,
		},
		{
			name:   "denied_subagents_over_both_already_off",
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
			utility := acquireUtility(t, h)

			tc.lock(t.Context(), h)
			select {
			case <-settled:
			case <-time.After(10 * time.Second):
				t.Fatal("the lock change never finished its hooks")
			}

			if restarted := stopped(utility); restarted != tc.wantRestart {
				t.Errorf("%s over stored %v: utility session stopped = %v, want %v", tc.name, tc.stored, restarted, tc.wantRestart)
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

func TestUtilitySession_AShellEditOfTelemetryRestartsItAtItsNextUse(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, _ := reopenFixture(t)
	held := acquireUtility(t, h)
	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	next := acquireUtility(t, h)

	if !stopped(held) {
		t.Error("turning telemetry off outside Marotte left the utility session's process running with telemetry on")
	}
	if next == held || !next.lastStartOpts().DisableTelemetry {
		t.Error("the utility session's next use did not respawn it with DisableTelemetry")
	}
}

func TestReconcileSessionSettings_AMemorySwitchLeavesTheUtilitySessionRunning(t *testing.T) {
	h, configDir := reopenFixture(t)
	utility := acquireUtility(t, h)

	reopened := reopensAfter(t, h, func() { writeSetting(t, configDir, settings.KeyMemoryMode, settings.MemoryOff) })

	if !reopened {
		t.Fatal("Setup: turning memory off did not reopen the open chat")
	}
	if stopped(utility) {
		t.Error("turning memory off stopped the utility session, which never reads memory")
	}
}

func presetPolicy(br *fakeBridge) {
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodV3PermissionsList {
			return nil, nil, false
		}
		var rules []marotte.PolicyRule
		for _, id := range br.lastStartOpts().Presets {
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
	writeSetting(t, configDir, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
	held := acquireUtility(t, h)
	if got := held.lastStartOpts().Presets; !slices.Equal(got, []string{policyfile.PresetAllowAll}) {
		t.Fatalf("Setup: the utility session spawned with presets %v, want [allow-all]", got)
	}

	rewriteConfigByHand(t, configDir, `{"`+settings.KeySecurityProfile+`":"`+policyfile.ProfileGuarded+`"}`)

	if err := h.EnsureCustomProfile(t.Context()); err != nil {
		t.Fatalf("EnsureCustomProfile after a hand edit to guarded = %v, want nil", err)
	}
	if !stopped(held) {
		t.Error("the utility session spawned under unrestricted kept answering after the hand edit to guarded")
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
	if second == first || !stopped(fakeOf(first)) {
		t.Fatal("a hand edit of security_profile left the chat on its old process")
	}
	opts := fakeOf(second).lastStartOpts()
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

	if during, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || during != first || stopped(fakeOf(first)) {
		t.Fatalf("an open during the turn replaced or stopped the busy bridge (err %v)", err)
	}
	first.releaseAfterPrompt()
	after, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the turn: %v", err)
	}
	if after == first || !stopped(fakeOf(first)) {
		t.Fatal("the first open after the turn kept the bridge spawned under the old profile")
	}
	opts := fakeOf(after).lastStartOpts()
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

	if during, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || during != first || stopped(fakeOf(first)) {
		t.Fatalf("an open while the chat hosts a live run replaced or stopped its bridge (err %v)", err)
	}
	runLive.Store(false)
	after, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the run: %v", err)
	}
	if after == first || !stopped(fakeOf(first)) {
		t.Fatal("the first open after the run kept the bridge spawned under the old profile")
	}
	if got := fakeOf(after).lastStartOpts().Presets; !slices.Equal(got, []string{"allow-all"}) {
		t.Errorf("the reopened bridge's presets = %v, want [allow-all]", got)
	}
}

func TestOpenBridge_LeavesTheUtilitySessionAloneWhenNothingMoved(t *testing.T) {
	h, _ := reopenFixture(t)
	utility := acquireUtility(t, h)

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	if stopped(utility) {
		t.Error("the first chat open restarted the utility session though no setting moved since boot")
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
	utility := acquireUtility(t, h)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	h.ReconcileSessionSettings(cancelled)

	if stopped(utility) {
		t.Error("a reconcile under a cancelled context stopped the utility session though no setting moved")
	}
	if second, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil || second != first {
		t.Errorf("a reconcile under a cancelled context reopened the open chat though no setting moved (err %v)", err)
	}
}

func TestOpenBridge_AShellEditOfTelemetryReopensTheChatAtItsNextOpen(t *testing.T) {
	cliJSON := withKiroTelemetry(t, `{"telemetry.enabled":true}`)
	h, _ := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	writeKiroCLISettings(t, cliJSON, `{"telemetry.enabled":false}`)

	second, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after the edit: %v", err)
	}
	if second == first || !fakeOf(second).lastStartOpts().DisableTelemetry {
		t.Error("cli.json edited outside Marotte left the open chat on a process spawned with telemetry on")
	}
}
