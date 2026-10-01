package chat

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// pageFixture is a Store with one open prompt turn and registry hooks whose
// reads take a mutex a concurrent appender holds across its Append, the shape
// the runtime's own locks have (lc.mu and Turn.mu are held into the store lock).
type pageFixture struct {
	s        *Store
	chatID   marotte.ChatID
	turn     string
	registry sync.Mutex
	tails    []OpenTurnTail
}

func newPageFixture(t *testing.T) *pageFixture {
	t.Helper()
	f := &pageFixture{chatID: marotte.ChatID("c-page-lock")}
	s, err := NewStore(t.TempDir(),
		WithLiveTurn(func(marotte.ChatID) bool {
			f.registry.Lock()
			defer f.registry.Unlock()
			return true
		}),
		WithOpenTurns(func(marotte.ChatID) []OpenTurnTail {
			f.registry.Lock()
			defer f.registry.Unlock()
			return f.tails
		}))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	f.s = s
	opened, err := s.OpenTurn(t.Context(), f.chatID, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hello"},
	}, func(c *marotte.Chat) { c.Name = "page lock" })
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	f.turn = opened.Turn
	f.tails = []OpenTurnTail{{ID: f.turn, Entries: []marotte.OpenEntry{
		{Turn: f.turn, ID: "say-1", Kind: marotte.EntryKindText, Text: "hel", N: 1},
	}}}
	return f
}

// appendUnderRegistry is the folder's seal: the registry lock is held while the
// store lock is taken inside Append, the order every registry writer takes.
func (f *pageFixture) appendUnderRegistry(t *testing.T, e *marotte.Entry) {
	t.Helper()
	f.registry.Lock()
	defer f.registry.Unlock()
	if err := f.s.Append(context.Background(), f.chatID, e); err != nil {
		t.Errorf("Append(%s) under the registry lock: %v", e.Kind, err)
	}
}

// TestStorePage_ReadsTheRegistryOutsideTheStoreLock pins the lock order at the
// GET: Page and TurnPage read the registry BEFORE taking the chat's lock.
// A seal that holds the registry lock and appends concurrently must complete;
// the reverse order deadlocks, which the budget turns into a failure.
func TestStorePage_ReadsTheRegistryOutsideTheStoreLock(t *testing.T) {
	f := newPageFixture(t)
	const rounds = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range rounds {
			f.appendUnderRegistry(t, entryOf(f.turn, "", "say-"+string(rune('a'+i%26)), marotte.EntryKindText, marotte.EntryText{Text: "x"}))
		}
	}()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for range rounds {
			if _, ok, err := f.s.Page(context.Background(), f.chatID, 20, ""); err != nil || !ok {
				t.Errorf("Page() = ok %v, err %v; want a page", ok, err)
				return
			}
			if _, err := f.s.TurnPage(context.Background(), f.chatID, f.turn, 0); err != nil {
				t.Errorf("TurnPage() error: %v", err)
				return
			}
		}
	}()
	budget := time.NewTimer(20 * time.Second)
	defer budget.Stop()
	for _, ch := range []chan struct{}{done, finished} {
		select {
		case <-ch:
		case <-budget.C:
			t.Fatal("Page/TurnPage and a registry-held Append did not both finish: the GET is reading the registry from under the store lock, which deadlocks against a seal that holds the registry lock")
		}
	}
}

// TestStorePage_ReconcilesATailSealedBetweenTheReads pins openTail: an open
// entry the window already holds sealed between the registry read and the log
// read and is served once, as the sealed entry; a tail whose turn closed in
// between is served as no tail and stamps no live_turn.
func TestStorePage_ReconcilesATailSealedBetweenTheReads(t *testing.T) {
	f := newPageFixture(t)
	ctx := context.Background()
	if err := f.s.Append(ctx, f.chatID, entryOf(f.turn, "", "say-1", marotte.EntryKindText, marotte.EntryText{Text: "hello"})); err != nil {
		t.Fatalf("Append(say-1): %v", err)
	}
	page, ok, err := f.s.Page(ctx, f.chatID, 20, "")
	if err != nil || !ok {
		t.Fatalf("Page() = ok %v, err %v", ok, err)
	}
	if len(page.OpenEntries) != 0 {
		t.Errorf("Page().OpenEntries = %v, want none: say-1 is sealed in the window", page.OpenEntries)
	}
	if len(page.Subject) != 2 {
		t.Errorf("Page().Subject has %d stamps, want chat + live_turn (the turn is still open)", len(page.Subject))
	}
	tp, err := f.s.TurnPage(ctx, f.chatID, f.turn, 0)
	if err != nil {
		t.Fatalf("TurnPage(): %v", err)
	}
	if len(tp.OpenEntries) != 0 || len(tp.Subject) != 1 {
		t.Errorf("TurnPage() = %d open, %d stamps; want 0 open, 1 live_turn stamp", len(tp.OpenEntries), len(tp.Subject))
	}

	f.tails[0].Entries = []marotte.OpenEntry{{Turn: f.turn, ID: "say-2", Kind: marotte.EntryKindText, Text: "la", N: 1}}
	if err := f.s.Append(ctx, f.chatID, entryOf(f.turn, "", f.turn+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})); err != nil {
		t.Fatalf("Append(turn_close): %v", err)
	}
	page, ok, err = f.s.Page(ctx, f.chatID, 20, "")
	if err != nil || !ok {
		t.Fatalf("Page() after close = ok %v, err %v", ok, err)
	}
	if len(page.OpenEntries) != 0 {
		t.Errorf("Page().OpenEntries after the close = %v, want none", page.OpenEntries)
	}
	if len(page.Subject) != 1 {
		t.Errorf("Page().Subject after the close has %d stamps, want the chat stamp alone", len(page.Subject))
	}
	tp, err = f.s.TurnPage(ctx, f.chatID, f.turn, 0)
	if err != nil {
		t.Fatalf("TurnPage() after close: %v", err)
	}
	if len(tp.OpenEntries) != 0 || len(tp.Subject) != 0 {
		t.Errorf("TurnPage() after the close = %d open, %d stamps; want none of either", len(tp.OpenEntries), len(tp.Subject))
	}
}
