package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// Transcript search: the in-chat scan over a chat's entries. SERVER-SIDE, because
// the client's store is a paginated window, and the reply's two halves (the COUNT
// and the hit list find-in-chat.ts steps through) are what make progressive
// collapse acceptable. Matching is textsearch's; this file owns the segmentation,
// the scoped filters and what travels beside a hit. No index: one log already
// read, where a linear pass is the whole cost (search_index.go is the cross-chat
// candidate index). Lexical, not `_kiro/knowledge`, which cannot answer "which turn".

// searchExcerptRadius is how much context surrounds a hit in its excerpt.
//
// The client's ranker slices the SAME radius out of the rendered text before
// comparing it against this excerpt (find-in-chat.ts's EXCERPT_RADIUS), so the two
// sides of that similarity score span the same amount of context. Nothing on the
// wire carries the number and neither side is generated, so the pair is held
// together by static-src/chat-search.node.test.ts, which reads both files as text.
const searchExcerptRadius = 60

// maxSearchHits caps the hit LIST. A query matching every turn is a query the
// reader will refine, not page through, and an unbounded response on a
// thousand-turn chat is a wire cost paid for nothing. Counting continues past
// the cap (SearchResult.Matched), so a reader sees the total rather than 200.
const maxSearchHits = 200

// SegmentKind identifies which span of an entry a hit landed in, so the client
// can pick the right rendered surface before applying the offset.
type SegmentKind string

// Segment kinds, in RENDERED order. A tool call exposes several SEPARATE segments
// across its tool_call and tool_result entries, so the kind is what disambiguates
// their offsets — and declaring them in the order the card renders them is what
// makes a reader's walk through one card follow the card rather than an accident
// of declaration.
const (
	SegmentContent       SegmentKind = "content"
	SegmentReasoning     SegmentKind = "reasoning"
	SegmentToolTitle     SegmentKind = "tool_title"
	SegmentToolDisclosed SegmentKind = "tool_disclosed"
	SegmentToolDiff      SegmentKind = "tool_diff"
	SegmentToolDenial    SegmentKind = "tool_denial"
	SegmentToolInput     SegmentKind = "tool_input"
	SegmentToolOutput    SegmentKind = "tool_output"
	// SegmentSteer is a steer entry's text, the note the transcript renders for it.
	SegmentSteer SegmentKind = "steer"
	// The TURN-level kinds: the prompt's text and attachment names on the
	// turn_open, the plan card, and the failure reason on the turn_close, which
	// renders in the card's footer.
	SegmentPrompt      SegmentKind = "prompt"
	SegmentPlan        SegmentKind = "plan"
	SegmentAttachment  SegmentKind = "attachment"
	SegmentTurnFailure SegmentKind = "turn_failure"
	// SegmentEntry is the filter-only kind: a query with filters and no free text
	// yields one synthetic hit per matching entry, locating the entry rather
	// than a span inside it (offset 0, zero segment length).
	SegmentEntry SegmentKind = "entry"
)

// segmentKinds is every declared kind, in emission order. Declared HERE so the
// exhaustiveness test and the golden's kind loop read ONE list rather than each
// spelling its own literal.
var segmentKinds = []SegmentKind{
	SegmentContent, SegmentReasoning, SegmentToolTitle, SegmentToolDisclosed,
	SegmentToolDiff, SegmentToolDenial, SegmentToolInput, SegmentToolOutput,
	SegmentSteer, SegmentPrompt, SegmentPlan, SegmentAttachment, SegmentTurnFailure,
	SegmentEntry,
}

// Hit locates one match. The client fetches only the turns it needs to reveal
// and highlights locally, so this carries position rather than markup. Position
// is segment-relative: Offset indexes runes inside the one segment named by
// SegmentKind on the entry EntryID, never a concatenation of the turn.
type Hit struct {
	// TurnID is the matched entry's turn.
	TurnID string `json:"turn_id"`
	// EntryID is the matched entry.
	EntryID string `json:"entry_id"`
	Excerpt string `json:"excerpt"`
	// SegmentKind names the span the hit landed in.
	SegmentKind SegmentKind `json:"segment_kind"`
	// Lane is the entry's lane: "" for the chat's own agent, a delegate's uuid
	// for a hit inside that delegate's stream, so the client can open the
	// delegate's tab before highlighting.
	Lane string `json:"lane,omitempty"`
	// Turn is the 1-based session-absolute turn ordinal, the turn_open's n, so a
	// hit can mark the timeline rail for a turn the window does not hold.
	Turn int `json:"turn"`
	// Offset is the rune index of the match inside its segment, so the client
	// can highlight the right occurrence rather than the first.
	Offset int `json:"offset"`
	// SegmentLen is the segment's rune length: the denominator for a relative
	// position, carried so the client never re-derives the server's
	// segmentation. Zero for entry-kind hits.
	SegmentLen int `json:"segment_len"`
}

