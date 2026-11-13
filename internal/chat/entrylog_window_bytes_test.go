package chat

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// noByteBound is the window bound for a test about turn selection, not bytes.
const noByteBound = math.MaxInt64

// bulkyTurns writes n closed turns, each carrying one text entry of about size bytes, and returns their ids oldest
// first.
func bulkyTurns(f *logFixture, n, size int) []string {
	f.t.Helper()
	var ids []string
	for i := range n {
		turn := f.prompt(fmt.Sprintf("p%d", i))
		f.append(turn, "", fmt.Sprintf("bulk-%d", i), marotte.EntryKindText,
			marotte.EntryText{Text: strings.Repeat("x", size)})
		f.closeTurn(turn, marotte.TurnOutcomeCompleted)
		ids = append(ids, turn)
	}
	return ids
}

func TestEntryLog_WindowStopsAtTheByteBound(t *testing.T) {
	f := newLogFixture(t)
	ids := bulkyTurns(f, 5, 400<<10)

	w, err := f.log.window(20, "", 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if got, want := slices.Compact(turnsOf(w.Entries)), ids[3:]; !slices.Equal(got, want) {
		t.Errorf("the byte-bounded page holds turns %v, want the newest two %v", got, want)
	}
	if !w.HasMore {
		t.Error("has_more is false with three older turns outside the page")
	}

	older, err := f.log.window(20, ids[3], 1<<20)
	if err != nil {
		t.Fatalf("window before: %v", err)
	}
	if got, want := slices.Compact(turnsOf(older.Entries)), ids[1:3]; !slices.Equal(got, want) {
		t.Errorf("the ?before page holds turns %v, want %v", got, want)
	}
	if !older.HasMore {
		t.Error("the ?before page reports nothing older with one turn left")
	}
}

func TestEntryLog_WindowServesAnOversizedTurnWhole(t *testing.T) {
	f := newLogFixture(t)
	small := bulkyTurns(f, 2, 1<<10)
	giant := f.prompt("giant")
	for i := range 4 {
		f.append(giant, "", fmt.Sprintf("g-%d", i), marotte.EntryKindText,
			marotte.EntryText{Text: strings.Repeat("y", 512<<10)})
	}
	f.closeTurn(giant, marotte.TurnOutcomeCompleted)

	w, err := f.log.window(20, "", 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if got := slices.Compact(turnsOf(w.Entries)); !slices.Equal(got, []string{giant}) {
		t.Fatalf("a 2 MB newest turn served turns %v, want that turn alone", got)
	}
	var texts int
	for _, e := range w.Entries {
		if e.Kind == marotte.EntryKindText {
			texts++
		}
	}
	if texts != 4 {
		t.Errorf("the oversized turn came back with %d of its 4 text entries; a turn is never split", texts)
	}
	if !w.HasMore {
		t.Errorf("has_more is false with %d older turns outside the page", len(small))
	}
}

func TestEntryLog_WindowKeepsTheTurnCountUnderTheBound(t *testing.T) {
	f := newLogFixture(t)
	ids := bulkyTurns(f, 5, 1<<10)

	w, err := f.log.window(3, "", 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if got, want := slices.Compact(turnsOf(w.Entries)), ids[2:]; !slices.Equal(got, want) {
		t.Errorf("small turns under the byte bound served %v, want the newest three %v", got, want)
	}
}
