package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	for name, data := range map[string][]byte{headerFileName: header, EntriesFileName: []byte(log.String())} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("stamp %s: %v", path, err)
		}
	}
}

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
		name     string
		items    int
		title    int
		docChars int
		want     float64
	}{
		{name: "items_divided_by_the_normaliser", items: 2, title: 0, docChars: 3 * 1024, want: 1},
		{name: "a_title_weighed_by_the_boost", items: 0, title: 1, docChars: 0, want: 10},
		{name: "both_terms_added", items: 2, title: 1, docChars: 3 * 1024, want: 11},
		{name: "no_hits_at_all", items: 0, title: 0, docChars: 3 * 1024, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scoreChat(tc.items, tc.title, tc.docChars); got != tc.want {
				t.Errorf("scoreChat(%d, %d, %d) = %v, want %v", tc.items, tc.title, tc.docChars, got, tc.want)
			}
		})
	}
}

// A title counts when it holds every term in any order; `file:x` names no title text.
func TestTitleAll(t *testing.T) {
	cases := []struct {
		name, title, query string
		want               int
	}{
		{"a filter-only query matches no title", "my file notes", "file:main.go", 0},
		{"free text beside a filter still matches", "Redis migration", "redis file:main.go", 1},
		{"every term in any order", "Redis migration", "migration redis", 1},
		{"one term missing", "Redis migration", "redis cache", 0},
		{"accents fold", "Sécurité deadset", "securite", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := titleAll(c.title, c.query); got != c.want {
				t.Errorf("titleAll(%q, %q) = %d, want %d", c.title, c.query, got, c.want)
			}
		})
	}
}

