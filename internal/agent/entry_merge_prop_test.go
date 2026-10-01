package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"pgregory.net/rapid"
)

// Property 3 of the design's test strategy: the merge is idempotent and
// order-preserving. Property 7's pure half rides along, because the two state the same
// invariant from either end — every id present before a rewrite is present after, in the
// same relative order within its turn, with the turns contiguous in n order.

// turnPlan is one turn of a generated transcript: which side holds it, how the two sides
// disagree about it, and how the record split its first say. One plan builds BOTH sides,
// so a paired turn cannot accidentally be generated unpairable.
type turnPlan struct {
	// InRecord and InProjected decide the three populations the merge has rules for: a
	// paired turn, a record turn no replay covers, and a replayed turn the record lost.
	InRecord    bool
	InProjected bool
	// Bound gives the record turn a turn_bind, rule one's only key. OutOfScope mints it
	// in another session, which is the scope that keeps the empty-turn retry straight,
	// and gives the projected side its own say ids so rule two cannot pair the two
	// either: the arm is a stale stamp meeting a turn it never named.
	Bound      bool
	OutOfScope bool
	// Says is how many content entries the turn holds, at least one so a projected turn
	// always carries rule two's key. Segments is how many pieces the record split the
	// first say into, which is the boundary set the union may never merge.
	Says     int
	Segments int
	// SteerLost is a steer the replay holds and the record does not, the crash case rule
	// 3 stamps.
	SteerLost bool
	// Placeholder decides whether the record's closer is one a process wrote or the
	// store's synthesized placeholder, the one arm KAS's own account may replace. A
	// record turn is ALWAYS closed, because the store's own open closer closes every
	// turn it finds open before it serves or appends anything, so an open record turn
	// cannot reach a merge.
	Placeholder bool
	// ProjClosed decides whether the replay saw a turn_end at all, which is rule 4's own
	// arm: an inserted turn with none gets the synthesized closer.
	ProjClosed bool
	// TailBytes is how much longer the replay's first say is than the record's segments
	// concatenated, which is where a crash loses deltas.
	TailBytes int
}

func genTurnPlans() *rapid.Generator[[]turnPlan] {
	plan := rapid.Custom(func(t *rapid.T) turnPlan {
		p := turnPlan{
			Says:     rapid.IntRange(1, 3).Draw(t, "says"),
			Segments: rapid.IntRange(1, 3).Draw(t, "segments"),
		}
		// One of the three populations, so every generated turn is in at least one side.
		switch rapid.IntRange(0, 2).Draw(t, "population") {
		case 0:
			p.InRecord, p.InProjected = true, true
		case 1:
			p.InRecord = true
		default:
			p.InProjected = true
		}
		p.Bound = rapid.Bool().Draw(t, "bound")
		p.OutOfScope = p.Bound && rapid.Bool().Draw(t, "out_of_scope")
		p.SteerLost = rapid.Bool().Draw(t, "steer_lost")
		p.Placeholder = rapid.Bool().Draw(t, "placeholder")
		p.ProjClosed = rapid.Bool().Draw(t, "proj_closed")
		p.TailBytes = rapid.IntRange(0, 8).Draw(t, "tail_bytes")
		return p
	})
	return rapid.SliceOfN(plan, 1, 4)
}

