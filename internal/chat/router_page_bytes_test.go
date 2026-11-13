package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestRouter_NewestPageIsBoundedByBytes(t *testing.T) {
	s := pageStore(t, false, nil)
	id := marotte.ChatID("c-bytes001")
	for i := range 20 {
		turn := openPromptTurn(t, s, id, fmt.Sprintf("m-%d", i))
		e := entryOf(turn, "", turn+":text", marotte.EntryKindText,
			marotte.EntryText{Text: strings.Repeat("z", 256<<10)})
		if err := s.Append(t.Context(), id, e); err != nil {
			t.Fatalf("Append(text): %v", err)
		}
		closeTurn(t, s, id, turn, marotte.TurnOutcomeCompleted)
	}

	page := getPage(t, s, id, "")
	opened := map[string]bool{}
	closed := map[string]bool{}
	for _, e := range page.Entries {
		switch e.Kind {
		case marotte.EntryKindTurnOpen:
			opened[e.Turn] = true
		case marotte.EntryKindTurnClose:
			closed[e.Turn] = true
		}
	}
	if len(opened) == 0 || len(opened) >= 20 {
		t.Errorf("a page over 5 MB of turns served %d turns, want fewer than 20 and at least one", len(opened))
	}
	if !page.HasMore {
		t.Error("has_more is false with older turns left out of the page")
	}
	for turn := range opened {
		if !closed[turn] {
			t.Errorf("turn %s was served without its turn_close: a page never splits a turn", turn)
		}
	}
}
