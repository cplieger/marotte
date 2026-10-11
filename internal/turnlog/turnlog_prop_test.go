package turnlog

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"pgregory.net/rapid"
)

// They render in EVERY view, which is what makes them part of both orderings the property checks.
var laneless = map[marotte.EntryKind]bool{
	marotte.EntryKindTurnBind:         true,
	marotte.EntryKindSteer:            true,
	marotte.EntryKindPlan:             true,
	marotte.EntryKindCompaction:       true,
	marotte.EntryKindCompactionFailed: true,
	marotte.EntryKindSafetyBlocked:    true,
	marotte.EntryKindModelSwitched:    true,
	marotte.EntryKindTurnClose:        true,
}

// The step vocabulary the generator draws from: every sealing-table event plus the two
// carry shapes, so a partial marker can sit on a lane when a card lands in it.
const (
	stepText = iota
	stepThinking
	stepToolCall
	stepToolResult
	stepHook
	stepSteerRead
	stepSteerDropped
	stepSteerAck
	stepPlan
	stepCompaction
	stepCarryPartial
	stepCarryProse
	stepCount
)

// Arrival order equals persisted order equals broadcast order, over a random interleaving
// into a real entry-log sink. Two oracles, neither a copy of the sealing rules: within one
// VIEW, seq order is arrival order (a text entry arrives with its FIRST delta), and the log
// read off disk matches the sequence handed back for broadcast.
func TestArrivalOrderIsPersistedOrder(t *testing.T) {
	ctx := t.Context()
	base := t.TempDir()
	var runs int
	rapid.Check(t, func(rt *rapid.T) {
		runs++
		root := filepath.Join(base, fmt.Sprintf("run-%d", runs))
		lg, err := chat.OpenEntryLog(ctx, root, chat.NoHeader())
		if err != nil {
			rt.Fatalf("open entry log: %v", err)
		}
		defer func() { _ = lg.Close() }()

		opened, err := lg.OpenTurn(ctx, &chat.TurnSpec{
			Source: marotte.TurnOpenNamePrompt,
			Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "do the thing"},
		})
		if err != nil {
			rt.Fatalf("open turn: %v", err)
		}
		d := &driver{
			rt: rt, ctx: ctx,
			turn:     Open(opened.Turn, lg),
			arrival:  map[string]int{},
			openedAt: map[string]int{},
			carryAt:  map[string]int{},
		}
		d.broadcast = append(d.broadcast, *opened)
		d.arrival[opened.ID] = 0

		n := rapid.IntRange(1, 24).Draw(rt, "steps")
		for i := 1; i <= n; i++ {
			d.step(i, rapid.IntRange(0, stepCount-1).Draw(rt, fmt.Sprintf("kind%d", i)),
				rapid.SampledFrom([]string{"", "d1"}).Draw(rt, fmt.Sprintf("lane%d", i)))
		}
		d.step(n+1, -1, "")

		persisted, _, err := lg.TurnPage(opened.Turn, 0)
		if err != nil {
			rt.Fatalf("read the turn back: %v", err)
		}
		d.checkPersistedMatchesBroadcast(persisted)
		d.checkSeqIsContiguous(persisted)
		d.checkViewOrder(persisted, "")
		d.checkViewOrder(persisted, "d1")
		d.checkNoMarkerSurvived(persisted)
	})
}

type driver struct {
	rt   *rapid.T
	ctx  context.Context
	turn *Turn
	// arrival is the ordinal of the event that produced each entry; openedAt the
	// ordinal an entry OPENED at, which is a text entry's own arrival.
	arrival   map[string]int
	openedAt  map[string]int
	carryAt   map[string]int
	broadcast []marotte.Entry
	calls     []string
	plans     int
}

func (d *driver) step(at, kind int, lane string) {
	sealed, err := d.apply(at, kind, lane)
	if err != nil {
		d.rt.Fatalf("step %d (kind %d, lane %q): %v", at, kind, lane, err)
	}
	for _, s := range sealed {
		if _, known := d.arrival[s.Entry.ID]; !known {
			d.arrival[s.Entry.ID] = d.arrivalOf(s.Entry, at)
		}
		d.broadcast = append(d.broadcast, *s.Entry)
	}
	d.noteOpenEntries(at)
}

// arrivalOf is when the event that produced e arrived: a text or thinking entry with its
// first delta, a carry-released entry with the carry, others with their append. Keyed on
// the ENTRY's lane: one all-lane seal releases carries that arrived at different moments.
func (d *driver) arrivalOf(e *marotte.Entry, at int) int {
	if e.Kind != marotte.EntryKindText && e.Kind != marotte.EntryKindThinking {
		return at
	}
	if opened, ok := d.openedAt[e.ID]; ok {
		return opened
	}
	if carried, ok := d.carryAt[e.Lane]; ok {
		return carried
	}
	return at
}

func (d *driver) noteOpenEntries(at int) {
	for _, oe := range d.turn.OpenEntries() {
		if _, known := d.openedAt[oe.ID]; !known {
			d.openedAt[oe.ID] = at
		}
	}
}

