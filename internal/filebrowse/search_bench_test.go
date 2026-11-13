package filebrowse

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// heapProbe reads the live heap at the first level-1 directory's leave: the root's listing is
// garbage by then and the queue holds every other level-1 directory.
type heapProbe struct {
	*nameVisitor
	live    uint64
	sampled bool
}

func (p *heapProbe) leave(d *walkDir) {
	if d.depth == 1 && !p.sampled {
		p.sampled = true
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		p.live = ms.HeapAlloc
	}
}

// BenchmarkSearchWalk_QueuedDirectoryHeap reports the live heap one queued directory costs, on a
// root as deep as a workspace's and as wide as the names directory budget.
func BenchmarkSearchWalk_QueuedDirectoryHeap(b *testing.B) {
	root := filepath.Join(b.TempDir(), "workspace", "project", "packages", "client", "src", "components")
	for i := range maxNameSearchDirs {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("component-%05d", i)), 0o700); err != nil {
			b.Fatal(err)
		}
	}
	h, err := New(Sensitive{}, []string{root})
	if err != nil {
		b.Fatal(err)
	}
	var perDir float64
	b.ReportAllocs()
	for b.Loop() {
		p := &heapProbe{nameVisitor: newNameVisitor(mustQuery(b, "needle"))}
		walk := newSearchWalk(b.Context(), p, budgetFor(modeNames, false))
		walk.ignoreRules = true
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		walk.walkRoots([]loc{{m: &h.mounts[0], abs: root}})
		if !p.sampled {
			b.Fatal("the walk never left a level-1 directory")
		}
		perDir = float64(p.live-ms.HeapAlloc) / float64(maxNameSearchDirs-1)
	}
	b.ReportMetric(perDir, "B/queued-dir")
	b.ReportMetric(perDir*float64(budgetFor(modeNames, true).dirs)/(1<<20), "MiB/deep-budget")
}
