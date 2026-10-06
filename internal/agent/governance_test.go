package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func sampleGovernance() marotte.GovernanceStatePayload {
	return marotte.GovernanceStatePayload{
		Known:        true,
		IsEnterprise: false,
		Features: marotte.GovernanceFeatures{
			MCPEnabled:        true,
			WebToolsEnabled:   true,
			ContentCollection: true,
			AutonomousAgents:  true,
		},
	}
}

func TestGovernanceCache_SetGetWarm(t *testing.T) {
	c := newGovernanceCache()
	if _, ok := c.get(); ok {
		t.Fatal("cold cache should report !ok")
	}
	select {
	case <-c.warm:
		t.Fatal("warm channel should be open on a cold cache")
	default:
	}

	c.setProfile(sampleGovernance())
	got, ok := c.get()
	if !ok || !got.Known || !got.Features.MCPEnabled {
		t.Fatalf("after set: got=%+v ok=%v", got, ok)
	}
	select {
	case <-c.warm:
	default:
		t.Fatal("warm channel should be closed after the first set")
	}

	// A second set must not panic (close-once) and must overwrite.
	c.setProfile(marotte.GovernanceStatePayload{Known: true})
	if got, _ := c.get(); got.Features.MCPEnabled {
		t.Error("second set did not overwrite the cached state")
	}
}

func TestGovernanceCache_AdminRulesResolveOnce(t *testing.T) {
	c := newGovernanceCache()
	if c.adminPolicyKnown() {
		t.Fatal("cold cache reports the administrator rules known")
	}
	if ch := c.setAdminFailed(false); ch.adminResolved || c.adminPolicyKnown() {
		t.Error("a reload with no fatal error resolved the administrator rules; nothing was read")
	}
	if ch := c.setAdmin(reduceAdminRules(nil)); !ch.adminResolved || !c.adminPolicyKnown() {
		t.Errorf("the first rules read: adminResolved=%v known=%v, want both true", ch.adminResolved, c.adminPolicyKnown())
	}
	if ch := c.setAdmin(reduceAdminRules(nil)); ch.adminResolved {
		t.Error("a second rules read reported a first resolution")
	}

	failed := newGovernanceCache()
	if ch := failed.setAdminFailed(true); !ch.adminResolved || !failed.adminPolicyKnown() {
		t.Errorf("a fatal policy error: adminResolved=%v known=%v, want both true (it fails closed)", ch.adminResolved, failed.adminPolicyKnown())
	}
}

func TestHandleGovernance_ServesWarmCache(t *testing.T) {
	h, _, _ := newTestHub()
	// Pre-seeded, so no bridge warm-up.
	h.config.SetGovernance(t.Context(), sampleGovernance())

	rec := httptest.NewRecorder()
	h.config.handleGovernance(rec, httptest.NewRequest(http.MethodGet, "/api/governance", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var got marotte.GovernanceStatePayload
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Known {
		t.Error("served snapshot Known = false, want true")
	}
	if !got.Features.MCPEnabled || !got.Features.WebToolsEnabled {
		t.Errorf("served features = %+v", got.Features)
	}
}

func TestCacheGovernanceFromUtility(t *testing.T) {
	h, _, _ := newTestHub()
	raw := json.RawMessage(`{"sessionId":"s","isEnterprise":false,"disabledReason":"org policy",` +
		`"features":{"mcpEnabled":false,"webToolsEnabled":true,"usageAnalytics":false,` +
		`"contentCollection":true,"promptLogging":false,"codeReferenceTracker":false,` +
		`"autonomousAgents":true}}`)

	h.config.cacheGovernanceFromUtility(raw)

	got, ok := h.config.governance.get()
	if !ok || !got.Known {
		t.Fatalf("cache not populated from utility copy: %+v ok=%v", got, ok)
	}
	if got.Features.MCPEnabled {
		t.Error("mcp_enabled should be false (disabled by org)")
	}
	if got.DisabledReason != "org policy" {
		t.Errorf("disabled_reason = %q, want 'org policy'", got.DisabledReason)
	}

	// An invalid copy is ignored.
	h.config.cacheGovernanceFromUtility(json.RawMessage("{"))
	if got2, _ := h.config.governance.get(); got2.DisabledReason != "org policy" {
		t.Error("invalid utility copy clobbered the cache")
	}
}
