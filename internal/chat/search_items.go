package chat

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// ItemKind is what one search hit is: a thing the transcript draws as one unit, which the client stamps
// `data-find-unit` with. internal/chat/testdata/search_items.json pins the boundaries against static-src's
// renderer.
type ItemKind string

const (
	// itemHeader is the turn header: the prompt and its attachment names, or a wake's header text.
	itemHeader ItemKind = "header"
	// itemMessage is one prose bubble: consecutive text entries of one lane (block-window.ts runBounds), or a
	// steer_ack.
	itemMessage   ItemKind = "message"
	itemReasoning ItemKind = "reasoning"
	// itemTool is one tool card: a tool_call and its tool_result.
	itemTool  ItemKind = "tool"
	itemSteer ItemKind = "steer"
	// itemPlan is the plan card, which every later plan entry of its lane updates.
	itemPlan ItemKind = "plan"
	// itemNotice is an engine notice row: compaction_failed, safety_blocked, model_routed, model_fallback.
	itemNotice ItemKind = "notice"
	// itemTurnFailure is the turn footer's failure notice.
	itemTurnFailure ItemKind = "turn_failure"
	// itemTurn is a spread hit: no item held every term, and this turn's items hold them between them.
	itemTurn ItemKind = "turn"
)

// searchItem is one item and every segment it renders.
type searchItem struct {
	// call and result are a tool item's payloads, which `tool:` and `file:` read.
	call   *marotte.EntryToolCall
	result *marotte.EntryToolResult
	kind   ItemKind
	turn   string
	lane   string
	// entries are every member entry id, the first being the anchor.
	entries []string
	segs    []segment
	ordinal int
	// at is the log index of the first member, the item order.
	at int
}

// searchItems lists the items of the drawn turns in log order of their first member. Boundaries are the client's:
// an entry renders in a lane's view per itemRenders, a prose run is broken by anything rendering in its view, and a
// run splits where MessageRun.take refuses.
func searchItems(entries []marotte.Entry, turns map[string]*searchTurn) []searchItem {
	b := itemBuilder{
		entries: entries,
		names:   wakeFactsOf(entries).names,
		silent:  silentSteers(entries),
		reverts: newestReverts(entries),
		results: make(map[string]int),
		plans:   make(map[toolRunKey]int),
		headers: make(map[string]int),
		runs:    make(map[toolRunKey]*proseRun),
	}
	for i := range entries {
		if e := &entries[i]; e.Kind == marotte.EntryKindToolResult {
			b.results[toolCallIDOfResult(e.ID)] = i
		}
	}
	for i := range entries {
		if t := turns[entries[i].Turn]; t != nil && t.drawn {
			b.observe(i, &entries[i], t)
		}
	}
	for k := range b.runs {
		b.flush(k)
	}
	// A header or plan seated before its text arrived may have none.
	b.items = slices.DeleteFunc(b.items, func(it searchItem) bool { return len(it.segs) == 0 })
	slices.SortStableFunc(b.items, func(x, y searchItem) int { return x.at - y.at })
	return b.items
}

type itemBuilder struct {
	entries []marotte.Entry
	names   map[string]string
	silent  map[int]struct{}
	reverts map[toolRunKey]int
	// results maps a call id to its tool_result's log index.
	results map[string]int
	// plans and headers map a lane's plan card and a turn's header to their index in items.
	plans   map[toolRunKey]int
	headers map[string]int
	runs    map[toolRunKey]*proseRun
	items   []searchItem
}

// proseRun is one lane's open message: the item so far, its text so far and the named say it belongs to
// (entry-ids.ts MessageRun).
type proseRun struct {
	say  string
	text strings.Builder
	item searchItem
}

func (b *itemBuilder) observe(i int, e *marotte.Entry, t *searchTurn) {
	k := toolRunKey{turn: e.Turn, lane: e.Lane}
	switch e.Kind {
	case marotte.EntryKindText:
		b.joinProse(i, e, k, t)
	case marotte.EntryKindSteerAck:
		// Its own bubble (placeEntry), so it ends the open message and joins none.
		b.flush(k)
		var text strings.Builder
		appendProse(&text, entrySegments(e, t, b.names))
		b.add(newItem(itemMessage, i, e, t, e.Lane), proseLines(text.String()))
	case marotte.EntryKindTurnOpen, marotte.EntryKindTurnWake:
		b.addHeader(i, e, t)
	case marotte.EntryKindTurnClose:
		b.add(newItem(itemTurnFailure, i, e, t, ""), entrySegments(e, t, b.names))
	case marotte.EntryKindSteer:
		if _, silent := b.silent[i]; !silent {
			// A steer is the reader's words, drawn in the chat's own flow whichever lane read it.
			b.flush(toolRunKey{turn: e.Turn})
			b.add(newItem(itemSteer, i, e, t, ""), entrySegments(e, t, b.names))
		}
	case marotte.EntryKindPlan:
		b.observePlan(i, e, k, t)
	case marotte.EntryKindToolCall:
		b.flush(k)
		b.addTool(i, e, t)
	default:
		b.observeRow(i, e, k, t)
	}
}

