package testsupport

import (
	"context"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// ChatStoreContract is the subject of ChatStoreContractTest: the 5 HEADER methods this suite
// exercises. Log operations are the real store's alone; the fakes hold headers only. SetDraft
// is here for its version contract only.
type ChatStoreContract interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	List(ctx context.Context) []marotte.ChatHeader
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	Delete(ctx context.Context, id marotte.ChatID) (sessionChain []string, err error)
	SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error)
}

// ChatStoreContractTest exercises the behaviour any chat store must hold, against fakes and
// the real store; each behaviour is its own helper.
func ChatStoreContractTest(t *testing.T, newStore func(t *testing.T) ChatStoreContract) {
	t.Helper()

	t.Run("Get_missing_returns_false", func(t *testing.T) { testGetMissingReturnsFalse(t, newStore(t)) })
	t.Run("Get_returns_an_independent_copy", func(t *testing.T) { testGetReturnsIndependentCopy(t, newStore(t)) })
	t.Run("Mutate_creates_new_chat", func(t *testing.T) { testMutateCreatesNewChat(t, newStore(t)) })
	t.Run("Mutate_updates_existing_chat", func(t *testing.T) { testMutateUpdatesExistingChat(t, newStore(t)) })
	t.Run("Mutate_noop_when_false_returned", func(t *testing.T) { testMutateNoopWhenFalseReturned(t, newStore(t)) })
	t.Run("Mutate_returns_a_version_that_moves_per_save", func(t *testing.T) {
		testMutateVersionMovesPerSave(t, newStore(t))
	})
	t.Run("SetDraft_fills_ComposerState_Version", func(t *testing.T) { testSetDraftFillsVersion(t, newStore(t)) })
	t.Run("Delete_removes_chat", func(t *testing.T) { testDeleteRemovesChat(t, newStore(t)) })
	t.Run("List_returns_created_chats", func(t *testing.T) { testListReturnsCreatedChats(t, newStore(t)) })
}

func testGetMissingReturnsFalse(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, ok := s.Get(context.Background(), "nonexistent")
	if ok {
		t.Error("Get returned true for missing chat")
	}
}

// testGetReturnsIndependentCopy pins that a Get result shares nothing with the stored record
// (or Mutate is not the only write path), on a scalar and a slice field.
func testGetReturnsIndependentCopy(t *testing.T, s ChatStoreContract) {
	t.Helper()
	const tampered = "tampered"
	ctx := context.Background()
	_, _ = s.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "keep"
		c.Attachments = []string{"a.txt"}
		return true
	})

	got, ok := s.Get(ctx, "c1")
	if !ok {
		t.Fatal("Get returned false for a chat that was just created")
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("fixture did not store what this case mutates: %+v", got.Attachments)
	}
	got.Name = tampered
	got.Attachments[0] = tampered
	got.Attachments = append(got.Attachments, "b.txt")

	after, _ := s.Get(ctx, "c1")
	if after.Name != "keep" {
		t.Errorf("Chat.Name = %q after a caller edited a Get result, want %q", after.Name, "keep")
	}
	if len(after.Attachments) != 1 || after.Attachments[0] != "a.txt" {
		t.Errorf("Chat.Attachments = %v after a caller edited a Get result, want [a.txt]", after.Attachments)
	}
}

func testMutateCreatesNewChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, err := s.Mutate(context.Background(), "c1", func(c *marotte.Chat, exists bool) bool {
		if exists {
			t.Error("exists should be false for new chat")
		}
		c.Name = "hello"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	c, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("Get returned false after Mutate create")
	}
	if c.Name != "hello" {
		t.Errorf("Name = %q, want hello", c.Name)
	}
}

func testMutateUpdatesExistingChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, _ = s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "first"
		return true
	})
	_, _ = s.Mutate(context.Background(), "c1", func(c *marotte.Chat, exists bool) bool {
		if !exists {
			t.Error("exists should be true for existing chat")
		}
		c.Name = "second"
		return true
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "second" {
		t.Errorf("Name = %q, want second", c.Name)
	}
}

func testMutateNoopWhenFalseReturned(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, _ = s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "created"
		return true
	})
	_, _ = s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "should-not-persist"
		return false
	})
	c, _ := s.Get(context.Background(), "c1")
	if c.Name != "created" {
		t.Errorf("Name = %q, want created (mutate returned false)", c.Name)
	}
}

func testDeleteRemovesChat(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, _ = s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "doomed"
		c.RecordSession("s1")
		c.RecordSession("s2")
		return true
	})
	chain, err := s.Delete(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if want := []string{"s1", "s2"}; !slices.Equal(chain, want) {
		t.Errorf("Delete(c1) chain = %v, want the removed record's %v", chain, want)
	}
	_, ok := s.Get(context.Background(), "c1")
	if ok {
		t.Error("Get returned true after Delete")
	}
}

func testListReturnsCreatedChats(t *testing.T, s ChatStoreContract) {
	t.Helper()
	_, _ = s.Mutate(context.Background(), "a", func(c *marotte.Chat, _ bool) bool {
		c.Name = "alpha"
		return true
	})
	_, _ = s.Mutate(context.Background(), "b", func(c *marotte.Chat, _ bool) bool {
		c.Name = "beta"
		return true
	})
	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("List len = %d, want 2", len(headers))
	}
}

// testMutateVersionMovesPerSave pins the `chat` version contract every store
// and double must hold: a saved mutation returns a non-empty version that
// differs from the previous save's, and a declined mutator returns "".
func testMutateVersionMovesPerSave(t *testing.T, s ChatStoreContract) {
	t.Helper()
	first, err := s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "one"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if first == "" {
		t.Fatal("a saved Mutate returned an empty version")
	}
	second, err := s.Mutate(context.Background(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "two"
		return true
	})
	if err != nil {
		t.Fatalf("second Mutate: %v", err)
	}
	if second == "" || second == first {
		t.Errorf("second Mutate returned %q after %q, want a different non-empty version", second, first)
	}
	declined, err := s.Mutate(context.Background(), "c1", func(*marotte.Chat, bool) bool { return false })
	if err != nil {
		t.Fatalf("declined Mutate: %v", err)
	}
	if declined != "" {
		t.Errorf("a declined Mutate returned %q, want \"\"", declined)
	}
}

// testSetDraftFillsVersion pins that the composer write mints a `chat` version
// onto the returned state, so the draft_changed broadcast has a stamp under
// the real store and both doubles alike.
func testSetDraftFillsVersion(t *testing.T, s ChatStoreContract) {
	t.Helper()
	if _, err := s.Mutate(context.Background(), "c1", func(*marotte.Chat, bool) bool { return true }); err != nil {
		t.Fatalf("Setup: Mutate: %v", err)
	}
	state, err := s.SetDraft(context.Background(), "c1", "draft")
	if err != nil || state == nil {
		t.Fatalf("SetDraft = (%v, %v), want a state", state, err)
	}
	if state.Version == "" {
		t.Error("ComposerState.Version is empty after a composer write")
	}
	again, err := s.SetDraft(context.Background(), "c1", "draft two")
	if err != nil || again == nil {
		t.Fatalf("second SetDraft = (%v, %v), want a state", again, err)
	}
	if again.Version == state.Version {
		t.Errorf("second composer write returned version %q, same as the first", again.Version)
	}
}
