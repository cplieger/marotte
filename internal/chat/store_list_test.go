package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func seedChats(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := s.Mutate(t.Context(), marotte.ChatID(id), func(c *marotte.Chat, _ bool) bool {
			c.Name = id
			return true
		}); err != nil {
			t.Fatalf("seed chat %s: %v", id, err)
		}
	}
}

// A scan the fan-out never finished must report itself INCOMPLETE: the session
// reaper derives its keep-list from it, so a partial list marked complete
// authorises deleting a live chat's KAS sessions.
func TestReadHeadersParallel_CancelledScanIsNotComplete(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a", "b", "c")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	headers, complete := readHeadersParallel(ctx, []chatEntry{
		{id: "a", path: s.dir + "/a"},
		{id: "b", path: s.dir + "/b"},
		{id: "c", path: s.dir + "/c"},
	})

	if complete {
		t.Errorf("complete = true after a cancelled scan that returned %d of 3 headers; "+
			"a partial keep-list marked complete is what deletes a live chat's sessions", len(headers))
	}
}

// A cancelled caller must not truncate the SHARED scan: the client's boot fires two
// GET /api/chats reads and the second aborts the first, so the request holding the
// singleflight slot is routinely cancelled while another is waiting on its answer.
func TestListWithCompleteness_ACancelledCallerStillGetsEveryChat(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a", "b", "c")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	headers, complete := s.listWithCompleteness(ctx)

	if len(headers) != 3 {
		t.Errorf("headers = %d, want 3: a cancelled caller must not truncate the scan it shares", len(headers))
	}
	if !complete {
		t.Error("complete = false; every chat was readable, so the scan is complete")
	}
}

// loopHeader replaces id's header with a symlink to itself, so a stat answers
// ELOOP whatever the euid.
func loopHeader(t *testing.T, s *Store, id string) {
	t.Helper()
	header := filepath.Join(s.dir, id, headerFileName)
	if err := os.Remove(header); err != nil {
		t.Fatalf("Setup: remove header: %v", err)
	}
	if err := os.Symlink(headerFileName, header); err != nil {
		t.Fatalf("Setup: symlink header: %v", err)
	}
}

// A header that exists but cannot be judged is not an absent one: the keep-list
// built from this scan feeds the session reaper, so it must not claim to be whole.
func TestListWithCompleteness_AHeaderThatCannotBeStattedMakesTheScanIncomplete(t *testing.T) {
	t.Run("eloop", func(t *testing.T) {
		s, _ := newTestStore(t)
		seedChats(t, s, "a", "b")
		loopHeader(t, s, "b")

		headers, complete := s.listWithCompleteness(t.Context())
		if complete {
			t.Errorf("listWithCompleteness() complete = true with %d headers, want false: b's header stat fails with ELOOP", len(headers))
		}
		if _, refsComplete := s.ReferencedSessionIDs(t.Context()); refsComplete {
			t.Error("ReferencedSessionIDs() complete = true, want false")
		}
	})
	t.Run("eacces", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		s, _ := newTestStore(t)
		seedChats(t, s, "a", "b")
		dir := filepath.Join(s.dir, "b")
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatalf("Setup: chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		if _, complete := s.listWithCompleteness(t.Context()); complete {
			t.Error("listWithCompleteness() complete = true, want false: b's header stat fails with EACCES")
		}
	})
}

// A chat directory holding no header yet is a chat mid-create: skipped, and the
// scan still complete.
func TestListWithCompleteness_AMissingHeaderKeepsTheScanComplete(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a")
	if err := os.Mkdir(filepath.Join(s.dir, "b"), 0o700); err != nil {
		t.Fatalf("Setup: mkdir: %v", err)
	}

	headers, complete := s.listWithCompleteness(t.Context())
	if len(headers) != 1 || headers[0].ID != "a" {
		t.Errorf("listWithCompleteness() headers = %+v, want just a", headers)
	}
	if !complete {
		t.Error("listWithCompleteness() complete = false, want true: a missing header is absence, not doubt")
	}
}

func TestChatEntries_AnUnstattableHeaderTruncatesTheSearch(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a", "b")
	loopHeader(t, s, "b")

	entries, truncated := s.chatEntries(t.Context())
	if !truncated {
		t.Errorf("chatEntries() truncated = false with %d entries, want true", len(entries))
	}
}
