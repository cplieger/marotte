package chat

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

func seedBotChat(t *testing.T, s *Store, id marotte.ChatID, bot *marotte.Bot) {
	t.Helper()
	if _, err := s.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = string(id)
		c.Bot = bot
		return true
	}); err != nil {
		t.Fatalf("seed bot %s: %v", id, err)
	}
}

func wantBotIDs(t *testing.T, s *Store, want []marotte.ChatID, wantComplete bool) {
	t.Helper()
	got, complete := s.BotIDs()
	if complete != wantComplete {
		t.Errorf("BotIDs() complete = %v, want %v", complete, wantComplete)
	}
	if !slices.Equal(got, want) {
		t.Errorf("BotIDs() = %v, want %v", got, want)
	}
}

// reopenStore opens a second store over s's directory: a restarted process, whose set starts empty.
func reopenStore(t *testing.T, s *Store) *Store {
	t.Helper()
	next, err := NewStore(s.dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return next
}

// The rule engine indexes Bots from this set, so it must not answer a partial set as whole: before the first complete
// scan it says incomplete, and from that scan on it holds every Bot on disk.
func TestBotIDs_IsCompleteOnlyFromTheFirstCompleteScan(t *testing.T) {
	for _, scan := range []struct {
		name string
		run  func(t *testing.T, s *Store)
	}{
		{name: "the list scan", run: func(t *testing.T, s *Store) { s.List(t.Context()) }},
		{name: "the owners scan", run: func(t *testing.T, s *Store) { s.SessionOwners(t.Context()) }},
	} {
		t.Run(scan.name, func(t *testing.T) {
			first, _ := newTestStore(t)
			seedBotChat(t, first, "b2", &marotte.Bot{Goal: "g", StartedAt: 1})
			seedBotChat(t, first, "b1", &marotte.Bot{})
			seedChats(t, first, "plain")

			s := reopenStore(t, first)
			wantBotIDs(t, s, nil, false)
			scan.run(t, s)
			wantBotIDs(t, s, []marotte.ChatID{"b1", "b2"}, true)
		})
	}
}

// A scan that skipped a chat could have skipped a Bot.
func TestBotIDs_AnIncompleteScanLeavesTheSetIncomplete(t *testing.T) {
	first, _ := newTestStore(t)
	seedBotChat(t, first, "b1", &marotte.Bot{})
	seedChats(t, first, "broken")
	loopHeader(t, first, "broken")

	s := reopenStore(t, first)
	s.List(t.Context())
	if _, complete := s.BotIDs(); complete {
		t.Error("BotIDs() is complete after a scan that skipped a chat")
	}
}

// The store is the records' one writer, so its save and remove paths keep the set: a write that makes a chat a Bot
// adds it, a delete or a purge removes it, and a regular chat never enters it.
func TestBotIDs_FollowsTheStoresWrites(t *testing.T) {
	s, _ := newTestStore(t)
	s.List(t.Context())
	wantBotIDs(t, s, nil, true)

	seedChats(t, s, "plain")
	if _, err := s.Mutate(t.Context(), "plain", func(c *marotte.Chat, _ bool) bool { c.Name = "renamed"; return true }); err != nil {
		t.Fatalf("rename: %v", err)
	}
	wantBotIDs(t, s, nil, true)

	seedBotChat(t, s, "b1", &marotte.Bot{Goal: "g", StartedAt: 1})
	wantBotIDs(t, s, []marotte.ChatID{"b1"}, true)

	seedChats(t, s, "later")
	if _, err := s.Mutate(t.Context(), "later", func(c *marotte.Chat, _ bool) bool { c.Bot = &marotte.Bot{}; return true }); err != nil {
		t.Fatalf("make a Bot: %v", err)
	}
	wantBotIDs(t, s, []marotte.ChatID{"b1", "later"}, true)

	if _, err := s.Delete(t.Context(), "b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantBotIDs(t, s, []marotte.ChatID{"later"}, true)

	ageChat(t, s, "later", 3*purgeWindow)
	if v, err := s.PurgeIdle(t.Context(), "later", time.Now().Add(-purgeWindow)); err != nil || v.Outcome != archive.PurgePurged {
		t.Fatalf("Setup: PurgeIdle = %+v, %v; want the draft purged", v, err)
	}
	wantBotIDs(t, s, nil, true)
}

// A Bot written before the first complete scan finishes is in the set once it completes, whichever saw it first.
func TestBotIDs_AWriteBeforeTheScanIsKept(t *testing.T) {
	first, _ := newTestStore(t)
	seedBotChat(t, first, "b1", &marotte.Bot{})

	s := reopenStore(t, first)
	seedBotChat(t, s, "b2", &marotte.Bot{})
	if _, err := s.Delete(t.Context(), "b1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wantBotIDs(t, s, []marotte.ChatID{"b2"}, false)
	s.List(t.Context())
	wantBotIDs(t, s, []marotte.ChatID{"b2"}, true)
}
