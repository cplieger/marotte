package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// pointCgroupAt points both cgroup probes at a fixture directory, writing v2 content when non-empty and v1 otherwise.
// An empty string means the file does not exist, which must read as unlimited.
func pointCgroupAt(t *testing.T, v2, v1 string) {
	t.Helper()
	dir := t.TempDir()
	set := func(name, content string) string {
		p := filepath.Join(dir, name)
		if content == "" {
			return p // deliberately absent
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
		return p
	}
	origV2, origV1 := cgroupMemMaxV2, cgroupMemMaxV1
	cgroupMemMaxV2 = set("memory.max", v2)
	cgroupMemMaxV1 = set("memory.limit_in_bytes", v1)
	t.Cleanup(func() { cgroupMemMaxV2, cgroupMemMaxV1 = origV2, origV1 })
}

// TestResolveChatFileCap pins which cgroup shapes give a cap or unlimited, and that the divisor and floor decide the
// number. The fixture can omit a file, since absence is an answer. Not parallel: it swaps the cgroup paths.
func TestResolveChatFileCap(t *testing.T) {
	cases := []struct {
		name string
		v2   string
		v1   string
		want chatFileCap
	}{
		// cgroup v2 with no limit set.
		{"v2 literal max is unlimited", "max", "", 0},
		{"v2 max with trailing newline", "max\n", "", 0},
		{"no cgroup file at all is unlimited", "", "", 0},
		// v1's no-limit sentinel near the top of int64; dividing it would invent a 288 PiB cap.
		{"v1 int64 sentinel is unlimited", "", "9223372036854771712", 0},
		{"v1 minus one is unlimited", "", "-1", 0},
		{"unparseable is unlimited", "", "not-a-number", 0},
		// Literal numbers, so changing the divisor cannot move both sides.
		{"v2 one gibibyte derives 32 MiB", "1073741824", "", 32 << 20},
		{"v1 one gibibyte derives the same", "", "1073741824", 32 << 20},
		{"v2 four gibibytes derive 128 MiB", "4294967296", "", 128 << 20},
		// Below 256 MiB the divisor would go under the floor.
		{"a small container gets the floor", "134217728", "", 8 << 20},
		{"a tiny container still gets the floor", "16777216", "", 8 << 20},
		// A readable v2 wins over a stale v1.
		{"v2 wins over v1", "1073741824", "16777216", 32 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointCgroupAt(t, tc.v2, tc.v1)
			if got := resolveChatFileCap(); got != tc.want {
				t.Errorf("resolveChatFileCap() = %d, want %d", got, tc.want)
			}
		})
	}
}