// buildSides turns a plan into the two accounts of one transcript. `stamp` is how a
// generated entry gets its Ts, so the same plan can be built with timestamps and without
// them: the merge reads none, and the only honest way to say so in Go is to show the
// output is identical either way.
func buildSides(t *rapid.T, plans []turnPlan, stamp func(int) int64) (record []RecordTurn, projected []translate.ProjectedTurn) {
	for i, p := range plans {
		recTurnID := fmt.Sprintf("T%d", i)
		projTurnID := fmt.Sprintf("P%d", i)
		kasID := fmt.Sprintf("kas-%d", i)
		var recRows, projRows []entryRow

		var prompt *marotte.EntryPrompt
		if p.Bound {
			prompt = &marotte.EntryPrompt{ID: fmt.Sprintf("m-%d", i), Text: "go"}
		}
		recRows = append(recRows, openRow(recTurnID, uint64(i+1), prompt))
		if p.Bound {
			sid := "sid-1"
			if p.OutOfScope {
				sid = "sid-other"
			}
			recRows = append(recRows, bindRow(recTurnID+":e1", kasID, sid))
		}
		projPrompt := prompt
		if p.Bound {
			// The replay carries KAS's own record id in the prompt, which is what rule one
			// compares against the record's bind.
			projPrompt = &marotte.EntryPrompt{ID: kasID, Text: "go"}
		}
		projRows = append(projRows, openRow(projTurnID, 0, projPrompt))

		for s := range p.Says {
			say := fmt.Sprintf("S%d-%d", i, s)
			projSay := say
			if p.OutOfScope {
				projSay = say + "-x"
			}
			whole := strings.Repeat("x", 4*(s+1))
			if s == 0 {
				// The record's own segment boundaries, which the union may never merge.
				per := len(whole) / p.Segments
				for k := range p.Segments {
					id := say
					if k > 0 {
						id = marotte.SaySegmentID(say, k+1)
					}
					from := k * per
					to := from + per
					if k == p.Segments-1 {
						to = len(whole)
					}
					recRows = append(recRows, textRow(id, "", whole[from:to]))
				}
				projRows = append(projRows, textRow(projSay, "", whole+strings.Repeat("y", p.TailBytes)))
				continue
			}
			recRows = append(recRows, textRow(say, "", whole))
			projRows = append(projRows, textRow(projSay, "", whole))
		}
		if p.SteerLost {
			projRows = append(projRows, steerRow(fmt.Sprintf("steer-lost-%d", i),
				marotte.EntrySteer{Text: "unread", Origin: marotte.SteerOriginUser}))
		}
		if p.Placeholder {
			recRows = append(recRows, placeholderClose(recTurnID+":e2"))
		} else {
			recRows = append(recRows, liveClose(recTurnID+":e2", marotte.TurnOutcomeCompleted, "end_turn"))
		}
		if p.ProjClosed {
			projRows = append(projRows, closeRow(projTurnID+":e1",
				marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}))
		}
		if p.InRecord {
			record = append(record, stampTurn(rapidTurn(t, recTurnID, recRows), stamp))
		}
		if p.InProjected {
			projected = append(projected, translate.ProjectedTurn{
				Entries: stampTurn(rapidTurn(t, projTurnID, projRows), stamp).Entries,
			})
		}
	}
	return record, projected
}

// rapidTurn is recTurn without a *testing.T, so the generator can build a turn.
func rapidTurn(t *rapid.T, turn string, rows []entryRow) RecordTurn {
	entries := make([]marotte.Entry, 0, len(rows))
	for i, r := range rows {
		entries = append(entries, marotte.Entry{
			ID: r.id, Turn: turn, Lane: r.lane, Kind: r.kind, Seq: uint64(i),
			Payload: mustJSONRapid(t, r.payload),
		})
	}
	return RecordTurn{Entries: entries}
}

func mustJSONRapid(t *rapid.T, v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return raw
}

// stampTurn writes each entry's Ts through the caller's own rule.
func stampTurn(turn RecordTurn, stamp func(int) int64) RecordTurn {
	for i := range turn.Entries {
		turn.Entries[i].Ts = stamp(i)
	}
	return turn
}

// TestMergeEntries_IsIdempotentAndOrderPreserving is property 3, plus property 7's pure
// half.
func TestMergeEntries_IsIdempotentAndOrderPreserving(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		plans := genTurnPlans().Draw(rt, "plans")
		record, projected := buildSides(rt, plans, func(i int) int64 { return int64(1000 + i) })

		merged, _ := MergeEntries(record, projected, "sid-1")
		assertMergeShape(rt, record, merged)
		assertArms(rt, plans, record, merged)

		// Idempotence: the second merge of the same replay must change nothing, which is
		// what makes a resumed chat stop rewriting its own log on every load.
		again, changed := MergeEntries(mergedAsRecord(merged), projected, "sid-1")
		if changed {
			rt.Fatalf("second merge reported a change:\nfirst:\n%s\nsecond:\n%s",
				dumpMerged(merged), dumpMerged(again))
		}
		if dumpMerged(merged) != dumpMerged(again) {
			rt.Fatalf("second merge differs:\nfirst:\n%s\nsecond:\n%s", dumpMerged(merged), dumpMerged(again))
		}
	})
}

// TestMergeEntries_ReadsNoTimestamp is the design's "no ts read" clause, stated the one
// way Go allows: the merge's output must be identical whether every timestamp is
// meaningful or every timestamp is zero. A merge that sorted by Ts, or fell back to it
// for a position, would answer differently.
func TestMergeEntries_ReadsNoTimestamp(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		plans := genTurnPlans().Draw(rt, "plans")
		// Descending stamps, so anything ordering by Ts would REVERSE what the spine says.
		withTs, projWithTs := buildSides(rt, plans, func(i int) int64 { return int64(9000 - i*7) })
		zeroed, projZeroed := buildSides(rt, plans, func(int) int64 { return 0 })

		a, _ := MergeEntries(withTs, projWithTs, "sid-1")
		b, _ := MergeEntries(zeroed, projZeroed, "sid-1")
		if got, want := shapeOf(a), shapeOf(b); got != want {
			rt.Fatalf("the merge's order depends on ts:\nwith ts:\n%s\nzeroed:\n%s", got, want)
		}
	})
}

