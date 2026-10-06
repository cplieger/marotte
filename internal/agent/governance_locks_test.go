package agent

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func enterpriseProfile() *marotte.GovernanceStatePayload {
	return &marotte.GovernanceStatePayload{
		Known:        true,
		IsEnterprise: true,
		Features: marotte.GovernanceFeatures{
			MCPEnabled:        true,
			WebToolsEnabled:   false,
			UsageAnalytics:    true,
			ContentCollection: false,
		},
	}
}

func adminRule(capability, effect string, match ...string) marotte.PolicyRule {
	return marotte.PolicyRule{Capability: capability, Effect: effect, Scope: "administration", Source: "/etc/kiro/managed-settings.json", Match: match}
}

func TestComposeLocks(t *testing.T) {
	personal := sampleGovernance()
	tests := []struct {
		profile *marotte.GovernanceStatePayload
		want    map[string]marotte.GovernanceLock
		name    string
		rules   []marotte.PolicyRule
	}{
		{name: "personal_account_pins_nothing", profile: &personal},
		{name: "unknown_profile_pins_nothing", profile: &marotte.GovernanceStatePayload{IsEnterprise: true}},
		{
			name:    "enterprise_profile",
			profile: enterpriseProfile(),
			want: map[string]marotte.GovernanceLock{
				marotte.LockContentCollection: {Value: false, Source: marotte.LockSourceOrganization, Reason: lockReasonSet},
				marotte.LockTelemetry:         {Value: true, Source: marotte.LockSourceOrganization, Reason: lockReasonSet},
				marotte.LockWebTools:          {Value: false, Source: marotte.LockSourceOrganization, Reason: lockReasonWebTools},
			},
		},
		{
			name:  "admin_denies_subagent",
			rules: []marotte.PolicyRule{adminRule("subagent", "deny")},
			want: map[string]marotte.GovernanceLock{
				marotte.LockWorkflows:    {Source: marotte.LockSourceAdministrator, Reason: lockReasonSubagents},
				marotte.LockInlineAgents: {Source: marotte.LockSourceAdministrator, Reason: lockReasonSubagents},
			},
		},
		{
			name:  "admin_denies_builtin_group",
			rules: []marotte.PolicyRule{adminRule("builtin", "deny", "**")},
			want: map[string]marotte.GovernanceLock{
				marotte.LockWorkflows:    {Source: marotte.LockSourceAdministrator, Reason: lockReasonSubagents},
				marotte.LockInlineAgents: {Source: marotte.LockSourceAdministrator, Reason: lockReasonSubagents},
				marotte.LockWebTools:     {Source: marotte.LockSourceAdministrator, Reason: lockReasonWebTools},
				marotte.LockPowers:       {Source: marotte.LockSourceAdministrator, Reason: lockReasonPowers},
			},
		},
		{
			name:  "admin_denies_mcp",
			rules: []marotte.PolicyRule{adminRule("mcp", "deny")},
			want: map[string]marotte.GovernanceLock{
				marotte.LockMCP: {Source: marotte.LockSourceAdministrator, Reason: lockReasonMCPBlocked},
			},
		},
		{
			name:  "one_web_tool_is_not_web_tools",
			rules: []marotte.PolicyRule{adminRule("web_fetch", "deny")},
		},
		{
			name:  "narrowed_deny_pins_nothing",
			rules: []marotte.PolicyRule{adminRule("subagent", "deny", "reviewer")},
		},
		{
			name:  "ask_pins_nothing",
			rules: []marotte.PolicyRule{adminRule("subagent", "ask")},
		},
		{
			name:  "user_scope_deny_pins_nothing",
			rules: []marotte.PolicyRule{{Capability: "subagent", Effect: "deny", Scope: "user"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeLocks(tc.profile, reduceAdminRules(tc.rules))
			if !maps.Equal(got, tc.want) {
				t.Errorf("composeLocks() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReduceAdminRules_AnyRuleRestricts(t *testing.T) {
	if p := reduceAdminRules([]marotte.PolicyRule{adminRule("shell", "ask", "rm *")}); !p.any {
		t.Error("an administration ask rule did not set any: the picker hint would not show")
	}
	if p := reduceAdminRules([]marotte.PolicyRule{{Capability: "shell", Effect: "deny", Scope: "workspace"}}); p.any {
		t.Error("a workspace rule set any: only administration rules restrict")
	}
}

type govRecorder struct {
	st     *Settings
	lc     *lifetime
	events []marotte.GovernanceStatePayload
	hooks  int
	mu     sync.Mutex
}

func newGovRecorder(t *testing.T) *govRecorder {
	t.Helper()
	r := &govRecorder{lc: &lifetime{shutdownCtx: t.Context()}}
	r.st = newSettings(r.lc, func(_ context.Context, evt marotte.ServerEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, evt.Payload.(marotte.GovernanceStatePayload))
	})
	r.st.onLocksChanged = func(context.Context) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.hooks++
	}
	return r
}

func (r *govRecorder) settle() (events []marotte.GovernanceStatePayload, hooks int) {
	r.lc.inflight.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events, r.hooks
}

func TestSettingsGovernance_PublishesComposedStateOnChangeOnly(t *testing.T) {
	r := newGovRecorder(t)
	r.st.SetGovernance(t.Context(), *enterpriseProfile())
	r.st.SetGovernance(t.Context(), *enterpriseProfile())
	events, hooks := r.settle()
	if len(events) != 1 {
		t.Fatalf("SetGovernance twice with one profile broadcast %d events, want 1", len(events))
	}
	if l, ok := events[0].Locks[marotte.LockContentCollection]; !ok || l.Value {
		t.Errorf("broadcast locks = %+v, want content collection pinned off", events[0].Locks)
	}
	if hooks != 1 {
		t.Errorf("lock hook ran %d times, want 1", hooks)
	}

	r.st.publishGovernance(t.Context(), r.st.governance.setAdmin(reduceAdminRules([]marotte.PolicyRule{adminRule("subagent", "deny")})))
	events, hooks = r.settle()
	if len(events) != 2 || !events[1].AdminRestricted {
		t.Fatalf("admin rules: events = %+v, want a second event with admin_restricted", events)
	}
	if _, ok := events[1].Locks[marotte.LockWorkflows]; !ok {
		t.Errorf("admin deny subagent: locks = %+v, want workflows locked", events[1].Locks)
	}
	if hooks != 2 {
		t.Errorf("lock hook ran %d times after a lock change, want 2", hooks)
	}

	reg := &marotte.GovernanceMCPRegistry{Servers: []marotte.GovernanceRegistryServer{{Name: "github"}}}
	r.st.SetMCPRegistry(t.Context(), reg)
	events, hooks = r.settle()
	if len(events) != 3 || events[2].MCPRegistry == nil || events[2].MCPRegistry.Servers[0].Name != "github" {
		t.Fatalf("SetMCPRegistry: events = %+v, want a third event carrying the registry", events)
	}
	if hooks != 2 {
		t.Errorf("a registry change ran the lock hook (%d runs); it moves no lock", hooks)
	}
}

func TestGovernanceLocks_ColdCacheCarriesAdminLocks(t *testing.T) {
	r := newGovRecorder(t)
	r.st.publishGovernance(t.Context(), r.st.governance.setAdmin(reduceAdminRules([]marotte.PolicyRule{adminRule("power", "deny")})))
	if _, ok := r.st.GovernanceLocks()[marotte.LockPowers]; !ok {
		t.Errorf("GovernanceLocks() = %+v before any profile, want the administrator's power lock", r.st.GovernanceLocks())
	}
	if p := r.st.governance.peek(); p.Known {
		t.Error("peek() Known = true with no profile observed")
	}
}

// Once the managed file fails to load, the lock map fails closed until a reload reports it healthy.
func TestPolicyChanged_FatalAdministrationErrorFailsClosed(t *testing.T) {
	fatal := []marotte.PolicyErrorItem{{Scope: "administration", Message: "parse", Fatal: true}}
	allLocked := []string{marotte.LockWorkflows, marotte.LockInlineAgents, marotte.LockWebTools, marotte.LockPowers, marotte.LockMCP}
	assertFailedClosed := func(t *testing.T, st *Settings) {
		t.Helper()
		locks := st.GovernanceLocks()
		for _, key := range allLocked {
			if l, ok := locks[key]; !ok || l.Reason != lockReasonPolicyFailed {
				t.Errorf("after a fatal administration error, lock %s = %+v (present %v), want the policy-failed lock", key, l, ok)
			}
		}
		if !st.governance.peek().AdminRestricted {
			t.Error("after a fatal administration error, AdminRestricted = false")
		}
	}

	t.Run("cold", func(t *testing.T) {
		r := newGovRecorder(t)
		r.st.PolicyChanged(t.Context(), fatal, false)
		r.settle()
		assertFailedClosed(t, r.st)
	})

	t.Run("previously_warm", func(t *testing.T) {
		r := newGovRecorder(t)
		r.st.publishGovernance(t.Context(), r.st.governance.setAdmin(reduceAdminRules([]marotte.PolicyRule{adminRule("power", "deny")})))
		r.st.PolicyChanged(t.Context(), fatal, false)
		r.settle()
		assertFailedClosed(t, r.st)

		r.st.publishGovernance(t.Context(), r.st.governance.setAdmin(reduceAdminRules(nil)))
		assertFailedClosed(t, r.st)

		r.st.PolicyChanged(t.Context(), []marotte.PolicyErrorItem{{Scope: "user", Message: "bad", Fatal: true}}, false)
		assertFailedClosed(t, r.st)

		r.st.publishGovernance(t.Context(), r.st.governance.setAdmin(reduceAdminRules([]marotte.PolicyRule{adminRule("power", "deny")})))
		r.st.PolicyChanged(t.Context(), nil, true)
		r.settle()
		locks := r.st.GovernanceLocks()
		if l, ok := locks[marotte.LockPowers]; !ok || l.Reason != lockReasonPowers {
			t.Errorf("after a healthy reload, power lock = %+v (present %v), want the rule's own lock", l, ok)
		}
		if _, ok := locks[marotte.LockMCP]; ok {
			t.Errorf("after a healthy reload, locks = %+v, want the fail-closed locks lifted", locks)
		}
	})
}

func writeSettings(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestContentCollectionEnabled(t *testing.T) {
	pinned := map[string]marotte.GovernanceLock{marotte.LockContentCollection: {Value: false}}
	tests := []struct {
		locks map[string]marotte.GovernanceLock
		name  string
		body  string
		want  bool
	}{
		{name: "absent_is_off", body: `{}`},
		{name: "stored_on", body: `{"content_collection_enabled":true}`, want: true},
		{name: "stored_off", body: `{"content_collection_enabled":false}`},
		{name: "lock_wins_over_stored_on", body: `{"content_collection_enabled":true}`, locks: pinned},
		{name: "unreadable_is_off", body: `{`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentCollectionEnabled(t.Context(), writeSettings(t, tc.body), tc.locks); got != tc.want {
				t.Errorf("contentCollectionEnabled(%s, %v) = %v, want %v", tc.body, tc.locks, got, tc.want)
			}
		})
	}
}

func TestAgentFeatures_SubagentLockWins(t *testing.T) {
	dir := writeSettings(t, `{"inline_agents_enabled":true,"workflows_enabled":true}`)
	locks := composeLocks(nil, reduceAdminRules([]marotte.PolicyRule{adminRule("subagent", "deny")}))
	got := agentFeatures(t.Context(), dir, locks)
	if got.InlineAgents || got.Workflows {
		t.Errorf("agentFeatures(stored on, subagent denied) = inline %v workflows %v, want both false", got.InlineAgents, got.Workflows)
	}
	if got := agentFeatures(t.Context(), dir, nil); !got.InlineAgents || !got.Workflows {
		t.Errorf("agentFeatures(stored on, no lock) = inline %v workflows %v, want both true", got.InlineAgents, got.Workflows)
	}
}
