package chat

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/cplieger/marotte/internal/marotte"
)

// A full filter admits everything, so an oversized chat is read for every query, never skipped.
func TestChatFilter_SaturatedAdmitsEveryQuery(t *testing.T) {
	f := new(chatFilter)
	for i := range f.words {
		f.words[i] = ^uint64(0)
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for range 1000 {
		key := trigramKey(rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)))
		if !f.holdsAll([]uint64{key}) {
			t.Fatalf("a saturated filter rejected trigram key %#x", key)
		}
	}
}

// One trigram sets three positions and clearing any one rejects it; the odd step keeps the count exact.
func TestChatFilter_ATrigramSetsAndDemandsThreePositions(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	for range 200 {
		key := trigramKey(rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)), rune(rng.IntN(0x110000)))
		f := new(chatFilter)
		f.add(key)
		var set []int
		for w, word := range f.words {
			for b := range 64 {
				if word&(1<<b) != 0 {
					set = append(set, w*64+b)
				}
			}
		}
		if len(set) != filterHashes {
			t.Fatalf("trigram key %#x set %d positions, want %d", key, len(set), filterHashes)
		}
		if !f.holdsAll([]uint64{key}) {
			t.Fatalf("trigram key %#x is not held by the filter it was added to", key)
		}
		for _, pos := range set {
			f.words[pos>>6] &^= 1 << (pos & 63)
			if f.holdsAll([]uint64{key}) {
				t.Fatalf("trigram key %#x held with position %d cleared", key, pos)
			}
			f.words[pos>>6] |= 1 << (pos & 63)
		}
	}
}

// Distinct trigrams never share a key: a filter rejects each single-rune variant.
func TestChatFilter_DistinctTrigramsAreDistinctKeys(t *testing.T) {
	f := new(chatFilter)
	f.addText("abc")
	for _, variant := range []string{"xbc", "axc", "abx", "cba", "bca"} {
		if f.holdsAll(queryTrigrams(variant)) {
			t.Errorf("a filter over %q holds %q", "abc", variant)
		}
	}
	keys := map[uint64]bool{}
	for _, a := range []rune{'a', 'b', 0x10FFFF} {
		for _, b := range []rune{'a', 'b', 0x10FFFF} {
			for _, c := range []rune{'a', 'b', 0x10FFFF} {
				keys[trigramKey(a, b, c)] = true
			}
		}
	}
	if len(keys) != 27 {
		t.Errorf("27 distinct trigrams packed into %d keys", len(keys))
	}
}

// Under three runes nothing is demanded; at three, one trigram of runes, case-folded. Invalid bytes collapse per run
// as NewNeedle does.
func TestQueryTrigrams_UnderThreeRunesDemandsNothing(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		wantN int
	}{
		{name: "empty", text: "", wantN: 0},
		{name: "one_rune", text: "a", wantN: 0},
		{name: "two_runes", text: "ab", wantN: 0},
		{name: "two_multibyte_runes", text: "é✓", wantN: 0},
		{name: "three_runes", text: "abc", wantN: 1},
		{name: "three_multibyte_runes", text: "é✓ß", wantN: 1},
		{name: "four_runes", text: "abcd", wantN: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(queryTrigrams(tc.text)); got != tc.wantN {
				t.Errorf("queryTrigrams(%q) has %d keys, want %d", tc.text, got, tc.wantN)
			}
		})
	}
	if a, b := queryTrigrams("AbC"), queryTrigrams("abc"); !slices.Equal(a, b) {
		t.Errorf("queryTrigrams(%q) = %v, want the folded %v", "AbC", a, b)
	}
	if a, b := queryTrigrams("a\xff\xfeb"), queryTrigrams("a\uFFFDb"); !slices.Equal(a, b) {
		t.Errorf("queryTrigrams(%q) = %v, want one U+FFFD per invalid run, %v", "a\xff\xfeb", a, b)
	}
}

