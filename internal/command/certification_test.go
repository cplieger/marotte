package command

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// storeAndBus is an in-memory store whose own broadcasts (the header frame) land
// on the same bus the command's broadcasts do, so a test sees the whole sequence
// one Mutate produces in wire order.
func storeAndBus(t *testing.T) (*testsupport.InMemoryChatStore, *capturingBus, *storeDeps) {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	bus := &capturingBus{}
	store.Bus = bus
	return store, bus, &storeDeps{benchDeps: newBenchDeps(), store: store}
}

func typesOf(evts []marotte.ServerEvent) []marotte.EventType {
	out := make([]marotte.EventType, len(evts))
	for i, e := range evts {
		out[i] = e.Type
	}
	return out
}

func chatStamps(evts []marotte.ServerEvent) []marotte.ServerEvent {
	var out []marotte.ServerEvent
	for _, e := range evts {
		if e.Subject != nil && e.Subject.Kind == "chat" {
			out = append(out, e)
		}
	}
	return out
}

// TestSettleComposerOnPrompt_OnlyDraftChangedCarriesTheStamp: draft_changed follows the header
// frame and is the ONLY chat-stamped frame, or a client that lost draft_changed would hold the sent
// text at a version the digest calls unchanged.
func TestSettleComposerOnPrompt_OnlyDraftChangedCarriesTheChatStamp(t *testing.T) {
	store, bus, _ := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetDraft(t.Context(), "c1", "the message about to be sent"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	bus.events = nil

	settleComposerOnPrompt(t.Context(), store, bus, "c1", &marotte.PromptCommand{
		Text: "the message about to be sent", MessageID: "m-1",
	})

	got := typesOf(bus.events)
	want := []marotte.EventType{marotte.EventChatUpdated, marotte.EventDraftChanged}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("broadcast order = %v, want %v", got, want)
	}
	stamped := chatStamps(bus.events)
	if len(stamped) != 1 || stamped[0].Type != marotte.EventDraftChanged {
		t.Fatalf("chat-stamped frames = %v, want exactly draft_changed", typesOf(stamped))
	}
	if stamped[0].Subject.Ref != "c1" || stamped[0].Subject.Version == "" {
		t.Errorf("draft_changed Subject = %+v, want {chat c1 <version>}", *stamped[0].Subject)
	}
	if hdr := bus.events[0].Subject; hdr != nil && hdr.Kind == "chat" {
		t.Errorf("the header frame carries a chat stamp %+v; it completes the chats projection, not the transcript", *hdr)
	}
}

// A retried prompt finds the name set and the composer spent, so the mutator
// declines, no version is minted and nothing is broadcast.
func TestSettleComposerOnPrompt_ASecondSettleBroadcastsNothing(t *testing.T) {
	store, bus, _ := storeAndBus(t)
	seedDefaultNamedChat(t, store, "c1")
	p := &marotte.PromptCommand{Text: "once", MessageID: "m-1"}
	settleComposerOnPrompt(t.Context(), store, bus, "c1", p)
	bus.events = nil

	settleComposerOnPrompt(t.Context(), store, bus, "c1", p)
	if len(bus.events) != 0 {
		t.Errorf("a retried prompt broadcast %v, want nothing: the mutator declined and no version was minted", typesOf(bus.events))
	}
}

func TestBroadcastComposer_StampsFromTheStateVersion(t *testing.T) {
	bus := &capturingBus{}
	broadcastComposer(t.Context(), bus, "c1", &marotte.ComposerState{Text: "draft", Version: "42"})
	if len(bus.events) != 1 {
		t.Fatalf("broadcast %d frames, want 1", len(bus.events))
	}
	want := marotte.SubjectStamp{Kind: "chat", Ref: "c1", Version: "42"}
	if bus.events[0].Type != marotte.EventDraftChanged || bus.events[0].Subject == nil || *bus.events[0].Subject != want {
		t.Errorf("draft_changed = %s with Subject %+v, want Subject %+v", bus.events[0].Type, bus.events[0].Subject, want)
	}
}

// TestCmdSetDraft_DraftChangedCarriesTheStoresVersion drives the command itself so
// the stamp is the one the store minted under its lock, not one the command read.
func TestCmdSetDraft_DraftChangedCarriesTheStoresVersion(t *testing.T) {
	store, bus, deps := storeAndBus(t)
	seedEmptyChat(t, store, "c1")
	bus.events = nil

	if _, err := cmdSetDraft(t.Context(), deps, bus, draftReq(t, "c1", "half a thought")); err != nil {
		t.Fatalf("CmdSetDraft: %v", err)
	}
	if len(bus.events) != 1 || bus.events[0].Type != marotte.EventDraftChanged {
		t.Fatalf("broadcasts = %v, want exactly draft_changed", typesOf(bus.events))
	}
	state, err := store.SetDraft(t.Context(), "c1", "the next thought")
	if err != nil || state == nil {
		t.Fatalf("SetDraft: %v", err)
	}
	got := bus.events[0].Subject
	if got == nil || got.Kind != "chat" || got.Ref != "c1" || got.Version == "" || got.Version == state.Version {
		t.Errorf("draft_changed Subject = %+v, want {chat c1 <the version of the command's own write>}, distinct from the later write's %q", got, state.Version)
	}
}
