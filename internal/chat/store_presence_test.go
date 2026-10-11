package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// breakHeader makes chatID's header unstattable without root by putting a file where its
// directory was (ENOTDIR), and answers the repair that puts the directory back.
func breakHeader(t *testing.T, s *Store, chatID marotte.ChatID) (repair func()) {
	t.Helper()
	dir := filepath.Join(s.Dir(), string(chatID))
	aside := dir + ".aside"
	if err := os.Rename(dir, aside); err != nil {
		t.Fatalf("Setup: move %s aside: %v", dir, err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatalf("Setup: file in place of %s: %v", dir, err)
	}
	return func() {
		if err := os.Remove(dir); err != nil {
			t.Fatalf("repair: remove %s: %v", dir, err)
		}
		if err := os.Rename(aside, dir); err != nil {
			t.Fatalf("repair: restore %s: %v", dir, err)
		}
	}
}

func TestPresence_TellsAFaultFromAnAbsence(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("Setup: seed c1: %v", err)
	}
	if _, err := s.Mutate(t.Context(), "c2", func(c *marotte.Chat, _ bool) bool { c.Name = "B"; return true }); err != nil {
		t.Fatalf("Setup: seed c2: %v", err)
	}
	if _, err := s.Delete(t.Context(), "c2"); err != nil {
		t.Fatalf("Setup: delete c2: %v", err)
	}

	if got := s.Presence("c1"); got != PresencePresent {
		t.Errorf("Presence(c1) = %v, want present", got)
	}
	if got := s.Presence("c2"); got != PresenceAbsent {
		t.Errorf("Presence(c2) after its delete = %v, want absent", got)
	}
	if got := s.Presence("never"); got != PresenceAbsent {
		t.Errorf("Presence(never) = %v, want absent", got)
	}
	repair := breakHeader(t, s, "c1")
	if got := s.Presence("c1"); got != PresenceUnreadable {
		t.Errorf("Presence(c1) with its header unstattable = %v, want unreadable", got)
	}
	repair()
	if got := s.Presence("c1"); got != PresencePresent {
		t.Errorf("Presence(c1) once storage recovered = %v, want present", got)
	}
}

// A log read on an unstattable header reports the fault, never ErrChatNotFound, which its
// callers read as the chat's deletion.
func TestPromptReceipt_AnUnreadableHeaderIsNotChatNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("Setup: seed c1: %v", err)
	}
	repair := breakHeader(t, s, "c1")
	defer repair()

	_, err := s.PromptReceipt(t.Context(), "c1", "p1")
	if err == nil || errors.Is(err, ErrChatNotFound) || errors.Is(err, ErrTombstoned) {
		t.Errorf("PromptReceipt(c1) with its header unstattable = %v, want the storage fault", err)
	}
}

func TestPublishIfPresent_PublishesOnlyAPresentRecordAndUnderItsLock(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("Setup: seed c1: %v", err)
	}

	published, lockHeld := false, false
	got := s.PublishIfPresent(t.Context(), "c1", func() {
		published = true
		m := s.Lock("c1")
		if lockHeld = !m.TryLock(); !lockHeld {
			m.Unlock()
		}
	})
	if got != PresencePresent || !published {
		t.Fatalf("PublishIfPresent(c1) = %v, published %v; want present and published", got, published)
	}
	if !lockHeld {
		t.Error("PublishIfPresent(c1) published without the chat's lock, so a delete could land between its verdict and the publication")
	}

	if _, err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Setup: delete c1: %v", err)
	}
	published = false
	if got := s.PublishIfPresent(t.Context(), "c1", func() { published = true }); got != PresenceAbsent || published {
		t.Errorf("PublishIfPresent(c1) after its delete = %v, published %v; want absent and nothing published", got, published)
	}
}

// A header that exists but does not decode is no record to publish over: the stat that Presence answers is not
// the record a ready generation stands on.
func TestPublishIfPresent_AHeaderThatDoesNotDecodeIsUnreadable(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("Setup: seed c1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "c1", headerFileName), []byte("{"), 0o600); err != nil {
		t.Fatalf("Setup: malformed header: %v", err)
	}

	published := false
	if got := s.PublishIfPresent(t.Context(), "c1", func() { published = true }); got != PresenceUnreadable || published {
		t.Errorf("PublishIfPresent(c1) over a malformed header = %v, published %v; want unreadable and nothing published",
			got, published)
	}
}