// searchQuery is a parsed query: scoped filters plus the free text.
//
// Filters make "the turn where you edited the composer" expressible, which
// a bare substring cannot do.
type searchQuery struct {
	text string
	file string
	tool string
	// needle is the free text prepared for the scan. The reader's case choice
	// applies to it ALONE: the scoped filters stay case-insensitive whatever
	// was asked, because a path is typed from memory.
	needle textsearch.Needle
	turn   int
	// needleRunes is the free text's rune length, which is how long a match is
	// in the segment it landed in.
	needleRunes int
}

// parseSearchQuery splits `file:` / `tool:` / `turn:` prefixes out of the raw
// query. Unknown prefixes stay in the free text rather than being dropped: a
// reader typing `http://` means it literally.
func parseSearchQuery(raw string, caseSensitive bool) searchQuery {
	q := searchQuery{turn: -1}
	var text []string
	for tok := range strings.FieldsSeq(raw) {
		name, val, ok := strings.Cut(tok, ":")
		if !ok || val == "" {
			text = append(text, tok)
			continue
		}
		switch strings.ToLower(name) {
		case "file":
			q.file = strings.ToLower(val)
		case "tool":
			q.tool = strings.ToLower(val)
		case "turn":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				q.turn = n
			} else {
				text = append(text, tok)
			}
		default:
			text = append(text, tok)
		}
	}
	q.text = strings.Join(text, " ")
	q.needle = textsearch.NewNeedle(q.text, caseSensitive)
	q.needleRunes = utf8.RuneCountInString(q.text)
	return q
}

// empty reports a query with neither free text nor a filter.
func (q *searchQuery) empty() bool {
	return q.text == "" && q.file == "" && q.tool == "" && q.turn < 0
}

// SearchResult is GET /api/chats/{id}/search's reply: the hits, cut at
// maxSearchHits, beside the tally that says how many there were.
type SearchResult struct {
	Matches []Hit `json:"matches"`
	textsearch.Tally
}

// hitScan accumulates one scan: the hits kept so far, every occurrence
// counted, including the ones past the cap that mint no Hit, and the bytes of
// every segment the walk read.
type hitScan struct {
	hits    []Hit
	matched int
	chars   int
}

// add counts one occurrence and keeps it while the list has room. The hit is
// built lazily because past the cap its excerpt would be discarded.
func (sc *hitScan) add(mk func() Hit) {
	sc.matched++
	if len(sc.hits) < maxSearchHits {
		sc.hits = append(sc.hits, mk())
	}
}

// searchTurn is one turn as the scan sees it: its ordinal, whether the rail
// draws it, and the tool_call payloads its tool_result entries are read against
// (the `tool:` and `file:` filters read the call; the result carries the output).
type searchTurn struct {
	calls map[string]*searchCall
	n     int
	drawn bool
}

// searchCall is one tool call as the scan reads it: the create payload, and the
// diff text its settled tool_result carries, so the input segment can drop the
// leaf the diff already covers (inputLeafInDiff).
type searchCall struct {
	call *marotte.EntryToolCall
	diff string
}

// Search scans a chat's entries for a query and reports the hits beside their
// tally: every entry of a drawn turn is read whatever the hit list holds, so
// Scanned is that entry count, Matched the occurrence count, and Truncated false,
// since nothing here can fail to read. drawn is the set of turn ids the rail
// draws; an undrawn turn's segments are skipped, because a hit there would name
// a turn no card renders. caseSensitive governs the FREE TEXT only; both halves
// of the in-chat search have to agree on it, so the flag travels on the request
// rather than being a server default either side could get wrong.
func Search(entries []marotte.Entry, drawn map[string]struct{}, raw string, caseSensitive bool) SearchResult {
	res, _ := searchEntries(entries, drawn, raw, caseSensitive)
	return res
}

