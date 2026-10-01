package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// seedChatEntries writes one chat through the store: the header named name, one
// prompt turn, and one text entry per reply.
func seedChatEntries(t *testing.T, s *Store, id marotte.ChatID, name string, replies ...string) {
	t.Helper()
	seedSearchChat(t, s, id, name, "seed", replies...)
}

// seedChatDir writes one chat directory verbatim, bypassing the store so a
// seeding loop over hundreds of chats costs two file writes each and no fsync:
// chat.json holding c, entries.jsonl holding entries in file order, both stamped
// with mtime.
func seedChatDir(t *testing.T, s *Store, c *marotte.Chat, entries []marotte.Entry, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(s.dir, c.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir chat %s: %v", c.ID, err)
	}
	header, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal chat %s: %v", c.ID, err)
	}
	var log strings.Builder
	for i := range entries {
		line, err := json.Marshal(&entries[i])
		if err != nil {
			t.Fatalf("marshal entry %s: %v", entries[i].ID, err)
		}
		log.Write(line)
		log.WriteByte('\n')
	}
	for name, data := range map[string][]byte{headerFileName: header, entriesFileName: []byte(log.String())} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("stamp %s: %v", path, err)
		}
	}
}

// oneTurnChat is a closed one-turn chat record: the header and one text entry.
func oneTurnChat(id, name, body string, updatedAt int64) (*marotte.Chat, []marotte.Entry) {
	entries, _ := chatOf(openTurn(id+"-t1", 1, prompt("m-1", "seed")).text("a1", body).
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}))
	return &marotte.Chat{ID: id, Name: name, UpdatedAt: updatedAt, TurnCount: 1}, entries
}

// The ranking's whole purpose: a short chat whose TITLE names the subject must
// outrank a long one that merely mentions it many times. Without the title
// boost, volume wins and the useful result is buried.
func TestScoreChat_TitleBeatsVolume(t *testing.T) {
	titled := scoreChat(1, 1, 200)
	rambling := scoreChat(30, 0, 200_000)
	if titled <= rambling {
		t.Errorf("a titled match (%v) must outrank a long mention-heavy chat (%v)", titled, rambling)
	}
}

// Naming the subject twice counts twice, unlike a boolean flag.
func TestScoreChat_TitleHitsMultiply(t *testing.T) {
	once := scoreChat(0, 1, 1000)
	twice := scoreChat(0, 2, 1000)
	if twice <= once {
		t.Errorf("two title hits (%v) must score above one (%v)", twice, once)
	}
}

// The normaliser reads characters rather than entry count: the same hit count in
// far more text is a weaker signal, and a count cannot see the difference.
func TestScoreChat_LengthNormalisesByChars(t *testing.T) {
	short := scoreChat(3, 0, 500)
	long := scoreChat(3, 0, 500_000)
	if short <= long {
		t.Errorf("the same hits in less text must score higher: short=%v long=%v", short, long)
	}
}

func TestScoreChat_TinyChatIsNotDividedByZero(t *testing.T) {
	if got := scoreChat(1, 0, 0); got <= 0 {
		t.Errorf("an empty-length chat must still score its content hits, got %v", got)
	}
}

// The ranking formula, at exact values. Every other score test compares two
// scores, which a formula that returns NaN for every input satisfies. Inputs are
// chosen so the normaliser is exact in binary: docChars of 3 KiB gives sqrt(1+3) = 2.
func TestScoreChat_MatchesTheDocumentedFormula(t *testing.T) {
	tests := []struct {
		name        string
		contentHits int
		titleHits   int
		docChars    int
		want        float64
	}{
		{name: "content_hits_divided_by_the_normaliser", contentHits: 2, titleHits: 0, docChars: 3 * 1024, want: 1},
		{name: "title_hits_multiplied_by_the_boost", contentHits: 0, titleHits: 3, docChars: 0, want: 30},
		{name: "both_terms_added", contentHits: 2, titleHits: 1, docChars: 3 * 1024, want: 11},
		{name: "no_hits_at_all", contentHits: 0, titleHits: 0, docChars: 3 * 1024, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scoreChat(tc.contentHits, tc.titleHits, tc.docChars); got != tc.want {
				t.Errorf("scoreChat(%d, %d, %d) = %v, want %v", tc.contentHits, tc.titleHits, tc.docChars, got, tc.want)
			}
		})
	}
}

