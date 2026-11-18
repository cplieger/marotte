package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// Cost is one call's heap cost: the allocations it makes and the bytes they hold.
type Cost struct {
	Allocs uint64
	Bytes  uint64
}

// CostSlack is how far a measured cost may stray from its budget, either way: it absorbs map growth, and nothing a
// change of shape could hide in. A drop past it fails too, so a change that lowers a cost lowers its budget in the
// same diff.
const CostSlack = 0.05

// RequireCostBuild skips t outside the -race build. A budget holds that build's costs because it is the one CI tests;
// the race instrumentation moves every count by about half again, so one table cannot hold both builds within
// CostSlack, and a table CI never checked would go stale unseen.
func RequireCostBuild(t testing.TB) {
	t.Helper()
	if !raceBuild {
		t.Skip("cost budgets hold the -race build's costs: run go test -race")
	}
}

// MeasureCost is f's heap cost per call, on one P as testing.AllocsPerRun runs, read from runtime.MemStats because
// AllocsPerRun reports no bytes: the mean of the cheaper half of n calls after a warm-up call. Noise only adds to a
// call, at random: the -race build's sync.Pool drops a quarter of what it is given (sync/pool.go, Put), so a call now
// and then regrows a large pooled buffer, and a plain mean moves with how many did. The collector stays off while f
// runs and collects only between calls, because a collection empties every pool at a point the clock picks.
func MeasureCost(n int, f func()) Cost {
	return MeasureCostAfter(n, func() {}, f)
}

// MeasureCostAfter is MeasureCost of f with prepare run unmeasured before each call, for an operation whose input a
// call consumes, such as a read that caches what it answers until the next change.
func MeasureCostAfter(n int, prepare, f func()) Cost {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	prepare()
	f()
	allocs := make([]uint64, n)
	bytes := make([]uint64, n)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	collected := before.TotalAlloc
	for i := range n {
		prepare()
		runtime.ReadMemStats(&before)
		// One collection between two calls leaves the pools' victims for the next call to take.
		if before.TotalAlloc-collected > costCollectEvery {
			runtime.GC()
			runtime.ReadMemStats(&before)
			collected = before.TotalAlloc
		}
		f()
		runtime.ReadMemStats(&after)
		allocs[i] = after.Mallocs - before.Mallocs
		bytes[i] = after.TotalAlloc - before.TotalAlloc
	}
	return Cost{Allocs: cheaperHalfMean(allocs), Bytes: cheaperHalfMean(bytes)}
}

func cheaperHalfMean(xs []uint64) uint64 {
	slices.Sort(xs)
	half := xs[:(len(xs)+1)/2]
	var sum uint64
	for _, x := range half {
		sum += x
	}
	n := uint64(len(half))
	return (sum + n/2) / n
}

// costCollectEvery bounds the heap a measurement grows with the collector off.
const costCollectEvery = 256 << 20

// FixedLengthDir is a fresh directory whose absolute path is always 192 bytes. t.TempDir's name ends in a random
// number of varying length, so an answer that carries a path would cost a few bytes more or less from run to run,
// and a buffer that grows by doubling turns those into a different allocation.
func FixedLengthDir(t testing.TB) string {
	t.Helper()
	base := t.TempDir()
	const length = 192
	pad := length - len(base) - 1
	if pad < 1 {
		t.Fatalf("FixedLengthDir: the temporary directory %s is %d bytes, over %d", base, len(base), length-2)
	}
	dir := filepath.Join(base, strings.Repeat("d", pad))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("FixedLengthDir: %v", err)
	}
	return dir
}

// HoldCost fails t when got strays from want by more than CostSlack on either count. op names the budget's entry, so
// the message says which number in the package's table to rewrite; -v logs every entry's measured cost.
func HoldCost(t testing.TB, op string, want, got Cost) {
	t.Helper()
	t.Logf("%q: {Allocs: %d, Bytes: %d},", op, got.Allocs, got.Bytes)
	if !within(got.Allocs, want.Allocs) || !within(got.Bytes, want.Bytes) {
		t.Errorf("%s costs %d allocs and %d B per call; its budget is %d allocs and %d B, within %.0f%%: "+
			"a cost that moved by design rewrites its entry", op, got.Allocs, got.Bytes, want.Allocs, want.Bytes,
			CostSlack*100)
	}
}

func within(got, want uint64) bool {
	slack := float64(want) * CostSlack
	return float64(got) >= float64(want)-slack && float64(got) <= float64(want)+slack
}

// CostRuns is the n MeasureCost takes at a fixture scale: n1 at x1, n10 at x10, where a call costs about ten times as
// much and needs fewer calls for its mean to settle.
func CostRuns(scale, n1, n10 int) int {
	if scale == 10 {
		return n10
	}
	return n1
}
