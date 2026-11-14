package kirorelease

import (
	"slices"
	"testing"
)

func TestSpawnEnv_TurnsSelfUpdateOffWithoutTouchingTheInput(t *testing.T) {
	in := make([]string, 1, 4)
	in[0] = NoAutoUpdateVar + "=0"
	got := SpawnEnv(in)
	if want := []string{NoAutoUpdateVar + "=0", NoAutoUpdateVar + "=1"}; !slices.Equal(got, want) {
		t.Errorf("SpawnEnv = %q, want %q: the last value is the one a child sees", got, want)
	}
	if spare := in[:2]; spare[1] != "" {
		t.Errorf("SpawnEnv wrote %q into the caller's spare capacity", spare[1])
	}
	if got := SpawnEnv(nil); !slices.Equal(got, []string{NoAutoUpdateVar + "=1"}) {
		t.Errorf("SpawnEnv(nil) = %q, want only the switch", got)
	}
}
