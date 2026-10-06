package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// newTestBridgeManager builds a bridgeManager whose factory returns a fresh fakeBridge.
func newTestBridgeManager() *bridgeManager {
	return newBridgeManager(func() ACPBridge { return newFakeBridge() })
}

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

// removeIfBridge removes only when the stored bridge instance matches.
func TestBridgeManager_RemoveIfBridge(t *testing.T) {
	bm := newTestBridgeManager()
	sb, _ := bm.orInsert("c1")
	stored := sb.bridge
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
	factory := func() ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

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
		bm2 := newBridgeManager(factory)
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

func TestRetireBridges_ClosesIdleChatBridges(t *testing.T) {
	h, cs, br := newTestHub()
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	h.RetireBridges("identity changed")

	if h.bridge.mgr.get("c1") != nil {
		t.Error("RetireBridges kept an idle chat bridge registered")
	}
	br.mu.Lock()
	stopped := br.stopped
	br.mu.Unlock()
	if !stopped {
		t.Error("RetireBridges did not stop the idle chat bridge")
	}
}

func TestRetireBridges_MarksBusyBridgeAndReplacesItAtNextOpen(t *testing.T) {
	cs := newTestChatStore()
	var made []*fakeBridge
	h := New(t.Context(), "/tmp/retire-busy", func() ACPBridge {
		br := newFakeBridge()
		made = append(made, br)
		return br
	}, cs)
	cs.wire(h)
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
	first, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if !first.tryAcquireForPrompt() {
		t.Fatal("first bridge did not enter the prompting state")
	}

	h.RetireBridges("identity changed")
	if h.bridge.mgr.get("c1") != first {
		t.Fatal("RetireBridges removed a busy bridge before its turn ended")
	}
	made[0].mu.Lock()
	stoppedDuringTurn := made[0].stopped
	made[0].mu.Unlock()
	if stoppedDuringTurn {
		t.Fatal("RetireBridges stopped a busy bridge mid-turn")
	}

	first.releaseAfterPrompt()
	second, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge after retirement: %v", err)
	}
	if second == first {
		t.Error("OpenBridge reused a bridge marked for retirement")
	}
	made[0].mu.Lock()
	stoppedAfterTurn := made[0].stopped
	made[0].mu.Unlock()
	if !stoppedAfterTurn {
		t.Error("OpenBridge did not stop the retired bridge before replacing it")
	}
}

func TestRetireBridges_LeavesRunBridgesUntouched(t *testing.T) {
	h, _, _ := newTestHub()
	br := newFakeBridge()
	sb := &sharedBridge{bridge: br, state: bridgeIdle}
	if !h.bridge.mgr.insert(runChatID("wf-1"), sb) {
		t.Fatal("insert run bridge = false, want true")
	}

	h.RetireBridges("identity changed")

	if h.bridge.mgr.get(runChatID("wf-1")) != sb {
		t.Error("RetireBridges removed a run bridge")
	}
	br.mu.Lock()
	stopped := br.stopped
	br.mu.Unlock()
	if stopped {
		t.Error("RetireBridges stopped a run bridge")
	}
}

func TestOpenBridge_ChecksIdentityBeforeReuse(t *testing.T) {
	h, cs, _ := newTestHub()
	checks := 0
	h.SetIdentityCheck(func(context.Context) { checks++ })
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge reuse: %v", err)
	}

	if checks != 2 {
		t.Errorf("OpenBridge identity checks = %d, want 2", checks)
	}
}

func TestRetireBridges_ResetsUtilitySession(t *testing.T) {
	h, _, _ := newTestHub()
	if _, err := h.UtilityPrompt(t.Context(), "warm", ""); err != nil {
		t.Fatalf("UtilityPrompt: %v", err)
	}
	if h.utility.peek() == nil {
		t.Fatal("utility runtime was not built")
	}

	h.RetireBridges("identity changed")

	if h.utility.peek() != nil {
		t.Error("RetireBridges kept the utility runtime")
	}
}