// observeRow handles the kinds drawn as one row of their lane, or nowhere of their own.
func (b *itemBuilder) observeRow(i int, e *marotte.Entry, k toolRunKey, t *searchTurn) {
	kind := itemNotice
	switch e.Kind {
	case marotte.EntryKindThinking:
		kind = itemReasoning
	case marotte.EntryKindCompactionFailed, marotte.EntryKindSafetyBlocked, marotte.EntryKindModelRouted:
	case marotte.EntryKindModelFallback:
		if subagentFallback(e) {
			return
		}
	case marotte.EntryKindTurnRevert:
		// Only a lane's newest rewind row draws, and it holds no searched text.
		if b.reverts[k] == i {
			b.flush(k)
		}
		return
	case marotte.EntryKindTurnBind, marotte.EntryKindRunNamed, marotte.EntryKindToolResult,
		marotte.EntryKindReconciled, marotte.EntryKindSteerDelivered, marotte.EntryKindSteerCleared,
		marotte.EntryKindStepGraded:
		// Drawn nowhere of their own (a result joins its call's card), so they end nothing.
		return
	default:
		// A drawn row with no searched text (compaction, a switch, a reset; entrySegments says why) ends a
		// message, as entryRenders' default does for a kind it does not name.
		b.flush(k)
		return
	}
	b.flush(k)
	b.add(newItem(kind, i, e, t, e.Lane), entrySegments(e, t, b.names))
}

func newItem(kind ItemKind, i int, e *marotte.Entry, t *searchTurn, lane string) *searchItem {
	return &searchItem{kind: kind, turn: e.Turn, lane: lane, entries: []string{e.ID}, ordinal: t.n, at: i}
}

// add keeps an item that renders searchable text.
func (b *itemBuilder) add(it *searchItem, segs []segment) {
	if len(segs) == 0 {
		return
	}
	it.segs = segs
	b.items = append(b.items, *it)
}

// joinProse adds a text entry to its lane's open message, or opens a new one where the say changes.
func (b *itemBuilder) joinProse(i int, e *marotte.Entry, k toolRunKey, t *searchTurn) {
	say := namedSayOf(e)
	run := b.runs[k]
	if run != nil && say != "" && run.say != "" && run.say != say {
		b.flush(k)
		run = nil
	}
	if run == nil {
		run = &proseRun{item: *newItem(itemMessage, i, e, t, e.Lane)}
		b.runs[k] = run
	} else {
		run.item.entries = append(run.item.entries, e.ID)
	}
	if run.say == "" {
		run.say = say
	}
	appendProse(&run.text, entrySegments(e, t, b.names))
}

// appendProse joins a text entry's segments to its message's text: its entries' texts with no separator, as
// messages-blocks.ts proseText draws the bubble, so a word the stream split across two entries is one word. The
// cross-chat filter indexes through it too (chatFilter.indexEntry), so a filter holds every word a message holds.
func appendProse(text *strings.Builder, segs []segment) {
	for _, sg := range segs {
		text.WriteString(sg.text)
	}
}

func (b *itemBuilder) flush(k toolRunKey) {
	if run := b.runs[k]; run != nil {
		delete(b.runs, k)
		b.add(&run.item, proseLines(run.text.String()))
	}
}

// proseLines is a message's text as the find engine reads its bubble: the markdown renderer draws each line break as
// a <br> or a new block, which a run (and so a phrase) never crosses, and drops a line's leading whitespace.
func proseLines(text string) []segment {
	var out []segment
	for line := range strings.Lines(text) {
		line = strings.TrimLeft(strings.TrimRight(line, "\r\n"), " \t")
		if strings.TrimSpace(line) != "" {
			out = append(out, segment{kind: segmentContent, text: line})
		}
	}
	return out
}

// addHeader folds a turn_open's prompt and a turn_wake's text into the turn's one header item, anchored on the
// turn_open.
func (b *itemBuilder) addHeader(i int, e *marotte.Entry, t *searchTurn) {
	segs := entrySegments(e, t, b.names)
	if at, ok := b.headers[e.Turn]; ok {
		b.items[at].segs = append(b.items[at].segs, segs...)
		b.items[at].entries = append(b.items[at].entries, e.ID)
		return
	}
	b.headers[e.Turn] = len(b.items)
	it := newItem(itemHeader, i, e, t, "")
	it.segs = segs
	b.items = append(b.items, *it)
}