// `file:x` names no title text, so it must not boost every chat whose name
// happens to contain "file".
func TestTitleHits_IgnoresFilterOnlyQueries(t *testing.T) {
	if n := titleHits("my file notes", "file:main.go"); n != 0 {
		t.Errorf("a filter-only query must not match a title, got %d", n)
	}
	if n := titleHits("Redis migration", "redis file:main.go"); n != 1 {
		t.Errorf("free text alongside a filter must still match, got %d", n)
	}
}

func TestSearchAll(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache to redis today")
	seedChatEntries(t, s, "c-bbbbbbbb", "Grocery list", "nothing relevant here at all")

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) != 1 {
		t.Fatalf("SearchAll(redis) = %d matches, want 1 (%+v)", len(got.Matches), got.Matches)
	}
	m := got.Matches[0]
	if m.ID != "c-aaaaaaaa" {
		t.Errorf("matched the wrong chat: %s", m.ID)
	}
	if m.Name != "Redis migration" {
		t.Errorf("match must carry the chat name for the row, got %q", m.Name)
	}
	if m.Hits < 1 {
		t.Errorf("match must report its hit count, got %d", m.Hits)
	}
	if m.Best == nil || m.Best.Excerpt == "" {
		t.Errorf("match must carry a best hit with an excerpt to show, got %+v", m.Best)
	}
	if got.Scanned != 2 || got.Matched != 1 || got.Truncated {
		t.Errorf("tally = scanned %d, matched %d, truncated %v; want 2, 1, false", got.Scanned, got.Matched, got.Truncated)
	}
}

// A row's hit count and its score spend EVERY occurrence in the chat, not the
// length of the capped hit list the in-chat scan carries: a chat with 250
// mentions outranks one with 200 and reads "250", not "200". The best hit is
// untouched by the cap: the list is in log order, so the earliest turn's hit is
// always inside it.
func TestSearchAll_HitsCountEveryOccurrencePastTheHitCap(t *testing.T) {
	s, _ := newTestStore(t)
	seedMentions := func(id marotte.ChatID, n int) {
		replies := make([]string, n)
		for i := range replies {
			replies[i] = "needle here"
		}
		seedChatEntries(t, s, id, "seeded", replies...)
	}
	busier := maxSearchHits + 50
	seedMentions("c-aaaaaaaa", busier)
	seedMentions("c-bbbbbbbb", maxSearchHits)

	got := s.SearchAll(t.Context(), "needle")
	if len(got.Matches) != 2 {
		t.Fatalf("SearchAll(needle) = %d matches, want 2 (%+v)", len(got.Matches), got.Matches)
	}
	first, second := got.Matches[0], got.Matches[1]
	if first.ID != "c-aaaaaaaa" {
		t.Errorf("the chat with %d mentions must outrank the one with %d, got %s first (scores %v, %v)",
			busier, maxSearchHits, first.ID, first.Score, second.Score)
	}
	if first.Hits != busier {
		t.Errorf("Hits = %d, want %d (every occurrence, not the %d-hit list)", first.Hits, busier, maxSearchHits)
	}
	if second.Hits != maxSearchHits {
		t.Errorf("Hits = %d, want %d", second.Hits, maxSearchHits)
	}
	if first.Best == nil || first.Best.Turn != 1 {
		t.Errorf("Best = %+v, want the hit in turn 1, the earliest", first.Best)
	}
}

