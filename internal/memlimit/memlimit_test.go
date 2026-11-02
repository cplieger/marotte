package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture writes content under name in a fresh directory and returns its path; empty content leaves the file absent.
func fixture(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if content == "" {
		return p
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("Setup: write %s: %v", name, err)
	}
	return p
}

// pointCgroupAt points both limit probes at fixtures. Not parallel: it swaps package paths.
func pointCgroupAt(t *testing.T, v2, v1 string) {
	t.Helper()
	origV2, origV1 := cgroupMemMaxV2, cgroupMemMaxV1
	cgroupMemMaxV2 = fixture(t, "memory.max", v2)
	cgroupMemMaxV1 = fixture(t, "memory.limit_in_bytes", v1)
	t.Cleanup(func() { cgroupMemMaxV2, cgroupMemMaxV1 = origV2, origV1 })
}

func TestLimit(t *testing.T) {
	cases := []struct {
		name string
		v2   string
		v1   string
		want int64
	}{
		{"v2 literal max is unlimited", "max", "", 0},
		{"v2 max with trailing newline", "max\n", "", 0},
		{"no cgroup file at all is unlimited", "", "", 0},
		// v1's no-limit sentinel near the top of int64.
		{"v1 int64 sentinel is unlimited", "", "9223372036854771712", 0},
		{"v1 minus one is unlimited", "", "-1", 0},
		{"unparseable is unlimited", "", "not-a-number", 0},
		{"v2 one gibibyte", "1073741824\n", "", 1 << 30},
		{"v1 one gibibyte", "", "1073741824", 1 << 30},
		{"v2 wins over v1", "1073741824", "16777216", 1 << 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointCgroupAt(t, tc.v2, tc.v1)
			if got, _ := Limit(); got != tc.want {
				t.Errorf("Limit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLimit_SignalNamesTheFileThatDecided(t *testing.T) {
	pointCgroupAt(t, "", "1073741824")
	if _, signal := Limit(); signal != "cgroup v1 "+cgroupMemMaxV1 {
		t.Errorf("signal = %q, want the v1 path", signal)
	}
	pointCgroupAt(t, "", "")
	if _, signal := Limit(); signal != "no cgroup memory file readable" {
		t.Errorf("signal = %q, want the unreadable signal", signal)
	}
}

func pointEventsAt(t *testing.T, content string) {
	t.Helper()
	orig := cgroupMemEvents
	cgroupMemEvents = fixture(t, "memory.events", content)
	t.Cleanup(func() { cgroupMemEvents = orig })
}

func TestOOMKills_ReadsTheCounter(t *testing.T) {
	pointEventsAt(t, "low 0\nhigh 0\nmax 12\noom 4\noom_kill 3\noom_group_kill 0\n")
	got, ok := OOMKills()
	if !ok || got != 3 {
		t.Errorf("OOMKills() = %d, %v, want 3, true", got, ok)
	}
}

func TestOOMKills_AbsentOnCgroupV1(t *testing.T) {
	pointEventsAt(t, "")
	if got, ok := OOMKills(); ok || got != 0 {
		t.Errorf("OOMKills() = %d, %v, want 0, false on a missing file", got, ok)
	}
}

func TestOOMKills_AMalformedCounterIsUnreadable(t *testing.T) {
	cases := map[string]string{
		"no oom_kill line":    "oom 1\n",
		"non-numeric counter": "oom_kill many\n",
		"negative counter":    "oom_kill -1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			pointEventsAt(t, content)
			if _, ok := OOMKills(); ok {
				t.Error("OOMKills() ok = true, want false")
			}
		})
	}
}