// shapeOf is a merged log's turn and entry ids in order, with the payloads and
// timestamps dropped, which is exactly the part a ts read could move.
func shapeOf(turns []MergedTurn) string {
	var b strings.Builder
	for i := range turns {
		fmt.Fprintf(&b, "turn %d:\n", i)
		for _, e := range turns[i].Entries {
			fmt.Fprintf(&b, "  seq=%d %s %s\n", e.Seq, e.Kind, e.ID)
		}
	}
	return b.String()
}

// assertMergeShape holds the invariants both properties share.
func assertMergeShape(rt *rapid.T, record []RecordTurn, merged []MergedTurn) {
	// Every record entry is present after, in the same relative order WITHIN its turn.
	byTurn := make(map[string][]string, len(merged))
	for i := range merged {
		for _, e := range merged[i].Entries {
			byTurn[e.Turn] = append(byTurn[e.Turn], e.ID)
		}
	}
	for i := range record {
		turn := record[i].Entries[0].Turn
		want := make([]string, 0, len(record[i].Entries))
		for _, e := range record[i].Entries {
			want = append(want, e.ID)
		}
		got := byTurn[turn]
		if !isSubsequence(want, got) {
			rt.Fatalf("turn %s lost an entry or reordered one: record %v, merged %v:\n%s",
				turn, want, got, dumpMerged(merged))
		}
	}
	for i := range merged {
		entries := merged[i].Entries
		if len(entries) == 0 {
			rt.Fatalf("merged turn %d is empty:\n%s", i, dumpMerged(merged))
		}
		for seq := range entries {
			if entries[seq].Seq != uint64(seq) {
				rt.Fatalf("turn %d entry %d has seq %d, want contiguous from 0:\n%s",
					i, seq, entries[seq].Seq, dumpMerged(merged))
			}
		}
		if got := countKind(merged[i], marotte.EntryKindTurnOpen); got != 1 {
			rt.Fatalf("merged turn %d holds %d turn_open entries, want 1:\n%s", i, got, dumpMerged(merged))
		}
		if got := countKind(merged[i], marotte.EntryKindTurnClose); got != 1 {
			rt.Fatalf("merged turn %d holds %d turn_close entries, want 1:\n%s", i, got, dumpMerged(merged))
		}
		var open marotte.EntryTurnOpen
		if err := json.Unmarshal(entryOpenPayload(merged[i]), &open); err != nil {
			rt.Fatalf("parse turn_open of merged turn %d: %v", i, err)
		}
		if open.N != uint64(i)+1 {
			rt.Fatalf("merged turn %d has n %d, want %d — turns are contiguous in n order:\n%s",
				i, open.N, i+1, dumpMerged(merged))
		}
	}
}