// The score's denominator is the text the scan READ. A chat whose reasoning
// dwarfs its prose is normalised by that reasoning: one mention in far more
// searched text is the weaker signal, whichever kind holds the bulk. Two chats
// with the same prose and the same single mention would otherwise tie and fall
// through to recency, which is what the newer, more verbose one is seeded to win.
func TestSearchAll_RankingDenominatorCoversEverySearchedSpan(t *testing.T) {
	s, _ := newTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prose := "we moved the cache to redis"
	verboseEntries, _ := chatOf(openTurn("v-t1", 1, nil).
		thinking("th1", strings.Repeat("weighing the options. ", 2000)).
		text("a1", prose))
	terseEntries, _ := chatOf(openTurn("t-t1", 1, nil).text("b1", prose))
	seedChatDir(t, s, &marotte.Chat{ID: "c-aaaaaaaa", Name: "Verbose", UpdatedAt: base.Add(time.Hour).UnixMilli(), TurnCount: 1}, verboseEntries, base.Add(time.Hour))
	seedChatDir(t, s, &marotte.Chat{ID: "c-bbbbbbbb", Name: "Terse", UpdatedAt: base.UnixMilli(), TurnCount: 1}, terseEntries, base)

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) != 2 {
		t.Fatalf("SearchAll(redis) = %d matches, want 2 (%+v)", len(got.Matches), got.Matches)
	}
	first, second := got.Matches[0], got.Matches[1]
	if first.ID != "c-bbbbbbbb" {
		t.Errorf("the terse chat must outrank the verbose one, got %s first (scores %v, %v)", first.ID, first.Score, second.Score)
	}
	if first.Score <= second.Score {
		t.Errorf("scores %v and %v: the verbose chat's reasoning must count against it, not tie", first.Score, second.Score)
	}
}

// A chat the reader refuses is NOT scanned. Scanned is the number the History
// page prints beside "no matches", so counting a chat whose contents were never
// read tells the reader their text is in none of N conversations when one of the
// N was skipped. The refusal sets Truncated instead. Both shapes are refused
// under any uid: a permission refusal would not reproduce as root.
func TestSearchAll_AnUnreadChatIsNotScanned(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, dir string)
	}{
		{
			name: "undecodable_header",
			plant: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, headerFileName), []byte("{not a chat"), 0o600); err != nil {
					t.Fatalf("write header: %v", err)
				}
			},
		},
		{
			name: "log_that_cannot_be_opened",
			plant: func(t *testing.T, dir string) {
				c, _ := oneTurnChat("c-cccccccc", "Redis too", "redis", 3000)
				header, err := json.Marshal(c)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, headerFileName), header, 0o600); err != nil {
					t.Fatalf("write header: %v", err)
				}
				// A directory at the log's name refuses the append-mode open with EISDIR.
				if err := os.Mkdir(filepath.Join(dir, entriesFileName), 0o700); err != nil {
					t.Fatalf("plant dir: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache to redis today")
			seedChatEntries(t, s, "c-bbbbbbbb", "Grocery list", "nothing relevant here at all")
			dir := filepath.Join(s.dir, "c-cccccccc")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			tc.plant(t, dir)

			got := s.SearchAll(t.Context(), "redis")

			if got.Scanned != 2 {
				t.Errorf("Scanned = %d, want 2: the unread chat is not among the chats the scan read", got.Scanned)
			}
			if !got.Truncated {
				t.Error("Truncated = false with a chat left unread, want true")
			}
			if got.Matched != 1 || len(got.Matches) != 1 || got.Matches[0].ID != "c-aaaaaaaa" {
				t.Errorf("Matched = %d, Matches = %+v, want the one readable match", got.Matched, got.Matches)
			}
		})
	}
}

// A chat matched on its NAME alone has no line inside the transcript, so its row
// carries no best hit rather than a zero one: a zero hit would claim a segment
// kind of "" on the wire, which the generated decoder refuses.
func TestSearchAll_TitleOnlyMatchCarriesNoBestHit(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache today")

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) != 1 {
		t.Fatalf("SearchAll(redis) = %d matches, want 1 (%+v)", len(got.Matches), got.Matches)
	}
	if m := got.Matches[0]; m.Best != nil || m.Hits != 0 {
		t.Errorf("title-only match = %+v, want no best hit and zero hits", m)
	}
}

