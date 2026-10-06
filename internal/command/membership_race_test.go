package command

// The coordinator under concurrency: each case asserts an INVARIANT, not an outcome, because who
// wins legitimately varies. Run under -race.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// TestMembership_ConcurrentCreateAndDeleteOfOneChat asserts a biconditional rather than a winner:
// the chat exists if and only if it has a tab.
func TestMembership_ConcurrentCreateAndDeleteOfOneChat(t *testing.T) {
	for i := range 40 {
		store := testsupport.NewInMemoryChatStore()
		mem, st, _ := newRacedMembership(t, store)
		first := createChat(t, mem, "op-race")
		chatID := marotte.ChatID(first.Chat.ID)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = mem.CreateChatAndOpen(t.Context(), ChatCreate{
				OpID: "op-race", Init: func(c *marotte.Chat) { c.Name = "racer" },
			})
		}()
		go func() {
			defer wg.Done()
			_ = mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del")
		}()
		wg.Wait()

		_, chatExists := store.Get(t.Context(), chatID)
		hasTab := len(tabIDsFor(st, chatID)) > 0
		if chatExists != hasTab {
			t.Fatalf("iteration %d: chat exists = %v but has a tab = %v; the two stores must agree",
				i, chatExists, hasTab)
		}
	}
}

// TestMembership_TwoOpensRaceForTheFinalSlot: exactly one wins, the loser gets the 409, and the set
// lands EXACTLY at MaxOpenTabs, which catches a check-then-act reservation.
func TestMembership_TwoOpensRaceForTheFinalSlot(t *testing.T) {
	for i := range 20 {
		store := testsupport.NewInMemoryChatStore()
		mem, st, _ := newRacedMembership(t, store)
		fillTabs(t, mem, tabs.MaxOpenTabs-1)
		seedRecord(t, store, "c-alpha")
		seedRecord(t, store, "c-beta")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, ref := range []string{"c-alpha", "c-beta"} {
			wg.Go(func() {
				_, errs[j] = mem.OpenTab(t.Context(),
					marotte.OpenTab{Kind: marotte.TabKindChat, Ref: ref}, "op-race")
			})
		}
		wg.Wait()

		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, errTabsFull):
			default:
				t.Fatalf("iteration %d: an open failed with %v, want either success or errTabsFull", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("iteration %d: %d of 2 opens won the final slot, want exactly 1", i, won)
		}
		if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
			t.Fatalf("iteration %d: the set holds %d tabs, want exactly %d: the limit is the limit",
				i, len(open), tabs.MaxOpenTabs)
		}
	}
}

// TestMembership_ADeleteWhoseTabCloseFailsRetriesUnderRace asserts that the retry lands in the same pass, so no
// tab for a deleted chat survives the call.
func TestMembership_ADeleteWhoseTabCloseFailsRetriesUnderRace(t *testing.T) {
	for i := range 20 {
		store := testsupport.NewInMemoryChatStore()
		mem, flaky, _ := newFlakyMembership(t, store)
		opened := createChat(t, mem, "op-a")
		chatID := marotte.ChatID(opened.Chat.ID)
		flaky.failCloseOnce()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del")
		}()
		go func() {
			defer wg.Done()
			_, _ = mem.OpenTab(t.Context(),
				marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(chatID)}, "op-open")
		}()
		wg.Wait()

		if _, ok := store.Get(t.Context(), chatID); ok {
			t.Fatalf("iteration %d: the chat record survived its delete", i)
		}
		if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
			t.Fatalf("iteration %d: tabs %v for a deleted chat survived the pass; the retry owes this without a restart",
				i, got)
		}
	}
}

// newRacedMembership is newTabbedMembership plus the teardown seam the delete
// path needs. Separate from newFlakyMembership because these cases want the REAL
// store's behaviour, failure injection included only where a case asks for it.
func newRacedMembership(t *testing.T, chats ChatStore) (*Membership, *tabs.Store, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return NewMembership(&MembershipDeps{
		Chats: chats, Tabs: st, Bus: bus, Teardown: &recordingTeardown{},
	}), st, bus
}

// hookedChats runs a hook the first time Get is called for a chat, inside the coordinator's
// critical section, so the lock's effect is observed rather than sampled.
type hookedChats struct {
	*testsupport.InMemoryChatStore
	hook func()
	on   marotte.ChatID
	once sync.Once
}

func (h *hookedChats) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	c, ok := h.InMemoryChatStore.Get(ctx, id)
	if id == h.on {
		h.once.Do(h.hook)
	}
	return c, ok
}

// TestMembership_ADeleteCannotInterleaveWithACreate fires the hook inside CreateChatAndOpen,
// between its record read and its tab write, the one window a delete could split.
func TestMembership_ADeleteCannotInterleaveWithACreate(t *testing.T) {
	inner := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	chats := &hookedChats{InMemoryChatStore: inner}
	mem := NewMembership(&MembershipDeps{
		Chats: chats, Tabs: st, Bus: &tabBus{}, Teardown: &recordingTeardown{},
	})
	first := createChat(t, mem, "op-race")
	chatID := marotte.ChatID(first.Chat.ID)

	deleted := make(chan struct{})
	chats.on = chatID
	chats.hook = func() {
		go func() {
			defer close(deleted)
			_ = mem.DeleteChatAndCloseTabs(context.Background(), chatID, "op-del")
		}()
		select {
		case <-deleted:
		case <-time.After(300 * time.Millisecond):
		}
	}

	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-race", Init: func(c *marotte.Chat) { c.Name = "racer" },
	})
	if err != nil {
		t.Fatalf("the replayed create = %v, want it to succeed", err)
	}
	<-deleted

	if _, ok := inner.Get(t.Context(), chatID); ok {
		t.Fatal("the chat record survived its delete")
	}
	if got := tabIDsFor(st, chatID); len(got) != 0 {
		t.Errorf("tabs %v survived for a deleted chat (the create returned subject %q); the operation "+
			"lock is what stops a delete landing between a create's record read and its tab write",
			got, opened.Subject.ID)
	}
}
