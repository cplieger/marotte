package translate

import (
	"maps"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// fullFeatures is the 7-flag governance feature set as it appears on the wire
// (verified live on a Builder-ID login).
func fullFeatures() map[string]any {
	return map[string]any{
		"mcpEnabled":           true,
		"webToolsEnabled":      true,
		"usageAnalytics":       false,
		"contentCollection":    true,
		"promptLogging":        false,
		"codeReferenceTracker": false,
		"autonomousAgents":     true,
	}
}

func govMsg(t *testing.T, sessionID string, extra map[string]any) *marotte.RPCResponse {
	t.Helper()
	params := map[string]any{"sessionId": sessionID, "features": fullFeatures()}
	maps.Copy(params, extra)
	return &marotte.RPCResponse{Params: mustJSON(t, params)}
}

// TestHandleGovernanceState_CachesWithoutBroadcasting pins that the translator broadcasts
// nothing: a frame without the lock map would race the runtime's composed one.
func TestHandleGovernanceState_CachesWithoutBroadcasting(t *testing.T) {
	deps, events := newEventCaptureDeps()
	var cached *marotte.GovernanceStatePayload
	deps.onSetGovernance = func(g marotte.GovernanceStatePayload) { cp := g; cached = &cp }
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), marotte.ChatID("c1"),
		govMsg(t, "sess-parent", map[string]any{"isEnterprise": false}))

	if len(*events) != 0 {
		t.Fatalf("HandleGovernanceState broadcast %d events (%v), want 0: the cache owns the SSE", len(*events), eventTypes(*events))
	}
	if cached == nil {
		t.Fatal("SetGovernance was not called (state not cached)")
	}
	if !cached.Known || !cached.Features.MCPEnabled || !cached.Features.WebToolsEnabled || !cached.Features.AutonomousAgents {
		t.Errorf("cached payload = %+v, want Known and the on flags carried through", cached)
	}
	if cached.Features.CodeReferenceTracker || cached.Features.PromptLogging || cached.Features.UsageAnalytics || cached.IsEnterprise {
		t.Errorf("off flags leaked as on: %+v", cached)
	}
}

// A subagent-session copy (sessionId differs from the chat's parent) is skipped
// so the identical account-global flags aren't re-broadcast/cached redundantly.
func TestHandleGovernanceState_SubagentSkipped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	cached := false
	deps.onSetGovernance = func(marotte.GovernanceStatePayload) { cached = true }
	deps.parent = "sess-parent"
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), marotte.ChatID("c1"),
		govMsg(t, "sess-subagent", nil))

	if len(*events) != 0 || cached {
		t.Errorf("subagent copy should be skipped: events=%d cached=%v", len(*events), cached)
	}
}

// Malformed params are dropped without a broadcast or cache write.
func TestHandleGovernanceState_Malformed(t *testing.T) {
	deps, events := newEventCaptureDeps()
	cached := false
	deps.onSetGovernance = func(marotte.GovernanceStatePayload) { cached = true }
	tr := New(rolesOf(deps))

	tr.HandleGovernanceState(t.Context(), "c1", &marotte.RPCResponse{Params: []byte("{")})

	if len(*events) != 0 || cached {
		t.Errorf("malformed governance should be dropped: events=%d cached=%v", len(*events), cached)
	}
}

// DecodeGovernanceState (the exported decoder the runtime reuses for the utility
// bridge copy) maps the wire shape to the domain payload and rejects
// empty/invalid input.
func TestDecodeGovernanceState(t *testing.T) {
	raw := mustJSON(t, map[string]any{
		"sessionId":      "s",
		"isEnterprise":   true,
		"disabledReason": "org policy",
		"features":       fullFeatures(),
	})
	g, ok := DecodeGovernanceState(raw)
	if !ok {
		t.Fatal("DecodeGovernanceState returned !ok for valid input")
	}
	if !g.Known || !g.IsEnterprise || g.DisabledReason != "org policy" {
		t.Errorf("decoded = %+v", g)
	}
	if !g.Features.MCPEnabled || g.Features.PromptLogging {
		t.Errorf("features = %+v", g.Features)
	}

	if _, ok := DecodeGovernanceState(nil); ok {
		t.Error("empty params should decode to !ok")
	}
	if _, ok := DecodeGovernanceState([]byte("{")); ok {
		t.Error("malformed params should decode to !ok")
	}
}
