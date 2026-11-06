package chat

// Cross-chat search for the History page: which conversation was it in, so it returns chats ranked by match quality,
// each with its best line. Each chat goes through the in-chat scan, so a hit here is one Ctrl-F would find; the
// candidate index (search_index.go) skips chats that cannot hold the query. This file owns the fan-out, the per-chat
// verdict (chatScan) and the ranking: matching items divided by the byte volume the same walk read, spread chats
// last.

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/parallel"
	"github.com/cplieger/marotte/internal/textsearch"
)

// The result cap and title boost are KiroCrew's `search_sessions(limit=50)` and `_TITLE_BOOST`, values included.
const (
	maxChatResults = 50
	// titleBoost weighs a title holding every term: titles are short and intentional, stronger evidence than a body
	// mention.
	titleBoost = 10.0
)

// searchWorkers matches readHeadersParallel: the bound is disk, not CPU.
const searchWorkers = 8

// Match is one chat that matched, with the evidence for showing it.
type Match struct {
	// Best is the hit whose terms sit closest together (the tightest window, earliest on a tie), the row's line and
	// jump target; absent on a title-only match.
	Best *Hit           `json:"best,omitempty"`
	Name string         `json:"name"`
	ID   marotte.ChatID `json:"id"`
	// Hits is every matching item (or spread turn) the chat holds, so a row can say "and 11 more".
	Hits int `json:"hits"`
	// Spread is true when no item of the chat holds every term, only a turn between its items.
	Spread bool `json:"spread"`
	// Score ranks the row (scoreChat).
	Score float64 `json:"score"`
	// UpdatedAt breaks ties toward the more recent conversation.
	UpdatedAt int64 `json:"updated_at"`
}

// SearchAllResult is GET /api/chats/search's reply: ranked chats capped at maxChatResults, with the tally over the
// chats covered.
type SearchAllResult struct {
	Matches []Match `json:"matches"`
	textsearch.Tally
}

// searchAll runs the per-chat search across every chat the index admits.
func (s *Store) searchAll(ctx context.Context, query string) SearchAllResult {
	if strings.TrimSpace(query) == "" {
		return SearchAllResult{Matches: []Match{}}
	}
	entries, truncated := s.chatEntries(ctx)
	if len(entries) == 0 {
		return SearchAllResult{Matches: []Match{}, Truncated: truncated}
	}

	// Ask the filters about the words the matcher scans; a filter-only query has none, so every chat is a candidate.
	want := queryTrigrams(parseSearchQuery(query, false).terms)
	found := make([]chatScan, len(entries))
	ran := parallel.Bounded(ctx, entries, searchWorkers, func(idx int, ce chatEntry) {
		found[idx] = s.searchOneChat(ctx, ce, query, want)
	})

	matches := make([]Match, 0, 16)
	unread := 0
	for i := range found {
		switch {
		case found[i].unread:
			unread++
		case found[i].match.ID != "":
			matches = append(matches, found[i].match)
		}
	}
	slices.SortStableFunc(matches, func(a, b Match) int {
		return cmp.Or(
			compareBool(a.Spread, b.Spread),
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(b.UpdatedAt, a.UpdatedAt),
		)
	})
	matched := len(matches)
	if len(matches) > maxChatResults {
		matches = matches[:maxChatResults]
	}
	// `ran` is what the fan-out dispatched: a context dying after chatEntries is caught only here, since an unreached slot
	// is zero-valued.
	return SearchAllResult{
		Matches:   matches,
		Scanned:   ran - unread,
		Matched:   matched,
		Truncated: truncated || ran < len(entries) || unread > 0,
	}
}

// Whether a chat was read travels on the result, since Bounded reports only dispatches. The zero
// value is a scanned chat with no match, read or answered by its filter.
type chatScan struct {
	match Match
	// unread marks an existing chat that could not be read: not scanned, and it truncates the reply.
	unread bool
}

