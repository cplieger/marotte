package agent

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func newTestBridgeManager() *bridgeManager { return newBridgeManager() }

// removeIfSame removes only when the stored bridge is the given one.
func TestBridgeManager_RemoveIfSame(t *testing.T) {
	bm := newTestBridgeManager()
	sb1, _ := bm.orInsert("c1")
	sb2, _ := bm.orInsert("c2")

	if removed := bm.removeIfSame("c1", sb2); removed {
		t.Errorf("removeIfSame(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfSame(c1, other) wrongly removed c1")
	}

	if removed := bm.removeIfSame("c1", sb1); !removed {
		t.Errorf("removeIfSame(c1, c1) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfSame(c1, c1) did not remove c1")
	}
}

// removeIfBridge removes only when the stored session instance matches.
func TestBridgeManager_RemoveIfBridge(t *testing.T) {
	bm := newTestBridgeManager()
	sb, _ := bm.orInsert("c1")
	stored := newFakeBridge()
	sb.attach(stored)
	sb.setIdle()
	other := newFakeBridge()

	if removed := bm.removeIfBridge("c1", other); removed {
		t.Errorf("removeIfBridge(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfBridge(c1, other) wrongly removed c1")
	}

	if removed := bm.removeIfBridge("c1", stored); !removed {
		t.Errorf("removeIfBridge(c1, stored) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfBridge(c1, stored) did not remove c1")
	}
}

func BenchmarkBridgeManagerGetOrInsert(b *testing.B) {
	quietLogs(b)
	bm := newBridgeManager()

	// Pre-populated so the "exists" path is exercised.
	for i := range 100 {
		sb, existed := bm.orInsert(marotte.ChatID(fmt.Sprintf("chat-%d", i)))
		if !existed {
			sb.state = bridgeIdle
		}
	}

	b.Run("exists", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				bm.orInsert(marotte.ChatID(fmt.Sprintf("chat-%d", i%100)))
				i++
			}
		})
	})

	b.Run("create", func(b *testing.B) {
		// A separate manager so creates do not accumulate.
		bm2 := newBridgeManager()
		var mu sync.Mutex
		var counter int

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				mu.Lock()
				counter++
				id := fmt.Sprintf("new-%d", counter)
				mu.Unlock()

				sb, existed := bm2.orInsert(marotte.ChatID(id))
				if !existed {
					sb.state = bridgeIdle
				}
			}
		})
	})
}