// assertArms checks each generated arm's own outcome, turn by turn: which turns paired
// and which were inserted, whose closer survived, where a lost steer landed and what it
// says, and that a longer replayed say extended the record's LAST segment and nothing
// else. The record is the oracle for the segments, so nothing here re-derives the merge.
func assertArms(rt *rapid.T, plans []turnPlan, record []RecordTurn, merged []MergedTurn) {
	at := make(map[string]int, len(merged))
	for i := range merged {
		at[merged[i].Entries[0].Turn] = i
	}
	recordAt := make(map[string]RecordTurn, len(record))
	for i := range record {
		recordAt[record[i].Entries[0].Turn] = record[i]
	}
	firstRecord := len(merged)
	for _, i := range at {
		if strings.HasPrefix(merged[i].Entries[0].Turn, "T") && i < firstRecord {
			firstRecord = i
		}
	}
	pairedSeen := false
	for i, p := range plans {
		recID, projID := fmt.Sprintf("T%d", i), fmt.Sprintf("P%d", i)
		pairs := p.InRecord && p.InProjected && !p.OutOfScope
		_, hasRec := at[recID]
		_, hasProj := at[projID]
		switch {
		case pairs:
			// Rule one when bound, rule two otherwise: ONE turn, the record's id.
			if !hasRec || hasProj {
				rt.Fatalf("plan %d should pair into %s alone, merged holds T=%v P=%v:\n%s",
					i, recID, hasRec, hasProj, dumpMerged(merged))
			}
			assertPairedTurn(rt, i, p, recordAt[recID], merged[at[recID]])
			pairedSeen = true
		case p.InRecord && p.InProjected:
			// A stale stamp and a turn it never named: both survive, neither touched.
			if !hasRec || !hasProj {
				rt.Fatalf("plan %d is out of scope and should stay unpaired, merged holds T=%v P=%v:\n%s",
					i, hasRec, hasProj, dumpMerged(merged))
			}
			assertKeptTurn(rt, i, recordAt[recID], merged[at[recID]])
			assertInsertedTurn(rt, i, p, merged[at[projID]], !pairedSeen && at[projID] >= firstRecord)
		case p.InRecord:
			if !hasRec || hasProj {
				rt.Fatalf("plan %d is record-only, merged holds T=%v P=%v:\n%s", i, hasRec, hasProj, dumpMerged(merged))
			}
			assertKeptTurn(rt, i, recordAt[recID], merged[at[recID]])
		default:
			if hasRec || !hasProj {
				rt.Fatalf("plan %d is replay-only, merged holds T=%v P=%v:\n%s", i, hasRec, hasProj, dumpMerged(merged))
			}
			assertInsertedTurn(rt, i, p, merged[at[projID]], !pairedSeen && at[projID] >= firstRecord)
		}
	}
}

// assertPairedTurn is the union's account of one paired turn.
func assertPairedTurn(rt *rapid.T, i int, p turnPlan, rec RecordTurn, got MergedTurn) {
	_, closer := closerOf(rt, got)
	switch {
	case !p.Placeholder:
		// A closer a process wrote is the live observation and stands, whatever the
		// replay's own turn_end said.
		if closer.Outcome != marotte.TurnOutcomeCompleted || closer.StopReasonRaw != "end_turn" {
			rt.Fatalf("plan %d: live closer replaced by %+v", i, closer)
		}
	case p.ProjClosed:
		// The placeholder yields to KAS's own account of the crashed turn.
		if closer.Outcome != marotte.TurnOutcomeCancelled || closer.StopReasonRaw != "cancelled" {
			rt.Fatalf("plan %d: placeholder closer did not take KAS's account, got %+v", i, closer)
		}
	default:
		// KAS had no account either, so the placeholder is the truth and stays.
		if closer.StopReasonRaw != string(marotte.StopReasonUnterminated) {
			rt.Fatalf("plan %d: placeholder closer replaced by %+v with no turn_end on the replay", i, closer)
		}
	}
	if closer.Model != "opus" {
		rt.Fatalf("plan %d: closer model = %q, want the record's — the replay carries none", i, closer.Model)
	}
	assertLostSteer(rt, i, p, got)

	// The record's segments of the first say, in order, are the oracle: every segment
	// but the last is byte-identical, and the last carries exactly the replayed tail.
	say := fmt.Sprintf("S%d-0", i)
	want := sayTexts(rt, rec.Entries, say)
	have := sayTexts(rt, got.Entries, say)
	if len(have) != len(want) || len(want) != p.Segments {
		rt.Fatalf("plan %d: say %s has %d segments after the merge, record had %d, plan said %d:\n%s",
			i, say, len(have), len(want), p.Segments, dumpMerged([]MergedTurn{got}))
	}
	want[len(want)-1] += strings.Repeat("y", p.TailBytes)
	for k := range want {
		if have[k] != want[k] {
			rt.Fatalf("plan %d: say %s segment %d = %q, want %q", i, say, k+1, have[k], want[k])
		}
	}
}

// assertKeptTurn is rule 2 at turn granularity: an unpaired record turn is emitted at its
// record position with nothing inserted and nothing rewritten but seq and n.
func assertKeptTurn(rt *rapid.T, i int, rec RecordTurn, got MergedTurn) {
	if len(got.Entries) != len(rec.Entries) {
		rt.Fatalf("plan %d: unpaired record turn gained entries, %d -> %d:\n%s",
			i, len(rec.Entries), len(got.Entries), dumpMerged([]MergedTurn{got}))
	}
	for k := range rec.Entries {
		if got.Entries[k].ID != rec.Entries[k].ID {
			rt.Fatalf("plan %d: unpaired record turn entry %d is %q, want %q", i, k, got.Entries[k].ID, rec.Entries[k].ID)
		}
		if rec.Entries[k].Kind != marotte.EntryKindTurnOpen && !sameRawJSON(rec.Entries[k].Payload, got.Entries[k].Payload) {
			rt.Fatalf("plan %d: unpaired record turn rewrote %s %q", i, rec.Entries[k].Kind, rec.Entries[k].ID)
		}
	}
}