// The result LIST is cut at maxChatResults and the COUNT is not, so a reader
// shown 50 rows out of 51 is told 51. Truncated stays false: every chat was
// read, and the cut is Matched exceeding the list.
func TestSearchAll_MatchedCountsPastTheResultCap(t *testing.T) {
	s, _ := newTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range maxChatResults + 1 {
		c, entries := oneTurnChat(fmt.Sprintf("chat-%03d", i), "seeded", "needle", base.Add(time.Duration(i)*time.Minute).UnixMilli())
		seedChatDir(t, s, c, entries, base.Add(time.Duration(i)*time.Minute))
	}

	got := s.SearchAll(t.Context(), "needle")
	if len(got.Matches) != maxChatResults {
		t.Errorf("len(Matches) = %d, want %d (the cap)", len(got.Matches), maxChatResults)
	}
	if got.Matched != maxChatResults+1 {
		t.Errorf("Matched = %d, want %d (every matching chat, cut or not)", got.Matched, maxChatResults+1)
	}
	if got.Scanned != maxChatResults+1 {
		t.Errorf("Scanned = %d, want %d", got.Scanned, maxChatResults+1)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false: every chat was read and the cut is Matched > len(Matches)")
	}
}

// A cancelled cross-chat scan may not claim it read every chat: an already
// cancelled context cuts the directory walk short, and without its truncated
// flag the empty list would publish an authoritative "your text is in none of
// your chats" for a scan that opened none of them. The fan-out half (the context
// dying between the walk and the drain) has no schedulable test and is not pinned.
func TestSearchAll_CancelledCollectionIsNotAnEmptyAnswer(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []marotte.ChatID{"c-aaaaaaaa", "c-bbbbbbbb", "c-cccccccc"} {
		seedChatEntries(t, s, id, "Redis migration", "we moved the cache to redis today")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got := s.SearchAll(ctx, "redis")

	if got.Scanned != 0 {
		t.Errorf("Scanned = %d over a scan whose walk was cancelled before it read anything, want 0", got.Scanned)
	}
	if !got.Truncated {
		t.Error("Truncated = false after a cancelled walk; the reader is owed the fact that their chats went unread")
	}
	if len(got.Matches) != 0 {
		t.Errorf("Matches = %d from a scan that opened nothing", len(got.Matches))
	}
}

// A store directory that cannot be listed read nothing it was asked to, so the
// reply is truncated rather than an authoritative "in none of your chats". A
// regular file stands in for the directory, so ReadDir fails with ENOTDIR under
// any uid.
func TestSearchAll_UnlistableDirIsNotAnEmptyAnswer(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache to redis today")
	notADir := filepath.Join(t.TempDir(), "chats")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	s.dir = notADir

	got := s.SearchAll(t.Context(), "redis")

	if len(got.Matches) != 0 || got.Scanned != 0 || got.Matched != 0 {
		t.Errorf("SearchAll over an unlistable dir = %d matches, scanned %d, matched %d; want 0/0/0",
			len(got.Matches), got.Scanned, got.Matched)
	}
	if !got.Truncated {
		t.Error("Truncated = false over a directory the scan could not list, want true")
	}
}

// An empty query must not fan out over every chat for nothing.
func TestSearchAll_EmptyQuery(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis", "redis")
	for _, q := range []string{"", "   "} {
		got := s.SearchAll(t.Context(), q)
		if len(got.Matches) != 0 || got.Scanned != 0 {
			t.Errorf("SearchAll(%q) = %d matches over %d scanned chats, want 0 and 0", q, len(got.Matches), got.Scanned)
		}
	}
}

// The end-to-end form of the ranking test. The long chat has to be REALISTIC
// (tens of KiB, like a real transcript) because the normaliser is calibrated in
// KiB: at a few hundred characters it barely discounts, and twenty mentions in a
// tiny document legitimately IS a strong signal.
func TestSearchAll_RanksTitleMatchFirst(t *testing.T) {
	s, _ := newTestStore(t)
	padding := strings.Repeat("context and discussion that surrounds the mention. ", 40)
	many := make([]string, 20)
	for i := range many {
		many[i] = "some long passage mentioning redis in passing. " + padding
	}
	seedChatEntries(t, s, "c-bbbbbbbb", "Assorted debugging", many...)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "moved the cache")

	got := s.SearchAll(t.Context(), "redis")
	if len(got.Matches) < 2 {
		t.Fatalf("expected both chats to match, got %+v", got.Matches)
	}
	if got.Matches[0].ID != "c-aaaaaaaa" {
		t.Errorf("the titled chat must rank first, got %s (scores: %v, %v)",
			got.Matches[0].ID, got.Matches[0].Score, got.Matches[1].Score)
	}
}

