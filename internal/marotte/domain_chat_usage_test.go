package marotte

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUsage_DropsPersistedThresholds pins that neither threshold survives in a chat record: a chat.json written by an
// older build still carries both keys, and the next header rewrite must drop them.
func TestUsage_DropsPersistedThresholds(t *testing.T) {
	var u Usage
	old := `{"context_pct":40,"summarization_threshold_pct":80,"truncation_threshold_pct":95}`
	if err := json.Unmarshal([]byte(old), &u); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("encode usage: %v", err)
	}
	for _, key := range []string{"summarization_threshold_pct", "truncation_threshold_pct"} {
		if strings.Contains(string(out), key) {
			t.Errorf("re-encoded usage still carries %s: %s", key, out)
		}
	}
	if !strings.Contains(string(out), `"context_pct":40`) {
		t.Errorf("re-encoded usage lost the context reading: %s", out)
	}
}

// A chat spans several KAS sessions: a retired session's total stays in the figure when the next reports from zero.
func TestRecordSession_FoldsTheSessionTotalIntoTheOffset(t *testing.T) {
	var c Chat
	c.RecordSession("s1")
	c.SetSessionCredits("s1", 2, false)
	c.RecordSession("s2")
	c.SetSessionCredits("s2", 1, false)
	if c.Usage.Credits != 3 {
		t.Errorf("Usage.Credits = %v, want s1's 2 plus s2's 1", c.Usage.Credits)
	}
}

// Subtracting a total and adding it back must read as no spend, not as a sub-cent one.
func TestResetSpend_LeavesNoFloatResidue(t *testing.T) {
	// Runtime addition: Go folds a constant 0.1+0.2 to exactly 0.3.
	tenth, fifth := 0.1, 0.2
	var c Chat
	c.RecordSession("s1")
	c.SetSessionCredits("s1", 0.3, false)
	c.ResetSpend()
	c.SetSessionCredits("s1", tenth+fifth, false)
	if c.Usage.Credits != 0 {
		t.Errorf("Usage.Credits = %v, want exactly 0", c.Usage.Credits)
	}
}