// An append extends the last query's filter (the same object), so text written later is found without a rebuild.
// Every append path extends: prompt, text entry, between-turns entry.
func TestSearchIndex_AnAppendExtendsTheFilterAndTheNextQueryFindsTheNewText(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	turn := openPromptTurn(t, s, id, "m-1")
	if err := s.Append(t.Context(), id, entryOf(turn, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "alpha bravo"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	closeTurn(t, s, id, turn, marotte.TurnOutcomeCompleted)

	s.searchAll(t.Context(), "alpha")
	built, ok := s.index.lookup(id)
	if !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	if err := s.Append(t.Context(), id, entryOf(turn, "", "a2", marotte.EntryKindText, marotte.EntryText{Text: "zetaword"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.AppendBetweenTurns(t.Context(), id, entryOf("", "", "", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "betweenword", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
	})); err != nil {
		t.Fatalf("AppendBetweenTurns: %v", err)
	}
	if _, err := s.OpenTurn(t.Context(), id, &TurnSpec{
		Source: marotte.TurnOpenNamePrompt,
		Prompt: &marotte.EntryPrompt{ID: "m-2", Text: "promptword"},
	}, nil); err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}

	for _, query := range []string{"zetaword", "betweenword", "promptword"} {
		if f, _ := s.index.lookup(id); f != built {
			t.Fatalf("the filter is not the object the first query built: the append dropped or replaced it")
		}
		got := s.searchAll(t.Context(), query)
		if got.Matched != 1 || len(got.Matches) != 1 || got.Matches[0].ID != id {
			t.Errorf("SearchAll(%q) after the append: Matched = %d, Matches = %+v, want the chat", query, got.Matched, got.Matches)
		}
	}
}

// A revert drops the filter; the rebuild finds the kept turn's word but not the reverted one's.
func TestSearchIndex_ARevertDropsTheFilter(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	first := openPromptTurn(t, s, id, "m-1")
	if err := s.Append(t.Context(), id, entryOf(first, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "keptword"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	closeTurn(t, s, id, first, marotte.TurnOutcomeCompleted)
	second := openPromptTurn(t, s, id, "m-2")
	if err := s.Append(t.Context(), id, entryOf(second, "", "a2", marotte.EntryKindText, marotte.EntryText{Text: "cutword"})); err != nil {
		t.Fatalf("Append: %v", err)
	}
	closeTurn(t, s, id, second, marotte.TurnOutcomeCompleted)
	s.searchAll(t.Context(), "cutword")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	if _, _, err := s.Revert(t.Context(), id, second, ""); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if _, ok := s.index.lookup(id); ok {
		t.Error("the revert left the chat's filter standing")
	}
	if got := s.searchAll(t.Context(), "cutword"); got.Matched != 0 {
		t.Errorf("SearchAll(cutword) after the revert: Matched = %d, want 0", got.Matched)
	}
	if got := s.searchAll(t.Context(), "keptword"); got.Matched != 1 {
		t.Errorf("SearchAll(keptword) after the revert: Matched = %d, want 1", got.Matched)
	}
}

// A merge rewrite drops the filter; Reconcile's swap calls Rewrite directly.
func TestSearchIndex_ARewriteDropsTheFilter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rewrite func(t *testing.T, s *Store, id marotte.ChatID, entries []marotte.Entry)
	}{
		{name: "reconcile", rewrite: func(t *testing.T, s *Store, id marotte.ChatID, entries []marotte.Entry) {
			if _, _, err := s.Reconcile(t.Context(), id, func(l *EntryLog, _ EntryHeader) (bool, error) {
				return true, l.Rewrite(t.Context(), entries)
			}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			const id = "c-aaaaaaaa"
			turn := openPromptTurn(t, s, id, "m-1")
			if err := s.Append(t.Context(), id, entryOf(turn, "", "a1", marotte.EntryKindText, marotte.EntryText{Text: "oldword"})); err != nil {
				t.Fatalf("Append: %v", err)
			}
			closeTurn(t, s, id, turn, marotte.TurnOutcomeCompleted)
			s.searchAll(t.Context(), "oldword")
			if _, ok := s.index.lookup(id); !ok {
				t.Fatal("the first query did not record the chat's filter")
			}

			entries, err := s.All(t.Context(), id)
			if err != nil {
				t.Fatalf("All: %v", err)
			}
			entries[1].Payload = entryOf("", "", "", marotte.EntryKindText, marotte.EntryText{Text: "mergedword"}).Payload
			tc.rewrite(t, s, id, entries)
			if _, ok := s.index.lookup(id); ok {
				t.Error("the rewrite left the chat's filter standing")
			}
			if got := s.searchAll(t.Context(), "mergedword"); got.Matched != 1 {
				t.Errorf("SearchAll(mergedword) after the rewrite: Matched = %d, want 1", got.Matched)
			}
			if got := s.searchAll(t.Context(), "oldword"); got.Matched != 0 {
				t.Errorf("SearchAll(oldword) after the rewrite: Matched = %d, want 0", got.Matched)
			}
		})
	}
}

// A delete drops the filter too.
func TestSearchIndex_ADeleteDropsTheEntry(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChatEntries(t, s, id, "Notes", "alpha bravo")
	s.searchAll(t.Context(), "alpha")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	if _, err := s.Delete(t.Context(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.index.lookup(id); ok {
		t.Error("the delete left the chat's filter standing")
	}
}

// Nothing is indexed at open: the first query records a filter for every chat it read, and the next reuses them.
func TestSearchIndex_IsBuiltByTheFirstQueryNotAtOpen(t *testing.T) {
	dir := t.TempDir()
	seeder, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ids := []string{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"}
	for i, id := range ids {
		c, entries := oneTurnChat(id, "seeded", "needle here", int64(1000*(i+1)))
		seedChatDir(t, seeder, c, entries, time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC))
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for _, id := range ids {
		if _, ok := s.index.lookup(marotte.ChatID(id)); ok {
			t.Errorf("chat %s holds a filter before any query", id)
		}
	}

	s.searchAll(t.Context(), "unrelatedword")
	first := make(map[string]*chatFilter, len(ids))
	for _, id := range ids {
		f, ok := s.index.lookup(marotte.ChatID(id))
		if !ok {
			t.Errorf("chat %s holds no filter after the first query", id)
		}
		first[id] = f
	}

	s.searchAll(t.Context(), "needle")
	for _, id := range ids {
		if f, _ := s.index.lookup(marotte.ChatID(id)); f != first[id] {
			t.Errorf("chat %s was re-indexed by a second query over an unchanged file", id)
		}
	}
}

// A rejecting filter answers without a read and the chat counts as scanned. The file is corrupted after the filter
// was recorded, so a read would report it unread.
func TestSearchAll_ARejectingFilterAnswersWithoutARead(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChatEntries(t, s, id, "Notes", "alpha bravo")
	s.searchAll(t.Context(), "alpha")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}
	if err := os.WriteFile(filepath.Join(s.dir, id, headerFileName), []byte("{not a chat"), 0o600); err != nil {
		t.Fatalf("corrupt chat file: %v", err)
	}

	pruned := s.searchAll(t.Context(), "zetaword")
	if pruned.Scanned != 1 || pruned.Truncated || pruned.Matched != 0 {
		t.Errorf("SearchAll(zetaword) = scanned %d, truncated %t, matched %d; want 1, false, 0: the filter answered without a read",
			pruned.Scanned, pruned.Truncated, pruned.Matched)
	}
	admitted := s.searchAll(t.Context(), "alpha")
	if admitted.Scanned != 0 || !admitted.Truncated {
		t.Errorf("SearchAll(alpha) = scanned %d, truncated %t; want 0, true: the filter admitted the chat and the read failed",
			admitted.Scanned, admitted.Truncated)
	}
	short := s.searchAll(t.Context(), "zz")
	if short.Scanned != 0 || !short.Truncated {
		t.Errorf("SearchAll(zz) = scanned %d, truncated %t; want 0, true: under three runes every chat is read",
			short.Scanned, short.Truncated)
	}
}

// Only the free text reaches the filters: `turn:1` and collapsed whitespace would prune every chat.
func TestSearchAll_AsksTheIndexAboutTheFreeTextOnly(t *testing.T) {
	s, _ := newTestStore(t)
	const id = "c-aaaaaaaa"
	seedChatEntries(t, s, id, "Notes", "we moved the cache to redis today")
	s.searchAll(t.Context(), "unrelatedword")
	if _, ok := s.index.lookup(id); !ok {
		t.Fatal("the first query did not record the chat's filter")
	}

	for _, query := range []string{"redis turn:1", "turn:1", "redis   today", "  redis  "} {
		got := s.searchAll(t.Context(), query)
		if got.Matched != 1 {
			t.Errorf("SearchAll(%q) matched %d chats, want 1: the filter must be asked about the free text alone", query, got.Matched)
		}
	}
}

// The index costs 64 KiB per chat, a power of two so a position is a mask.
func TestChatFilter_MemoryIsSixtyFourKiBPerChat(t *testing.T) {
	if got := unsafe.Sizeof(chatFilter{}); got != 64<<10 {
		t.Errorf("sizeof(chatFilter) = %d, want %d", got, 64<<10)
	}
	if filterBits != filterBytes*8 || filterBits&(filterBits-1) != 0 {
		t.Errorf("filterBits = %d, want %d and a power of two", filterBits, filterBytes*8)
	}

	s, _ := newTestStore(t)
	ids := []marotte.ChatID{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"}
	for _, id := range ids {
		seedChatEntries(t, s, id, "Notes", "alpha bravo")
	}
	s.searchAll(t.Context(), "alpha")
	s.index.mu.Lock()
	held := len(s.index.filters)
	s.index.mu.Unlock()
	if got, want := uintptr(held)*unsafe.Sizeof(chatFilter{}), uintptr(len(ids))*64<<10; got != want {
		t.Errorf("index over %d chats holds %d bytes of filters, want %d", len(ids), got, want)
	}
}
