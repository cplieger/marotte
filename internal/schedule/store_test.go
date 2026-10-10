package schedule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewStore_ReadsLegacyLastResult pins that a schedules.json written while the
// outcome was one prefix-coded string still shows each row's last outcome, and
// that the next write stores the status and reason fields only.
func TestNewStore_ReadsLegacyLastResult(t *testing.T) {
	const orphan = "The run stopped because the server restarted while it was running. Run it again, or wait for the next slot"
	legacy := `[
  {"id":"a","source":"bundled://x","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"started"},
  {"id":"b","source":"bundled://x","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"failed: needed approval for fs_write with nobody watching. Add a permission rule to allow it"},
  {"id":"c","source":"bundled://x","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"unknown: no terminal signal was seen and the run stayed absent for 6 hours"},
  {"id":"d","source":"bundled://x","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"` + orphan + `"},
  {"id":"e","source":"bundled://x","spec":{"freq":"daily","hour":1,"minute":0},"enabled":true,"anchor":"2026-08-04T01:00:00Z","last_result":"stopped: the server restarted while it was running — run it again, or wait for the next slot"}
]`
	dir := t.TempDir()
	if err := writeFile(dir, legacy); err != nil {
		t.Fatalf("seed: %v", err)
	}
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	want := map[string]Outcome{
		"a": {Status: statusStarted},
		"b": {Status: StatusFailed, Reason: "needed approval for fs_write with nobody watching. Add a permission rule to allow it"},
		"c": {Status: StatusUnknown, Reason: "no terminal signal was seen and the run stayed absent for 6 hours"},
		"d": {Status: StatusFailed, Reason: orphan},
		"e": {Status: StatusFailed, Reason: "stopped because the server restarted while it was running — run it again, or wait for the next slot"},
	}
	if got := len(st.List()); got != len(want) {
		t.Fatalf("NewStore(legacy) kept %d rows, want %d", got, len(want))
	}
	for _, e := range st.List() {
		got := Outcome{Status: e.LastStatus, Reason: e.LastReason}
		if got != want[e.ID] {
			t.Errorf("NewStore(legacy) entry %q outcome = %+v, want %+v", e.ID, got, want[e.ID])
		}
	}

	extra := Entry{ID: "z", Source: "bundled://x", Spec: Spec{Freq: FreqDaily, Hour: 3}, Enabled: true}
	if err := st.Put(t.Context(), &extra); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "last_result") {
		t.Errorf("rewritten store still carries last_result:\n%s", raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("decode rewritten store: %v", err)
	}
	for _, row := range rows {
		if row["id"] == "b" && (row["last_status"] != "failed" || row["last_reason"] == nil) {
			t.Errorf("rewritten row b = %v, want last_status failed and a last_reason", row)
		}
	}
}
