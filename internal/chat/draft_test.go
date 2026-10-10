package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// writeRawChat plants a chat file the store did not write, such as one whose stored id disagrees with its name (a
// truncated write, a hand-edited /config).
func writeRawChat(t *testing.T, dir string, chatID marotte.ChatID, body string) {
	t.Helper()
	path := filepath.Join(dir, string(chatID), headerFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("plant %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), fileMode); err != nil {
		t.Fatalf("plant %s: %v", path, err)
	}
}

// Compared raw, since a round-tripping rewrite would hide in a decode.
func readRawChat(t *testing.T, dir string, chatID marotte.ChatID) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, string(chatID), headerFileName))
	if err != nil {
		t.Fatalf("read %s: %v", chatID, err)
	}
	return string(b)
}

// newChat seeds a persisted chat so SetDraft has a record to write to.
func newChat(t *testing.T, s *Store, id marotte.ChatID) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "a chat"
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestSetDraft(t *testing.T) {
	t.Parallel()

	t.Run("round_trips_through_the_chat_file", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetDraft(t.Context(), "c1", "half a question"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		got, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if got.Draft != "half a question" {
			t.Errorf("Draft = %q, want %q", got.Draft, "half a question")
		}
	})

	// Retention ages a chat from UpdatedAt (archive.purgeReferenceTime), so a stamping autosave would keep an abandoned
	// draft, which can hold a credential, from ever being purged.
	t.Run("does_not_move_the_retention_clock", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		// The clock is millisecond, so age the record first, through the no-stamp path.
		aged := time.Now().Add(-72 * time.Hour).UnixMilli()
		c, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		c.UpdatedAt = aged
		if err := s.writeHeader(t.Context(), "c1", c); err != nil {
			t.Fatalf("writeHeader: %v", err)
		}

		if _, err := s.SetDraft(t.Context(), "c1", "typed and walked away"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		after, ok := s.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("chat vanished")
		}
		if after.UpdatedAt != aged {
			t.Errorf("UpdatedAt moved %d -> %d; a draft save must not count as activity, or a chat holding an abandoned draft never purges",
				aged, after.UpdatedAt)
		}
		if after.Draft != "typed and walked away" {
			t.Errorf("Draft = %q, want it saved", after.Draft)
		}
	})

	// Every other write is activity and still stamps.
	t.Run("mutate_still_stamps_the_clock", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		c, _ := s.Get(t.Context(), "c1")
		aged := time.Now().Add(-72 * time.Hour).UnixMilli()
		c.UpdatedAt = aged
		if err := s.writeHeader(t.Context(), "c1", c); err != nil {
			t.Fatalf("writeHeader: %v", err)
		}
		if _, err := s.Mutate(t.Context(), "c1", func(ch *marotte.Chat, _ bool) bool {
			ch.Name = "renamed"
			return true
		}); err != nil {
			t.Fatalf("Mutate: %v", err)
		}
		after, _ := s.Get(t.Context(), "c1")
		if after.UpdatedAt == aged {
			t.Error("Mutate left UpdatedAt alone; ordinary mutations must record activity")
		}
	})

	t.Run("clears_on_empty", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetDraft(t.Context(), "c1", "something"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		if _, err := s.SetDraft(t.Context(), "c1", ""); err != nil {
			t.Fatalf("SetDraft clear: %v", err)
		}
		got, _ := s.Get(t.Context(), "c1")
		if got.Draft != "" {
			t.Errorf("Draft = %q, want cleared", got.Draft)
		}
	})

	// Typing must not create a chat, or every keystroke adds a sidebar row.
	t.Run("no_op_on_a_chat_that_does_not_exist", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		if _, err := s.SetDraft(t.Context(), "c-nope", "text"); err != nil {
			t.Fatalf("SetDraft: %v", err)
		}
		if _, ok := s.Get(t.Context(), "c-nope"); ok {
			t.Error("SetDraft created a chat record; typing must not create a conversation")
		}
	})

	t.Run("refuses_a_draft_over_the_cap", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetDraft(t.Context(), "c1", strings.Repeat("x", marotte.MaxDraftBytes+1)); err == nil {
			t.Error("oversize draft accepted")
		}
		if _, err := s.SetDraft(t.Context(), "c1", strings.Repeat("x", marotte.MaxDraftBytes)); err != nil {
			t.Errorf("draft at exactly the cap rejected: %v", err)
		}
	})

	// A draft that cannot round-trip through JSON would make the chat unloadable.
	t.Run("refuses_invalid_utf8", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		if _, err := s.SetDraft(t.Context(), "c1", string([]byte{0xff, 0xfe})); err == nil {
			t.Error("invalid UTF-8 draft accepted")
		}
	})

	// A draft save writes the whole loaded object, and the destination came from its own id, so a c1.json holding
	// `"id":"c2"` overwrote c2.json under c1's mutex. Hand edits and truncated writes make such a file reachable.
	t.Run("refuses_a_chat_file_holding_another_chats_id", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		if _, err := s.SetDraft(t.Context(), "c2", "c2's own unsent question"); err != nil {
			t.Fatalf("SetDraft c2: %v", err)
		}
		c2Before := readRawChat(t, dir, "c2")

		// c1's file claims to be c2.
		writeRawChat(t, dir, "c1", `{"id":"c2","name":"impostor","messages":[]}`)

		if _, err := s.SetDraft(t.Context(), "c1", "a draft typed into c1"); err == nil {
			t.Error("SetDraft accepted a chat file holding another chat's id; an autosave for c1 writes the whole object over c2.json")
		}
		// Clearing the composer reaches the no-change shortcut and must still report the corruption.
		if _, err := s.SetDraft(t.Context(), "c1", ""); err == nil {
			t.Error("SetDraft returned nil for an empty draft on a mismatched file; the corruption stayed silent")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a SetDraft for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
		// A refusal, not a repair: guessing which half is right destroys the other.
		if got := readRawChat(t, dir, "c1"); !strings.Contains(got, `"impostor"`) {
			t.Errorf("c1.json = %s, want it left exactly as found", got)
		}
	})

	// Mutate refuses the same mismatch; both writers hold the invariant.
	t.Run("mutate_refuses_the_same_mismatch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		c2Before := readRawChat(t, dir, "c2")
		writeRawChat(t, dir, "c1", `{"id":"c2","name":"impostor","messages":[]}`)

		_, err = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "renamed"
			return true
		})
		if err == nil {
			t.Error("Mutate accepted a chat file holding another chat's id")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2.json changed under a Mutate for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
	})

	// The guard belongs to the write primitive, so a future no-stamp writer cannot bypass it.
	t.Run("write_primitive_refuses_a_mismatched_object", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c2")
		c2Before := readRawChat(t, dir, "c2")

		if err := s.writeHeader(t.Context(), "c1", &marotte.Chat{ID: "c2", Name: "impostor"}); err == nil {
			t.Error("writeHeader accepted an object whose id is not its destination")
		}
		if got := readRawChat(t, dir, "c2"); got != c2Before {
			t.Errorf("c2 header changed under a writeHeader for c1\nbefore: %s\nafter:  %s", c2Before, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "c1", headerFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat c1.json = %v, want it never created", err)
		}
	})

	t.Run("mutate_rejects_invalid_utf8_in_a_draft", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		newChat(t, s, "c1")
		_, err = s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Draft = string([]byte{0xff})
			return true
		})
		if err == nil {
			t.Error("Mutate persisted an invalid-UTF-8 draft")
		}
	})
}
