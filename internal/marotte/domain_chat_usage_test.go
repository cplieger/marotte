package marotte

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUsage_DropsAPersistedTruncationThreshold pins that the truncation threshold is
// gone end to end: a chat.json written by an older build still carries the key, and
// the next header rewrite must drop it while keeping the summarization threshold.
func TestUsage_DropsAPersistedTruncationThreshold(t *testing.T) {
	var u Usage
	old := `{"context_pct":40,"summarization_threshold_pct":80,"truncation_threshold_pct":95}`
	if err := json.Unmarshal([]byte(old), &u); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("encode usage: %v", err)
	}
	if strings.Contains(string(out), "truncation_threshold_pct") {
		t.Errorf("re-encoded usage still carries the truncation threshold: %s", out)
	}
	if !strings.Contains(string(out), `"summarization_threshold_pct":80`) {
		t.Errorf("re-encoded usage lost the summarization threshold: %s", out)
	}
}