// searchOneChat scans one chat, or lets its filter answer: a filter lacking a query trigram cannot match, so the log
// is not read and it counts as scanned. A chat with no filter goes through indexedRead, which records one. The scan
// runs unlocked; the log read takes the chat's lock, since the offset index is the store's.
func (s *Store) searchOneChat(ctx context.Context, ce chatEntry, query string, want []uint64) chatScan {
	id := marotte.ChatID(ce.id)
	if f, ok := s.index.lookup(id); ok && !f.holdsAll(want) {
		return chatScan{}
	}
	c, entries, drawn, err := s.indexedRead(ctx, ce)
	if err != nil {
		// Deleted since the listing is a covered skip; anything else is an unread chat the answer must report.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrChatNotFound) {
			return chatScan{}
		}
		slog.Warn("chat search: skipping unreadable chat", "chat_id", ce.id, "error", err)
		return chatScan{unread: true}
	}
	// Always case-insensitive: the question is asked from memory.
	sc := searchEntries(s.searchView(entries), drawn, query, false)
	res := sc.res
	// A title naming the subject is a result even if the body never repeats it.
	title := titleAll(c.Name, query)
	if res.Matched == 0 && title == 0 {
		return chatScan{}
	}
	m := Match{
		Name:      c.Name,
		ID:        marotte.ChatID(c.ID),
		Hits:      res.Matched,
		Spread:    res.Spread,
		Score:     scoreChat(res.Matched, title, sc.chars),
		UpdatedAt: c.UpdatedAt,
	}
	// A title-only match has no line; the row shows the name.
	if sc.best >= 0 {
		best := res.Matches[sc.best]
		m.Best = &best
	}
	return chatScan{match: m}
}

// indexedRead reads one chat's header, log and rail under its lock and records its filter when it has none. Writers
// append under the same lock, so the filter matches the bytes on disk. Two concurrent builds cost only hashing.
func (s *Store) indexedRead(ctx context.Context, ce chatEntry) (c *marotte.Chat, entries []marotte.Entry, drawn map[string]struct{}, err error) {
	id := marotte.ChatID(ce.id)
	m := s.lock(id)
	m.Lock()
	defer m.Unlock()
	c, err = s.load(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	l, err := s.logFor(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	entries, err = l.All()
	if err != nil {
		return nil, nil, nil, err
	}
	drawn = drawnSet(l.RailRows())
	if _, indexed := s.index.lookup(id); !indexed {
		s.index.put(id, buildChatFilter(c.Name, entries))
	}
	return c, entries, drawn, nil
}

// chatEntries lists every chat directory (a valid id with chat.json). A failed listing or a dying context cuts it
// short and sets `truncated`, so SearchAll never claims "in none of your chats" for a scan that read none.
func (s *Store) chatEntries(ctx context.Context) (entries []chatEntry, truncated bool) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat search: unreadable dir", "dir", s.dir, "error", err)
		return nil, true
	}
	dirs, skipped := chatDirs(des, s.dir)
	entries = make([]chatEntry, 0, len(dirs))
	for _, ce := range dirs {
		if ctx.Err() != nil {
			return entries, true
		}
		entries = append(entries, ce)
	}
	return entries, len(skipped) > 0
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// scoreChat ranks a matching chat with KiroCrew's formula:
//
//	score = title_all*titleBoost + items/sqrt(1 + docChars/1024)
//
// items counts matching items (or spread turns), title_all is 1 when the title holds every term, and docChars is the
// byte volume the scan read; the `1 +` keeps a tiny chat from dividing by nearly zero.
func scoreChat(items, title, docChars int) float64 {
	lengthNorm := math.Sqrt(1 + float64(docChars)/1024)
	return float64(title)*titleBoost + float64(items)/lengthNorm
}

// titleAll is 1 when the chat name holds every term of the query's free text, else 0. Through parseSearchQuery, so
// a filter-only query has no terms and matches no title.
func titleAll(name, query string) int {
	q := parseSearchQuery(query, false)
	if q.matcher.Terms() == 0 {
		return 0
	}
	seen := make(map[int]bool)
	for _, sp := range q.matcher.Spans(name) {
		seen[sp.Term] = true
	}
	if len(seen) < q.matcher.Terms() {
		return 0
	}
	return 1
}
