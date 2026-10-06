package chat

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A queue a previous process left is settled at the log's first open in this one:
// a row a turn already opened is pruned, every other row is held.
func TestQueue_OpenPrunesSentAndHoldsRest(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	openPromptTurn(t, first, "c1", "m-sent")
	if _, err := first.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.QueuedPrompts = []marotte.QueuedPrompt{{ID: "m-sent", Text: "a"}, {ID: "m-wait", Text: "b"}}
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	// Opened in this process, so the rows the same process queued stay sendable.
	if c, _ := first.Get(t.Context(), "c1"); len(c.QueuedPrompts) != 2 || c.QueuedPrompts[1].Held {
		t.Fatalf("queue in the writing process = %+v, want both rows, none held", c.QueuedPrompts)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if _, err := second.All(t.Context(), "c1"); err != nil {
		t.Fatalf("All: %v", err)
	}

	c, _ := second.Get(t.Context(), "c1")
	if len(c.QueuedPrompts) != 1 || c.QueuedPrompts[0].ID != "m-wait" || !c.QueuedPrompts[0].Held {
		t.Errorf("queue after a restart = %+v, want only m-wait, held", c.QueuedPrompts)
	}
}

// Dequeue takes the row off the header and records it opened, so an unqueue racing
// the send answers sending rather than a discard.
func TestDequeue_RemovesTheRowAndRecordsItOpened(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedQueued(t, s, "c1", marotte.QueuedPrompt{ID: "m-1", Text: "a"}, marotte.QueuedPrompt{ID: "m-2", Text: "b"})

	if err := s.Dequeue(t.Context(), "c1", "m-1"); err != nil {
		t.Fatalf("Dequeue(m-1) = %v", err)
	}

	if !s.Opened("c1", "m-1") || s.Opened("c1", "m-2") {
		t.Errorf("Opened = (m-1 %v, m-2 %v), want only m-1", s.Opened("c1", "m-1"), s.Opened("c1", "m-2"))
	}
	if c, _ := s.Get(t.Context(), "c1"); len(c.QueuedPrompts) != 1 || c.QueuedPrompts[0].ID != "m-2" {
		t.Errorf("queue = %+v, want only m-2", c.QueuedPrompts)
	}
}

// A removal whose header write did not land stays owed: the chat's next mutation
// applies it, even one whose own mutator declines to write.
func TestDequeue_AnOwedRemovalLandsWithTheNextMutation(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedQueued(t, s, "c1", marotte.QueuedPrompt{ID: "m-1", Text: "a"}, marotte.QueuedPrompt{ID: "m-2", Text: "b"})
	// The state a failed Dequeue write leaves: opened and pending, still on disk.
	s.dequeued.open("c1", "m-1")
	if c, _ := s.Get(t.Context(), "c1"); len(c.QueuedPrompts) != 2 {
		t.Fatalf("queue before any mutation = %+v, want both rows on disk", c.QueuedPrompts)
	}

	if _, err := s.Mutate(t.Context(), "c1", func(*marotte.Chat, bool) bool { return false }); err != nil {
		t.Fatalf("Mutate = %v", err)
	}

	if c, _ := s.Get(t.Context(), "c1"); len(c.QueuedPrompts) != 1 || c.QueuedPrompts[0].ID != "m-2" {
		t.Errorf("queue after a declining mutation = %+v, want the owed removal applied", c.QueuedPrompts)
	}
	if !s.Opened("c1", "m-1") {
		t.Error("m-1 stopped reading as opened once its removal landed")
	}
}

func seedQueued(t *testing.T, s *Store, chatID marotte.ChatID, rows ...marotte.QueuedPrompt) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = "c1"
		c.QueuedPrompts = rows
		return true
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
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
		c.QueuedPrompts = []marotte.QueuedPrompt{{ID: "m-wait", Text: "b"}}
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
	if len(c.QueuedPrompts) != 1 || !c.QueuedPrompts[0].Held {
		t.Errorf("queue after a failed then a good open = %+v, want m-wait held", c.QueuedPrompts)
	}
}

type failingReaderAt struct{}

func (failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, errors.New("read failed") }

// A queued follow-up keeps a chat from the purge as a draft does.
func TestRetentionHeader_QueuedPromptsCountAsDrafting(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         bool
	}{
		{"queued", `{"id":"c1","queued_prompts":[{"id":"m-1","text":"x"}]}`, true},
		{"queued before an empty draft", `{"id":"c1","queued_prompts":[{"id":"m-1","text":"x"}],"draft":""}`, true},
		{"empty queue", `{"id":"c1","queued_prompts":[]}`, false},
		{"neither", `{"id":"c1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := decodeRetentionHeader(strings.NewReader(tc.header))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if h.Drafting != tc.want {
				t.Errorf("Drafting = %v, want %v", h.Drafting, tc.want)
			}
		})
	}
}
