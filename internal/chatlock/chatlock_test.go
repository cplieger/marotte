package chatlock

import (
	"context"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// contenders reports the holder plus waiters on chatID's lock: a snapshot, stale on return.
func contenders(s *Set, chatID marotte.ChatID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.m[chatID]; e != nil {
		return e.refs
	}
	return 0
}

func TestSet_SerializesOneChatAndNotOthers(t *testing.T) {
	locks := New()
	unlock, err := locks.Lock(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Lock(c1): %v", err)
	}
	other, err := locks.Lock(t.Context(), "c2")
	if err != nil {
		t.Fatalf("Lock(c2) while c1 is held: %v", err)
	}
	other()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.Lock(ctx, "c1"); err == nil {
		t.Fatal("second Lock(c1) succeeded while the first was held")
	}
	acquired := make(chan func())
	go func() {
		u, err := locks.Lock(t.Context(), "c1")
		if err != nil {
			t.Errorf("waiting Lock(c1): %v", err)
		}
		acquired <- u
	}()
	deadline := time.Now().Add(5 * time.Second)
	for contenders(locks, "c1") != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Contenders(c1) = %d with a holder and a waiter, want 2", contenders(locks, "c1"))
		}
		time.Sleep(time.Millisecond)
	}
	unlock()
	select {
	case u := <-acquired:
		u()
	case <-time.After(5 * time.Second):
		t.Fatal("a waiter was not woken by the unlock")
	}
	if n := contenders(locks, "c1"); n != 0 {
		t.Errorf("Contenders(c1) = %d after every holder left, want 0", n)
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.m) != 0 {
		t.Errorf("map holds %d chats after every holder left, want 0", len(locks.m))
	}
}
