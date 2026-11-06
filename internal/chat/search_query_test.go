package chat

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The user's own query: every word in one message or header, in any order.
func TestSearchAllTermsInOneItem(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, prompt("m-1", "why did the failure in deadset look like security")).
			text("s1#0", "A security failure ").
			text("s1#1", "hit deadset last night.").
			text("s2", "The deadset security notes are fine.").
			close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}),
	)
	res := search(entries, drawn, "deadset security failure", false)
	if res.Spread {
		t.Errorf("Spread = true, want a match inside one item")
	}
	type key struct {
		entry string
		kind  ItemKind
	}
	var got []key
	for _, h := range res.Matches {
		got = append(got, key{h.EntryID, h.Kind})
	}
	want := []key{{"t-1", itemHeader}, {"s1#0", itemMessage}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hits = %+v, want the header and the one message holding all three words (the run of two entries is one item, the two-word message none)", got)
	}
	if res.Matched != 2 {
		t.Errorf("Matched = %d, want 2 items", res.Matched)
	}
}

func TestSearchPhraseInOrder(t *testing.T) {
	entries, drawn := chatOf(openTurn("t-1", 1, nil).
		text("a1", "a security\n  failure in order").
		text("a2", "the failure was not security"))
	hits := search(entries, drawn, `"security failure"`, false).Matches
	if len(hits) != 1 || hits[0].EntryID != "a1" {
		t.Fatalf("hits = %+v, want only a1, where the phrase is in order", hits)
	}
}

func TestSearchSpreadFallback(t *testing.T) {
	entries, drawn := chatOf(
		openTurn("t-1", 1, nil).
			text("a1", "deadset first").
			thinking("r1", "then security").
			text("a2", "and a failure"),
		openTurn("t-2", 2, nil).text("b1", "deadset alone"),
	)
	res := search(entries, drawn, "deadset security failure", false)
	if !res.Spread {
		t.Errorf("Spread = false, want the turn fallback when no item holds every word")
	}
	if len(res.Matches) != 1 || res.Matches[0].Kind != itemTurn || res.Matches[0].TurnID != "t-1" {
		t.Fatalf("hits = %+v, want one turn hit on t-1", res.Matches)
	}
	if res.Matched != 1 {
		t.Errorf("Matched = %d, want 1 turn", res.Matched)
	}
}

func TestSearchAccentFold(t *testing.T) {
	entries, drawn := oneText("La Sécurité du système")
	if hits := search(entries, drawn, "securite systeme", false).Matches; len(hits) != 1 {
		t.Errorf("insensitive hits = %+v, want the accented message", hits)
	}
	if hits := search(entries, drawn, "securite", true).Matches; len(hits) != 0 {
		t.Errorf("case-sensitive hits = %+v, want none: Aa makes matching literal", hits)
	}
}
