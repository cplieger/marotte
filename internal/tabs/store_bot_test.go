package tabs

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// botOf reads tab id's Bot fact from a list, failing the test when the tab is missing.
func botOf(t *testing.T, tabs []marotte.TabSubject, id string) bool {
	t.Helper()
	i := indexOfID(tabs, id)
	if i < 0 {
		t.Fatalf("tab %q is not in the list %v", id, idsOf(tabs))
	}
	return tabs[i].Bot
}

// TestOpen_CopiesTheBotFactOntoTheSubject pins that the store keeps what the coordinator derived: the client sorts a
// tab into the Bots section from this fact alone, so a dropped copy files a Bot among the open chats.
func TestOpen_CopiesTheBotFactOntoTheSubject(t *testing.T) {
	s, dir := newTestStore(t)
	bot := mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Bot: true})
	plain := mustOpen(t, s, chatSpec("c-plain"))

	if !bot.Bot {
		t.Error("Open(Bot: true) returned a subject with Bot false")
	}
	if plain.Bot {
		t.Error("Open of a regular chat returned a subject with Bot true")
	}
	if doc := onDisk(t, dir); !botOf(t, doc.Tabs, bot.ID) || botOf(t, doc.Tabs, plain.ID) {
		t.Errorf("tabs.json = %+v, want bot true on %q only", doc.Tabs, bot.ID)
	}

	again, created, _, err := s.Open(t.Context(), chatSpec("c-bot"))
	if err != nil || created {
		t.Fatalf("Open(c-bot) again = (created %v, %v), want the open tab", created, err)
	}
	if !again.Bot {
		t.Error("an idempotent open of the Bot's chat returned Bot false; the fact is set once at open")
	}
}

// TestBotFact_SurvivesEveryLaterTabWrite pins the "set once at open, never changed" contract across the writes that
// rebuild or move a subject: a reorder, a reparent of another tab under the Bot tab, and a reload of tabs.json.
func TestBotFact_SurvivesEveryLaterTabWrite(t *testing.T) {
	s, dir := newTestStore(t)
	bot := mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-bot", Bot: true})
	other := mustOpen(t, s, chatSpec("c-other"))
	spec := mustOpen(t, s, marotte.OpenTab{Kind: marotte.TabKindSpec, Ref: ".kiro/specs/x"})

	if _, err := s.Reorder(t.Context(), []string{other.ID, spec.ID, bot.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if tabs, _ := s.List(); !botOf(t, tabs, bot.ID) {
		t.Error("the Bot fact was lost by a reorder")
	}

	if _, err := s.Reparent(t.Context(), spec.ID, bot.ID); err != nil {
		t.Fatalf("Reparent(spec under the Bot tab): %v", err)
	}
	tabs, _ := s.List()
	if !botOf(t, tabs, bot.ID) {
		t.Error("the Bot fact was lost when another tab was reparented under the Bot tab")
	}
	if botOf(t, tabs, spec.ID) {
		t.Error("a tab reparented under a Bot tab gained the Bot fact")
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dir, err)
	}
	stored, _ := reloaded.List()
	if !botOf(t, stored, bot.ID) {
		t.Error("the Bot fact was lost by a reload of tabs.json")
	}
	if botOf(t, stored, other.ID) || botOf(t, stored, spec.ID) {
		t.Errorf("a reload gave a regular tab the Bot fact: %+v", stored)
	}
}
