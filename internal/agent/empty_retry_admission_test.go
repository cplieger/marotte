package agent

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// The empty turn's close starts its drain, which holds the chat's slot a moment while it looks for a queued prompt;
// the empty turn's retry waits that out rather than reading it as another turn admitted.
func TestEmptyTurnRetry_WaitsOutTheClosesDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		answerFirst := make(chan struct{})
		var (
			mu     sync.Mutex
			minted []*fakeBridge
		)
		factory := func() engineSession {
			mu.Lock()
			defer mu.Unlock()
			br := newFakeBridge()
			if len(minted) == 0 {
				br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: answerFirst}
			}
			minted = append(minted, br)
			return br
		}
		cs := newTestChatStore()
		h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(factory, nil), cs)
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		settleBoot(t, h)
		seedChat(t, cs, "c1")
		if code := postPrompt(t, h, "tidy"); code != http.StatusOK {
			t.Fatalf("Setup: prompt = %d", code)
		}
		synctest.Wait()
		mu.Lock()
		first := minted[0]
		mu.Unlock()
		first.deliver(newTurnStartMsg())
		first.deliver(newTurnEndMsg("end_turn"))
		// A holder that opens no turn, as the close's drain is while it looks for a queued prompt, taking the slot the
		// moment the empty turn's prompt lets it go.
		held := make(chan struct{})
		go func() {
			res, ok := h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp())
			for !ok {
				runtime.Gosched()
				res, ok = h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp())
			}
			close(held)
			time.Sleep(50 * time.Millisecond)
			h.coord.ReleaseTurnReservation(res)
		}()
		close(answerFirst)
		<-held
		time.Sleep(time.Second)
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()
		if len(minted) != 2 {
			t.Errorf("sessions = %d, want the empty turn's retry on a fresh one", len(minted))
		}
	})
}