// searchEntries is Search beside the byte volume of the segments the scan read.
// Cross-chat ranking divides a chat's occurrence count by that volume, and
// taking both from one walk is what keeps the numerator and the denominator
// over the same spans.
func searchEntries(entries []marotte.Entry, drawn map[string]struct{}, raw string, caseSensitive bool) (res SearchResult, chars int) {
	q := parseSearchQuery(raw, caseSensitive)
	if q.empty() {
		return SearchResult{Matches: []Hit{}}, 0
	}
	turns := indexSearchTurns(entries, drawn)
	sc := hitScan{hits: make([]Hit, 0, 16)}
	scanned := 0
	for i := range entries {
		e := &entries[i]
		t := turns[e.Turn]
		if t == nil || !t.drawn {
			continue
		}
		scanned++
		segs := entrySegments(e, t)
		if !entryMatchesFilters(e, t, &q) {
			continue
		}
		appendEntryHits(&sc, e, t.n, &q, segs)
	}
	return SearchResult{
		Matches: sc.hits,
		Scanned: scanned,
		Matched: sc.matched,
	}, sc.chars
}

// indexSearchTurns reads every turn_open's n and every tool_call's payload, so
// the per-entry walk can answer a result's filters from its call.
func indexSearchTurns(entries []marotte.Entry, drawn map[string]struct{}) map[string]*searchTurn {
	turns := make(map[string]*searchTurn)
	for i := range entries {
		e := &entries[i]
		t := turns[e.Turn]
		if t == nil {
			_, isDrawn := drawn[e.Turn]
			t = newSearchTurn(isDrawn)
			turns[e.Turn] = t
		}
		t.observe(e)
	}
	return turns
}

func newSearchTurn(drawn bool) *searchTurn {
	return &searchTurn{calls: make(map[string]*searchCall), drawn: drawn}
}

// observe folds one entry into the turn: the ordinal off a turn_open, the create
// payload off a tool_call, the diff off a tool_result.
func (t *searchTurn) observe(e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindTurnOpen:
		var open marotte.EntryTurnOpen
		if json.Unmarshal(e.Payload, &open) == nil {
			t.n = int(min(open.N, uint64(1<<31-1)))
		}
	case marotte.EntryKindToolCall:
		var call marotte.EntryToolCall
		if json.Unmarshal(e.Payload, &call) == nil {
			t.callFor(e.ID).call = &call
		}
	case marotte.EntryKindToolResult:
		var res marotte.EntryToolResult
		if json.Unmarshal(e.Payload, &res) == nil && len(res.Diffs) > 0 {
			t.callFor(toolCallIDOfResult(e.ID)).diff = res.Diffs[0].NewText
		}
	}
}

// callFor answers the turn's record for one tool call id, minting it on first
// sight: the create and the result each fill their half whatever order the log
// holds them in.
func (t *searchTurn) callFor(id string) *searchCall {
	c := t.calls[id]
	if c == nil {
		c = &searchCall{}
		t.calls[id] = c
	}
	return c
}

// entryMatchesFilters applies the scoped filters, all of which must hold. The
// `tool:` and `file:` filters read a tool_call and its tool_result together, so a
// hit in a call's output is found by the call's title.
func entryMatchesFilters(e *marotte.Entry, t *searchTurn, q *searchQuery) bool {
	if q.turn >= 0 && t.n != q.turn {
		return false
	}
	if q.tool == "" && q.file == "" {
		return true
	}
	call, result := toolPairOf(e, t)
	if call == nil {
		return false
	}
	if q.tool != "" && !strings.Contains(strings.ToLower(call.Title), q.tool) &&
		!strings.Contains(strings.ToLower(string(call.Kind)), q.tool) {
		return false
	}
	if q.file != "" && !toolTouchesFile(call, result, q.file) {
		return false
	}
	return true
}

// toolPairOf answers the tool_call payload and, for a tool_result entry, its own
// decoded payload; nil call for an entry that is neither.
func toolPairOf(e *marotte.Entry, t *searchTurn) (*marotte.EntryToolCall, *marotte.EntryToolResult) {
	switch e.Kind {
	case marotte.EntryKindToolCall:
		return t.callPayload(e.ID), nil
	case marotte.EntryKindToolResult:
		var res marotte.EntryToolResult
		if json.Unmarshal(e.Payload, &res) != nil {
			return nil, nil
		}
		return t.callPayload(toolCallIDOfResult(e.ID)), &res
	}
	return nil, nil
}

