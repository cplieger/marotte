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

// In-chat transcript search, server-side because the client holds a paginated window; the count and the hit list
// find-in-chat.ts steps through make progressive collapse acceptable. Matching is textsearch's; this file owns
// segmentation, scoped filters and hit context. No index: one linear pass over a log already read. Lexical, since
// `_kiro/knowledge` cannot answer "which turn".

// searchExcerptRadius is the context around a hit in its excerpt. The client's ranker slices the same radius
// (find-in-chat.ts's EXCERPT_RADIUS); static-src/chat-search.node.test.ts keeps the two equal.
const searchExcerptRadius = 60

// maxSearchHits caps the hit list: a query matching every turn gets refined, not paged. Counting continues past it
// (SearchResult.Matched).
const maxSearchHits = 200

// SegmentKind identifies the span of an entry a hit landed in, so the client picks the right rendered surface.
type SegmentKind string

// Segment kinds in rendered order: a tool call has several segments across its tool_call and tool_result, so the kind
// disambiguates offsets and a walk through one card follows the card.
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
	// The turn-level kinds: the prompt's text and attachment names, the plan card, and the turn_close failure reason in
	// the footer.
	SegmentPrompt      SegmentKind = "prompt"
	SegmentPlan        SegmentKind = "plan"
	SegmentAttachment  SegmentKind = "attachment"
	SegmentTurnFailure SegmentKind = "turn_failure"
	// SegmentEntry is the filter-only kind: a filter query with no text yields one hit per matching entry (offset 0,
	// zero length).
	SegmentEntry SegmentKind = "entry"
)

// segmentKinds is every declared kind in emission order, one list for the exhaustiveness test and the golden.
var segmentKinds = []SegmentKind{
	SegmentContent, SegmentReasoning, SegmentToolTitle, SegmentToolDisclosed,
	SegmentToolDiff, SegmentToolDenial, SegmentToolInput, SegmentToolOutput,
	SegmentSteer, SegmentPrompt, SegmentPlan, SegmentAttachment, SegmentTurnFailure,
	SegmentEntry,
}

// Hit locates one match; the client fetches the turns it needs and highlights locally. Offset indexes runes inside
// the one segment SegmentKind names on EntryID, never a whole turn.
type Hit struct {
	// TurnID is the matched entry's turn.
	TurnID string `json:"turn_id"`
	// EntryID is the matched entry.
	EntryID string `json:"entry_id"`
	Excerpt string `json:"excerpt"`
	// SegmentKind names the span the hit landed in.
	SegmentKind SegmentKind `json:"segment_kind"`
	// Lane is the entry's lane: "" for the chat's agent, a delegate's uuid inside its stream, so the client opens that
	// tab first.
	Lane string `json:"lane,omitempty"`
	// Turn is the 1-based session-absolute ordinal (turn_open's n), for marking the rail beyond the window.
	Turn int `json:"turn"`
	// Offset is the match's rune index inside its segment, so the right occurrence is highlighted.
	Offset int `json:"offset"`
	// SegmentLen is the segment's rune length, so the client never re-derives segmentation. Zero for entry hits.
	SegmentLen int `json:"segment_len"`
}

// searchQuery is a parsed query: scoped filters plus free text.
type searchQuery struct {
	text string
	file string
	tool string
	// needle is the prepared free text. Case sensitivity applies to it alone; filters stay case-insensitive, since paths
	// are typed from memory.
	needle textsearch.Needle
	turn   int
	// needleRunes is the free text's rune length, a match's length in its segment.
	needleRunes int
}

// parseSearchQuery splits `file:` / `tool:` / `turn:` prefixes out of the query. Unknown prefixes stay in the text:
// `http://` is literal.
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

// SearchResult is GET /api/chats/{id}/search's reply: hits capped at maxSearchHits plus the full tally.
type SearchResult struct {
	Matches []Hit `json:"matches"`
	textsearch.Tally
}

// hitScan accumulates one scan: the kept hits, every occurrence counted past the cap, and the bytes of every segment
// read.
type hitScan struct {
	hits    []Hit
	matched int
	chars   int
}

// add counts one occurrence and keeps it while there is room; the hit is built lazily, since past the cap its excerpt
// is discarded.
func (sc *hitScan) add(mk func() Hit) {
	sc.matched++
	if len(sc.hits) < maxSearchHits {
		sc.hits = append(sc.hits, mk())
	}
}

// searchTurn is one turn as the scan sees it: its ordinal, whether the rail draws it, and the tool_call payloads its
// results are filtered against.
type searchTurn struct {
	calls map[string]*searchCall
	n     int
	drawn bool
}

// searchCall is one tool call for the scan: the create payload and its settled result's diff text, so the input
// segment can drop the leaf the diff covers (inputLeafInDiff).
type searchCall struct {
	call *marotte.EntryToolCall
	diff string
}