// observePlan draws a lane's first plan as the card and folds every later one into it: the card shows the newest
// plan's entries.
func (b *itemBuilder) observePlan(i int, e *marotte.Entry, k toolRunKey, t *searchTurn) {
	segs := entrySegments(e, t, b.names)
	if at, ok := b.plans[k]; ok {
		b.items[at].segs = segs
		b.items[at].entries = append(b.items[at].entries, e.ID)
		return
	}
	b.flush(k)
	b.plans[k] = len(b.items)
	it := newItem(itemPlan, i, e, t, e.Lane)
	it.segs = segs
	b.items = append(b.items, *it)
}

// addTool adds a tool card, its call's segments then its settled result's. The engine's bookkeeping call ends a
// message (entryRenders draws it) but is never mounted, so it is no item.
func (b *itemBuilder) addTool(i int, e *marotte.Entry, t *searchTurn) {
	it := newItem(itemTool, i, e, t, e.Lane)
	if c := t.calls[e.ID]; c != nil {
		it.call = c.call
	}
	if it.call != nil && it.call.Title == internalToolTitle {
		return
	}
	segs := entrySegments(e, t, b.names)
	if at, ok := b.results[e.ID]; ok {
		r := &b.entries[at]
		var res marotte.EntryToolResult
		if json.Unmarshal(r.Payload, &res) == nil {
			it.result = &res
		}
		it.entries = append(it.entries, r.ID)
		segs = append(segs, entrySegments(r, t, b.names)...)
	}
	b.add(it, segs)
}

// namedSayOf is the named say a text entry belongs to, "" under a say turnlog minted for id-less deltas
// (`<turn>:e<k>`), which names no message (entry-ids.ts namedSay).
func namedSayOf(e *marotte.Entry) string {
	say := marotte.SayIDOf(e.ID)
	rest, ok := strings.CutPrefix(say, e.Turn+":e")
	if ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
		return ""
	}
	return say
}

// itemSpans is one item's spans, each with its segment and its rune offset in the item laid end to end (segments
// one rune apart, so no window bridges two by accident).
type itemSpans struct {
	spans []itemSpan
	terms int
}

type itemSpan struct {
	textsearch.Span
	seg int
	at  int
}

func spansOfItem(it *searchItem, m *textsearch.Matcher) itemSpans {
	var out itemSpans
	base := 0
	for si := range it.segs {
		text := it.segs[si].text
		for _, sp := range m.Spans(text) {
			out.spans = append(out.spans, itemSpan{Span: sp, seg: si, at: base + sp.Rune})
		}
		base += len([]rune(text)) + 1
	}
	seen := make(map[int]bool, m.Terms())
	for _, sp := range out.spans {
		seen[sp.Term] = true
	}
	out.terms = len(seen)
	return out
}

// tightest is the shortest window, from a span's start to the furthest end of the spans after it in item order, that
// holds one span of every term; ties go to the earliest. ok is false when a term has no span.
func (s *itemSpans) tightest(terms int) (first itemSpan, width int, ok bool) {
	if terms == 0 || s.terms < terms {
		return itemSpan{}, 0, false
	}
	count := make(map[int]int, terms)
	have, lo := 0, 0
	width = -1
	var ends furthestEnds
	for i, sp := range s.spans {
		if count[sp.Term] == 0 {
			have++
		}
		count[sp.Term]++
		ends.push(i, sp.at+sp.Runes)
		for have == terms {
			l := s.spans[lo]
			if w := ends.furthest() - l.at; width < 0 || w < width {
				first, width = l, w
			}
			count[l.Term]--
			if count[l.Term] == 0 {
				have--
			}
			ends.drop(lo)
			lo++
		}
	}
	return first, width, true
}

// furthestEnds is a sliding window's span ends, kept falling from front to back so the front is the furthest: spans
// are ordered by start, and an earlier phrase can end past a later word.
type furthestEnds struct {
	at  []int
	end []int
}

func (f *furthestEnds) push(i, end int) {
	for n := len(f.end); n > 0 && f.end[n-1] <= end; n = len(f.end) {
		f.at, f.end = f.at[:n-1], f.end[:n-1]
	}
	f.at, f.end = append(f.at, i), append(f.end, end)
}

func (f *furthestEnds) furthest() int { return f.end[0] }

// drop leaves the window's first span, i, out.
func (f *furthestEnds) drop(i int) {
	if f.at[0] == i {
		f.at, f.end = f.at[1:], f.end[1:]
	}
}

