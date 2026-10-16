package translate

import "testing"

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

// DecodeGovernanceState maps the wire shape to the domain payload and rejects
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