// Search scans a chat's entries for a query and returns the hits and tally: Scanned counts every entry of a drawn
// turn, Matched the occurrences, Truncated is false. Undrawn turns (absent from drawn) are skipped: no card renders
// them. caseSensitive governs the free text only and travels on the request so both halves agree.
func Search(entries []marotte.Entry, drawn map[string]struct{}, raw string, caseSensitive bool) SearchResult {
	res, _ := searchEntries(entries, drawn, raw, caseSensitive)
	return res
}

// searchEntries is Search plus the byte volume of the segments read, from one walk, for cross-chat ranking's
// occurrences-per-byte.
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

// indexSearchTurns reads every turn_open's n and tool_call payload, so a result's filters read its call.
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

// observe folds one entry into the turn: the ordinal from turn_open, the payload from tool_call, the diff from
// tool_result.
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

// callFor returns the turn's record for a tool call id, minting it on first sight, so create and result fill their
// halves in any order.
func (t *searchTurn) callFor(id string) *searchCall {
	c := t.calls[id]
	if c == nil {
		c = &searchCall{}
		t.calls[id] = c
	}
	return c
}

// entryMatchesFilters applies the scoped filters, all of which must hold. `tool:` and `file:` read a call and its
// result together, so output hits are found by the call's title.
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

// toolPairOf returns the tool_call payload and, for a tool_result, its own payload; nil call for neither.
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

// callPayload is the decoded tool_call payload for one id, nil when its create is gone.
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

// toolTouchesFile matches a substring against a call's and its result's locations and diff paths; a read has a
// location and no diff.
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

// appendEntryHits adds every match in one entry, per segment, so no match spans two. ordinal is the turn's
// turn_open.N. A filter-only query yields one hit per matching entry.
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

// segment is one searchable span of an entry, the unit a hit's offset is relative to.
type segment struct {
	kind SegmentKind
	text string
}

// entrySegments lists an entry's searchable spans in rendered order: text; tool_call title and input; tool_result
// claim, diff, denial and output; steer text; plan entries; compaction_failed or safety_blocked text; turn_close
// failure reason; turn_open prompt and attachment names. Empty spans add nothing.
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
		// A compaction summary restates history searchable at its source; bind and switch render no text. An ack's words
		// are the one rendered prose left out of the index.
		return nil
	}
	return nil
}

// decodeText decodes payload into p and returns read()'s text, or "" when it does not decode.
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

// turnOpenSegments is the prompt's text and each attachment's name, not path: attachment-pill.ts shows the name and
// puts the path in a `title` attribute the client cannot mark. The directory part is unfindable.
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

// toolCallSegments is a call's create half: title and input, as the card renders them. Claim, diff, denial and
// output belong to the settled tool_result.
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

// toolResultSegments is the settled half: claim, diff preview, denial, output, each the one rendered field
// (DisplayName, Diffs[0].NewText, Denial.Resource). old_text is skipped: 97.4% of its lines repeat in new_text. Path is
// reachable via `file:` and the title.
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

// inputLeafDedupeMin is the leaf length at which a leaf found verbatim in the diff text stops being searched. Shorter
// leaves are paths, patterns or commands; at or above it, 7,281 of 7,361 measured calls with input and diffs carry the
// leaf verbatim in new_text.
const inputLeafDedupeMin = 40

// inputLeafText is a tool call's Input string leaves, one per line, in document order, minus those diffText carries.
// Keys, numbers and bools are skipped. Decoder.Token() keeps the card's order, which a map would shuffle. Malformed
// bytes and `null` yield "".
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
		// EOF inside an open container means truncated, which is malformed.
		return ""
	}
	return c.b.String()
}

// leafCollector is inputLeafText's walk state: the open containers and the lines so far. Never copied (Builder).
type leafCollector struct {
	stack []leafFrame
	b     strings.Builder
}

// take folds one token into the walk: structure moves the stack, keys are skipped, and a string value the diff does
// not carry becomes a line.
func (c *leafCollector) take(tok json.Token, diffText string) {
	if d, ok := tok.(json.Delim); ok {
		if d == '}' || d == ']' {
			c.stack = c.stack[:len(c.stack)-1]
			return
		}
		// A container fills its parent's slot first.
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

// leafFrame is one open container in the walk. expectKey, objects only, tells a key from a value, both strings on the
// token stream.
type leafFrame struct {
	object    bool
	expectKey bool
}

// takeLeafSlot spends the innermost object's next slot, reporting whether it was a key. Arrays and the top level fill
// none.
func takeLeafSlot(stack []leafFrame) bool {
	n := len(stack) - 1
	if n < 0 || !stack[n].object {
		return false
	}
	key := stack[n].expectKey
	stack[n].expectKey = !key
	return key
}

// inputLeafInDiff reports whether a leaf is the write payload its diff already covers, length-gated by
// inputLeafDedupeMin.
func inputLeafInDiff(leaf, diffText string) bool {
	return diffText != "" && len(leaf) >= inputLeafDedupeMin && strings.Contains(diffText, leaf)
}

// excerptAround returns the match with context, ellipsised where cut; rune-indexed so no character splits.
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
