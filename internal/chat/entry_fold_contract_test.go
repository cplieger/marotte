package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// entryFoldFixture is testdata/entry_fold.json: one log of two interleaved turns, the export's grouping, and per
// entry whether the export renders at that entry's position. Go produces it from the production writer
// (UPDATE_GOLDEN=1); entry-fold-contract.node.test.ts decodes it and asks block-window.ts's entryRenders.
type entryFoldFixture struct {
	Comment []string        `json:"_comment"`
	Entries []marotte.Entry `json:"entries"`
	Turns   []entryFoldTurn `json:"turns"`
}

type entryFoldTurn struct {
	Turn    string         `json:"turn"`
	N       uint64         `json:"n"`
	Entries []entryFoldRow `json:"entries"`
}

// Compared is false where the halves deliberately disagree or render at different scopes; Edge says
// which.
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

// entryFoldEdges is the declared-edge table: the kinds not compared, each with its reason in the golden.
var entryFoldEdges = map[marotte.EntryKind]string{
	marotte.EntryKindTurnOpen:  "card-level on the client: the header band, not a body row",
	marotte.EntryKindTurnClose: "card-level on the client: the footer, a sibling of .turn-body",
}

// Turn ids are normalised to t-1, t-2 and timestamps zeroed. Nothing may assume a turn is
// contiguous, and with one turn file order is the grouping.
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
	// The second plan folds into the first plan's card on both sides: a position rule, not a kind rule.
	f.append(one, "", "plan-2", marotte.EntryKindPlan, marotte.EntryPlan{Entries: []marotte.PlanEntry{
		{Content: "Read the retry path", Status: marotte.PlanCompleted},
		{Content: "Cap the retry count", Status: marotte.PlanInProgress},
	}})
	f.append(one, "", "cmp-1", marotte.EntryKindCompaction,
		marotte.EntryCompaction{Summary: "The retry work so far."})
	f.closeTurn(two, marotte.TurnOutcomeCompleted)
	f.closeTurn(one, marotte.TurnOutcomeCompleted)

	// The revert is over a throwaway turn: reverting takes every turn from its target to the newest in file order. The
	// record lands in the newest survivor, so the boundary renders in a surviving turn.
	gone := f.openTurn(&TurnSpec{Source: marotte.TurnOpenNameEvent})
	f.closeTurn(gone, marotte.TurnOutcomeCompleted)
	_, minted, err := f.log.Revert(t.Context(), gone, marotte.TurnRevertCauseRewind, "kas-revert")
	if err != nil {
		t.Fatalf("revert %s: %v", gone, err)
	}
	if len(minted) != 0 {
		t.Fatalf("the revert minted a carrier; a surviving turn is what this fixture " +
			"reverts into, so step 3 must not fire")
	}

	entries, err := f.log.All()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return normalizeFoldEntries(entries)
}

// mintedTurnID matches a server-minted turn id: "t-" plus 26 base32 characters (ids.New(16, ids.StdLower)). The
// lower bound of 8 keeps normalised names out; a tighter quantifier would match a prefix and leave the tail.
var mintedTurnID = regexp.MustCompile(`t-[0-9a-z]{8,32}`)

// normalizeFoldEntries renames each turn to t-<first appearance> and zeroes every ts, so the golden pins shape. A
// minted id reaches the envelope's Turn, derived ids (<turn>:close, <turn>:revert) and turn_revert's From and
// Through; one byte-level substitution covers the last two for any payload.
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

// foldRows derives one turn's rows from the export path: writeEntry per entry with one turnRender per turn, so the
// plan-once fold is the writer's own.
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
	// Interleaved, or the grouping is just file order.
	if !foldInterleaved(entries) {
		t.Fatal("every turn's entries are contiguous in file order; the fixture pins nothing")
	}
	// Every declared edge occurs.
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
	// Every tool_result is paired, avoiding the one shape the sides genuinely disagree on.
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

// TestEntryFoldContract_PayloadsDecode pins that an undecodable payload renders nothing, so every `renders: false` must be the
// rule, not a decode failure.
func TestEntryFoldContract_PayloadsDecode(t *testing.T) {
	for _, e := range entryFoldLog(t) {
		var into map[string]any
		if err := json.Unmarshal(e.Payload, &into); err != nil {
			t.Errorf("entry %s (%s): payload does not decode: %v", e.ID, e.Kind, err)
		}
	}
}