// callPayload is the decoded tool_call payload for one id, nil when the log
// holds no create for it (a result whose call was truncated away).
func (t *searchTurn) callPayload(id string) *marotte.EntryToolCall {
	if c := t.calls[id]; c != nil {
		return c.call
	}
	return nil
}

// toolCallIDOfResult inverts marotte.ToolResultID.
func toolCallIDOfResult(resultID string) string {
	return strings.TrimSuffix(resultID, ":result")
}

// toolTouchesFile matches a substring against a call's and its result's
// locations and diff paths — a call that only READ a file has a location and no
// diff.
func toolTouchesFile(call *marotte.EntryToolCall, result *marotte.EntryToolResult, want string) bool {
	for _, loc := range call.Locations {
		if strings.Contains(strings.ToLower(loc.Path), want) {
			return true
		}
	}
	if result == nil {
		return false
	}
	for _, loc := range result.Locations {
		if strings.Contains(strings.ToLower(loc.Path), want) {
			return true
		}
	}
	for _, d := range result.Diffs {
		if strings.Contains(strings.ToLower(d.Path), want) {
			return true
		}
	}
	return false
}

// appendEntryHits adds every match within one entry, matching each segment
// independently — a hit's Offset and SegmentLen are the segment's, so a match
// can never span two segments. `ordinal` is the turn's own turn_open.N, clamped
// at :281, and is an ORDINAL rather than a count of anything.
//
// A filter-only query still yields one hit per matching entry, so a scoped
// search without free text lists turns rather than finding nothing.
func appendEntryHits(sc *hitScan, e *marotte.Entry, ordinal int, q *searchQuery, segs []segment) {
	if q.text == "" {
		sc.add(func() Hit {
			var first string
			if len(segs) > 0 {
				first = segs[0].text
			}
			return Hit{
				TurnID:      e.Turn,
				EntryID:     e.ID,
				Lane:        e.Lane,
				Turn:        ordinal,
				SegmentKind: SegmentEntry,
				Excerpt:     excerptAround([]rune(first), 0, 0),
			}
		})
		return
	}
	for i := range segs {
		sc.chars += len(segs[i].text)
		appendSegmentHits(sc, e, ordinal, q, &segs[i])
	}
}

// appendSegmentHits counts every occurrence of the query text inside one segment.
func appendSegmentHits(sc *hitScan, e *marotte.Entry, ordinal int, q *searchQuery, seg *segment) {
	var runes []rune
	for hit := range q.needle.Occurrences(seg.text) {
		if runes == nil {
			runes = []rune(seg.text)
		}
		sc.add(func() Hit {
			return Hit{
				TurnID:      e.Turn,
				EntryID:     e.ID,
				Lane:        e.Lane,
				Turn:        ordinal,
				SegmentKind: seg.kind,
				Offset:      hit.Rune,
				SegmentLen:  len(runes),
				Excerpt:     excerptAround(runes, hit.Rune, q.needleRunes),
			}
		})
	}
}

// segment is one searchable span of an entry: the unit a hit's offset is
// relative to.
type segment struct {
	kind SegmentKind
	text string
}

