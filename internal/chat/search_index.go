package chat

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// The cross-chat candidate index: one Bloom filter per chat over the rune trigrams
// of what the per-chat scan searches (the entrySegments spans of every turn plus
// the title, through textsearch.Fold). It PRUNES and never decides: a Bloom
// filter has no false negatives, so a chat holding the query is always read and
// scanned exactly as an unindexed one, and a saturated filter admits everything.
// In memory only: the first query to read a chat builds its filter from that read,
// every append EXTENDS it under the per-chat lock, and a rewind's truncate, a merge
// rewrite, a header write (the title is indexed) and Remove DROP it to be rebuilt.

const (
	// filterBytes is one chat's filter, and so the index's cost per chat.
	filterBytes = 64 << 10
	filterWords = filterBytes / 8
	filterBits  = filterBytes * 8
	// filterHashes is the positions set per trigram, taken by double hashing
	// from the two halves of one 64-bit hash.
	filterHashes = 3
	trigramRunes = 3
)

// chatFilter is one chat's Bloom filter. It is complete when it enters the index
// and grows by every append after, so a lookup hands out a pointer the appender is
// still writing to; a query reads it outside the chat's lock. Every bit is set by
// an atomic OR and read by an atomic load, so the two never race, and a filter
// only ever gains bits: a query that reads a word before the append's OR sees the
// log as it was before that append, which is the answer a scan taken before the
// append would give.
type chatFilter struct {
	words [filterWords]uint64
}

// buildChatFilter indexes one chat: its title and every span of every turn,
// folded as the scan folds them. The scan skips an undrawn turn, but drawn is a
// bit the appender flips, and a filter built while a turn was undrawn outlives
// the append that draws it; indexing every turn is what keeps the filter a
// superset of the scan whatever the bit does after the build. A trigram never
// straddles two spans, because a hit never does.
func buildChatFilter(name string, entries []marotte.Entry) *chatFilter {
	f := new(chatFilter)
	f.addText(name)
	turns := indexSearchTurns(entries, nil)
	for i := range entries {
		for _, seg := range entrySegments(&entries[i], turns[entries[i].Turn]) {
			f.addText(seg.text)
		}
	}
	return f
}

// addEntry records one appended entry's segments. The entry is read into a turn of
// its own, so a tool_call's input is indexed whole: the tool_result whose diff drops
// a leaf from that input at query time has not landed yet, and a filter may hold
// more than the scan reads, never less. An undrawn turn's entry is indexed too;
// the scan skips it, at the cost of one read that finds nothing.
func (f *chatFilter) addEntry(e *marotte.Entry) {
	t := newSearchTurn(false)
	t.observe(e)
	for _, seg := range entrySegments(e, t) {
		f.addText(seg.text)
	}
}

// addText records every rune trigram of one folded span.
func (f *chatFilter) addText(s string) {
	var a, b rune
	seen := 0
	for _, r := range textsearch.Fold(s) {
		seen++
		if seen >= trigramRunes {
			f.add(trigramKey(a, b, r))
		}
		a, b = b, r
	}
}

// add sets the trigram's positions.
func (f *chatFilter) add(key uint64) {
	h1, h2 := splitHash(key)
	for i := range uint64(filterHashes) {
		pos := (h1 + i*h2) & (filterBits - 1)
		atomic.OrUint64(&f.words[pos>>6], 1<<(pos&63))
	}
}

// holdsAll reports whether every key's positions are set. No keys means no
// demand, so it holds: that is how a query under three runes makes every chat a
// candidate.
func (f *chatFilter) holdsAll(keys []uint64) bool {
	for _, key := range keys {
		h1, h2 := splitHash(key)
		for i := range uint64(filterHashes) {
			pos := (h1 + i*h2) & (filterBits - 1)
			if atomic.LoadUint64(&f.words[pos>>6])&(1<<(pos&63)) == 0 {
				return false
			}
		}
	}
	return true
}

// A rune is at most 21 bits, so three fit in one key with room to spare.
const (
	runeBits = 21
	runeMask = 1<<runeBits - 1
)

// trigramKey packs three runes into one key; distinct trigrams never share one.
func trigramKey(a, b, c rune) uint64 {
	return uint64(a&runeMask)<<(2*runeBits) | uint64(b&runeMask)<<runeBits | uint64(c&runeMask)
}

// splitHash is the one 64-bit hash of a trigram, split into the two halves the
// positions are derived from. The key is three packed fields, so it goes through
// a finalizer with full avalanche (splitmix64's) before the split, or the halves
// would carry the runes' own structure. The step is forced odd so the three
// positions can never collapse onto one.
func splitHash(key uint64) (h1, h2 uint64) {
	h := key
	h = (h ^ (h >> 30)) * 0xbf58476d1ce4e5b9
	h = (h ^ (h >> 27)) * 0x94d049bb133111eb
	h ^= h >> 31
	return h & (1<<32 - 1), h>>32 | 1
}

// queryTrigrams returns the trigram keys of the free text the needle will scan
// for, or nil when that text is under three runes.
//
// The text is prepared exactly as NewNeedle prepares it: invalid UTF-8 repaired
// to one U+FFFD per run BEFORE folding. Folding the raw text instead would give
// such a run one U+FFFD per byte, and a filter asked for a trigram the needle
// never scans with could reject a chat the scan matches.
func queryTrigrams(text string) []uint64 {
	runes := []rune(textsearch.Fold(strings.ToValidUTF8(text, "\uFFFD")))
	if len(runes) < trigramRunes {
		return nil
	}
	keys := make([]uint64, 0, len(runes)-trigramRunes+1)
	for i := range len(runes) - trigramRunes + 1 {
		keys = append(keys, trigramKey(runes[i], runes[i+1], runes[i+2]))
	}
	return keys
}

// searchIndex holds the filters by chat id. The zero value is ready to use.
type searchIndex struct {
	filters map[marotte.ChatID]*chatFilter
	mu      sync.Mutex
}

func (x *searchIndex) lookup(id marotte.ChatID) (*chatFilter, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f, ok := x.filters[id]
	return f, ok
}

func (x *searchIndex) put(id marotte.ChatID, f *chatFilter) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.filters == nil {
		x.filters = make(map[marotte.ChatID]*chatFilter)
	}
	x.filters[id] = f
}

func (x *searchIndex) drop(id marotte.ChatID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.filters, id)
}

// extend folds one appended entry into the chat's filter, when it holds one. A
// chat with no filter is left without: only a full read of the log can build a
// complete one, so an append never creates a filter, and the next query does. The
// caller holds the chat's lock, which is what orders the extension against the
// query-side build and against the neighbouring appends.
func (x *searchIndex) extend(id marotte.ChatID, e *marotte.Entry) {
	if f, ok := x.lookup(id); ok {
		f.addEntry(e)
	}
}
