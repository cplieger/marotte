package bridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/kasnode"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// pointProcAt points procRoot at a fixture tree holding one pid's status. Not parallel: it swaps a package path.
func pointProcAt(t *testing.T, pid int, status string) {
	t.Helper()
	root := t.TempDir()
	if status != "" {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("Setup: mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatalf("Setup: write status: %v", err)
		}
	}
	orig := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = orig })
}

// The fixtures follow proc(5)'s /proc/pid/status layout: "Name:\tvalue", memory figures as "<n> kB".
func TestReadProcMemory(t *testing.T) {
	const pid = 4242
	cases := []struct {
		name   string
		status string
		want   procMemory
		ok     bool
	}{
		{
			"node's status",
			"Name:\tnode\nState:\tS (sleeping)\nVmPeak:\t 9000000 kB\nVmHWM:\t 3600000 kB\nVmRSS:\t 3444436 kB\nThreads:\t11\n",
			procMemory{rssKiB: 3444436, hwmKiB: 3600000},
			true,
		},
		{"rss without a high-water mark", "Name:\tnode\nVmRSS:\t 1024 kB\n", procMemory{rssKiB: 1024}, true},
		{"a kernel thread has no VmRSS", "Name:\tkthreadd\nState:\tS (sleeping)\nThreads:\t1\n", procMemory{}, false},
		{"a figure with no unit", "VmRSS:\t1024\n", procMemory{}, false},
		{"a figure in another unit", "VmRSS:\t1024 MB\n", procMemory{}, false},
		{"a non-numeric figure", "VmRSS:\tlots kB\n", procMemory{}, false},
		{"a negative figure", "VmRSS:\t-5 kB\n", procMemory{}, false},
		{"a malformed high-water mark fails the read", "VmRSS:\t1024 kB\nVmHWM:\tx kB\n", procMemory{}, false},
		{"no status file", "", procMemory{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointProcAt(t, pid, tc.status)
			got, ok := readProcMemory(pid)
			if ok != tc.ok || got != tc.want {
				t.Errorf("readProcMemory = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func sampleMiB(rss, hwm int64) procMemory { return procMemory{rssKiB: rss << 10, hwmKiB: hwm << 10} }

// The high line fires at the first sample reaching 80% of the limit, once per generation (one Engine, one memWatch),
// and the exit record carries the generation's peak and its limit. Not parallel: it swaps the slog default.
func TestRecordMemory_WarnsOncePerGenerationAtTheThreshold(t *testing.T) {
	logs := captureLogs(t)
	first := NewEngine("/nonexistent", "/work", WithGeneration(1))
	w := memWatch{limitMiB: 1000}
	first.recordMemory(&w, 7, sampleMiB(799, 799))
	if strings.Contains(logs.String(), "engine memory high") {
		t.Fatalf("a sample at 79.9%% of the limit logged the high line:\n%s", logs)
	}
	first.recordMemory(&w, 7, sampleMiB(800, 1200))
	first.recordMemory(&w, 7, sampleMiB(950, 950))
	second := NewEngine("/nonexistent", "/work", WithGeneration(2))
	second.recordMemory(&memWatch{limitMiB: 1000}, 8, sampleMiB(850, 850))

	var high []string
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `msg="engine memory high"`) {
			high = append(high, line)
		}
	}
	if len(high) != 2 || !strings.Contains(high[0], "generation=1") || !strings.Contains(high[1], "generation=2") {
		t.Errorf("high lines = %q, want one for generation 1 then one for generation 2", high)
	}
	if len(high) > 0 && (!strings.Contains(high[0], "rss_mib=800") || !strings.Contains(high[0], "heap_limit_mib=1000")) {
		t.Errorf("generation 1's high line = %q, want rss_mib=800 heap_limit_mib=1000", high[0])
	}
	if got := first.Exit(); got.RSSPeakMiB != 1200 || got.HeapLimitMiB != 1000 {
		t.Errorf("Exit() peak, limit = %d, %d MiB; want 1200 (the VmHWM seen), 1000", got.RSSPeakMiB, got.HeapLimitMiB)
	}
}

// With no known limit there is no threshold, so no sample is high.
func TestRecordMemory_AnUnknownLimitNeverWarns(t *testing.T) {
	logs := captureLogs(t)
	e := NewEngine("/nonexistent", "/work")
	e.recordMemory(&memWatch{}, 7, sampleMiB(1<<20, 1<<20))
	if strings.Contains(logs.String(), "engine memory high") {
		t.Errorf("a sample with no heap limit logged the high line:\n%s", logs)
	}
}

// memLeaderFake is a kiro-cli that forks one child leading its own group, as the node engine does, and answers
// initialize. The child ends with its parent.
func memLeaderFake(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not on PATH")
	}
	script := `#!/bin/sh
setsid sh -c 'while kill -0 $PPID 2>/dev/null; do sleep 0.05; done' &
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  if [ -n "$id" ]; then
    printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id"
  fi
done
`
	path := filepath.Join(dir, "mem-leader-kiro-cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("Setup: write fake: %v", err)
	}
	return path
}

// A started engine samples its own child group's leader: the exit record carries the configured limit once a sample
// landed, which needs the leader found and its status read.
func TestWatchMemory_SamplesTheSidecarsGroupLeader(t *testing.T) {
	dir := t.TempDir()
	e := NewEngine(memLeaderFake(t, dir), dir)
	t.Cleanup(e.Stop)
	if err := e.Start(t.Context(), &marotte.EngineOpts{Lifetime: context.Background(), HeapMiB: 4096}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.Exit().HeapLimitMiB != 4096 {
		if time.Now().After(deadline) {
			t.Fatalf("Exit() = %+v after 5 s, want HeapLimitMiB 4096 from a sample of the child group's leader", e.Exit())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := e.Exit().RSSPeakMiB; got <= 0 {
		t.Errorf("RSSPeakMiB = %d after a sample, want the leader's VmHWM, above 0", got)
	}
}

// kasNode is the node binary kiro-cli unpacks for KAS, or "" when it is not installed here.
func kasNode() string {
	p := workspace.KASLookupFromEnv().Find(os.Getenv("KIRO_CLI_VERSION")).Node
	if st, err := os.Stat(p); p == "" || err != nil || st.Mode()&0o111 == 0 {
		return ""
	}
	return p
}

// V8 reports a heap limit of --max-old-space-size plus its 48 MiB young-generation allowance.
func TestProbeHeapLimitMiB_ReadsTheNodeUnderTheComposedOptions(t *testing.T) {
	node := kasNode()
	if node == "" {
		t.Skip("the KAS node binary is not installed here")
	}
	got, err := probeHeapLimitMiB(t.Context(), node, nil, []string{nodeOptionsVar + "=--max-old-space-size=2048"})
	if err != nil || got != 2048+48 {
		t.Errorf("probeHeapLimitMiB = %d, %v; want %d", got, err, 2048+48)
	}
	// The young generation's allowance is three semi-spaces, so the launcher's 64 MiB one makes it 192.
	got, err = probeHeapLimitMiB(t.Context(), node, kasnode.Flags, []string{nodeOptionsVar + "=--max-old-space-size=2048"})
	if err != nil || got != 2048+3*64 {
		t.Errorf("probeHeapLimitMiB(the launcher's flags) = %d, %v; want %d", got, err, 2048+3*64)
	}
	if _, err := probeHeapLimitMiB(t.Context(), "/bin/true", nil, nil); err == nil {
		t.Error("probeHeapLimitMiB(a binary printing nothing) = nil error, want a refusal")
	}
}
