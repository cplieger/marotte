package chat

// Cross-chat search for the History page: which conversation was it in, so it returns chats ranked by match quality,
// each with its best line. Each chat goes through the in-chat scan, so a hit here is one Ctrl-F would find; the
// candidate index (search_index.go) skips chats that cannot hold the query. This file owns the fan-out, the per-chat
// verdict (chatScan) and the ranking: every occurrence divided by the byte volume the same walk read.

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
	// maxChatResults caps the returned list.
	maxChatResults = 50
	// titleBoost multiplies title hits: titles are short and intentional, stronger evidence than a body mention.
	titleBoost = 10.0
)

// searchWorkers matches readHeadersParallel: the bound is disk, not CPU.
const searchWorkers = 8

// Match is one chat that matched, with the evidence for showing it.
type Match struct {
	// Best is the earliest hit (bestHit), the row's line and jump target; absent on a title-only match.
	Best *Hit           `json:"best,omitempty"`
	Name string         `json:"name"`
	ID   marotte.ChatID `json:"id"`
	// Hits is every occurrence the chat holds, so a row can say "and 11 more".
	Hits int `json:"hits"`
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

// SearchAll runs the per-chat search across every chat the index admits.
func (s *Store) SearchAll(ctx context.Context, query string) SearchAllResult {
	if strings.TrimSpace(query) == "" {
		return SearchAllResult{Matches: []Match{}}
	}
	entries, truncated := s.chatEntries(ctx)
	if len(entries) == 0 {
		return SearchAllResult{Matches: []Match{}, Truncated: truncated}
	}

	// Ask the filters about the free text the needle scans; a filter-only query has none, so every chat is a candidate.
	want := queryTrigrams(parseSearchQuery(query, false).text)
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

// chatScan is one chat's fan-out verdict. Whether a chat was read travels on the result, since Bounded reports only
// dispatches. The zero value is a scanned chat with no match, read or answered by its filter.
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
	res, chars := searchEntries(entries, drawn, query, false)
	// A title naming the subject is a result even if the body never repeats it.
	titles := titleHits(c.Name, query)
	if res.Matched == 0 && titles == 0 {
		return chatScan{}
	}
	m := Match{
		Name:      c.Name,
		ID:        marotte.ChatID(c.ID),
		Hits:      res.Matched,
		Score:     scoreChat(res.Matched, titles, chars),
		UpdatedAt: c.UpdatedAt,
	}
	// A title-only match has no line; the row shows the name. The capped list is in log order, so the earliest hit is in
	// it.
	if len(res.Matches) > 0 {
		best := bestHit(res.Matches)
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
	dirs, complete := chatDirs(des, s.dir)
	entries = make([]chatEntry, 0, len(dirs))
	for _, ce := range dirs {
		if ctx.Err() != nil {
			return entries, true
		}
		entries = append(entries, ce)
	}
	return entries, !complete
}

// bestHit picks the row's hit: the earliest, where the conversation first touches the subject.
func bestHit(hits []Hit) Hit {
	best := hits[0]
	for i := range hits {
		if hits[i].Turn < best.Turn {
			best = hits[i]
		}
	}
	return best
}

// scoreChat ranks a matching chat with KiroCrew's formula:
//
//	score = title_hits*titleBoost + content_hits/sqrt(1 + docChars/1024)
//
// docChars is the byte volume the scan read; the `1 +` keeps a tiny chat from dividing by nearly zero.
func scoreChat(contentHits, titleHitCount, docChars int) float64 {
	lengthNorm := math.Sqrt(1 + float64(docChars)/1024)
	return float64(titleHitCount)*titleBoost + float64(contentHits)/lengthNorm
}

// titleHits counts the query's free text in the chat name, through parseSearchQuery so a filter-only query has an
// empty needle that matches nothing.
func titleHits(name, query string) int {
	return parseSearchQuery(query, false).needle.Count(name)
}