// matchItems keeps the items the filters admit and every term matches, or, when terms match no item, the turns
// whose items hold them between them.
func matchItems(items []searchItem, q *searchQuery) itemScan {
	sc := itemScan{res: SearchResult{Matches: make([]Hit, 0, 16)}, best: -1}
	terms := q.matcher.Terms()
	bestWidth := -1
	var kept []int
	var spans []itemSpans
	for i := range items {
		it := &items[i]
		if !itemMatchesFilters(it, q) {
			continue
		}
		if terms == 0 {
			sc.addHit(func() Hit { return filterHit(it) })
			continue
		}
		for _, sg := range it.segs {
			sc.chars += len(sg.text)
		}
		sp := spansOfItem(it, &q.matcher)
		kept, spans = append(kept, i), append(spans, sp)
		first, width, ok := sp.tightest(terms)
		if !ok {
			continue
		}
		if len(sc.res.Matches) < maxSearchHits && (bestWidth < 0 || width < bestWidth) {
			sc.best, bestWidth = len(sc.res.Matches), width
		}
		sc.addHit(func() Hit { return itemHit(it, &sp, first, width) })
	}
	if terms > 0 && sc.res.Matched == 0 {
		spreadTurns(&sc, items, kept, spans, terms)
	}
	return sc
}

// addHit counts a hit and keeps it under the cap; the hit is built lazily, since past the cap it is discarded.
func (sc *itemScan) addHit(mk func() Hit) {
	sc.res.Matched++
	if len(sc.res.Matches) < maxSearchHits {
		sc.res.Matches = append(sc.res.Matches, mk())
	}
}

func baseHit(it *searchItem) Hit {
	h := Hit{TurnID: it.turn, EntryID: it.entries[0], Lane: it.lane, Kind: it.kind, Turn: it.ordinal, Segments: []SegmentKind{}}
	for _, sg := range it.segs {
		h.Automatic = h.Automatic || sg.automatic
		if sg.caller != nil && h.Caller == nil {
			h.Caller = sg.caller
		}
	}
	return h
}

func filterHit(it *searchItem) Hit {
	h := baseHit(it)
	h.Excerpt = excerptAround([]rune(it.segs[0].text), 0, 0)
	return h
}

// itemHit is a matching item's hit, its excerpt cut around the tightest window in the segment where it starts.
func itemHit(it *searchItem, sp *itemSpans, first itemSpan, width int) Hit {
	h := baseHit(it)
	h.Segments = spanKinds(it, sp.spans)
	runes := []rune(it.segs[first.seg].text)
	h.Excerpt = excerptAround(runes, first.Rune, min(width, len(runes)-first.Rune))
	return h
}

// spanKinds lists the distinct segment kinds holding a span, in rendered order.
func spanKinds(it *searchItem, spans []itemSpan) []SegmentKind {
	held := make([]bool, len(it.segs))
	for _, sp := range spans {
		held[sp.seg] = true
	}
	out := []SegmentKind{}
	for si, sg := range it.segs {
		if held[si] && !slices.Contains(out, sg.kind) {
			out = append(out, sg.kind)
		}
	}
	return out
}

// spreadGroup is one (turn, lane)'s kept items as spreadTurns folds them: the terms they hold between them, the
// segment kinds holding a span, and the first span, whose item and segment the hit's excerpt is cut from.
type spreadGroup struct {
	seen  map[int]bool
	hit   *itemSpan
	kinds []SegmentKind
	first int
}

func (g *spreadGroup) take(i int, it *searchItem, sp *itemSpans) {
	for si := range sp.spans {
		g.seen[sp.spans[si].Term] = true
		if g.hit == nil {
			g.hit = &sp.spans[si]
			g.first = i
		}
	}
	for _, kind := range spanKinds(it, sp.spans) {
		if !slices.Contains(g.kinds, kind) {
			g.kinds = append(g.kinds, kind)
		}
	}
}

// spreadTurns adds one hit per (turn, lane) whose kept items hold every term between them, in item order.
func spreadTurns(sc *itemScan, items []searchItem, kept []int, spans []itemSpans, terms int) {
	var order []toolRunKey
	groups := make(map[toolRunKey]*spreadGroup)
	for j, i := range kept {
		k := toolRunKey{turn: items[i].turn, lane: items[i].lane}
		g := groups[k]
		if g == nil {
			g = &spreadGroup{first: i, seen: make(map[int]bool)}
			groups[k] = g
			order = append(order, k)
		}
		g.take(i, &items[i], &spans[j])
	}
	for _, k := range order {
		g := groups[k]
		if len(g.seen) < terms {
			continue
		}
		sc.res.Spread = true
		if sc.best < 0 {
			sc.best = 0
		}
		sc.addHit(func() Hit {
			it := &items[g.first]
			h := baseHit(it)
			h.Kind = itemTurn
			h.Segments = g.kinds
			runes := []rune(it.segs[g.hit.seg].text)
			h.Excerpt = excerptAround(runes, g.hit.Rune, g.hit.Runes)
			return h
		})
	}
}
