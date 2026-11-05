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
