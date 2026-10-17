package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const readPosChat marotte.ChatID = "c-0000aa01"

// closedTurns writes n closed prompt turns to the chat through the store.
func closedTurns(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := range n {
		opened, err := s.OpenTurn(t.Context(), readPosChat, &TurnSpec{
			Source: marotte.TurnOpenNamePrompt,
			Prompt: &marotte.EntryPrompt{ID: "m-" + itoa(i), Text: "hi"},
		}, nil)
		if err != nil {
			t.Fatalf("Setup: OpenTurn: %v", err)
		}
		closeStoreTurn(t, s, opened.Turn)
	}
}

func closeStoreTurn(t *testing.T, s *Store, turn string) {
	t.Helper()
	e := entryOf(turn, "", turn+":close", marotte.EntryKindTurnClose, marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted})
	if err := s.Append(t.Context(), readPosChat, e); err != nil {
		t.Fatalf("Setup: close %s: %v", turn, err)
	}
}

// reopenedStore is a store over dir with no log open, as after a restart.
func reopenedStore(t *testing.T, dir string) (*Store, *fakeBroadcaster) {
	t.Helper()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore: %v", err)
	}
	b := &fakeBroadcaster{}
	WithBroadcaster(b)(s)
	return s, b
}

// seededDir is a chat dir holding readPosChat with n closed turns, written by a store since dropped.
func seededDir(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	s, _ := reopenedStore(t, dir)
	newChat(t, s, readPosChat)
	closedTurns(t, s, n)
	return dir
}

func updatedHeaders(b *fakeBroadcaster) []marotte.ChatHeader {
	var out []marotte.ChatHeader
	for _, e := range b.snapshot() {
		if h, ok := e.Payload.(marotte.ChatHeader); ok && e.Type == marotte.EventChatUpdated {
			out = append(out, h)
		}
	}
	return out
}

func TestWireHeader_EveryProducerCarriesTheOpenLogsPosition(t *testing.T) {
	t.Run("the list", func(t *testing.T) {
		s, _ := reopenedStore(t, seededDir(t, 2))
		if err := s.Warm(t.Context(), readPosChat); err != nil {
			t.Fatalf("Setup: Warm: %v", err)
		}
		hs := s.List(t.Context())
		if len(hs) != 1 || hs[0].ReadPosition != 2 {
			t.Errorf("List() = %+v, want one header with read_position 2", hs)
		}
	})
	t.Run("the log page", func(t *testing.T) {
		s, _ := reopenedStore(t, seededDir(t, 2))
		page, ok, err := s.page(t.Context(), readPosChat, 10, "")
		if err != nil || !ok {
			t.Fatalf("page() = (%v, %v)", ok, err)
		}
		if page.Chat.ReadPosition != 2 {
			t.Errorf("page().Chat.ReadPosition = %d, want 2", page.Chat.ReadPosition)
		}
	})
	t.Run("a header mutation", func(t *testing.T) {
		s, b := reopenedStore(t, seededDir(t, 2))
		if err := s.Warm(t.Context(), readPosChat); err != nil {
			t.Fatalf("Setup: Warm: %v", err)
		}
		b.reset()
		if _, err := s.Mutate(t.Context(), readPosChat, func(c *marotte.Chat, _ bool) bool {
			c.Name = "renamed"
			return true
		}); err != nil {
			t.Fatalf("Mutate: %v", err)
		}
		hs := updatedHeaders(b)
		if len(hs) != 1 || hs[0].ReadPosition != 2 {
			t.Errorf("chat_updated headers = %+v, want one with read_position 2", hs)
		}
	})
}

func TestLogFor_FirstOpenAnnouncesThePosition(t *testing.T) {
	s, b := reopenedStore(t, seededDir(t, 3))
	if _, held := s.TurnSeq(t.Context(), readPosChat, "no-such-turn"); held {
		t.Fatalf("Setup: TurnSeq of an absent turn answered held")
	}
	hs := updatedHeaders(b)
	if len(hs) != 1 || hs[0].ReadPosition != 3 {
		t.Fatalf("first open broadcast %+v, want one chat_updated with read_position 3", hs)
	}

	s.TurnSeq(t.Context(), readPosChat, "no-such-turn")
	if n := len(updatedHeaders(b)); n != 1 {
		t.Errorf("a second read broadcast %d headers in all, want the first open's 1", n)
	}
}

