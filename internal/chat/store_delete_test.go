package chat

import (
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A session a concurrent writer records while the chat is deleted is either in the chain the
// delete answers, so its teardown removes it, or refused: never recorded and missed.
func TestDelete_ASessionRecordedConcurrentlyIsInItsChainOrRefused(t *testing.T) {
	s, _ := newTestStore(t)
	for i := range 40 {
		chatID := marotte.ChatID("c" + strconv.Itoa(i))
		if _, err := s.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
			c.RecordSession("s0")
			return true
		}); err != nil {
			t.Fatalf("Setup: seed %s: %v", chatID, err)
		}

		var (
			mu       sync.Mutex
			recorded = []string{"s0"}
			wg       sync.WaitGroup
		)
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			for n := 1; ; n++ {
				sid := "s" + strconv.Itoa(n)
				version, err := s.Mutate(t.Context(), chatID, func(c *marotte.Chat, exists bool) bool {
					if !exists {
						return false
					}
					c.RecordSession(sid)
					return true
				})
				if err != nil || version == "" {
					if err != nil && !errors.Is(err, ErrTombstoned) {
						t.Errorf("Mutate(%s) record %s = %v, want success or ErrTombstoned", chatID, sid, err)
					}
					return
				}
				mu.Lock()
				recorded = append(recorded, sid)
				mu.Unlock()
			}
		})
		close(start)
		chain, err := s.Delete(t.Context(), chatID)
		wg.Wait()
		if err != nil {
			t.Fatalf("Delete(%s) = %v", chatID, err)
		}
		for _, sid := range recorded {
			if !slices.Contains(chain, sid) {
				t.Fatalf("Delete(%s) chain = %v, missing %s that a concurrent write recorded (recorded %v)", chatID, chain, sid, recorded)
			}
		}
	}
}
