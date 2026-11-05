package bridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// memSampleEvery is the engine memory sampler's period.
const memSampleEvery = 60 * time.Second

// memHighPct is the share of the effective heap limit at which RSS logs "engine memory high"; the Loki
// MarotteEngineMemoryHigh alert matches that message verbatim.
const memHighPct = 80

// procRoot is /proc; a var for fixtures.
var procRoot = "/proc"

// procMemory is the part of /proc/<pid>/status the sampler reads, in KiB: the resident set now and its high-water
// mark over the process's life.
type procMemory struct {
	rssKiB, hwmKiB int64
}

func readProcMemory(pid int) (procMemory, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return procMemory{}, false
	}
	return parseProcStatus(raw)
}

// parseProcStatus reads VmRSS and VmHWM out of a /proc/<pid>/status body ("VmRSS:\t  123 kB"). ok needs a well-formed
// VmRSS; a kernel thread has none. A malformed figure fails the whole read rather than reading as zero.
func parseProcStatus(raw []byte) (procMemory, bool) {
	var m procMemory
	haveRSS := false
	for line := range bytes.Lines(raw) {
		key, rest, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		var dst *int64
		switch string(key) {
		case "VmRSS":
			dst, haveRSS = &m.rssKiB, true
		case "VmHWM":
			dst = &m.hwmKiB
		default:
			continue
		}
		n, ok := parseKiB(rest)
		if !ok {
			return procMemory{}, false
		}
		*dst = n
	}
	return m, haveRSS
}

// parseKiB reads a /proc figure's value part, "\t  123 kB".
func parseKiB(rest []byte) (int64, bool) {
	f := strings.Fields(string(rest))
	if len(f) != 2 || f[1] != "kB" {
		return 0, false
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	return n, err == nil && n >= 0
}

// memWatch is one generation's sampler state, owned by its watchMemory goroutine.
type memWatch struct {
	// limitMiB is the effective heap limit, 0 while unknown.
	limitMiB int
	probed   bool
	warned   bool
}

// crossedHigh reports whether rssMiB is the generation's first sample at or above memHighPct of the heap limit.
func (w *memWatch) crossedHigh(rssMiB int) bool {
	if w.warned || w.limitMiB <= 0 || rssMiB*100 < w.limitMiB*memHighPct {
		return false
	}
	w.warned = true
	return true
}

// watchMemory samples the node engine's memory every memSampleEvery until the engine's teardown starts.
func (e *Engine) watchMemory() {
	var w memWatch
	tick := time.NewTicker(memSampleEvery)
	defer tick.Stop()
	for {
		if pid, ok := e.engineLeader(); ok {
			if m, ok := readProcMemory(pid); ok {
				e.resolveHeapLimit(&w, pid)
				e.recordMemory(&w, pid, m)
			}
		}
		select {
		case <-e.stopping:
			return
		case <-tick.C:
		}
	}
}

// recordMemory folds one sample into the generation's peak and logs the one high-memory line it may cross.
func (e *Engine) recordMemory(w *memWatch, pid int, m procMemory) {
	rss := int(m.rssKiB >> 10)
	e.mu.Lock()
	e.rssPeakMiB = max(e.rssPeakMiB, int(max(m.rssKiB, m.hwmKiB)>>10))
	e.heapLimitMiB = w.limitMiB
	peak := e.rssPeakMiB
	e.mu.Unlock()
	slog.Debug("engine memory", "generation", e.generation, "pid", pid,
		"rss_mib", rss, "rss_peak_mib", peak, "heap_limit_mib", w.limitMiB)
	if w.crossedHigh(rss) {
		slog.Warn("engine memory high", "generation", e.generation, "pid", pid,
			"rss_mib", rss, "heap_limit_mib", w.limitMiB, "threshold_pct", memHighPct)
	}
}

// engineLeader returns the node engine's pid: the leader of a noted group that is the kiro-cli process's own child.
// Groups are noted again when none qualifies, since the engine may not have spawned yet at the first look.
func (e *Engine) engineLeader() (int, bool) {
	if e.cmd == nil || e.cmd.Process == nil {
		return 0, false
	}
	cli := e.cmd.Process.Pid
	if pid, ok := e.groupLedBy(cli); ok {
		return pid, true
	}
	e.noteEngineGroups()
	return e.groupLedBy(cli)
}

func (e *Engine) groupLedBy(parent int) (int, bool) {
	e.mu.Lock()
	groups := slices.Clone(e.engineGroups)
	e.mu.Unlock()
	for _, g := range groups {
		if g.LedBy(parent) {
			return g.ID, true
		}
	}
	return 0, false
}

// resolveHeapLimit sets w's effective heap limit: the configured one, else V8's default for this engine, probed once
// per generation and cached across generations.
func (e *Engine) resolveHeapLimit(w *memWatch, pid int) {
	if e.heapMiB > 0 {
		w.limitMiB = e.heapMiB
		return
	}
	if w.probed {
		return
	}
	w.probed = true
	node, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	if err == nil {
		w.limitMiB, err = cachedHeapLimitMiB(e.lifecycleCtx, heapProbeKey{cli: e.cliPath, node: node, nodeOptions: nodeOptionsOf(e.cmd.Env), nodeArgs: strings.Join(e.nodeArgs, " ")}, e.nodeArgs, e.cmd.Env)
	}
	if err != nil {
		slog.Warn("engine: the default heap limit is unknown, so no high-memory line will be logged",
			"generation", e.generation, "error", err)
	}
}

func nodeOptionsOf(env []string) string {
	out := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, nodeOptionsVar+"="); ok {
			out = v
		}
	}
	return out
}

// heapProbeKey names what decides V8's default heap limit: the kiro-cli version, its node binary, NODE_OPTIONS and the
// launcher's node flags (the semi-space size counts toward the limit).
type heapProbeKey struct {
	cli, node, nodeOptions, nodeArgs string
}

var heapProbes = struct {
	m map[heapProbeKey]int
	sync.Mutex
}{m: map[heapProbeKey]int{}}

// heapProbeBudget bounds one probe; node prints the figure within a second.
const heapProbeBudget = 10 * time.Second

// cachedHeapLimitMiB returns the probed limit for key, probing under env on a miss. A failure is not cached.
func cachedHeapLimitMiB(ctx context.Context, key heapProbeKey, nodeArgs, env []string) (int, error) {
	heapProbes.Lock()
	n, ok := heapProbes.m[key]
	heapProbes.Unlock()
	if ok {
		return n, nil
	}
	n, err := probeHeapLimitMiB(ctx, key.node, nodeArgs, env)
	if err != nil {
		return 0, err
	}
	heapProbes.Lock()
	heapProbes.m[key] = n
	heapProbes.Unlock()
	return n, nil
}

// probeHeapLimitMiB runs node with nodeArgs under env and returns V8's heap_size_limit in MiB.
func probeHeapLimitMiB(ctx context.Context, node string, nodeArgs, env []string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, heapProbeBudget)
	defer cancel()
	args := append(slices.Clone(nodeArgs), "-p", "v8.getHeapStatistics().heap_size_limit")
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("probe %s: %w", node, err)
	}
	b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || b <= 0 {
		return 0, fmt.Errorf("probe %s printed %q, not a heap size", node, out)
	}
	return int(b >> 20), nil
}
