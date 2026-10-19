package agent

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The client renders a pause with one of marotte's reasons as marotte's own (static-src/run-store.ts stopSentence),
// reading the same fixture.
func TestPauseReasons_MatchTheClientFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/pause_reasons.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f struct {
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if want := []string{signInHoldReason, providerHoldReason, manualResumeReason}; !slices.Equal(f.Reasons, want) {
		t.Errorf("fixture reasons = %q, want %q (update testdata/pause_reasons.json and run-store.ts together)",
			f.Reasons, want)
	}
	for _, r := range []string{signInHoldReason, providerHoldReason} {
		if !isHoldReason(r) {
			t.Errorf("isHoldReason(%q) = false", r)
		}
	}
	// A run parked for the user is never resumed automatically.
	if isHoldReason(manualResumeReason) {
		t.Errorf("isHoldReason(%q) = true, want false", manualResumeReason)
	}
}
