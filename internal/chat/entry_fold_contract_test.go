package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// entryFoldFixture is the envelope of testdata/entry_fold.json: one real log of two
// INTERLEAVED turns, the grouping the export derives from it, and per entry whether the
// export renders anything AT THAT ENTRY'S OWN POSITION.
//
// The Go side PRODUCES it (golden, regenerated behind UPDATE_GOLDEN=1) from the
// production writer rather than from a second switch, so no vocabulary is enumerated
// twice here; entry-fold-contract.node.test.ts DECODES it, partitions the same entries
// the way the store does, and asks block-window.ts's entryRenders. An export and a
// transcript that describe one turn differently is what this catches.
type entryFoldFixture struct {
	Comment []string        `json:"_comment"`
	Entries []marotte.Entry `json:"entries"`
	Turns   []entryFoldTurn `json:"turns"`
}

// entryFoldTurn is one turn of the grouping, in groupTurns' own order.
type entryFoldTurn struct {
	Turn    string         `json:"turn"`
	N       uint64         `json:"n"`
	Entries []entryFoldRow `json:"entries"`
}

// entryFoldRow is one entry's answer. Compared is false for an entry the two halves
// deliberately disagree about or render at different SCOPES; Edge says which.
type entryFoldRow struct {
	ID       string            `json:"id"`
	Seq      uint64            `json:"seq"`
	Kind     marotte.EntryKind `json:"kind"`
	Renders  bool              `json:"renders"`
	Compared bool              `json:"compared"`
	Edge     string            `json:"edge,omitempty"`
}

var entryFoldFixtureComment = []string{
	"The FOLD contract: which entries render at their own position, over one real entry",
	"log holding two interleaved turns. Ids and timestamps are normalised at write time —",
	"a turn id is server-minted and ts is the appender's wall clock, so neither can be in",
	"a byte-pinned golden.",
	"",
	"`renders` is the EXPORT's answer, derived by calling the production writer",
	"(export_md.go turnRender.writeEntry) per entry and recording whether it emitted",
	"bytes. TestEntryFoldContract (Go) asserts the log marshals to exactly these bytes,",
	"and entry-fold-contract.node.test.ts (TypeScript) asserts block-window.ts's",
	"entryRenders agrees on every row marked `compared`.",
	"",
	"TWO DECLARED EDGES, each carried as `compared: false` with its reason:",
	"  turn_open, turn_close — the export renders both in the flow (heading plus prompt,",
	"    and the footer) while the client renders them at CARD level, as the header band",
	"    and the footer beside .turn-body. A scope difference, not a disagreement.",
	"",
	"The turn_revert row is the record of a rewind, and it sits in the newest SURVIVING",
	"turn rather than in the turn it names: a revert takes every turn from its target to",
	"the newest in file order, so the range it states is gone from this log's own view",
	"while the boundary row renders inside a turn that stayed. It renders on BOTH sides",
	"(the export writes a rule plus \"Rewound to here\"; the client draws the boundary",
	"row), and because it renders it BREAKS a prose run — block-window.node.test.ts pins",
	"that half over a synthetic turn, which is the shape this log cannot produce (a",
	"record always lands at the file's tail, never between two text entries).",
	"",
	"Every tool_result here is PAIRED with a tool_call in its own turn, deliberately:",
	"writeUnpairedResult RENDERS a result whose call the turn does not hold, while the",
	"client refuses every tool_result unconditionally, so a fixture carrying one would pin",
	"agreement where the two sides diverge. The unpaired case is outside this fixture.",
	"",
	"Regenerate with: UPDATE_GOLDEN=1 go test ./internal/chat/ -run TestEntryFoldContract",
	"then re-run the TS half: npx vitest --run entry-fold-contract (from static-src/).",
}

// entryFoldEdges is the declared-edge table: the kinds this fixture does NOT compare,
// with the reason each carries into the golden. One table, so a kind cannot be excluded
// silently at one site and compared at another.
var entryFoldEdges = map[marotte.EntryKind]string{
	marotte.EntryKindTurnOpen:  "card-level on the client: the header band, not a body row",
	marotte.EntryKindTurnClose: "card-level on the client: the footer, a sibling of .turn-body",
}

