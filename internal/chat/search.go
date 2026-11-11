package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/textsearch"
)

// In-chat transcript search, server-side because the client holds a paginated window; the count and the hit list
// find-in-chat.ts steps through make progressive collapse acceptable. Matching is textsearch's; search_items.go owns
// what one hit is (an item the transcript draws); this file owns the query, segmentation, scoped filters and hit
// context. No index: one linear pass over a log already read. Lexical, since `_kiro/knowledge` cannot answer
// "which turn".

// searchExcerptRadius is the runes of context an excerpt keeps on each side of the item's tightest window.
const searchExcerptRadius = 60

// maxSearchHits caps the hit list: a query matching every item gets refined, not paged. Counting continues past it
// (SearchResult.Matched).
const maxSearchHits = 200

// SegmentKind identifies a searchable span of an item, so the client opens the surface that renders it.
type SegmentKind string

// Segment kinds in rendered order: a tool card has several segments across its tool_call and tool_result.
const (
	segmentContent       SegmentKind = "content"
	segmentReasoning     SegmentKind = "reasoning"
	segmentToolTitle     SegmentKind = "tool_title"
	segmentToolDisclosed SegmentKind = "tool_disclosed"
	segmentToolDiff      SegmentKind = "tool_diff"
	segmentToolDenial    SegmentKind = "tool_denial"
	segmentToolInput     SegmentKind = "tool_input"
	segmentToolOutput    SegmentKind = "tool_output"
	// segmentSteer is a steer entry's text, the note the transcript renders for it.
	segmentSteer SegmentKind = "steer"
	// The turn-level kinds: the prompt's text and attachment names, the plan card, and the turn_close failure reason in
	// the footer.
	segmentPrompt      SegmentKind = "prompt"
	segmentPlan        SegmentKind = "plan"
	segmentAttachment  SegmentKind = "attachment"
	segmentTurnFailure SegmentKind = "turn_failure"
	// segmentWake is a turn_wake's header text (marotte.WakeSearchTexts): its label and the messages it draws, which
	// their own rows then leave out.
	segmentWake SegmentKind = "wake"
)

// Hit is one matching item; the client pages its turn in, opens the containers holding it and marks its terms.
type Hit struct {
	// Caller is set on a hit in a prompt or steer an API caller sent, so a result names who sent the words.
	Caller *marotte.Caller `json:"caller,omitempty"`
	// TurnID is the item's turn.
	TurnID string `json:"turn_id"`
	// EntryID is the item's anchor, its first member entry, which the client resolves the item's element by.
	EntryID string `json:"entry_id"`
	// Lane is the item's lane: "" for the chat's own flow, a delegate's uuid inside its stream, so the client opens that
	// tab first.
	Lane string `json:"lane,omitempty"`
	// Kind is the item's kind, or "turn" for a spread hit.
	Kind    ItemKind `json:"kind"`
	Excerpt string   `json:"excerpt"`
	// Segments are the distinct kinds of the item's segments holding a span, in rendered order; empty for a filter-only
	// hit.
	Segments []SegmentKind `json:"segments"`
	// Turn is the 1-based session-absolute ordinal (turn_open's n), for marking the rail beyond the window.
	Turn int `json:"turn"`
}

// searchQuery is a parsed query: scoped filters plus the free text's matcher.
type searchQuery struct {
	file  string
	tool  string
	terms []textsearch.Term
	// matcher is the prepared free text. Case sensitivity applies to it alone; filters stay case-insensitive, since
	// paths are typed from memory.
	matcher textsearch.Matcher
	turn    int
}

// isChatFilter is the chat search's filter rule: `file:` and `tool:` take any value, `turn:` a positive integer.
// Anything else stays free text, so `http://` is literal.
func isChatFilter(name, value string) bool {
	switch name {
	case "file", "tool":
		return true
	case "turn":
		n, err := strconv.Atoi(value)
		return err == nil && n > 0
	}
	return false
}

// parseSearchQuery parses raw through textsearch.ParseQuery with isChatFilter; the last filter of a name wins.
func parseSearchQuery(raw string, caseSensitive bool) searchQuery {
	pq := textsearch.ParseQuery(raw, isChatFilter)
	q := searchQuery{terms: pq.Terms, turn: -1, matcher: textsearch.NewMatcher(pq.Terms, caseSensitive)}
	for _, f := range pq.Filters {
		switch f.Name {
		case "file":
			q.file = strings.ToLower(f.Value)
		case "tool":
			q.tool = strings.ToLower(f.Value)
		case "turn":
			q.turn, _ = strconv.Atoi(f.Value)
		}
	}
	return q
}

func (q *searchQuery) empty() bool {
	return q.matcher.Terms() == 0 && q.file == "" && q.tool == "" && q.turn < 0
}