func (d *driver) apply(at, kind int, lane string) ([]Sealed, error) {
	say := fmt.Sprintf("say-%d", at)
	switch kind {
	case -1:
		return d.turn.Close(d.ctx, marotte.TurnConclusion{
			Outcome: marotte.TurnOutcomeCompleted, RawStop: marotte.StopReasonEndTurn,
		})
	case stepText:
		return d.turn.TextDelta(d.ctx, lane, say, fmt.Sprintf("text %d ", at))
	case stepThinking:
		return d.turn.ThinkingDelta(d.ctx, lane, say, fmt.Sprintf("thought %d ", at))
	case stepToolCall:
		id := fmt.Sprintf("call-%d", at)
		d.calls = append(d.calls, id)
		return d.turn.ToolCall(d.ctx, lane, &marotte.EntryToolCall{ID: id, Status: marotte.ToolInProgress})
	case stepToolResult:
		if len(d.calls) == 0 {
			return nil, nil
		}
		id := d.calls[0]
		d.calls = d.calls[1:]
		return d.turn.ToolResult(d.ctx, lane, id, &marotte.EntryToolResult{Status: marotte.ToolCompleted})
	case stepHook:
		id := fmt.Sprintf("hook-%d", at)
		d.calls = append(d.calls, id)
		return d.turn.ToolCall(d.ctx, "", &marotte.EntryToolCall{ID: id, Kind: marotte.ToolKindHook})
	case stepSteerRead:
		return d.turn.Steer(d.ctx, fmt.Sprintf("steer-%d", at), &marotte.EntrySteer{
			Text: "also", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
		})
	case stepSteerDropped:
		return d.turn.Steer(d.ctx, fmt.Sprintf("steer-%d", at), &marotte.EntrySteer{
			Origin: marotte.SteerOriginUser, State: marotte.SteerStateDropped,
		})
	case stepSteerAck:
		return d.turn.SteerAck(d.ctx, lane, fmt.Sprintf("steer-%d", at), "handled")
	case stepPlan:
		d.plans++
		return d.turn.Plan(d.ctx, marotte.EntryPlan{
			Entries: []marotte.PlanEntry{{Content: fmt.Sprintf("step %d", d.plans)}},
		})
	case stepCompaction:
		return d.turn.Compaction(d.ctx, fmt.Sprintf("summary %d", at), 0)
	case stepCarryPartial:
		d.turn.SetSteerCarry(lane, say, SteerAckPrefix+fmt.Sprintf("%d: partly", at))
		d.carryAt[lane] = at
		return nil, nil
	case stepCarryProse:
		d.turn.SetSteerCarry(lane, say, "[maybe")
		d.carryAt[lane] = at
		return nil, nil
	}
	return nil, nil
}

func (d *driver) checkPersistedMatchesBroadcast(persisted []marotte.Entry) {
	if len(persisted) != len(d.broadcast) {
		d.rt.Fatalf("the log holds %d entries and %d were handed back for broadcast:\n log %v\n bus %v",
			len(persisted), len(d.broadcast), shapeOf(persisted), shapeOf(d.broadcast))
	}
	for i := range persisted {
		if persisted[i].ID != d.broadcast[i].ID || persisted[i].Seq != d.broadcast[i].Seq {
			d.rt.Fatalf("entry %d on disk is %s seq %d, broadcast was %s seq %d",
				i, persisted[i].ID, persisted[i].Seq, d.broadcast[i].ID, d.broadcast[i].Seq)
		}
	}
}

// checkSeqIsContiguous holds seq's own contract: per turn, contiguous from 0, where
// the turn_open is 0.
func (d *driver) checkSeqIsContiguous(persisted []marotte.Entry) {
	for i := range persisted {
		if persisted[i].Seq != uint64(i) {
			d.rt.Fatalf("entry %d carries seq %d, want %d: %v", i, persisted[i].Seq, i, shapeOf(persisted))
		}
	}
}

// checkViewOrder is the property: inside one view, seq order is arrival order.
func (d *driver) checkViewOrder(persisted []marotte.Entry, lane string) {
	prev, prevID := -1, ""
	for i := range persisted {
		e := &persisted[i]
		if e.Lane != lane && !laneless[e.Kind] {
			continue
		}
		got := d.arrival[e.ID]
		if got < prev {
			d.rt.Fatalf("in view %q, %s (arrived at %d) sealed after %s (arrived at %d): %v",
				lane, e.ID, got, prevID, prev, shapeOf(persisted))
		}
		prev, prevID = got, e.ID
	}
}

// checkNoMarkerSurvived holds the carry rule end to end: a marker the model opened
// and never closed never reaches the log, in any lane, however the draw interleaved
// it with a card.
func (d *driver) checkNoMarkerSurvived(persisted []marotte.Entry) {
	for i := range persisted {
		e := &persisted[i]
		if e.Kind != marotte.EntryKindText && e.Kind != marotte.EntryKindThinking {
			continue
		}
		var payload marotte.EntryText
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			d.rt.Fatalf("parse %s: %v", e.ID, err)
		}
		if strings.Contains(payload.Text, SteerAckPrefix) {
			d.rt.Fatalf("%s carries an unclosed steer marker: %q", e.ID, payload.Text)
		}
	}
}

func shapeOf(entries []marotte.Entry) []string {
	out := make([]string, 0, len(entries))
	for i := range entries {
		out = append(out, fmt.Sprintf("%d:%s/%s", entries[i].Seq, entries[i].Lane, entries[i].Kind))
	}
	return out
}

// A steer is lane-less and lands in the chat's OWN open turn whatever session produced the
// frame; which turn is the registry's decision.
func TestASteerLandsInTheOpenTurnWhateverSessionProducedIt(t *testing.T) {
	ctx := t.Context()
	turn, rec := open(t)
	rec.seal(turn.TextDelta(ctx, "d1", "say-1", "delegate prose"))
	sealed := rec.seal(turn.Steer(ctx, "steer-1", &marotte.EntrySteer{Text: "from a step session"}))
	last := sealed[len(sealed)-1].Entry
	switch {
	case last.Kind != marotte.EntryKindSteer:
		t.Fatalf("last sealed entry is %q, want the steer", last.Kind)
	case last.Turn != turn.ID():
		t.Errorf("steer landed in turn %q, want the open turn %q", last.Turn, turn.ID())
	case last.Lane != "":
		t.Errorf("steer carries lane %q, want lane-less", last.Lane)
	}
}
