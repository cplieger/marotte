package chat

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// The cross-chat candidate index: one Bloom filter per chat over the rune trigrams of what the scan searches
// (entrySegments spans plus the title, via textsearch.Fold). It prunes, never decides: no false negatives, and a
// saturated filter admits all. In memory: the first reading query builds it, appends extend it under the chat lock,
// and a rewind, rewrite, header write or Remove drops it.

const (
	// filterBytes is one chat's filter, and so the index's cost per chat.
	filterBytes = 64 << 10
	filterWords = filterBytes / 8
	filterBits  = filterBytes * 8
	// filterHashes is the positions per trigram, double-hashed from one 64-bit hash's halves.
	filterHashes = 3
	trigramRunes = 3
)

// chatFilter is one chat's Bloom filter, complete on entry and grown by every append while queries read it unlocked.
// Bits are set by atomic OR and read by atomic load, and only ever added, so a query before an append's OR sees the
// pre-append log.
type chatFilter struct {
	words [filterWords]uint64
}

// buildChatFilter indexes one chat's title and every span of every turn, folded as the scan folds. Undrawn turns are
// indexed too: drawn flips later, and the filter must stay a superset. No trigram straddles two spans.
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

// addEntry records one appended entry's segments, read as its own turn, so a tool_call's input is indexed whole
// before any diff drops a leaf at query time. A filter may hold more than the scan reads, never less.
func (f *chatFilter) addEntry(e *marotte.Entry) {
	t := newSearchTurn(false)
	t.observe(e)
	for _, seg := range entrySegments(e, t) {
		f.addText(seg.text)
	}
}

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

func (f *chatFilter) add(key uint64) {
	h1, h2 := splitHash(key)
	for i := range uint64(filterHashes) {
		pos := (h1 + i*h2) & (filterBits - 1)
		atomic.OrUint64(&f.words[pos>>6], 1<<(pos&63))
	}
}

// holdsAll reports whether every key's positions are set. No keys holds, so a query under three runes matches every
// chat.
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

// A rune is at most 21 bits, so three fit in one key.
const (
	runeBits = 21
	runeMask = 1<<runeBits - 1
)

// Distinct trigrams never share one.
func trigramKey(a, b, c rune) uint64 {
	return uint64(a&runeMask)<<(2*runeBits) | uint64(b&runeMask)<<runeBits | uint64(c&runeMask)
}

// The packed key goes through splitmix64's finalizer first, or the halves would carry rune
// structure. The step is forced odd so positions stay distinct.
func splitHash(key uint64) (h1, h2 uint64) {
	h := key
	h = (h ^ (h >> 30)) * 0xbf58476d1ce4e5b9
	h = (h ^ (h >> 27)) * 0x94d049bb133111eb
	h ^= h >> 31
	return h & (1<<32 - 1), h>>32 | 1
}

// queryTrigrams returns the trigram keys of the free text the needle scans, or nil under three runes. Prepared as
// NewNeedle does: invalid UTF-8 becomes one U+FFFD per run before folding, so no trigram is demanded the needle never
// scans.
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

// Only a full read builds a filter. The caller holds the chat's lock, ordering this against the
// query-side build and other appends.
func (x *searchIndex) extend(id marotte.ChatID, e *marotte.Entry) {
	if f, ok := x.lookup(id); ok {
		f.addEntry(e)
	}
}