// entrySegments lists an entry's searchable spans in rendered order, per 8.9: a
// text entry is one segment, a tool_call its title and input, a tool_result the
// disclosed claim, the diff, the denial and the output, a steer its text, a plan
// its entries, a compaction_failed or safety_blocked its text, a turn_close its
// failure reason, a turn_open the prompt and its attachment names. An empty span
// contributes no segment.
func entrySegments(e *marotte.Entry, t *searchTurn) []segment {
	switch e.Kind {
	case marotte.EntryKindText:
		var p marotte.EntryText
		return oneSegment(SegmentContent, decodeText(e.Payload, &p, func() string { return p.Text }))
	case marotte.EntryKindThinking:
		var p marotte.EntryThinking
		return oneSegment(SegmentReasoning, decodeText(e.Payload, &p, func() string { return p.Text }))
	case marotte.EntryKindToolCall:
		return toolCallSegments(t.calls[e.ID])
	case marotte.EntryKindToolResult:
		return toolResultPayloadSegments(e.Payload)
	case marotte.EntryKindSteer:
		var p marotte.EntrySteer
		return oneSegment(SegmentSteer, decodeText(e.Payload, &p, func() string { return p.Text }))
	case marotte.EntryKindPlan:
		return planSegments(e.Payload)
	case marotte.EntryKindCompactionFailed:
		var p marotte.EntryCompactionFailed
		return oneSegment(SegmentContent, decodeText(e.Payload, &p, func() string { return p.Reason }))
	case marotte.EntryKindSafetyBlocked:
		var p marotte.EntrySafetyBlocked
		return oneSegment(SegmentContent, decodeText(e.Payload, &p, func() string { return strings.Join(p.Properties, "\n") }))
	case marotte.EntryKindTurnClose:
		var p marotte.EntryTurnClose
		return oneSegment(SegmentTurnFailure, decodeText(e.Payload, &p, func() string { return p.FailureReason }))
	case marotte.EntryKindTurnOpen:
		return turnOpenSegments(e.Payload)
	case marotte.EntryKindTurnBind, marotte.EntryKindSteerAck, marotte.EntryKindCompaction,
		marotte.EntryKindModelSwitched, marotte.EntryKindModeSwitched:
		// A compaction's summary is the model's own account of history already
		// searchable at its source; a bind and a switch render no text. An ack's text
		// is the agent's own words at its own position, so it is the one kind here
		// whose rendered prose the index leaves out.
		return nil
	}
	return nil
}

// decodeText decodes payload into p and answers read()'s text, or "" on a payload
// that does not decode: a search must not fail because one entry is odd.
func decodeText(payload json.RawMessage, p any, read func() string) string {
	if json.Unmarshal(payload, p) != nil {
		return ""
	}
	return read()
}

func toolResultPayloadSegments(payload json.RawMessage) []segment {
	var res marotte.EntryToolResult
	if json.Unmarshal(payload, &res) != nil {
		return nil
	}
	return toolResultSegments(&res)
}

// planSegments is one span per plan entry, in plan order.
func planSegments(payload json.RawMessage) []segment {
	var p marotte.EntryPlan
	if json.Unmarshal(payload, &p) != nil {
		return nil
	}
	segs := make([]segment, 0, len(p.Entries))
	for i := range p.Entries {
		segs = append(segs, oneSegment(SegmentPlan, p.Entries[i].Content)...)
	}
	return segs
}

// oneSegment is a single-span segment list, empty for an empty span.
func oneSegment(kind SegmentKind, text string) []segment {
	if text == "" {
		return nil
	}
	return []segment{{kind: kind, text: text}}
}

// turnOpenSegments is the prompt's text and each attachment's NAME, not path:
// attachment-pill.ts renders `att.name` as the pill's text and puts the path in
// the `title` ATTRIBUTE, which the client's DOM walker cannot mark. Loss: the
// directory part is unfindable.
func turnOpenSegments(payload json.RawMessage) []segment {
	var p marotte.EntryTurnOpen
	if json.Unmarshal(payload, &p) != nil || p.Prompt == nil {
		return nil
	}
	segs := oneSegment(SegmentPrompt, p.Prompt.Text)
	for i := range p.Prompt.Attachments {
		segs = append(segs, oneSegment(SegmentAttachment, p.Prompt.Attachments[i].Name)...)
	}
	return segs
}

// toolCallSegments is the CREATE half of a tool call's spans: the title and the
// input, in the order the card renders them. The disclosed claim, the diff, the
// denial and the output belong to the settled tool_result, so a hit in them names
// that entry.
func toolCallSegments(c *searchCall) []segment {
	if c == nil || c.call == nil {
		return nil
	}
	segs := oneSegment(SegmentToolTitle, c.call.Title)
	if input := inputLeafText(c.call.Input, c.diff); input != "" {
		segs = append(segs, segment{kind: SegmentToolInput, text: input})
	}
	return segs
}

// toolResultSegments is the SETTLED half: the disclosed claim, the diff preview,
// the denial block, then the output. Each span is the one rendered field
// (DisplayName, Diffs[0].NewText, Denial.Resource): 97.4% of old_text's lines are
// also in new_text, so searching both mints a second hit for one rendered line, and
// Path is reachable through the `file:` filter and the title.
func toolResultSegments(res *marotte.EntryToolResult) []segment {
	var segs []segment
	if res.Disclosed != nil {
		segs = append(segs, oneSegment(SegmentToolDisclosed, res.Disclosed.DisplayName)...)
	}
	if len(res.Diffs) > 0 {
		segs = append(segs, oneSegment(SegmentToolDiff, res.Diffs[0].NewText)...)
	}
	if res.Denial != nil {
		segs = append(segs, oneSegment(SegmentToolDenial, res.Denial.Resource)...)
	}
	return append(segs, oneSegment(SegmentToolOutput, res.Output)...)
}

