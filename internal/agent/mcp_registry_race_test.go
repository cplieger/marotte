package agent

import (
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestMCPRegistry_ConcurrentRecordClear races record and clearAll under -race, and checks
// Broadcast does not deadlock against clearAll.
func TestMCPRegistry_ConcurrentRecordClear(t *testing.T) {
	h, _, _ := newTestHub()
	reg := h.mcpRegistry

	// A nil mcpConfig attributes every name to OriginUser, so this exercises the lock.
	h.mcpConfig = nil

	const N = 100
	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range N {
			name := "server-" + string(rune('A'+i%10))
			reg.RecordConnected(h.lifecycle.shutdownCtx, name, marotte.MCPSource{}, nil, nil, nil, nil)
		}
	})

	wg.Go(func() {
		for i := range N {
			name := "server-" + string(rune('A'+i%10))
			reg.RecordInitFailure(h.lifecycle.shutdownCtx, name, marotte.MCPSource{}, "timeout")
		}
	})

	wg.Go(func() {
		for range N / 10 {
			reg.clearAll(h.lifecycle.shutdownCtx)
		}
	})

	wg.Go(func() {
		for range N {
			_ = reg.Snapshot()
		}
	})

	wg.Wait()
}