func TestSearchAll(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache to redis today")
	seedChatEntries(t, s, "c-bbbbbbbb", "Grocery list", "nothing relevant here at all")

	got := s.searchAll(t.Context(), "redis")
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

// Hits and score count every matching item, not the capped hit list: 250 mentions read "250". The best hit is in
// the capped list.
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

	got := s.searchAll(t.Context(), "needle")
	if len(got.Matches) != 2 {
		t.Fatalf("SearchAll(needle) = %d matches, want 2 (%+v)", len(got.Matches), got.Matches)
	}
	first, second := got.Matches[0], got.Matches[1]
	if first.ID != "c-aaaaaaaa" {
		t.Errorf("the chat with %d mentions must outrank the one with %d, got %s first (scores %v, %v)",
			busier, maxSearchHits, first.ID, first.Score, second.Score)
	}
	if first.Hits != busier {
		t.Errorf("Hits = %d, want %d (every item, not the %d-hit list)", first.Hits, busier, maxSearchHits)
	}
	if second.Hits != maxSearchHits {
		t.Errorf("Hits = %d, want %d", second.Hits, maxSearchHits)
	}
	if first.Best == nil || first.Best.Turn != 1 {
		t.Errorf("Best = %+v, want a hit in turn 1", first.Best)
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

	got := s.searchAll(t.Context(), "redis")
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
				if err := os.Mkdir(filepath.Join(dir, EntriesFileName), 0o700); err != nil {
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

			got := s.searchAll(t.Context(), "redis")

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

// A name-only match carries no best hit: a zero hit would claim item kind "", which the decoder refuses.
func TestSearchAll_TitleOnlyMatchCarriesNoBestHit(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Redis migration", "we moved the cache today")

	got := s.searchAll(t.Context(), "redis")
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

	got := s.searchAll(t.Context(), "needle")
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
	got := s.searchAll(ctx, "redis")

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

	got := s.searchAll(t.Context(), "redis")

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
		got := s.searchAll(t.Context(), q)
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

	got := s.searchAll(t.Context(), "redis")
	if len(got.Matches) < 2 {
		t.Fatalf("expected both chats to match, got %+v", got.Matches)
	}
	if got.Matches[0].ID != "c-aaaaaaaa" {
		t.Errorf("the titled chat must rank first, got %s (scores: %v, %v)",
			got.Matches[0].ID, got.Matches[0].Score, got.Matches[1].Score)
	}
}

// Cross-chat search ignores case, in the body scan and in titleAll, which folds independently.
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

			got := s.searchAll(t.Context(), tc.query)
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
	var f searchAllSignature = s.searchAll
	if got := f(t.Context(), ""); len(got.Matches) != 0 {
		t.Errorf("empty query must return no matches, got %+v", got.Matches)
	}
}

// searchAllSignature is what handleSearchAll forwards to: a context and a query, no case flag.
type searchAllSignature func(context.Context, string) SearchAllResult

// The row shows the item whose terms sit closest together; a tie keeps the earliest, so the line is stable.
func TestSearchAll_BestIsTheTightestItem(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "seeded",
		"deadset "+strings.Repeat("filler words ", 20)+"security failure",
		"the deadset security failure",
		"deadset security failure again")
	got := s.searchAll(t.Context(), "deadset security failure")
	if len(got.Matches) != 1 || got.Matches[0].Best == nil {
		t.Fatalf("SearchAll = %+v, want one match with a best hit", got.Matches)
	}
	if ex := got.Matches[0].Best.Excerpt; ex != "the deadset security failure" {
		t.Errorf("Best excerpt = %q, want the tightest item, the earlier of the two tied ones", ex)
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

	got := s.searchAll(t.Context(), "stalewordxyz")

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

// History finds the chat the user's words describe, in any order inside one message, and shows that message.
func TestSearchAllAnyOrder(t *testing.T) {
	s, _ := newTestStore(t)
	seedChatEntries(t, s, "c-aaaaaaaa", "Weekly review", "unrelated", "a security failure hit deadset last night")
	seedChatEntries(t, s, "c-bbbbbbbb", "Notes", "deadset security notes")

	got := s.searchAll(t.Context(), "deadset security failure")
	if len(got.Matches) != 1 || got.Matches[0].ID != "c-aaaaaaaa" {
		t.Fatalf("SearchAll = %+v, want only the chat holding all three words in one message", got.Matches)
	}
	m := got.Matches[0]
	if m.Spread || m.Hits != 1 || m.Best == nil || !strings.Contains(m.Best.Excerpt, "deadset") {
		t.Errorf("match = %+v (best %+v), want one item hit whose excerpt shows the message", m, m.Best)
	}
}

// A chat holding the words only across messages ranks below every chat holding them in one, whatever the scores.
func TestSearchAllSpreadRanksLast(t *testing.T) {
	s, _ := newTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	padding := strings.Repeat("context and discussion that surrounds the mention. ", 400)
	item, itemEntries := oneTurnChat("c-aaaaaaaa", "Long chat", "deadset security failure. "+padding, base.UnixMilli())
	spreadEntries, _ := chatOf(openTurn("c-bbbbbbbb-t1", 1, prompt("m-1", "deadset")).
		text("a1", "security").text("a2", "failure"))
	spread := &marotte.Chat{ID: "c-bbbbbbbb", Name: "Short chat", UpdatedAt: base.Add(time.Hour).UnixMilli(), TurnCount: 1}
	seedChatDir(t, s, item, itemEntries, base)
	seedChatDir(t, s, spread, spreadEntries, base.Add(time.Hour))

	got := s.searchAll(t.Context(), "deadset security failure")
	if len(got.Matches) != 2 {
		t.Fatalf("SearchAll = %+v, want both chats", got.Matches)
	}
	if got.Matches[0].ID != "c-aaaaaaaa" || got.Matches[1].ID != "c-bbbbbbbb" || !got.Matches[1].Spread || got.Matches[0].Spread {
		t.Errorf("order = %s (spread %v), %s (spread %v); want the one-message chat first and the spread chat last",
			got.Matches[0].ID, got.Matches[0].Spread, got.Matches[1].ID, got.Matches[1].Spread)
	}
}

// A title holding every word ranks with the one-message matches, even when the body only spreads them.
func TestSearchAllTitleMatchIsNotSpread(t *testing.T) {
	s, _ := newTestStore(t)
	entries, _ := chatOf(openTurn("c-aaaaaaaa-t1", 1, prompt("m-1", "deadset")).
		text("a1", "security").text("a2", "failure"))
	seedChatDir(t, s, &marotte.Chat{ID: "c-aaaaaaaa", Name: "Deadset security failure", TurnCount: 1}, entries, time.Now())

	got := s.searchAll(t.Context(), "deadset security failure")
	if len(got.Matches) != 1 || got.Matches[0].Spread {
		t.Errorf("SearchAll = %+v, want the titled chat as a one-place match, not a spread one", got.Matches)
	}
}

// History and Find in chat apply one rule: a chat matches iff its own search has hits, and Best is one of them.
func TestSearchAllAgreesWithChatSearch(t *testing.T) {
	s, _ := newTestStore(t)
	bodies := map[marotte.ChatID][]string{
		"c-aaaaaaaa": {"security failure in deadset"},
		"c-bbbbbbbb": {"deadset", "security failure"},
		"c-cccccccc": {"deadset security"},
		"c-dddddddd": {"La Sécurité de deadset: failure"},
	}
	for id, replies := range bodies {
		seedChatEntries(t, s, id, "seeded", replies...)
	}
	for _, q := range []string{"deadset security failure", `"security failure"`, "securite deadset", "failure"} {
		t.Run(q, func(t *testing.T) {
			got := s.searchAll(t.Context(), q)
			byID := make(map[marotte.ChatID]Match, len(got.Matches))
			for _, m := range got.Matches {
				byID[m.ID] = m
			}
			for id := range bodies {
				own := searchVia(t, s, id, "?q="+url.QueryEscape(q))
				m, listed := byID[id]
				if listed != (len(own.Matches) > 0) {
					t.Errorf("%s: History lists it %v, its own search has %d hits", id, listed, len(own.Matches))
					continue
				}
				if !listed {
					continue
				}
				if m.Hits != own.Matched || m.Spread != own.Spread {
					t.Errorf("%s: History hits %d spread %v, own search matched %d spread %v", id, m.Hits, m.Spread, own.Matched, own.Spread)
				}
				if m.Best == nil || !slices.ContainsFunc(own.Matches, func(h Hit) bool {
					return h.EntryID == m.Best.EntryID && h.Kind == m.Best.Kind
				}) {
					t.Errorf("%s: Best %+v is not one of its own hits %+v", id, m.Best, own.Matches)
				}
			}
		})
	}
}
