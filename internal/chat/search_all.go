package chat

// Cross-chat search: the History page's box, answering "which conversation was
// that in". A DIFFERENT question from the in-chat Ctrl-F, so this returns CHATS
// ranked by match quality, each with its single best line, rather than every hit.
// Lexical, fanning the per-chat scan out over the bounded-parallel reader, with
// the candidate index (search_index.go) deciding which chats are read at all: a
// chat whose filter cannot hold the query is not opened, and one it admits is
// scanned exactly as it would be without the index.
//
// MATCHING is not this file's: each chat goes through the in-chat scan, so a body
// and a title fold through the same needle and a hit here is a hit Ctrl-F would
// find. What this file owns is the fan-out, the per-chat verdict (chatScan) and
// the ranking, which spends the whole per-chat tally: Hits is every occurrence,
// and the score divides it by the byte volume of the spans the SAME walk read.
// The reply's tally counts chats; its three fields are textsearch.Tally's, and
// chatScan says which chats are read, unread, and skipped.

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

// The result cap and title boost are KiroCrew's `search_sessions(limit=50)` and
// `_TITLE_BOOST`, adopted with their values.
const (
	// maxChatResults caps the returned list. Past this a search is not a search.
	maxChatResults = 50
	// titleBoost multiplies TITLE hits: titles are short and intentional, so a hit
	// there is stronger evidence than a mention in the body.
	titleBoost = 10.0
)

// searchWorkers matches readHeadersParallel: the bound is disk, not CPU.
const searchWorkers = 8

// Match is one chat that matched, with the evidence for showing it.
type Match struct {
	// Best is the earliest hit (bestHit): the line the row shows and the jump target.
	// Absent on a title-only match, which has no line inside the transcript.
	Best *Hit           `json:"best,omitempty"`
	Name string         `json:"name"`
	ID   marotte.ChatID `json:"id"`
	// Hits is every occurrence the chat holds, so a row can say "and 11 more".
	Hits int `json:"hits"`
	// Score ranks the row; see scoreChat for what it balances.
	Score float64 `json:"score"`
	// UpdatedAt breaks ties toward the more recent conversation.
	UpdatedAt int64 `json:"updated_at"`
}

// SearchAllResult is GET /api/chats/search's reply: the ranked chats, cut at
// maxChatResults, beside the tally over the chats the answer covers.
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

	// The filters are asked about the FREE text, the string the needle scans
	// with; a filter-only query has none, so every chat is a candidate for it.
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
	// `ran` is what the fan-out actually dispatched, not how many entries it was given:
	// a context dying between chatEntries returning and this drain is caught only
	// here. A slot the fan-out never reached is zero-valued, so it is neither a match
	// nor an unread chat, and only `ran` accounts for it.
	return SearchAllResult{
		Matches:   matches,
		Scanned:   ran - unread,
		Matched:   matched,
		Truncated: truncated || ran < len(entries) || unread > 0,
	}
}

// chatScan is one chat's verdict from the fan-out. Bounded reports only how many
// entries it dispatched, so whether a chat was READ has to travel back on the
// result, or Scanned would count a file the scan never got into. The zero value is
// a scanned chat with no match: the verdict for a chat read and found empty, and
// for one its filter answered for without a read.
type chatScan struct {
	match Match
	// unread marks a chat that exists and could not be read: it is not scanned,
	// and the reply is truncated by it.
	unread bool
}

// searchOneChat scans one chat, or lets its filter answer for it: a chat whose
// filter lacks a trigram of the query cannot hold the query, so its log is not read
// and its zero verdict counts it as scanned. A chat with no filter yet is read
// through indexedRead, which records one from that read.
//
// The scan runs outside every lock; the read of an admitted chat takes the chat's
// own lock for the length of the log read, because the offset index it reads
// through is the store's. searchWorkers of them run at once, so an unlimited cap
// bounds the fan-out by nothing but the chats on disk.
func (s *Store) searchOneChat(ctx context.Context, ce chatEntry, query string, want []uint64) chatScan {
	id := marotte.ChatID(ce.id)
	if f, ok := s.index.lookup(id); ok && !f.holdsAll(want) {
		return chatScan{}
	}
	c, entries, drawn, err := s.indexedRead(ctx, ce)
	if err != nil {
		// A chat deleted since the listing is a skip the answer covers; anything
		// else left an existing chat unread, and the answer must say so.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrChatNotFound) {
			return chatScan{}
		}
		slog.Warn("chat search: skipping unreadable chat", "chat_id", ce.id, "error", err)
		return chatScan{unread: true}
	}
	// Case-INSENSITIVE always: the question is asked from memory, which drops case.
	res, chars := searchEntries(entries, drawn, query, false)
	// A TITLE naming the subject is a result even when the body never repeats the word.
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
	// A title-only match has no line to show; the row falls back to the name. The
	// hit list is capped, but it is in log order, so the earliest turn's hit is
	// always inside it.
	if len(res.Matches) > 0 {
		best := bestHit(res.Matches)
		m.Best = &best
	}
	return chatScan{match: m}
}

// indexedRead reads one chat's header, log and rail under its own lock and records
// its filter from that read when it has none. The lock is the one every writer
// holds when it appends, so a filter built here describes exactly the bytes that
// were on disk and a later append extends it under that lock rather than racing
// it. Two queries missing on one chat at once both build; the second put replaces
// an equal filter, which costs the hashing and nothing else.
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

// chatEntries lists every chat directory in the store's directory: a valid id
// holding a chat.json. A directory that cannot be listed, or a context dying
// mid-walk, cuts the list SHORT, and `truncated` is the field that says so; without
// it SearchAll publishes an authoritative "in none of your chats" for a scan that
// opened none of them.
func (s *Store) chatEntries(ctx context.Context) (entries []chatEntry, truncated bool) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Error("chat search: unreadable dir", "dir", s.dir, "error", err)
		return nil, true
	}
	entries = make([]chatEntry, 0, len(des))
	for _, ce := range chatDirs(des, s.dir) {
		if ctx.Err() != nil {
			return entries, true
		}
		entries = append(entries, ce)
	}
	return entries, false
}

// bestHit picks the hit a result row shows: the earliest one, where the conversation
// first touches the subject.
func bestHit(hits []Hit) Hit {
	best := hits[0]
	for i := range hits {
		if hits[i].Turn < best.Turn {
			best = hits[i]
		}
	}
	return best
}

// scoreChat ranks a matching chat, using KiroCrew's formula verbatim:
//
//	score = title_hits*titleBoost + content_hits/sqrt(1 + docChars/1024)
//
// The title term MULTIPLIES by the hit count, the normaliser divides by the byte
// volume of the spans the scan read, in KiB, and the `1 +` keeps a tiny chat from
// being divided by nearly zero.
func scoreChat(contentHits, titleHitCount, docChars int) float64 {
	lengthNorm := math.Sqrt(1 + float64(docChars)/1024)
	return float64(titleHitCount)*titleBoost + float64(contentHits)/lengthNorm
}

// titleHits counts the query's free text in the chat name. It goes through
// parseSearchQuery so the filter vocabulary (`file:x`) lives in one place, and a
// filter-only query leaves an empty needle, which matches nothing.
func titleHits(name, query string) int {
	return parseSearchQuery(query, false).needle.Count(name)
}