// entryFoldLog writes one log of two interleaved turns and answers its entries with the
// server-minted turn ids normalised to t-1, t-2 in order of first appearance and every ts
// zeroed. Two turns open together is the shape the log must hold (marotte.md: nothing may
// assume a turn is a contiguous byte range), and it is what makes the grouping worth
// pinning at all — with one turn per run, file order IS the grouping.
func entryFoldLog(t *testing.T) []marotte.Entry {
	t.Helper()
	f := newLogFixture(t)

	one := f.prompt("where does the retry backoff live")
	f.append(one, "", "bind-1", marotte.EntryKindTurnBind,
		marotte.EntryTurnBind{KASMessageID: "kas-1", SessionID: "sess-1"})
	f.append(one, "", "th-1", marotte.EntryKindThinking,
		marotte.EntryThinking{Text: "The retry helper is in fetch.go."})
	f.append(one, "", "a-1", marotte.EntryKindText,
		marotte.EntryText{Text: "It lives in **fetch.go**."})

	two := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameEvent})

	f.append(one, "", "tc-1", marotte.EntryKindToolCall,
		marotte.EntryToolCall{ID: "tc-1", Title: "Read fetch.go", Kind: marotte.ToolKindRead})
	f.append(two, "", "a-2", marotte.EntryKindText,
		marotte.EntryText{Text: "The workflow step finished."})
	f.append(one, "", marotte.ToolResultID("tc-1"), marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "func retry(ctx) error"})
	f.append(one, "", "plan-1", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
		{Content: "Read the retry path", Status: marotte.PlanCompleted},
	}})
	f.append(two, "", "tc-2", marotte.EntryKindToolCall,
		marotte.EntryToolCall{ID: "tc-2", Title: "Run Command", Kind: marotte.ToolKindExecute})
	f.append(one, "", "s-1", marotte.EntryKindSteer, marotte.EntrySteer{
		Text: "also cap the retry count", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
	})
	f.append(one, "", "s-1:ack", marotte.EntryKindSteerAck,
		marotte.EntrySteerAck{SteerID: "s-1", Text: "reading your note"})
	f.append(two, "", marotte.ToolResultID("tc-2"), marotte.EntryKindToolResult,
		marotte.EntryToolResult{Status: marotte.ToolCompleted, Output: "ok"})
	// The turn's SECOND plan: it folds into the first plan's card on both sides, which is
	// the one fold that is a position rule rather than a kind rule.
	f.append(one, "", "plan-2", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
		{Content: "Read the retry path", Status: marotte.PlanCompleted},
		{Content: "Cap the retry count", Status: marotte.PlanInProgress},
	}})
	f.append(one, "", "cmp-1", marotte.EntryKindCompaction,
		marotte.EntryCompaction{Summary: "The retry work so far."})
	f.closeTurn(two, marotte.TurnOutcomeCompleted)
	f.closeTurn(one, marotte.TurnOutcomeCompleted)

	// The revert row, over a THROWAWAY turn: a revert takes every turn from its target
	// to the newest in FILE order, so reverting either turn above would take the other
	// with it and leave the interleaving this fixture exists for with nothing in it. The
	// record lands in the newest SURVIVOR rather than in the turn it names, so the
	// boundary renders inside a turn that survives while the range it states is gone
	// from every read surface — which is why the fixture still groups two turns.
	gone := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameEvent})
	f.closeTurn(gone, marotte.TurnOutcomeCompleted)
	_, opened, err := f.log.Revert(t.Context(), gone, marotte.TurnRevertCauseRewind, "kas-revert")
	if err != nil {
		t.Fatalf("revert %s: %v", gone, err)
	}
	if opened != nil {
		t.Fatalf("the revert minted a carrier; a surviving turn is what this fixture " +
			"reverts into, so step 3 must not fire")
	}

	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return normalizeFoldEntries(entries)
}

// mintedTurnID matches a server-minted turn id, which is the one value in this log that
// cannot be in a byte-pinned golden. OpenTurn mints "t-" plus ids.New(16, ids.StdLower),
// which is lowercase RFC 4648 base32 of 16 random bytes and therefore 26 characters
// (ceil(16*8/5)); the lower bound of 8 is what keeps a normalised name (t-1, t-2) out of
// the match, so this pattern can never rewrite its own output. A tighter quantifier
// matches a PREFIX of a real id and leaves its tail attached, which is a golden that
// regenerates to different bytes every run rather than a red test.
var mintedTurnID = regexp.MustCompile(`t-[0-9a-z]{8,32}`)

// normalizeFoldEntries renames each turn to t-<first appearance> and zeroes every ts, so
// the golden's bytes are the log's SHAPE rather than one run's random ids and clock.
//
// A minted id reaches THREE places and every one of them has to leave: the envelope's
// Turn; an id derived from a turn, which is not always its OWN turn (a turn_open's id IS
// the turn, a closer's is <turn>:close, and a revert record's is <reverted turn>:revert,
// filed in the surviving carrier); and a turn_revert payload's From and Through, which
// name a turn no read surface holds any more. One substitution over the bytes covers the
// last two, so no payload type is enumerated here and a later payload that names a turn
// is normalised by construction.
func normalizeFoldEntries(entries []marotte.Entry) []marotte.Entry {
	name := make(map[string]string)
	rename := func(id string) string {
		renamed, seen := name[id]
		if !seen {
			renamed = fmt.Sprintf("t-%d", len(name)+1)
			name[id] = renamed
		}
		return renamed
	}
	out := make([]marotte.Entry, 0, len(entries))
	for _, e := range entries {
		turn := rename(e.Turn)
		e.ID = mintedTurnID.ReplaceAllStringFunc(e.ID, rename)
		e.Payload = mintedTurnID.ReplaceAllFunc(e.Payload, func(id []byte) []byte {
			return []byte(rename(string(id)))
		})
		e.Turn = turn
		e.Ts = 0
		out = append(out, e)
	}
	return out
}