func TestLogFor_FirstOpenAnnouncesTheSettledQueueOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		turns int
	}{
		{name: "at a read position", turns: 2},
		{name: "at read position 0", turns: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seededDir(t, tc.turns)
			writer, _ := reopenedStore(t, dir)
			if _, err := writer.Mutate(t.Context(), readPosChat, func(c *marotte.Chat, _ bool) bool {
				c.QueuedPrompts = []marotte.QueuedPrompt{{ID: "m-wait", Text: "b"}}
				return true
			}); err != nil {
				t.Fatalf("Setup: queue a row: %v", err)
			}

			s, b := reopenedStore(t, dir)
			s.TurnSeq(t.Context(), readPosChat, "no-such-turn")

			hs := updatedHeaders(b)
			if len(hs) != 1 {
				t.Fatalf("first open broadcast %+v, want exactly one chat_updated", hs)
			}
			if got := hs[0].ReadPosition; got != uint64(tc.turns) {
				t.Errorf("announced read_position = %d, want %d", got, tc.turns)
			}
			if q := hs[0].QueuedPrompts; len(q) != 1 || !q[0].Held {
				t.Errorf("announced queue = %+v, want the row held", q)
			}
		})
	}
}

func TestLogFor_FirstOpenAtPositionZeroIsSilent(t *testing.T) {
	s, b := reopenedStore(t, seededDir(t, 0))
	s.TurnSeq(t.Context(), readPosChat, "no-such-turn")
	if hs := updatedHeaders(b); len(hs) != 0 {
		t.Errorf("first open of a log at position 0 broadcast %+v, want nothing", hs)
	}
}

func TestWriteCounters_BroadcastsAPositionMovedWithCountersUnchanged(t *testing.T) {
	s, b := newTestStore(t)
	newChat(t, s, readPosChat)
	closedTurns(t, s, 1)
	if err := s.WriteCounters(t.Context(), readPosChat); err != nil {
		t.Fatalf("Setup: WriteCounters: %v", err)
	}
	// A second completed turn's open writes the counters before its close, so the close moves only
	// the position.
	opened, err := s.OpenTurn(t.Context(), readPosChat, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt, Prompt: &marotte.EntryPrompt{ID: "m-2", Text: "again"},
	}, nil)
	if err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	if err := s.WriteCounters(t.Context(), readPosChat); err != nil {
		t.Fatalf("Setup: WriteCounters at open: %v", err)
	}
	closeStoreTurn(t, s, opened.Turn)
	b.reset()

	if err := s.WriteCounters(t.Context(), readPosChat); err != nil {
		t.Fatalf("WriteCounters: %v", err)
	}
	hs := updatedHeaders(b)
	if len(hs) != 1 || hs[0].ReadPosition != 2 {
		t.Errorf("WriteCounters after the close broadcast %+v, want one header with read_position 2", hs)
	}
}

func TestReadBound_OpensAClosedLogAndAnswersThePosition(t *testing.T) {
	dir := seededDir(t, 2)
	s, _ := reopenedStore(t, dir)
	if _, err := s.OpenTurn(t.Context(), readPosChat, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt, Prompt: &marotte.EntryPrompt{ID: "m-live", Text: "live"},
	}, nil); err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	s, _ = reopenedStore(t, dir)

	got, err := s.ReadBound(t.Context(), readPosChat)
	if err != nil {
		t.Fatalf("ReadBound: %v", err)
	}
	// The reopen's store-open closer closes the running turn, so it is the newest closed turn.
	if got != 3 {
		t.Errorf("ReadBound() = %d, want 3", got)
	}
}

func TestReadBound_ExcludesARunningTurn(t *testing.T) {
	s, _ := newTestStore(t)
	newChat(t, s, readPosChat)
	closedTurns(t, s, 2)
	if _, err := s.OpenTurn(t.Context(), readPosChat, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt, Prompt: &marotte.EntryPrompt{ID: "m-live", Text: "live"},
	}, nil); err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	got, err := s.ReadBound(t.Context(), readPosChat)
	if err != nil || got != 2 {
		t.Errorf("ReadBound() with a turn running = (%d, %v), want 2: no ack may name a turn still running", got, err)
	}
}
