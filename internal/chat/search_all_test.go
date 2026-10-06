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

// seedChatEntries writes one chat through the store: header name, one prompt turn, one text entry per reply.
func seedChatEntries(t *testing.T, s *Store, id marotte.ChatID, name string, replies ...string) {
	t.Helper()
	seedSearchChat(t, s, id, name, "seed", replies...)
}

// seedChatDir writes one chat directory verbatim, bypassing the store's fsyncs for loops over hundreds of chats:
// chat.json, entries.jsonl, both stamped with mtime.
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

// oneTurnChat is a closed one-turn chat record.
func oneTurnChat(id, name, body string, updatedAt int64) (*marotte.Chat, []marotte.Entry) {
	entries, _ := chatOf(openTurn(id+"-t1", 1, prompt("m-1", "seed")).text("a1", body).
		close(marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted}))
	return &marotte.Chat{ID: id, Name: name, UpdatedAt: updatedAt, TurnCount: 1}, entries
}

// A short chat whose title names the subject outranks a long one that mentions it often.
func TestScoreChat_TitleBeatsVolume(t *testing.T) {
	titled := scoreChat(1, 1, 200)
	rambling := scoreChat(30, 0, 200_000)
	if titled <= rambling {
		t.Errorf("a titled match (%v) must outrank a long mention-heavy chat (%v)", titled, rambling)
	}
}

// Naming the subject twice counts twice.
func TestScoreChat_TitleHitsMultiply(t *testing.T) {
	once := scoreChat(0, 1, 1000)
	twice := scoreChat(0, 2, 1000)
	if twice <= once {
		t.Errorf("two title hits (%v) must score above one (%v)", twice, once)
	}
}

// The normaliser reads characters, not entries: the same hits in more text are weaker.
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

// The formula at exact values, which a NaN-returning formula cannot pass; 3 KiB gives sqrt(1+3) = 2.
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

// `file:x` names no title text, so it must not boost names containing "file".
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

// Hits and score count every occurrence, not the capped hit list: 250 mentions read "250". The best hit is in the
// log-ordered list.
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

// The denominator is the text the scan read, reasoning included, so equal prose and one mention do not tie.
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

// A refused chat is not scanned: History prints Scanned beside "no matches". It sets Truncated. Both refusals work
// under any uid, root included.
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

// A name-only match carries no best hit: a zero hit would claim segment kind "", which the decoder refuses.
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

// The list caps at maxChatResults, the count does not; Truncated stays false.
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

// A cancelled scan must not claim it read every chat: the cut walk sets truncated. The context dying between walk
// and drain is not schedulable and not pinned.
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

// An unlistable store directory truncates the reply; a regular file makes ReadDir fail with ENOTDIR under any uid.
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

// An empty query must not fan out.
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

// End to end. The long chat is realistic, tens of KiB, since the normaliser is calibrated in KiB.
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

// Cross-chat search ignores case, in the body scan and in titleHits, which folds independently.
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
			// One chat matches only in its title, the other only in its body, each in a case the queries do not use.
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

// The compile-time half: the client's toggle relies on SearchAll having no case parameter, so adding one means
// adding the History `Aa` button. handleSearchAll forwards only `q`.
func TestSearchAll_TakesNoCaseArgument(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	var f searchAllSignature = s.SearchAll
	if got := f(t.Context(), ""); len(got.Matches) != 0 {
		t.Errorf("empty query must return no matches, got %+v", got.Matches)
	}
}

// searchAllSignature is what handleSearchAll forwards to: a context and a query, no case flag.
type searchAllSignature func(context.Context, string) SearchAllResult

// The earliest hit shows; a tie within a turn keeps the first, so the excerpt is stable.
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

// Every chat is a candidate however many and however old: the word in the oldest hundred of six hundred is found
// with nothing unread. The old chats sit mid-listing so order cannot decide.
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
