package agent

import (
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestBridgeManager_ConcurrentGetOrInsertClose races orInsert against close on one chatID under -race.
func TestBridgeManager_ConcurrentGetOrInsertClose(t *testing.T) {
	factory := func() ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

	const N = 100
	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range N {
			chatID := marotte.ChatID("chat-" + string(rune('A'+i%5)))
			bm.orInsert(chatID)
		}
	})

	wg.Go(func() {
		for i := range N {
			chatID := marotte.ChatID("chat-" + string(rune('A'+i%5)))
			bm.close(chatID)
		}
	})

	wg.Go(func() {
		for i := range N {
			chatID := marotte.ChatID("chat-" + string(rune('A'+i%5)))
			_ = bm.get(chatID)
		}
	})

	wg.Go(func() {
		for range N {
			_ = bm.count()
		}
	})

	wg.Wait()
}

// TestBridgeManager_CloseConcurrentDrain races per-chat close against drain (a tab close during Shutdown).
func TestBridgeManager_CloseConcurrentDrain(t *testing.T) {
	factory := func() ACPBridge { return newNoopBridge() }
	bm := newBridgeManager(factory)

	for i := range 20 {
		chatID := marotte.ChatID("drain-" + string(rune('A'+i)))
		bm.orInsert(chatID)
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		for _, id := range []marotte.ChatID{"drain-A", "drain-B", "drain-C"} {
			bm.close(id)
		}
	})

	wg.Go(func() {
		_ = bm.drain()
	})

	wg.Wait()
}