// assertInsertedTurn is rule 3 at turn granularity plus rule 4: a replayed turn the
// record lacks is inserted whole, its steers stamped dropped/restart, its closer KAS's
// own when the replay had one and the synthesized placeholder otherwise. The caller
// reports headMissed when the turn had no paired predecessor and still landed at or
// past a record turn — the head rule the resumed session's first merge rests on.
func assertInsertedTurn(rt *rapid.T, i int, p turnPlan, got MergedTurn, headMissed bool) {
	closerID, closer := closerOf(rt, got)
	if p.ProjClosed {
		if closer.Outcome != marotte.TurnOutcomeCancelled || closerID != got.Entries[0].Turn+":e1" {
			rt.Fatalf("plan %d: inserted turn's own closer replaced by %+v (id %q)", i, closer, closerID)
		}
	} else if closer.StopReasonRaw != string(marotte.StopReasonUnterminated) || closerID != got.Entries[0].Turn+synthesizedCloserSuffix {
		rt.Fatalf("plan %d: inserted turn with no turn_end has closer %+v (id %q), want the synthesized placeholder", i, closer, closerID)
	}
	if got.Entries[len(got.Entries)-1].Kind != marotte.EntryKindTurnClose {
		rt.Fatalf("plan %d: inserted turn does not end with its closer:\n%s", i, dumpMerged([]MergedTurn{got}))
	}
	assertLostSteer(rt, i, p, got)
	if headMissed {
		rt.Fatalf("plan %d: inserted before any paired turn, so it belongs at the HEAD ahead of every record turn", i)
	}
}

// assertLostSteer is rule 3's stamp: a steer the record never held is inserted as
// dropped/restart, never as read and never as not-known.
func assertLostSteer(rt *rapid.T, i int, p turnPlan, got MergedTurn) {
	id := fmt.Sprintf("steer-lost-%d", i)
	found := false
	for _, e := range got.Entries {
		if e.ID != id {
			continue
		}
		found = true
		var steer marotte.EntrySteer
		if err := json.Unmarshal(e.Payload, &steer); err != nil {
			rt.Fatalf("plan %d: parse steer %s: %v", i, id, err)
		}
		if steer.State != marotte.SteerStateDropped || steer.Reason != marotte.SteerReasonRestart {
			rt.Fatalf("plan %d: lost steer %s is %+v, want dropped/restart", i, id, steer)
		}
	}
	if found != p.SteerLost {
		rt.Fatalf("plan %d: lost steer present=%v, plan said %v:\n%s", i, found, p.SteerLost, dumpMerged([]MergedTurn{got}))
	}
}

// closerOf is a turn's one turn_close: its entry id and its payload.
func closerOf(rt *rapid.T, turn MergedTurn) (string, marotte.EntryTurnClose) {
	var closer marotte.EntryTurnClose
	for _, e := range turn.Entries {
		if e.Kind != marotte.EntryKindTurnClose {
			continue
		}
		if err := json.Unmarshal(e.Payload, &closer); err != nil {
			rt.Fatalf("parse turn_close %s: %v", e.ID, err)
		}
		return e.ID, closer
	}
	rt.Fatalf("turn %s holds no turn_close:\n%s", turn.Entries[0].Turn, dumpMerged([]MergedTurn{turn}))
	return "", closer
}

// sayTexts is the text of every segment of one say, in order.
func sayTexts(rt *rapid.T, entries []marotte.Entry, say string) []string {
	var texts []string
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindText || marotte.SayIDOf(entries[i].ID) != say {
			continue
		}
		var payload marotte.EntryText
		if err := json.Unmarshal(entries[i].Payload, &payload); err != nil {
			rt.Fatalf("parse text %s: %v", entries[i].ID, err)
		}
		texts = append(texts, payload.Text)
	}
	return texts
}

// entryOpenPayload is a merged turn's turn_open payload bytes.
func entryOpenPayload(turn MergedTurn) []byte {
	for _, e := range turn.Entries {
		if e.Kind == marotte.EntryKindTurnOpen {
			return e.Payload
		}
	}
	return nil
}

// isSubsequence reports whether want appears inside got in order, which is what "the
// same relative order within its turn" means when entries may be inserted between.
func isSubsequence(want, got []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}
