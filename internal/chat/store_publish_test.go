package chat

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

const publishChat marotte.ChatID = "c-0000bb01"

func framesOf(b *fakeBroadcaster, typ marotte.EventType) []marotte.ServerEvent {
	var out []marotte.ServerEvent
	for _, e := range b.snapshot() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func rename(t *testing.T, s *Store, name string) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), publishChat, func(c *marotte.Chat, _ bool) bool {
		c.Name = name
		return true
	}); err != nil {
		t.Fatalf("Mutate(%q): %v", name, err)
	}
}

func TestPublishHeader_ABurstOfSavesSendsOneFrameWithTheLastHeader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, b, v := newVersionedTestStore(t)
		rename(t, s, "created")
		opened, err := s.OpenTurn(t.Context(), publishChat, &TurnSpec{
			Source: marotte.TurnOpenNamePrompt, Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "hi"},
		}, nil)
		if err != nil {
			t.Fatalf("Setup: OpenTurn: %v", err)
		}
		if opened.Turn == "" {
			t.Fatal("Setup: OpenTurn minted no turn id")
		}

		rename(t, s, "first")
		if err := s.WriteCounters(t.Context(), publishChat); err != nil {
			t.Fatalf("WriteCounters: %v", err)
		}
		rename(t, s, "last")
		synctest.Sleep(headerPublishWindow - time.Millisecond)
		if n := len(framesOf(b, marotte.EventChatUpdated)); n != 0 {
			t.Fatalf("chat_updated frames before the window closed = %d, want 0", n)
		}
		synctest.Sleep(time.Millisecond)

		updated := framesOf(b, marotte.EventChatUpdated)
		if len(updated) != 1 {
			t.Fatalf("chat_updated frames for three saves in one window = %d, want 1", len(updated))
		}
		h, ok := updated[0].Payload.(marotte.ChatHeader)
		if !ok {
			t.Fatalf("chat_updated payload = %T, want marotte.ChatHeader", updated[0].Payload)
		}
		if h.Name != "last" || h.TurnCount != 1 {
			t.Errorf("published header name %q turn_count %d, want \"last\" and 1", h.Name, h.TurnCount)
		}
		// A fresh registry: the create mints chats 1, the one window chats 2.
		if chats, _ := v.Current(subject.KindChats, ""); chats != "2" {
			t.Errorf("chats version after the create and one window = %q, want \"2\"", chats)
		}
		if st := updated[0].Subject; st == nil || st.Kind != "chats" || st.Version != "2" {
			t.Errorf("chat_updated stamp = %+v, want chats@2", st)
		}
	})
}

func TestPublishHeader_ASaveAfterTheWindowSendsTheNextFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, b := newTestStore(t)
		rename(t, s, "created")
		rename(t, s, "one")
		synctest.Sleep(headerPublishWindow)
		rename(t, s, "two")
		synctest.Sleep(headerPublishWindow)

		updated := framesOf(b, marotte.EventChatUpdated)
		if len(updated) != 2 {
			t.Fatalf("chat_updated frames for saves in two windows = %d, want 2", len(updated))
		}
		if h, _ := updated[1].Payload.(marotte.ChatHeader); h.Name != "two" {
			t.Errorf("second frame's name = %q, want \"two\"", h.Name)
		}
	})
}

func TestPublishHeader_ADeleteInsideTheWindowSendsNoUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, b, v := newVersionedTestStore(t)
		rename(t, s, "created")
		rename(t, s, "renamed")
		if _, err := s.Delete(t.Context(), publishChat); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		synctest.Sleep(headerPublishWindow)

		deleted := framesOf(b, marotte.EventChatDeleted)
		if len(deleted) != 1 {
			t.Fatalf("chat_deleted frames = %d, want 1", len(deleted))
		}
		if updated := framesOf(b, marotte.EventChatUpdated); len(updated) != 0 {
			t.Errorf("chat_updated after the chat was deleted: %+v", updated)
		}
		// Only the version tells the gone-record arm from the unreadable one: neither sends a frame.
		if deleted[0].Subject == nil {
			t.Fatal("chat_deleted carries no subject stamp")
		}
		if chats, _ := v.Current(subject.KindChats, ""); chats != deleted[0].Subject.Version {
			t.Errorf("chats version after the flush = %q, want the chat_deleted stamp's %q", chats, deleted[0].Subject.Version)
		}
	})
}

func TestPublishHeader_ACreateIsAnnouncedAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, b := newTestStore(t)
		rename(t, s, "created")

		created := framesOf(b, marotte.EventChatCreated)
		if len(created) != 1 {
			t.Fatalf("chat_created frames right after the create = %d, want 1", len(created))
		}
		if h, _ := created[0].Payload.(marotte.ChatHeader); h.Name != "created" {
			t.Errorf("chat_created name = %q, want \"created\"", h.Name)
		}
		synctest.Sleep(headerPublishWindow)
		if updated := framesOf(b, marotte.EventChatUpdated); len(updated) != 0 {
			t.Errorf("a lone create also sent chat_updated: %+v", updated)
		}
	})
}

// The composition root wires the broadcaster after construction, while a header save's flush may already be pending
// on the coalescer's timer goroutine: the wiring must be safe beside that flush, which then sends its frame.
func TestWithBroadcaster_Race_WiredWhileAHeaderFlushIsPending(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	rename(t, s, "created")
	rename(t, s, "before the wiring")
	b := &fakeBroadcaster{}
	WithBroadcaster(b)(s)

	deadline := time.Now().Add(5 * time.Second)
	for len(framesOf(b, marotte.EventChatUpdated)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no chat_updated reached the broadcaster wired after the save")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