// inputLeafDedupeMin is the leaf length at which a leaf occurring verbatim in the
// diff text handed beside it stops being searched. Below it a leaf is a path, a
// pattern or a short command and cannot be the write payload; at or above it,
// 99% of measured calls carrying both an input and diffs hold such a leaf
// verbatim in the new_text.
const inputLeafDedupeMin = 40

// inputLeafText is a tool call's Input string LEAF VALUES, one per line, in
// DOCUMENT order, minus the leaves diffText already carries (inputLeafInDiff).
// Keys are skipped (a raw scan matches `command`, `path` and every escape), and
// so are numbers and bools. It walks Decoder.Token() rather than a map because
// document order is what the card prints and map iteration would reorder hits
// between two requests. Malformed bytes and a literal `null` yield "".
func inputLeafText(raw json.RawMessage, diffText string) string {
	if len(raw) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var c leafCollector
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ""
		}
		c.take(tok, diffText)
	}
	if len(c.stack) > 0 {
		// The bytes ran out inside a container: Token reports that as a plain EOF,
		// so an open frame is the only evidence the document was truncated, and
		// half a document is malformed like any other non-JSON input.
		return ""
	}
	return c.b.String()
}

// leafCollector is inputLeafText's walk state: the open containers, and the leaf
// lines gathered so far. Never copied — Builder forbids it.
type leafCollector struct {
	stack []leafFrame
	b     strings.Builder
}

// take folds one token into the walk: a structural token moves the stack, a
// member KEY is skipped, and a string VALUE the diff does not already carry
// becomes a line of its own.
func (c *leafCollector) take(tok json.Token, diffText string) {
	if d, ok := tok.(json.Delim); ok {
		if d == '}' || d == ']' {
			c.stack = c.stack[:len(c.stack)-1]
			return
		}
		// A container is its parent's value, so the parent's slot is spent before
		// this frame exists.
		takeLeafSlot(c.stack)
		c.stack = append(c.stack, leafFrame{object: d == '{', expectKey: d == '{'})
		return
	}
	if takeLeafSlot(c.stack) {
		return
	}
	leaf, ok := tok.(string)
	if !ok || inputLeafInDiff(leaf, diffText) {
		return
	}
	if c.b.Len() > 0 {
		c.b.WriteString("\n")
	}
	c.b.WriteString(leaf)
}

// leafFrame is one open container in inputLeafText's walk. expectKey is
// meaningful for an object only, and is what tells a member's key from its value:
// on the token stream both are plain strings.
type leafFrame struct {
	object    bool
	expectKey bool
}

// takeLeafSlot spends the innermost object's next slot, reporting whether it was
// a KEY. An array element and a top-level value fill no slot, so both answer
// false.
func takeLeafSlot(stack []leafFrame) bool {
	n := len(stack) - 1
	if n < 0 || !stack[n].object {
		return false
	}
	key := stack[n].expectKey
	stack[n].expectKey = !key
	return key
}

// inputLeafInDiff reports whether a leaf is the write payload its call's own diff
// segment already covers. Length-gated first, on inputLeafDedupeMin's
// measurement: a short leaf can sit inside a payload without being one.
func inputLeafInDiff(leaf, diffText string) bool {
	return diffText != "" && len(leaf) >= inputLeafDedupeMin && strings.Contains(diffText, leaf)
}

// excerptAround returns the match plus surrounding context, with ellipses where
// it was cut. Rune-indexed so a multi-byte character is never split.
func excerptAround(runes []rune, at, length int) string {
	start := max(at-searchExcerptRadius, 0)
	end := min(at+length+searchExcerptRadius, len(runes))
	var b strings.Builder
	if start > 0 {
		b.WriteString("\u2026")
	}
	b.WriteString(strings.Join(strings.Fields(string(runes[start:end])), " "))
	if end < len(runes) {
		b.WriteString("\u2026")
	}
	return b.String()
}
