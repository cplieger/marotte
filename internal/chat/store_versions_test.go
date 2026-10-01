package chat

import (
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// newVersionedTestStore is newTestStore with a registry the test can read back.
func newVersionedTestStore(t *testing.T) (*Store, *fakeBroadcaster, *subject.Versions) {
	t.Helper()
	s, b := newTestStore(t)
	v := &subject.Versions{}
	WithVersions(v)(s)
	return s, b, v
}

func lastEvent(t *testing.T, b *fakeBroadcaster, typ marotte.EventType) marotte.ServerEvent {
	t.Helper()
	evts := b.snapshot()
	for _, evt := range slices.Backward(evts) {
		if evt.Type == typ {
			return evt
		}
	}
	t.Fatalf("no %s frame among %d broadcasts", typ, len(evts))
	return marotte.ServerEvent{}
}

func TestMutate_ReturnsTheChatVersionTheRegistryReports(t *testing.T) {
	s, _, v := newVersionedTestStore(t)
	got, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "one"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got == "" {
		t.Fatal("Mutate returned an empty version for a saved mutation")
	}
	if cur, ok := v.Current(subject.KindChat, "c1"); !ok || cur != got {
		t.Errorf("Current(chat, c1) = (%q, %v), want (%q, true)", cur, ok, got)
	}
	second, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "two"
		return true
	})
	if err != nil {
		t.Fatalf("second Mutate: %v", err)
	}
	if second == got {
		t.Errorf("second Mutate returned %q, same as the first; a saved mutation must move the counter", second)
	}
}

func TestMutate_HeaderFrameCarriesChatsAndNoChatStamp(t *testing.T) {
	s, b, v := newVersionedTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "one"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	created := lastEvent(t, b, marotte.EventChatCreated)
	if created.Subject == nil {
		t.Fatal("chat_created carries no Subject")
	}
	chats, _ := v.Current(subject.KindChats, "")
	want := marotte.SubjectStamp{Kind: "chats", Version: chats}
	if *created.Subject != want {
		t.Errorf("chat_created Subject = %+v, want %+v", *created.Subject, want)
	}
	b.reset()
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "two"
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	updated := lastEvent(t, b, marotte.EventChatUpdated)
	if updated.Subject == nil || updated.Subject.Kind != "chats" {
		t.Fatalf("chat_updated Subject = %+v, want a chats stamp", updated.Subject)
	}
	if updated.Subject.Version == chats {
		t.Errorf("chat_updated chats version %q did not move from the create's", chats)
	}
}

func TestMutate_DeclinedMutatorReturnsNoVersionAndMovesNothing(t *testing.T) {
	s, _, v := newVersionedTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { return true }); err != nil {
		t.Fatalf("Setup: Mutate: %v", err)
	}
	chatBefore, _ := v.Current(subject.KindChat, "c1")
	chatsBefore, _ := v.Current(subject.KindChats, "")
	got, err := s.Mutate(t.Context(), "c1", func(*marotte.Chat, bool) bool { return false })
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got != "" {
		t.Errorf("declined Mutate returned %q, want \"\"", got)
	}
	if after, _ := v.Current(subject.KindChat, "c1"); after != chatBefore {
		t.Errorf("chat version moved %q -> %q on a declined mutator", chatBefore, after)
	}
	if after, _ := v.Current(subject.KindChats, ""); after != chatsBefore {
		t.Errorf("chats version moved %q -> %q on a declined mutator", chatsBefore, after)
	}
}

func TestRemove_ReturnsTheChatsVersionDeleteStamps(t *testing.T) {
	s, b, v := newVersionedTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { return true }); err != nil {
		t.Fatalf("Setup: Mutate: %v", err)
	}
	before, _ := v.Current(subject.KindChats, "")
	b.reset()
	if err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, _ := v.Current(subject.KindChats, "")
	if after == before {
		t.Fatalf("chats version did not move on Delete (still %q)", before)
	}
	deleted := lastEvent(t, b, marotte.EventChatDeleted)
	want := marotte.SubjectStamp{Kind: "chats", Version: after}
	if deleted.Subject == nil || *deleted.Subject != want {
		t.Errorf("chat_deleted Subject = %+v, want %+v", deleted.Subject, want)
	}
}

func TestDelete_MissingChatCarriesNoStamp(t *testing.T) {
	s, b, v := newVersionedTestStore(t)
	before, _ := v.Current(subject.KindChats, "")
	if err := s.Delete(t.Context(), "c-missing"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if after, _ := v.Current(subject.KindChats, ""); after != before {
		t.Errorf("chats version moved %q -> %q on a missing chat", before, after)
	}
	if deleted := lastEvent(t, b, marotte.EventChatDeleted); deleted.Subject != nil {
		t.Errorf("chat_deleted for a missing chat carries %+v, want no Subject", *deleted.Subject)
	}
}

func TestSetDraft_FillsComposerStateVersion(t *testing.T) {
	s, _, v := newVersionedTestStore(t)
	if _, err := s.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { return true }); err != nil {
		t.Fatalf("Setup: Mutate: %v", err)
	}
	state, err := s.SetDraft(t.Context(), "c1", "typing")
	if err != nil || state == nil {
		t.Fatalf("SetDraft = (%v, %v), want a state", state, err)
	}
	cur, _ := v.Current(subject.KindChat, "c1")
	if state.Version == "" || state.Version != cur {
		t.Errorf("ComposerState.Version = %q, want the registry's %q", state.Version, cur)
	}
	again, err := s.SetDraft(t.Context(), "c1", "typing")
	if err != nil {
		t.Fatalf("SetDraft repeat: %v", err)
	}
	if again != nil {
		t.Errorf("an unchanged draft reported a state (%+v); it must write and mint nothing", *again)
	}
	if after, _ := v.Current(subject.KindChat, "c1"); after != cur {
		t.Errorf("chat version moved %q -> %q on an unchanged draft", cur, after)
	}
}
