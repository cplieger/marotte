package chat

import (
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// The cross-chat candidate index: one Bloom filter per chat over the rune trigrams of what the scan searches (the
// title, every entry's entrySegments and each message's text as appendProse joins it, via textsearch.FoldText). It prunes, never decides:
// no false negatives, and a saturated filter admits all. In memory: the first reading query builds it, appends extend it under the chat lock,
// and a rewind, rewrite, run_named, header write or remove drops it.

const (
	// filterBytes is one chat's bits, and with its open turns' tails (a few bytes per lane) the index's cost per chat.
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
	// tails is each open (turn, lane)'s last message text runes a trigram across the next text entry takes
	// (indexEntry). Written only under the chat's lock, never read by a query.
	tails map[toolRunKey]string
	// words is its own allocation: beside the map it would round the filter up to the next 8 KiB page.
	words *[filterWords]uint64
}

func newChatFilter() *chatFilter {
	return &chatFilter{words: new([filterWords]uint64)}
}

// buildChatFilter indexes one chat's title and every entry, folded as the scan folds. Undrawn turns are indexed too:
// drawn flips later, and the filter must stay a superset.
func buildChatFilter(name string, entries []marotte.Entry) *chatFilter {
	f := newChatFilter()
	f.addText(name)
	turns := indexSearchTurns(entries, nil)
	names := wakeFactsOf(entries).names
	for i := range entries {
		f.indexEntry(&entries[i], turns[entries[i].Turn], names)
	}
	return f
}

// addEntry records one appended entry, read as its own turn, so a tool_call's input is indexed whole before any diff
// drops a leaf at query time. A filter may hold more than the scan reads, never less.
func (f *chatFilter) addEntry(e *marotte.Entry) {
	t := newSearchTurn(false)
	t.observe(e)
	f.indexEntry(e, t, nil)
}

// indexEntry adds an entry's entrySegments, a text entry's through appendProse after its lane's tail, whatever a view
// drops between the two: every trigram across a message's join is held, and a few across a break the scan never
// joins, which a superset allows.
func (f *chatFilter) indexEntry(e *marotte.Entry, t *searchTurn, names map[string]string) {
	segs := entrySegments(e, t, names)
	if e.Kind != marotte.EntryKindText {
		for _, seg := range segs {
			f.addText(seg.text)
		}
		if e.Kind == marotte.EntryKindTurnClose {
			// No text follows its turn's close (only lane-less between-turns kinds do), so its tails are dead.
			maps.DeleteFunc(f.tails, func(k toolRunKey, _ string) bool { return k.turn == e.Turn })
		}
		return
	}
	if f.tails == nil {
		f.tails = make(map[toolRunKey]string)
	}
	k := toolRunKey{turn: e.Turn, lane: e.Lane}
	var b strings.Builder
	b.WriteString(f.tails[k])
	appendProse(&b, segs)
	text := b.String()
	f.addText(text)
	// A clone: the slice would keep the whole joined text alive.
	f.tails[k] = strings.Clone(trigramTail(text))
}

// trigramTail is the runes of s a trigram can still take from it: the last trigramRunes-1, or s when it has fewer.
func trigramTail(s string) string {
	at := len(s)
	for range trigramRunes - 1 {
		if at == 0 {
			break
		}
		_, size := utf8.DecodeLastRuneInString(s[:at])
		at -= size
	}
	return s[at:]
}

func (f *chatFilter) addText(s string) {
	var a, b rune
	seen := 0
	for _, r := range textsearch.FoldText(s) {
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

// holdsAll reports whether every key's positions are set. No keys holds, so a query whose words are all under three
// runes matches every chat.
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

// queryTrigrams returns the trigram keys of every word the matcher scans, a phrase's words one by one since no
// trigram spans the whitespace between them; a word under three runes adds none. Prepared as the matcher folds, so
// no trigram is demanded the scan never looks for.
func queryTrigrams(terms []textsearch.Term) []uint64 {
	var keys []uint64
	for _, t := range terms {
		for _, w := range t.Words {
			runes := []rune(textsearch.FoldText(w))
			for i := 0; i+trigramRunes <= len(runes); i++ {
				keys = append(keys, trigramKey(runes[i], runes[i+1], runes[i+2]))
			}
		}
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
	if e.Kind == marotte.EntryKindRunNamed {
		// A name changes the text of wakes filed before it, which only a rebuild indexes.
		x.drop(id)
		return
	}
	if f, ok := x.lookup(id); ok {
		f.addEntry(e)
	}
}
