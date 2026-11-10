package chat

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A run list a previous process left is settled at the log's first open in this one: a row a turn already opened is
// pruned, every other row is held.
func TestQueue_OpenPrunesSentAndHoldsRest(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Run = []marotte.RunEntry{rowEntry("m-sent", marotte.RunPending), rowEntry("m-wait", marotte.RunPaused)}
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	// Opened in this process, so the rows the same process queued stay sendable.
	if c, _ := first.Get(t.Context(), "c1"); len(c.Run) != 2 || c.Run[1].State == marotte.RunHeld {
		t.Fatalf("queue in the writing process = %+v, want both rows, none held", c.Run)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All: %v", err)
	}

	c, _ := second.Get(t.Context(), "c1")
	if len(c.Run) != 1 || c.Run[0].ID != "m-wait" || c.Run[0].State != marotte.RunHeld {
		t.Errorf("queue after a restart = %+v, want only m-wait, held", c.Run)
	}
}

// A compaction a previous process listed has no process to run it, so the first open drops it, and the writing process
// keeps its own.
func TestQueue_OpenDropsAPreviousProcesssQueuedCompaction(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Run = []marotte.RunEntry{{ID: "compact-1", Kind: marotte.RunKindCompaction, State: marotte.RunPending}}
		return true
	}); err != nil {
		t.Fatalf("seed the request: %v", err)
	}
	if c, _ := first.Get(t.Context(), "c1"); len(c.Run) != 1 {
		t.Fatal("the writing process lost its own queued compaction")
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All: %v", err)
	}
	if c, _ := second.Get(t.Context(), "c1"); len(c.Run) != 0 {
		t.Errorf("run list after a restart = %+v, want no compaction", c.Run)
	}
}

// An open whose settling fails publishes no log, so the next open settles the
// queue instead of finding a cached log and leaving the rows sendable.
func TestQueue_AFailedSettleIsRetriedAtTheNextOpen(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Run = []marotte.RunEntry{rowEntry("m-wait", marotte.RunPaused)}
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	orig := readEntries
	t.Cleanup(func() { readEntries = orig })
	readEntries = func(*os.File) io.ReaderAt { return failingReaderAt{} }
	if _, err := second.All(t.Context(), "c1"); err == nil {
		t.Fatal("All with the log unreadable = nil error, want the settle failure")
	}
	readEntries = orig
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All after the log is readable again: %v", err)
	}

	c, _ := second.Get(t.Context(), "c1")
	if len(c.Run) != 1 || c.Run[0].State != marotte.RunHeld {
		t.Errorf("queue after a failed then a good open = %+v, want m-wait held", c.Run)
	}
}

type failingReaderAt struct{}

func (failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errors.New("read failed") }

// rowEntry is a queued row on a run list.
func rowEntry(id string, state marotte.RunEntryState) marotte.RunEntry {
	return marotte.RunEntry{ID: id, Kind: marotte.RunKindRow, State: state, Row: &marotte.QueuedPrompt{ID: id, Text: id}}
}
