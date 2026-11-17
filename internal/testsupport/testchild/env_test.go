package testchild

import (
	"os"
	"slices"
	"testing"
)

func TestEnv_EndsWithExtraThenTheRaceSetting(t *testing.T) {
	cases := []struct {
		name      string
		inherited string // "" leaves GORACE unset
		wantRace  string
	}{
		{"no inherited options", "", "GORACE=atexit_sleep_ms=0"},
		{"inherited options kept first", "halt_on_error=1 log_path=/tmp/race", "GORACE=halt_on_error=1 log_path=/tmp/race atexit_sleep_ms=0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GORACE", tc.inherited)
			if tc.inherited == "" {
				if err := os.Unsetenv("GORACE"); err != nil {
					t.Fatal(err)
				}
			}
			base := os.Environ()
			want := append(slices.Clone(base), "A=1", "B=2")
			if wantRaceLine {
				want = append(want, tc.wantRace)
			}
			if got := Env("A=1", "B=2"); !slices.Equal(got, want) {
				t.Errorf("Env(A=1, B=2) has %d entries ending %q, want %d ending %q (race build %v)",
					len(got), got[min(len(base), len(got)):], len(want), want[len(base):], wantRaceLine)
			}
		})
	}
}
