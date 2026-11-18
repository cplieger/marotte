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

// A revert a previous process listed and its Compact call's running mark have no process to run them, so the first
// open drops both, and the writing process keeps its own.
func TestQueue_OpenDropsAPreviousProcesssRevertAndCompactCall(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Run = []marotte.RunEntry{{ID: "revert-1", Kind: marotte.RunKindRevert, State: marotte.RunRunning}}
		c.Compacting = true
		return true
	}); err != nil {
		t.Fatalf("seed the revert and the call: %v", err)
	}
	if c, _ := first.Get(t.Context(), "c1"); len(c.Run) != 1 || !c.Compacting {
		t.Fatal("the writing process lost its own revert or call")
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All: %v", err)
	}
	c, _ := second.Get(t.Context(), "c1")
	if len(c.Run) != 0 {
		t.Errorf("run list after a restart = %+v, want no revert", c.Run)
	}
	if c.Compacting || c.Header().Run.Compacting {
		t.Error("the header still says a compaction runs after a restart, which no call ever ends")
	}
}

// Only a Compact call's running mark left by a previous process is cleared at the first open, so the header stops
// showing a compaction nothing runs.
func TestQueue_OpenClearsAPreviousProcesssCompactCallAlone(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Run, c.Compacting = nil, true
		return true
	}); err != nil {
		t.Fatalf("seed the call: %v", err)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All: %v", err)
	}
	if c, _ := second.Get(t.Context(), "c1"); c.Compacting {
		t.Error("the running mark outlived the process that ran the call")
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