// The DECISION behind the missing `case` parameter: a cross-chat "which
// conversation was that in" is asked from memory, and memory does not remember
// capitalisation. Two halves have to hold end to end, the body scan and the title
// boost, because titleHits folds independently of Search.
func TestSearchAll_IsAlwaysCaseInsensitive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		query     string
		wantChats []string
	}{
		{name: "lowercase query", query: "redis", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
		{name: "uppercase query", query: "REDIS", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
		{name: "mixed-case query", query: "ReDiS", wantChats: []string{"c-aaaaaaaa", "c-bbbbbbbb"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestStore(t)
			// One chat matches only in its TITLE, the other only in its BODY, and
			// both are spelled in a case the queries disagree with.
			seedChatEntries(t, s, "c-aaaaaaaa", "REDIS migration", "moved the cache over")
			seedChatEntries(t, s, "c-bbbbbbbb", "Assorted notes", "we touched Redis in passing")

			got := s.SearchAll(t.Context(), tc.query)
			ids := make(map[string]bool, len(got.Matches))
			for i := range got.Matches {
				ids[string(got.Matches[i].ID)] = true
			}
			for _, want := range tc.wantChats {
				if !ids[want] {
					t.Errorf("SearchAll(%q) missed chat %s; matches: %+v", tc.query, want, got.Matches)
				}
			}
		})
	}
}

// The compile-time half of the decision: the client's toggle is gated on the
// parameter's ABSENCE, so the moment SearchAll grows one the History box must
// gain its `Aa` button in the same change. handleSearchAll forwards only `q`.
func TestSearchAll_TakesNoCaseArgument(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	var f searchAllSignature = s.SearchAll
	if got := f(t.Context(), ""); len(got.Matches) != 0 {
		t.Errorf("empty query must return no matches, got %+v", got.Matches)
	}
}

// searchAllSignature is the shape handleSearchAll forwards to: a context and a
// query, and NO case flag.
type searchAllSignature func(context.Context, string) SearchAllResult

// The row shows the EARLIEST hit, where the conversation first touches the
// subject. Ties within one turn keep the first hit found, so the excerpt a result
// row shows does not move around between searches.
func TestBestHit_PicksTheEarliestTurnAndKeepsTheFirstOfATie(t *testing.T) {
	tests := []struct {
		name string
		hits []Hit
		want string
	}{
		{
			name: "lowest_turn_wins_whatever_the_order",
			hits: []Hit{{EntryID: "c", Turn: 3}, {EntryID: "a", Turn: 1}, {EntryID: "b", Turn: 2}},
			want: "a",
		},
		{
			name: "a_tie_keeps_the_first",
			hits: []Hit{{EntryID: "first", Turn: 2}, {EntryID: "second", Turn: 2}},
			want: "first",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bestHit(tc.hits).EntryID; got != tc.want {
				t.Errorf("bestHit(%+v).EntryID = %q, want %q", tc.hits, got, tc.want)
			}
		})
	}
}

// Every chat is a candidate, however many there are and however old. The oldest
// hundred of six hundred chats carry a word the rest do not; all six hundred are
// scanned, the word is found, and nothing is reported unread. The old chats sit
// at MIDDLE filename positions so the verdict cannot ride on how a directory
// listing happens to be ordered.
func TestSearchAll_ReadsEveryChatHoweverMany(t *testing.T) {
	s, _ := newTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const chats, old = 600, 100
	for i := range chats {
		body, mtime := "recentword", base.Add(time.Duration(1000+i)*time.Minute)
		if i >= 250 && i < 250+old {
			body, mtime = "stalewordxyz", base.Add(time.Duration(i-250)*time.Minute)
		}
		c, entries := oneTurnChat(fmt.Sprintf("chat-%03d", i), "seeded", body, mtime.UnixMilli())
		seedChatDir(t, s, c, entries, mtime)
	}

	got := s.SearchAll(t.Context(), "stalewordxyz")

	if got.Matched != old {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Matched = %d, want %d (the oldest hundred)", chats, got.Matched, old)
	}
	if got.Scanned != chats {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Scanned = %d, want %d", chats, got.Scanned, chats)
	}
	if got.Truncated {
		t.Errorf("SearchAll(stalewordxyz) over %d chats: Truncated = true, want false: every chat was read", chats)
	}
}