// foldRows derives one turn's rows from the production export path: writeEntry on a fresh
// builder per entry, with ONE turnRender per turn so the plan-once fold is the writer's
// own rather than a second rule stated here.
func foldRows(turn []marotte.Entry) []entryFoldRow {
	results, calls := indexResults(turn)
	r := turnRender{results: results, calls: calls, plan: newestPlan(turn)}
	rows := make([]entryFoldRow, 0, len(turn))
	for i := range turn {
		var b strings.Builder
		r.writeEntry(&b, &turn[i])
		edge, excluded := entryFoldEdges[turn[i].Kind]
		rows = append(rows, entryFoldRow{
			ID:       turn[i].ID,
			Seq:      turn[i].Seq,
			Kind:     turn[i].Kind,
			Renders:  b.Len() > 0,
			Compared: !excluded,
			Edge:     edge,
		})
	}
	return rows
}

// TestEntryFoldContract pins the fold contract to testdata/entry_fold.json, the
// cross-language fixture entry-fold-contract.node.test.ts reads.
func TestEntryFoldContract(t *testing.T) {
	entries := entryFoldLog(t)
	fx := entryFoldFixture{Comment: entryFoldFixtureComment, Entries: entries}
	for _, turn := range groupTurns(entries) {
		var open marotte.EntryTurnOpen
		if !decodePayload(&turn[0], &open) {
			t.Fatalf("turn %s: first entry %s is not a decodable turn_open", turn[0].Turn, turn[0].ID)
		}
		fx.Turns = append(fx.Turns, entryFoldTurn{Turn: turn[0].Turn, N: open.N, Entries: foldRows(turn)})
	}

	if len(fx.Turns) != 2 {
		t.Fatalf("grouped %d turns, want 2: the interleaving is the fixture's subject", len(fx.Turns))
	}
	// The log INTERLEAVES, or the grouping the fixture pins is file order under another
	// name and the TS half's own partition cannot disagree with it.
	if !foldInterleaved(entries) {
		t.Fatal("every turn's entries are contiguous in file order; the fixture pins nothing")
	}
	// Every declared edge OCCURS, or an exclusion table entry is a rule nothing exercises.
	seen := make(map[marotte.EntryKind]int)
	verdicts := make(map[bool]int)
	for _, turn := range fx.Turns {
		for i, row := range turn.Entries {
			if row.Seq != uint64(i) {
				t.Errorf("turn %s row %d: seq %d, want %d (contiguous from 0, turn_open at 0)",
					turn.Turn, i, row.Seq, i)
			}
			seen[row.Kind]++
			if row.Compared {
				verdicts[row.Renders]++
			}
		}
	}
	for kind := range entryFoldEdges {
		if seen[kind] == 0 {
			t.Errorf("declared edge %q never occurs in the fixture", kind)
		}
	}
	if verdicts[true] == 0 || verdicts[false] == 0 {
		t.Errorf("compared rows carry %d render and %d no-render verdicts; both must occur",
			verdicts[true], verdicts[false])
	}
	// Every tool_result is PAIRED, which is what keeps the fixture off the one shape the
	// two sides genuinely disagree about (see the comment block).
	for _, turn := range groupTurns(entries) {
		_, calls := indexResults(turn)
		for i := range turn {
			if turn[i].Kind != marotte.EntryKindToolResult {
				continue
			}
			if _, paired := calls[toolCallIDOfResult(turn[i].ID)]; !paired {
				t.Errorf("tool_result %s has no call in its turn; an unpaired result is outside this fixture", turn[i].ID)
			}
		}
	}

	pinGolden(t, "testdata/entry_fold.json", fx, "TestEntryFoldContract", "entry-fold-contract.node.test.ts")
}

// foldInterleaved answers whether any turn's entries are split by another turn's in file
// order.
func foldInterleaved(entries []marotte.Entry) bool {
	for i := 1; i < len(entries); i++ {
		if entries[i].Turn != entries[i-1].Turn {
			for j := i + 1; j < len(entries); j++ {
				if entries[j].Turn == entries[i-1].Turn {
					return true
				}
			}
		}
	}
	return false
}

// TestEntryFoldContract_PayloadsDecode is the fixture's other half of honesty: a writer
// that could not decode its payload emits nothing, which would read as a no-render
// verdict. Every payload here decodes, so every `renders: false` is the rule rather than
// a marshalling accident.
func TestEntryFoldContract_PayloadsDecode(t *testing.T) {
	for _, e := range entryFoldLog(t) {
		var into map[string]any
		if err := json.Unmarshal(e.Payload, &into); err != nil {
			t.Errorf("entry %s (%s): payload does not decode: %v", e.ID, e.Kind, err)
		}
	}
}
