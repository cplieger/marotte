package settings

import "testing"

// The user's decision: on by default, so an install that never touched the switch gets the optimizations, and only a
// stored false turns them off.
func TestHighThroughput_OnUnlessStoredOff(t *testing.T) {
	for _, tc := range []struct {
		name, stored string
		want         bool
	}{
		{"absent", `{}`, true},
		{"stored on", `{"high_throughput_optimizations":true}`, true},
		{"stored off", `{"high_throughput_optimizations":false}`, false},
		{"a wrong type", `{"high_throughput_optimizations":"no"}`, true},
		{"an unreadable file", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSettings(t, dir, []byte(tc.stored))
			if got := HighThroughput(t.Context(), dir); got != tc.want {
				t.Errorf("HighThroughput(%s) = %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
	if !EffectiveDefaults().HighThroughput {
		t.Error("EffectiveDefaults().HighThroughput = false, want on: the client renders the switch from it")
	}
}