// SearchResult is GET /api/chats/{id}/search's reply: hits capped at maxSearchHits plus the full tally.
type SearchResult struct {
	Matches []Hit `json:"matches"`
	// Spread is true when no item held every term and the hits are turns whose items hold them between them.
	Spread bool `json:"spread"`
	// Matched counts matching items (or spread turns); Scanned counts the entries of drawn turns.
	textsearch.Tally
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

// search scans a chat's entries for a query and returns the hits and tally. Undrawn turns (absent from drawn) are
// skipped: no card renders them. caseSensitive governs the free text only and travels on the request so both halves
// agree.
func search(entries []marotte.Entry, drawn map[string]struct{}, raw string, caseSensitive bool) SearchResult {
	return searchEntries(entries, drawn, raw, caseSensitive).res
}

// itemScan is searchEntries' answer: the reply, the byte volume of the segments read (cross-chat ranking's
// length norm) and the index in res.Matches of the tightest hit, -1 for none.
type itemScan struct {
	res   SearchResult
	chars int
	best  int
}

// searchEntries matches the query against every item of the drawn turns, falling back to turns when the query has
// terms and no item holds them all.
func searchEntries(entries []marotte.Entry, drawn map[string]struct{}, raw string, caseSensitive bool) itemScan {
	q := parseSearchQuery(raw, caseSensitive)
	if q.empty() {
		return itemScan{res: SearchResult{Matches: []Hit{}}, best: -1}
	}
	turns := indexSearchTurns(entries, drawn)
	scanned := 0
	for i := range entries {
		if t := turns[entries[i].Turn]; t != nil && t.drawn {
			scanned++
		}
	}
	items := searchItems(entries, turns)
	sc := matchItems(items, &q)
	sc.res.Scanned = scanned
	return sc
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

// itemMatchesFilters applies the scoped filters, all of which must hold. `tool:` and `file:` keep only tool items,
// read through the call and its result together, so output hits are found by the call's title.
func itemMatchesFilters(it *searchItem, q *searchQuery) bool {
	if q.turn >= 0 && it.ordinal != q.turn {
		return false
	}
	if q.tool == "" && q.file == "" {
		return true
	}
	if it.call == nil {
		return false
	}
	if q.tool != "" && !strings.Contains(strings.ToLower(it.call.Title), q.tool) &&
		!strings.Contains(strings.ToLower(string(it.call.Kind)), q.tool) {
		return false
	}
	return q.file == "" || toolTouchesFile(it.call, it.result, q.file)
}

func toolCallIDOfResult(resultID string) string {
	return strings.TrimSuffix(resultID, ":result")
}

// A read has a location and no diff.
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

type segment struct {
	// caller is the API caller that sent a prompt's or steer's words, nil for the user's and every other span.
	caller *marotte.Caller
	kind   SegmentKind
	text   string
}

// sentBy stamps c on every span of a message an API caller sent.
func sentBy(segs []segment, c *marotte.Caller) []segment {
	for i := range segs {
		segs[i].caller = c
	}
	return segs
}

// entrySegments lists an entry's searchable spans in rendered order: text and steer_ack text; tool_call title and
// input; tool_result claim, diff, denial and output; steer text; plan entries; compaction_failed or safety_blocked
// text; model_routed's message; model_fallback's drawn models; turn_close failure reason; turn_open prompt and
// attachment names; turn_wake label and messages, its runs named from names. Empty spans add nothing.
func entrySegments(e *marotte.Entry, t *searchTurn, names map[string]string) []segment {
	switch e.Kind {
	case marotte.EntryKindText, marotte.EntryKindSteerAck:
		// A steer_ack's words are its `text`, as a text entry's are.
		var p marotte.EntryText
		return oneSegment(segmentContent, decodeText(e.Payload, &p, func() string { return p.Text }))
	case marotte.EntryKindThinking:
		var p marotte.EntryThinking
		return oneSegment(segmentReasoning, decodeText(e.Payload, &p, func() string { return p.Text }))
	case marotte.EntryKindToolCall:
		return toolCallSegments(t.calls[e.ID])
	case marotte.EntryKindToolResult:
		return toolResultPayloadSegments(e.Payload)
	case marotte.EntryKindSteer:
		var p marotte.EntrySteer
		return sentBy(drawnSegments(segmentSteer, decodeText(e.Payload, &p, func() string { return p.Text })), p.Caller)
	case marotte.EntryKindPlan:
		return planSegments(e.Payload)
	case marotte.EntryKindCompactionFailed:
		var p marotte.EntryCompactionFailed
		return oneSegment(segmentContent, decodeText(e.Payload, &p, func() string { return p.Reason }))
	case marotte.EntryKindSafetyBlocked:
		// Joined as the row joins them (messages-events.ts), so a phrase spans two properties in neither.
		var p marotte.EntrySafetyBlocked
		return oneSegment(segmentContent, decodeText(e.Payload, &p, func() string { return strings.Join(p.Properties, ", ") }))
	case marotte.EntryKindModelRouted:
		var p marotte.EntryModelRouted
		return oneSegment(segmentContent, decodeText(e.Payload, &p, func() string { return p.Message }))
	case marotte.EntryKindModelFallback:
		return modelFallbackSegments(e.Payload)
	case marotte.EntryKindTurnClose:
		var p marotte.EntryTurnClose
		return oneSegment(segmentTurnFailure, decodeText(e.Payload, &p, func() string { return p.FailureReason }))
	case marotte.EntryKindTurnOpen:
		return turnOpenSegments(e.Payload)
	case marotte.EntryKindTurnWake:
		return turnWakeSegments(e.Payload, names)
	case marotte.EntryKindTurnBind, marotte.EntryKindCompaction,
		marotte.EntryKindModelSwitched, marotte.EntryKindModeSwitched, marotte.EntryKindSteerDelivered,
		marotte.EntryKindSteerCleared, marotte.EntryKindRunNamed, marotte.EntryKindContextReset,
		marotte.EntryKindStepGraded:
		// A compaction summary restates history searchable at its source; bind, switch and reset render no text.
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

// modelFallbackSegments is the model ids the row draws (messages-events.ts fallbackLabel): the model that stopped,
// then the one the turn moved to, which a decline or a retry on the same model does not name. The row words the rest.
func modelFallbackSegments(payload json.RawMessage) []segment {
	var p marotte.EntryModelFallback
	if json.Unmarshal(payload, &p) != nil {
		return nil
	}
	segs := oneSegment(segmentContent, p.From)
	if p.Reason == "" && p.To != p.From {
		segs = append(segs, oneSegment(segmentContent, p.To)...)
	}
	return segs
}

func planSegments(payload json.RawMessage) []segment {
	var p marotte.EntryPlan
	if json.Unmarshal(payload, &p) != nil {
		return nil
	}
	segs := make([]segment, 0, len(p.Entries))
	for i := range p.Entries {
		segs = append(segs, oneSegment(segmentPlan, p.Entries[i].Content)...)
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

// drawnSegments is a message's text as its note or header draws it (drawnWords), so a quote block's `>` marks and
// attribution are no hit and each band and stretch between is a run of its own.
func drawnSegments(kind SegmentKind, text string) []segment {
	all := drawnWords(text)
	segs := make([]segment, 0, len(all))
	for _, words := range all {
		segs = append(segs, oneSegment(kind, words)...)
	}
	return segs
}

func turnWakeSegments(payload json.RawMessage, names map[string]string) []segment {
	var w marotte.EntryTurnWake
	if json.Unmarshal(payload, &w) != nil {
		return nil
	}
	named := marotte.NamedWake(&w, names)
	var segs []segment
	for _, text := range marotte.WakeSearchTexts(&named) {
		segs = append(segs, oneSegment(segmentWake, text)...)
	}
	return segs
}

// turnOpenSegments is the prompt's text and each attachment's name, not path: attachment-pill.ts shows the name and
// puts the path in a `title` attribute the client cannot mark. The directory part is unfindable.
func turnOpenSegments(payload json.RawMessage) []segment {
	var p marotte.EntryTurnOpen
	if json.Unmarshal(payload, &p) != nil || p.Prompt == nil {
		return nil
	}
	segs := drawnSegments(segmentPrompt, p.Prompt.Text)
	for i := range p.Prompt.Attachments {
		segs = append(segs, oneSegment(segmentAttachment, p.Prompt.Attachments[i].Name)...)
	}
	return sentBy(segs, p.Prompt.Caller)
}

// toolCallSegments is a call's create half: title and input, as the card renders them. Claim, diff, denial and
// output belong to the settled tool_result.
func toolCallSegments(c *searchCall) []segment {
	if c == nil || c.call == nil {
		return nil
	}
	segs := oneSegment(segmentToolTitle, c.call.Title)
	if input := inputLeafText(c.call.Input, c.diff); input != "" {
		segs = append(segs, segment{kind: segmentToolInput, text: input})
	}
	return segs
}

// toolResultSegments is the settled half: claim, diff preview, denial, output, each the one rendered field
// (DisplayName, Diffs[0].NewText, Denial.Resource). old_text is skipped: 97.4% of its lines repeat in new_text. Path is
// reachable via `file:` and the title.
func toolResultSegments(res *marotte.EntryToolResult) []segment {
	var segs []segment
	if res.Disclosed != nil {
		segs = append(segs, oneSegment(segmentToolDisclosed, res.Disclosed.DisplayName)...)
	}
	if len(res.Diffs) > 0 {
		segs = append(segs, oneSegment(segmentToolDiff, res.Diffs[0].NewText)...)
	}
	if res.Denial != nil {
		segs = append(segs, oneSegment(segmentToolDenial, res.Denial.Resource)...)
	}
	return append(segs, oneSegment(segmentToolOutput, res.Output)...)
}

// inputLeafDedupeMin is the leaf length at which a leaf found verbatim in the diff text stops being
// searched. Shorter leaves are paths, patterns or commands; at or above it, 7,281 of 7,361 measured
// calls with input and diffs carry the leaf verbatim in new_text.
const inputLeafDedupeMin = 40

// Decoder.Token() keeps the card's order, which a map would shuffle. Malformed bytes and `null`
// yield "".
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

// excerptAround cuts runes around [at, at+length), rune-indexed so no character splits.
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
