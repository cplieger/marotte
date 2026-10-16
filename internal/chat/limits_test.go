package chat

import "testing"

// TestResolveChatFileCap pins that the divisor and the floor decide the number, and that no limit means no cap. The
// cgroup parsing itself is memlimit's. Not parallel: it swaps the limit source.
func TestResolveChatFileCap(t *testing.T) {
	cases := []struct {
		name  string
		limit int64
		want  chatFileCap
	}{
		{"no limit is uncapped", 0, 0},
		// Literal numbers, so changing the divisor cannot move both sides.
		{"one gibibyte derives 32 MiB", 1 << 30, 32 << 20},
		{"four gibibytes derive 128 MiB", 4 << 30, 128 << 20},
		// Below 256 MiB the divisor would go under the floor.
		{"a small container gets the floor", 128 << 20, 8 << 20},
		{"a tiny container still gets the floor", 16 << 20, 8 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := containerMemLimit
			containerMemLimit = func() (int64, string) { return tc.limit, "fixture" }
			t.Cleanup(func() { containerMemLimit = orig })
			if got := resolveChatFileCap(); got != tc.want {
				t.Errorf("resolveChatFileCap() = %d, want %d", got, tc.want)
			}
		})
	}
}
