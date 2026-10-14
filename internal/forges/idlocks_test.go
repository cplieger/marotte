package forges

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func heldIDs(l *idLocks) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held)
}

func TestIDLocks_FreesAnEntryAtZeroHolders(t *testing.T) {
	var l idLocks
	for i := range 50 {
		unlock, err := l.lock(t.Context(), fmt.Sprint("id-", i))
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	}
	if n := heldIDs(&l); n != 0 {
		t.Errorf("after lock and unlock of 50 ids the registry holds %d entries, want none", n)
	}

	unlock, err := l.lock(t.Context(), "x")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.lock(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a waiter whose context ended = %v, want its error", err)
	}
	unlock()
	if n := heldIDs(&l); n != 0 {
		t.Errorf("after a cancelled waiter the registry holds %d entries, want none", n)
	}
}

// A holder releasing while another waits hands the lock to the waiter, and no
// third caller holds it beside the waiter.
func TestIDLocks_AWaiterKeepsTheEntry(t *testing.T) {
	var l idLocks
	first, err := l.lock(t.Context(), "x")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func())
	go func() {
		unlock, err := l.lock(t.Context(), "x")
		if err != nil {
			t.Error(err)
		}
		acquired <- unlock
	}()
	waitFor(t, "the second caller to wait", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.held["x"] != nil && l.held["x"].refs == 2
	})
	first()
	second := <-acquired
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if unlock, err := l.lock(ctx, "x"); err == nil {
		unlock()
		t.Error("a third caller took the lock while the second held it")
	}
	second()
}
