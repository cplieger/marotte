package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// entryOf builds one entry; a projected entry passes seq 0.
func entryOf(t *testing.T, turn string, seq uint64, kind marotte.EntryKind, id, lane string, payload any) marotte.Entry {
	t.Helper()
	return marotte.Entry{
		ID: id, Turn: turn, Lane: lane, Kind: kind, Seq: seq, Payload: mustJSON(t, payload),
	}
}

// recTurn builds one record turn, numbering seq from 0 as the log does.
func recTurn(t *testing.T, turn string, rows ...entryRow) RecordTurn {
	t.Helper()
	entries := make([]marotte.Entry, 0, len(rows))
	for i, r := range rows {
		entries = append(entries, entryOf(t, turn, uint64(i), r.kind, r.id, r.lane, r.payload))
	}
	return RecordTurn{Entries: entries}
}

// projTurn builds one projected turn: KAS order, seq unassigned.
func projTurn(t *testing.T, turn string, rows ...entryRow) translate.ProjectedTurn {
	t.Helper()
	entries := make([]marotte.Entry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, entryOf(t, turn, 0, r.kind, r.id, r.lane, r.payload))
	}
	return translate.ProjectedTurn{Entries: entries}
}

// entryRow is one row of a built turn, named so a lane and an id cannot be transposed.
type entryRow struct {
	kind    marotte.EntryKind
	id      string
	lane    string
	payload any
}

func openRow(id string, n uint64, prompt *marotte.EntryPrompt) entryRow {
	source := marotte.TurnOpenNameWireTurnStart
	if prompt != nil {
		source = marotte.TurnOpenNamePrompt
	}
	return entryRow{
		kind:    marotte.EntryKindTurnOpen,
		id:      id,
		payload: marotte.EntryTurnOpen{Prompt: prompt, Source: source, N: n},
	}
}

func bindRow(id, kasID, sid string) entryRow {
	return entryRow{
		kind:    marotte.EntryKindTurnBind,
		id:      id,
		payload: marotte.EntryTurnBind{KASMessageID: kasID, SessionID: sid},
	}
}

func textRow(id, lane, text string) entryRow {
	return entryRow{kind: marotte.EntryKindText, id: id, lane: lane, payload: marotte.EntryText{Text: text}}
}

func callRow(id, lane, delegate string) entryRow {
	return entryRow{
		kind:    marotte.EntryKindToolCall,
		id:      id,
		lane:    lane,
		payload: marotte.EntryToolCall{ID: id, AgentSubtaskID: delegate},
	}
}

func resultRow(callID, lane string, status marotte.ToolStatus) entryRow {
	return entryRow{
		kind:    marotte.EntryKindToolResult,
		id:      marotte.ToolResultID(callID),
		lane:    lane,
		payload: marotte.EntryToolResult{Status: status},
	}
}

func steerRow(id string, steer marotte.EntrySteer) entryRow {
	return entryRow{kind: marotte.EntryKindSteer, id: id, payload: steer}
}

func closeRow(id string, conclusion marotte.EntryTurnClose) entryRow {
	return entryRow{kind: marotte.EntryKindTurnClose, id: id, payload: conclusion}
}

// liveClose is a process-written closer, which the union must leave alone.
func liveClose(id string, outcome marotte.TurnOutcome, raw marotte.StopReason) entryRow {
	return closeRow(id, marotte.EntryTurnClose{Outcome: outcome, StopReasonRaw: string(raw), Model: "opus"})
}

// placeholderClose is the store's synthesized closer, which KAS's account may replace.
func placeholderClose(id string) entryRow {
	return closeRow(id, marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
		Model:         "opus",
	})
}

// dumpMerged renders a merged log as `n turn seq kind id` lines.
func dumpMerged(turns []MergedTurn) string {
	var b strings.Builder
	for i := range turns {
		fmt.Fprintf(&b, "turn %d:\n", i)
		for _, e := range turns[i].Entries {
			fmt.Fprintf(&b, "  seq=%d %s[%s] %s %s\n", e.Seq, e.Kind, e.Lane, e.ID, e.Payload)
		}
	}
	return b.String()
}

// entryIn is a merged turn's first entry of one kind.
func entryIn(t *testing.T, turn MergedTurn, kind marotte.EntryKind) marotte.Entry {
	t.Helper()
	for _, e := range turn.Entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("merged turn holds no %s:\n%s", kind, dumpMerged([]MergedTurn{turn}))
	return marotte.Entry{}
}

// decodePayload decodes one entry's payload into v.
func decodePayload(t *testing.T, e marotte.Entry, v any) {
	t.Helper()
	if err := json.Unmarshal(e.Payload, v); err != nil {
		t.Fatalf("parse %s payload %s: %v", e.Kind, e.Payload, err)
	}
}

// countKind is how many entries of one kind a merged turn holds.
func countKind(turn MergedTurn, kind marotte.EntryKind) int {
	n := 0
	for _, e := range turn.Entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// idsOf is a merged turn's entry ids in order.
func idsOf(turn MergedTurn) []string {
	ids := make([]string, 0, len(turn.Entries))
	for _, e := range turn.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// TestMergeEntries_TwoClosersYieldOneAndItIsTheRecords pins one turn_close, the record's: closers pair by kind.
func TestMergeEntries_TwoClosersYieldOneAndItIsTheRecords(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "hello"),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "hello"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want 1:\n%s", len(merged), dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindTurnClose); got != 1 {
		t.Fatalf("merged turn holds %d turn_close entries, want 1:\n%s", got, dumpMerged(merged))
	}
	closer := entryIn(t, merged[0], marotte.EntryKindTurnClose)
	if closer.ID != "T1:e1" {
		t.Errorf("closer id = %q, want the record's", closer.ID)
	}
	var payload marotte.EntryTurnClose
	decodePayload(t, closer, &payload)
	if payload.Outcome != marotte.TurnOutcomeCompleted {
		t.Errorf("outcome = %q, want the record's %q — a live close is the live observation",
			payload.Outcome, marotte.TurnOutcomeCompleted)
	}
	if changed {
		t.Errorf("changed = true, want false — nothing moved:\n%s", dumpMerged(merged))
	}
}

// TestMergeEntries_AnInsertedTurnWithNoCloseGetsTheSynthesizedOne pins rule 4's placeholder closer.
func TestMergeEntries_AnInsertedTurnWithNoCloseGetsTheSynthesizedOne(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, &marotte.EntryPrompt{ID: "kas-1", Text: "go"}),
		bindRow("T1:e1", "kas-1", "sid-1"),
		textRow("S1", "", "on it"),
		liveClose("T1:e2", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{
		projTurn(t, "P0", openRow("P0", 0, nil), textRow("S0", "", "older history")),
		projTurn(t, "P1",
			openRow("P1", 0, &marotte.EntryPrompt{ID: "kas-1", Text: "go"}),
			textRow("S1", "", "on it"),
			closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
		),
	}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true — a turn was inserted:\n%s", dumpMerged(merged))
	}
	if len(merged) != 2 {
		t.Fatalf("merged %d turns, want 2:\n%s", len(merged), dumpMerged(merged))
	}
	closer := entryIn(t, merged[0], marotte.EntryKindTurnClose)
	if closer.ID != "P0"+":close" {
		t.Errorf("synthesized closer id = %q, want a derived one so the merge is idempotent", closer.ID)
	}
	var payload marotte.EntryTurnClose
	decodePayload(t, closer, &payload)
	if payload.Outcome != marotte.TurnOutcomeInterrupted || payload.StopReasonRaw != string(marotte.StopReasonUnterminated) {
		t.Errorf("synthesized closer = %+v, want the interrupted/unterminated placeholder", payload)
	}
	if got := merged[0].Entries[len(merged[0].Entries)-1].Kind; got != marotte.EntryKindTurnClose {
		t.Errorf("last entry of the inserted turn is %s, want the closer appended last", got)
	}
}

// TestMergeEntries_TheEmptyTurnRetryPairsOnlyTheRetry is why rule one is session-scoped: both
// turns carry one prompt id in two sessions.
func TestMergeEntries_TheEmptyTurnRetryPairsOnlyTheRetry(t *testing.T) {
	record := []RecordTurn{
		recTurn(t, "T1",
			openRow("T1", 1, &marotte.EntryPrompt{ID: "P", Text: "go"}),
			bindRow("T1:e1", "kas-P", "sid-0"),
			liveClose("T1:e2", marotte.TurnOutcomeEmpty, "end_turn"),
		),
		recTurn(t, "T2",
			openRow("T2", 2, &marotte.EntryPrompt{ID: "P", Text: "go"}),
			bindRow("T2:e1", "kas-retry", "sid-1"),
			textRow("S", "", "second time"),
			liveClose("T2:e2", marotte.TurnOutcomeCompleted, "end_turn"),
		),
	}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, &marotte.EntryPrompt{ID: "kas-retry", Text: "go"}),
		textRow("S", "", "second time"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, _ := MergeEntries(record, projected, "sid-1")
	if len(merged) != 2 {
		t.Fatalf("merged %d turns, want 2 — nothing may be inserted:\n%s", len(merged), dumpMerged(merged))
	}
	// The empty turn is untouched; the retry is paired.
	if got := merged[0].Entries[0].Turn; got != "T1" {
		t.Errorf("first merged turn is %q, want the empty turn T1", got)
	}
	if got := merged[1].Entries[0].Turn; got != "T2" {
		t.Errorf("second merged turn is %q, want the retry T2", got)
	}
	if got := countKind(merged[0], marotte.EntryKindText); got != 0 {
		t.Errorf("the empty turn gained %d text entries, want 0 — it must not pair", got)
	}
}

// TestMergeEntries_AnOutOfScopeBindStaysUnpaired pins that an out-of-scope bind offers nothing.
func TestMergeEntries_AnOutOfScopeBindStaysUnpaired(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, &marotte.EntryPrompt{ID: "P", Text: "go"}),
		bindRow("T1:e1", "kas-1", "sid-other"),
		liveClose("T1:e2", marotte.TurnOutcomeEmpty, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, &marotte.EntryPrompt{ID: "kas-1", Text: "go"}),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true — the unpaired projected turn is inserted")
	}
	if len(merged) != 2 {
		t.Fatalf("merged %d turns, want 2 (the insertion plus the record's):\n%s", len(merged), dumpMerged(merged))
	}
}

// TestMergeEntries_AWithheldBindPairsByTheFirstContentID pins rule two for a turn whose bind never arrived.
func TestMergeEntries_AWithheldBindPairsByTheFirstContentID(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, &marotte.EntryPrompt{ID: "P", Text: "go"}),
		textRow("S", "", "answer"),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, &marotte.EntryPrompt{ID: "kas-1", Text: "go"}),
		textRow("S", "", "answer"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if len(merged) != 1 || changed {
		t.Fatalf("merged %d turns, changed=%v; want 1 turn and no change:\n%s",
			len(merged), changed, dumpMerged(merged))
	}
}

// TestMergeEntries_ARecordLaneOutranksADisagreeingProjectedOne pins the record's live lane and the logged disagreement.
func TestMergeEntries_ARecordLaneOutranksADisagreeingProjectedOne(t *testing.T) {
	logs := captureLogs(t)
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		callRow("call-1", "d-9", ""),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		callRow("call-1", "d-other", ""),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, _ := MergeEntries(record, projected, "sid-1")
	call := entryIn(t, merged[0], marotte.EntryKindToolCall)
	if call.Lane != "d-9" {
		t.Errorf("lane = %q, want the record's %q", call.Lane, "d-9")
	}
	if got := logs.String(); !strings.Contains(got, "another lane than the record") {
		t.Errorf("logged %q, want a lane-disagreement warning", got)
	}
}

// TestMergeEntries_AnEmptyRecordLaneTakesTheProjectedOne is the other half of the lane rule.
func TestMergeEntries_AnEmptyRecordLaneTakesTheProjectedOne(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		callRow("call-1", "", ""),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		callRow("call-1", "d-9", ""),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if call := entryIn(t, merged[0], marotte.EntryKindToolCall); call.Lane != "d-9" {
		t.Errorf("lane = %q, want the projected %q", call.Lane, "d-9")
	}
	if !changed {
		t.Errorf("changed = false, want true — the lane moved")
	}
}

// TestMergeEntries_AnInvocationPairsOnTheDelegateUUID pins that the replayed id carries KAS's
// sub_agent_start suffix, so an id rule would duplicate every delegate card on resume.
func TestMergeEntries_AnInvocationPairsOnTheDelegateUUID(t *testing.T) {
	fx := loadInvocationFixture(t)
	projected := projectInvocationFixture(t, fx)

	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("7c3d-say", "", "delegating the sweep"),
		callRow(fx.ActionID, "", fx.DelegateID),
		textRow("d9-1-say", fx.DelegateID, "swept 3 files"),
		resultRow(fx.ActionID, "", marotte.ToolCompleted),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}

	merged, _ := MergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want 1:\n%s", len(merged), dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindToolCall); got != 1 {
		t.Errorf("merged turn holds %d tool_call entries, want 1:\n%s", got, dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindToolResult); got != 1 {
		t.Errorf("merged turn holds %d tool_result entries, want 1:\n%s", got, dumpMerged(merged))
	}
	if call := entryIn(t, merged[0], marotte.EntryKindToolCall); call.ID != fx.ActionID {
		t.Errorf("card id = %q, want the record's %q", call.ID, fx.ActionID)
	}
	if res := entryIn(t, merged[0], marotte.EntryKindToolResult); res.ID != marotte.ToolResultID(fx.ActionID) {
		t.Errorf("result id = %q, want the record's %q", res.ID, marotte.ToolResultID(fx.ActionID))
	}
}

// TestMergeEntries_ADelegationThatLeadsItsTurnPairsTheTURN pins the same suffix at turn level:
// a turn led by an invocation keys on the delegate uuid.
func TestMergeEntries_ADelegationThatLeadsItsTurnPairsTheTURN(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		callRow("act-1", "", "d-9"),
		textRow("d9-1-say", "d-9", "swept 3 files"),
		resultRow("act-1", "", marotte.ToolCompleted),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		callRow("act-1-sub-agent-start", "", "d-9"),
		textRow("d9-1-say", "d-9", "swept 3 files"),
		resultRow("act-1-sub-agent-start", "", marotte.ToolCompleted),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, _ := MergeEntries(record, projected, "sid-1")
	if len(merged) != 1 {
		t.Fatalf("merged %d turns, want 1 — the two are one delegating turn:\n%s",
			len(merged), dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindToolCall); got != 1 {
		t.Errorf("merged turn holds %d tool_call entries, want 1:\n%s", got, dumpMerged(merged))
	}
	if call := entryIn(t, merged[0], marotte.EntryKindToolCall); call.ID != "act-1" {
		t.Errorf("card id = %q, want the record's %q", call.ID, "act-1")
	}
}

// TestMergeEntries_ASynthesizedCloserYieldsToKASsOwnAccount pins the placeholder yielding.
func TestMergeEntries_ASynthesizedCloserYieldsToKASsOwnAccount(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		callRow("call-1", "", ""),
		resultRow("call-1", "", marotte.ToolAborted),
		placeholderClose("T1:e1"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		callRow("call-1", "", ""),
		resultRow("call-1", "", marotte.ToolFailed),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true:\n%s", dumpMerged(merged))
	}
	var closer marotte.EntryTurnClose
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindTurnClose), &closer)
	if closer.Outcome != marotte.TurnOutcomeCancelled || closer.StopReasonRaw != "cancelled" {
		t.Errorf("closer = %+v, want KAS's own cancelled account", closer)
	}
	if closer.Model != "opus" {
		t.Errorf("model = %q, want the record's — the replay carries none", closer.Model)
	}
	var res marotte.EntryToolResult
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindToolResult), &res)
	if res.Status != marotte.ToolFailed {
		t.Errorf("result status = %q, want %q — `aborted` is a close-time inference",
			res.Status, marotte.ToolFailed)
	}
}

// TestMergeEntries_ALiveCloserIsNotReplaced pins that only the placeholder yields.
func TestMergeEntries_ALiveCloserIsNotReplaced(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "done"),
		liveClose("T1:e1", marotte.TurnOutcomeInterrupted, "interrupted"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "done"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	var closer marotte.EntryTurnClose
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindTurnClose), &closer)
	if closer.Outcome != marotte.TurnOutcomeInterrupted {
		t.Errorf("outcome = %q, want the record's %q", closer.Outcome, marotte.TurnOutcomeInterrupted)
	}
	if changed {
		t.Errorf("changed = true, want false:\n%s", dumpMerged(merged))
	}
}

// TestMergeEntries_ATextlessSteerLearnsItsTextAndSeverity pins the fill of KAS-persisted fields,
// keeping the record's state and position.
func TestMergeEntries_ATextlessSteerLearnsItsTextAndSeverity(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "working"),
		steerRow("notify-1", marotte.EntrySteer{
			State:  marotte.SteerStateDropped,
			Origin: marotte.SteerOriginAgent,
		}),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		steerRow("notify-1", marotte.EntrySteer{
			Text:     "the step needs a decision",
			Origin:   marotte.SteerOriginAgent,
			Severity: "warning",
		}),
		textRow("S", "", "working"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true:\n%s", dumpMerged(merged))
	}
	var steer marotte.EntrySteer
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindSteer), &steer)
	if steer.Text != "the step needs a decision" {
		t.Errorf("text = %q, want the replayed bare text", steer.Text)
	}
	if steer.Severity != "warning" {
		t.Errorf("severity = %q, want the replayed notification status", steer.Severity)
	}
	if steer.State != marotte.SteerStateDropped {
		t.Errorf("state = %q, want the record's %q — KAS's log cannot say whether the model read it",
			steer.State, marotte.SteerStateDropped)
	}
	if steer.Reason != "" {
		t.Errorf("reason = %q, want empty — reason has one writer, the insertion rule", steer.Reason)
	}
	// The record's position is the injection point, so the steer stays after the text it interrupted.
	want := []string{"T1", "S", "notify-1", "T1:e1"}
	if got := idsOf(merged[0]); !equalStrings(got, want) {
		t.Errorf("entry ids = %v, want %v:\n%s", got, want, dumpMerged(merged))
	}
}

// TestMergeEntries_RecordlessSteersBecomeDroppedRestarts is rule 3's stamp.
func TestMergeEntries_RecordlessSteersBecomeDroppedRestarts(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "working"),
		steerRow("steer-paired", marotte.EntrySteer{
			Text:   "read this one",
			State:  marotte.SteerStateRead,
			Origin: marotte.SteerOriginUser,
		}),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "working"),
		steerRow("steer-paired", marotte.EntrySteer{Text: "read this one", Origin: marotte.SteerOriginUser}),
		steerRow("steer-lost-1", marotte.EntrySteer{Text: "and this", Origin: marotte.SteerOriginUser}),
		steerRow("steer-lost-2", marotte.EntrySteer{Text: "and that", Origin: marotte.SteerOriginUser}),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true:\n%s", dumpMerged(merged))
	}
	dropped, read := 0, 0
	for _, e := range merged[0].Entries {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var steer marotte.EntrySteer
		decodePayload(t, e, &steer)
		switch steer.State {
		case marotte.SteerStateDropped:
			dropped++
			if steer.Reason != marotte.SteerReasonRestart {
				t.Errorf("steer %s reason = %q, want %q", e.ID, steer.Reason, marotte.SteerReasonRestart)
			}
		case marotte.SteerStateRead:
			read++
			if steer.Reason != "" {
				t.Errorf("paired steer %s carries reason %q, want none", e.ID, steer.Reason)
			}
		default:
			t.Errorf("steer %s state = %q, want read or dropped — never not-known", e.ID, steer.State)
		}
	}
	if dropped != 2 || read != 1 {
		t.Errorf("merged %d dropped and %d read steers, want 2 and 1:\n%s", dropped, read, dumpMerged(merged))
	}
}

// TestMergeEntries_AResumedSessionsHistoryLandsAheadOfThePrompt pins the head rule: the open
// prompt turn is unpaired, so it cannot anchor.
func TestMergeEntries_AResumedSessionsHistoryLandsAheadOfThePrompt(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1", openRow("T1", 1, &marotte.EntryPrompt{ID: "m-new", Text: "carry on"}))}
	var projected []translate.ProjectedTurn
	const k = 3
	for i := range k {
		id := fmt.Sprintf("P%d", i)
		projected = append(projected, projTurn(t, id,
			openRow(id, 0, nil),
			textRow(fmt.Sprintf("S%d", i), "", "history"),
			closeRow(id+":e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
		))
	}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if len(merged) != k+1 {
		t.Fatalf("merged %d turns, want %d:\n%s", len(merged), k+1, dumpMerged(merged))
	}
	for i := range k {
		want := fmt.Sprintf("P%d", i)
		if got := merged[i].Entries[0].Turn; got != want {
			t.Errorf("merged turn %d is %q, want the inserted %q in KAS order", i, got, want)
		}
	}
	last := merged[k]
	if got := last.Entries[0].Turn; got != "T1" {
		t.Errorf("last merged turn is %q, want the record's own prompt turn", got)
	}
	var open marotte.EntryTurnOpen
	decodePayload(t, entryIn(t, last, marotte.EntryKindTurnOpen), &open)
	if open.N != k+1 {
		t.Errorf("the prompt turn's n = %d, want %d — an inserted turn shifts the turns after it", open.N, k+1)
	}
	// The record's turn keeps its id.
	if got := entryIn(t, last, marotte.EntryKindTurnOpen).ID; got != "T1" {
		t.Errorf("the prompt turn's turn_open id = %q, want the record's", got)
	}
	// Still the live prompt turn: no synthesized closer.
	if len(last.Entries) != 1 {
		t.Errorf("the kept prompt turn holds %d entries, want its turn_open alone:\n%s",
			len(last.Entries), dumpMerged(merged))
	}
}

// TestMergeEntries_ARecordSaySplitAtAnAckKeepsEverySegment pins that segments are never merged
// and the excess lands on the last one.
func TestMergeEntries_ARecordSaySplitAtAnAckKeepsEverySegment(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "first half "),
		entryRow{
			kind: marotte.EntryKindSteerAck, id: marotte.SteerAckID("steer-1"),
			payload: marotte.EntrySteerAck{SteerID: "steer-1", Text: "used tabs"},
		},
		textRow(marotte.SaySegmentID("S", 2), "", "second half"),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "first half second half and a lost tail"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("changed = false, want true — the tail was appended:\n%s", dumpMerged(merged))
	}
	want := []string{"T1", "S", marotte.SteerAckID("steer-1"), marotte.SaySegmentID("S", 2), "T1:e1"}
	if got := idsOf(merged[0]); !equalStrings(got, want) {
		t.Fatalf("entry ids = %v, want %v — every segment and the ack between them:\n%s",
			got, want, dumpMerged(merged))
	}
	var first, second marotte.EntryText
	decodePayload(t, merged[0].Entries[1], &first)
	decodePayload(t, merged[0].Entries[3], &second)
	if first.Text != "first half " {
		t.Errorf("first segment = %q, want it untouched", first.Text)
	}
	if second.Text != "second half and a lost tail" {
		t.Errorf("last segment = %q, want the excess appended to it", second.Text)
	}
}

// TestMergeEntries_ADivergingSayKeepsTheRecordsText pins the no-prefix case.
func TestMergeEntries_ADivergingSayKeepsTheRecordsText(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "the record's own words"),
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "something else entirely, and longer"),
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	var say marotte.EntryText
	decodePayload(t, entryIn(t, merged[0], marotte.EntryKindText), &say)
	if say.Text != "the record's own words" {
		t.Errorf("text = %q, want the record's kept", say.Text)
	}
	if changed {
		t.Errorf("changed = true, want false:\n%s", dumpMerged(merged))
	}
}

// TestMergeEntries_AMidTurnCompactionIsOneTurnOnBothSides pins that a compaction pairs in place.
func TestMergeEntries_AMidTurnCompactionIsOneTurnOnBothSides(t *testing.T) {
	summary := "we agreed to keep the index in memory"
	id := marotte.CompactionEntryID([]byte(summary), 1)
	compaction := entryRow{
		kind: marotte.EntryKindCompaction, id: id,
		payload: marotte.EntryCompaction{Summary: summary},
	}
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "before"),
		compaction,
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "before"),
		compaction,
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, changed := MergeEntries(record, projected, "sid-1")
	if len(merged) != 1 || changed {
		t.Fatalf("merged %d turns, changed=%v; want 1 turn and no change:\n%s",
			len(merged), changed, dumpMerged(merged))
	}
	if got := countKind(merged[0], marotte.EntryKindCompaction); got != 1 {
		t.Errorf("merged turn holds %d compaction entries, want 1:\n%s", got, dumpMerged(merged))
	}
}

// TestMergeEntries_TwoUnpairedCompactionsAreReported pins two rows, not a silent drop.
func TestMergeEntries_TwoUnpairedCompactionsAreReported(t *testing.T) {
	logs := captureLogs(t)
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, nil),
		textRow("S", "", "before"),
		entryRow{
			kind: marotte.EntryKindCompaction, id: "compaction-aaaa",
			payload: marotte.EntryCompaction{Summary: "the record's summary"},
		},
		liveClose("T1:e1", marotte.TurnOutcomeCompleted, "end_turn"),
	)}
	projected := []translate.ProjectedTurn{projTurn(t, "P1",
		openRow("P1", 0, nil),
		textRow("S", "", "before"),
		entryRow{
			kind: marotte.EntryKindCompaction, id: "compaction-bbbb",
			payload: marotte.EntryCompaction{Summary: "the replay's summary"},
		},
		closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCompleted, StopReasonRaw: "end_turn"}),
	)}

	merged, _ := MergeEntries(record, projected, "sid-1")
	if got := countKind(merged[0], marotte.EntryKindCompaction); got != 2 {
		t.Errorf("merged turn holds %d compaction entries, want both:\n%s", got, dumpMerged(merged))
	}
	got := logs.String()
	if !strings.Contains(got, "different summary bytes") ||
		!strings.Contains(got, "compaction-aaaa") || !strings.Contains(got, "compaction-bbbb") {
		t.Errorf("logged %q, want a warning naming both compaction ids", got)
	}
}

// TestMergeEntries_ASecondMergeChangesNothing pins one turn_open and one turn_close per paired
// turn, after one merge and after two.
func TestMergeEntries_ASecondMergeChangesNothing(t *testing.T) {
	record := []RecordTurn{recTurn(t, "T1",
		openRow("T1", 1, &marotte.EntryPrompt{ID: "m-1", Text: "go"}),
		bindRow("T1:e1", "kas-1", "sid-1"),
		textRow("S", "", "answer"),
		placeholderClose("T1:e2"),
	)}
	projected := []translate.ProjectedTurn{
		projTurn(t, "P0", openRow("P0", 0, nil), textRow("S0", "", "older")),
		projTurn(t, "P1",
			openRow("P1", 0, &marotte.EntryPrompt{ID: "kas-1", Text: "go"}),
			textRow("S", "", "answer and its lost tail"),
			steerRow("steer-lost", marotte.EntrySteer{Text: "unread", Origin: marotte.SteerOriginUser}),
			closeRow("P1:e1", marotte.EntryTurnClose{Outcome: marotte.TurnOutcomeCancelled, StopReasonRaw: "cancelled"}),
		),
	}

	first, changed := MergeEntries(record, projected, "sid-1")
	if !changed {
		t.Fatalf("first merge reported no change:\n%s", dumpMerged(first))
	}
	for i := range first {
		if got := countKind(first[i], marotte.EntryKindTurnOpen); got != 1 {
			t.Errorf("turn %d holds %d turn_open entries, want 1:\n%s", i, got, dumpMerged(first))
		}
		if got := countKind(first[i], marotte.EntryKindTurnClose); got != 1 {
			t.Errorf("turn %d holds %d turn_close entries, want 1:\n%s", i, got, dumpMerged(first))
		}
	}
	second, changedAgain := MergeEntries(mergedAsRecord(first), projected, "sid-1")
	if changedAgain {
		t.Errorf("second merge reported a change:\nfirst:\n%s\nsecond:\n%s",
			dumpMerged(first), dumpMerged(second))
	}
	if dumpMerged(first) != dumpMerged(second) {
		t.Errorf("second merge differs:\nfirst:\n%s\nsecond:\n%s", dumpMerged(first), dumpMerged(second))
	}
}

// mergedAsRecord feeds a merge's output back in as the record.
func mergedAsRecord(turns []MergedTurn) []RecordTurn {
	out := make([]RecordTurn, 0, len(turns))
	for i := range turns {
		out = append(out, RecordTurn{Entries: turns[i].Entries})
	}
	return out
}

// TestRecordTurnsOf_OrdersByFileOrderAndSeq pins file order, not stored n (a revert reuses
// ordinals), entries seq-sorted with the turn_open leading.
func TestRecordTurnsOf_OrdersByFileOrderAndSeq(t *testing.T) {
	// T2's line is first and its n higher, so file order and n order disagree.
	entries := []marotte.Entry{
		entryOf(t, "T2", 0, marotte.EntryKindTurnOpen, "T2", "", marotte.EntryTurnOpen{N: 2}),
		entryOf(t, "T1", 1, marotte.EntryKindText, "S1", "", marotte.EntryText{Text: "b"}),
		entryOf(t, "T1", 0, marotte.EntryKindTurnOpen, "T1", "", marotte.EntryTurnOpen{N: 1}),
		entryOf(t, "T2", 1, marotte.EntryKindText, "S2", "", marotte.EntryText{Text: "c"}),
	}
	turns := RecordTurnsOf(entries, nil)
	if len(turns) != 2 {
		t.Fatalf("grouped %d turns, want 2", len(turns))
	}
	if turns[0].Entries[0].Turn != "T2" || turns[1].Entries[0].Turn != "T1" {
		t.Errorf("turn order = %q, %q; want T2 then T1, the order the read held them in",
			turns[0].Entries[0].Turn, turns[1].Entries[0].Turn)
	}
	if turns[0].Entries[0].Kind != marotte.EntryKindTurnOpen {
		t.Errorf("T2 leads with %s, want its turn_open first", turns[0].Entries[0].Kind)
	}
	if got := turns[1].Entries[0].Seq; got != 0 {
		t.Errorf("T1 leads with seq %d, want its seq-0 turn_open: the within-turn sort is gone", got)
	}
}

// TestRecordTurnsOf_StampsTheRevertedSet pins the flag from the log's own scan.
func TestRecordTurnsOf_StampsTheRevertedSet(t *testing.T) {
	entries := []marotte.Entry{
		entryOf(t, "T1", 0, marotte.EntryKindTurnOpen, "T1", "", marotte.EntryTurnOpen{N: 1}),
		entryOf(t, "T2", 0, marotte.EntryKindTurnOpen, "T2", "", marotte.EntryTurnOpen{N: 2}),
	}
	turns := RecordTurnsOf(entries, map[string]struct{}{"T2": {}})
	if len(turns) != 2 {
		t.Fatalf("grouped %d turns, want 2", len(turns))
	}
	if turns[0].Reverted {
		t.Errorf("T1 is marked reverted, and the set does not name it")
	}
	if !turns[1].Reverted {
		t.Errorf("T2 is not marked reverted, so a projected turn can pair with a turn the " +
			"rewind hid and resurrect it")
	}
}

// TestRecordTurnsOf_ATurnWithNoOpenIsSkippedAndSaidOutLoud pins the skip and its log.
func TestRecordTurnsOf_ATurnWithNoOpenIsSkippedAndSaidOutLoud(t *testing.T) {
	logs := captureLogs(t)
	entries := []marotte.Entry{
		entryOf(t, "T1", 0, marotte.EntryKindTurnOpen, "T1", "", marotte.EntryTurnOpen{N: 1}),
		entryOf(t, "orphan", 3, marotte.EntryKindText, "S", "", marotte.EntryText{Text: "x"}),
	}
	turns := RecordTurnsOf(entries, nil)
	if len(turns) != 1 {
		t.Fatalf("grouped %d turns, want 1", len(turns))
	}
	if got := logs.String(); !strings.Contains(got, "no turn_open") {
		t.Errorf("logged %q, want a warning naming the skipped turn", got)
	}
}

// invocationFixture is internal/translate's replay_invocation.json, read from there so the
// projection and merge halves cannot drift.
type invocationFixture struct {
	ActionID       string `json:"action_id"`
	ReplayedCallID string `json:"replayed_call_id"`
	DelegateID     string `json:"delegate_id"`
	Frames         []struct {
		Kind   marotte.ACPUpdateKind `json:"kind"`
		Update json.RawMessage       `json:"update"`
	} `json:"frames"`
}

func loadInvocationFixture(t *testing.T) invocationFixture {
	t.Helper()
	path := filepath.Join("..", "translate", "testdata", "replay_invocation.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var fx invocationFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return fx
}

// projectInvocationFixture drives the real EntryProjection over that stream.
func projectInvocationFixture(t *testing.T, fx invocationFixture) []translate.ProjectedTurn {
	t.Helper()
	n := 0
	p := translate.NewEntryProjection(func() string {
		n++
		return fmt.Sprintf("P%d", n)
	}, "")
	for _, f := range fx.Frames {
		p.Ingest(f.Kind, f.Update)
	}
	return p.Turns()
}

// turnCloseEntry is one turn_close entry carrying c.
func turnCloseEntry(c marotte.EntryTurnClose) *marotte.Entry {
	e := &marotte.Entry{Kind: marotte.EntryKindTurnClose}
	setPayload(e, c)
	return e
}

// toolResultEntry is one tool_result entry carrying r.
func toolResultEntry(r marotte.EntryToolResult) *marotte.Entry {
	e := &marotte.Entry{Kind: marotte.EntryKindToolResult}
	setPayload(e, r)
	return e
}

// TestUnionTurnClose_PlaceholderTakesProjectedRefusal pins that a recovered `refused` keeps its callout.
func TestUnionTurnClose_PlaceholderTakesProjectedRefusal(t *testing.T) {
	out := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
	})
	proj := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeRefused,
		StopReasonRaw: string(marotte.StopReasonRefusal),
		Refusal:       &marotte.RefusalInfo{Category: "c"},
	})

	unionTurnClose(out, proj)

	var got marotte.EntryTurnClose
	decodePayload(t, *out, &got)
	if got.Outcome != marotte.TurnOutcomeRefused || got.Refusal == nil || got.Refusal.Category != "c" {
		t.Errorf("unioned closer = outcome %q refusal %+v, want refused with category c", got.Outcome, got.Refusal)
	}
}

// TestUnionTurnClose_RealCloserKeepsItsRefusal: a closer a process wrote is the
// record's, refusal included.
func TestUnionTurnClose_RealCloserKeepsItsRefusal(t *testing.T) {
	out := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeRefused,
		StopReasonRaw: string(marotte.StopReasonRefusal),
		Refusal:       &marotte.RefusalInfo{Category: "a"},
	})
	proj := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeRefused,
		StopReasonRaw: string(marotte.StopReasonRefusal),
		Refusal:       &marotte.RefusalInfo{Category: "b"},
	})

	unionTurnClose(out, proj)

	var got marotte.EntryTurnClose
	decodePayload(t, *out, &got)
	if got.Refusal == nil || got.Refusal.Category != "a" {
		t.Errorf("unioned closer refusal = %+v, want the record's category a", got.Refusal)
	}
}

// TestUnionTurnClose_FailureKindMovesWithTheConclusion pins the kind travelling with the conclusion.
func TestUnionTurnClose_FailureKindMovesWithTheConclusion(t *testing.T) {
	placeholder := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
		FailureKind:   marotte.FailureKindContextLimit,
	})
	proj := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeCompleted,
		StopReasonRaw: string(marotte.StopReasonEndTurn),
	})
	unionTurnClose(placeholder, proj)
	var got marotte.EntryTurnClose
	decodePayload(t, *placeholder, &got)
	if got.FailureKind != "" {
		t.Errorf("placeholder took a completed conclusion but kept failure_kind %q", got.FailureKind)
	}

	real := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonInterrupted),
		FailureKind:   marotte.FailureKindContextLimit,
	})
	unionTurnClose(real, proj)
	decodePayload(t, *real, &got)
	if got.FailureKind != marotte.FailureKindContextLimit {
		t.Errorf("a real closer's failure_kind = %q, want it kept", got.FailureKind)
	}
}

func TestUnionToolResult_FillsAnOffloadOnlyTheReplayHolds(t *testing.T) {
	off := &marotte.ToolOffload{Path: "/k/sessions/b/sess_1/tool-outputs/execute_bash-0a1b2c3d.txt", TotalChars: 40000}
	mk := func(r marotte.EntryToolResult) *marotte.Entry {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return &marotte.Entry{Kind: marotte.EntryKindToolResult, Payload: raw}
	}
	read := func(e *marotte.Entry) *marotte.ToolOffload {
		var r marotte.EntryToolResult
		if err := json.Unmarshal(e.Payload, &r); err != nil {
			t.Fatal(err)
		}
		return r.Offload
	}

	rec := mk(marotte.EntryToolResult{Status: marotte.ToolCompleted})
	unionToolResult(rec, mk(marotte.EntryToolResult{Status: marotte.ToolCompleted, Offload: off}))
	if got := read(rec); got == nil || *got != *off {
		t.Errorf("record offload after union = %+v, want the replay's %+v", got, off)
	}

	held := &marotte.ToolOffload{Path: "/k/held.txt", TotalChars: 1}
	rec = mk(marotte.EntryToolResult{Status: marotte.ToolCompleted, Offload: held})
	unionToolResult(rec, mk(marotte.EntryToolResult{Status: marotte.ToolCompleted, Offload: off}))
	if got := read(rec); got == nil || *got != *held {
		t.Errorf("record offload after union = %+v, want the record's own %+v kept", got, held)
	}
}

// A replayed answer fills a crash-lost one; a recorded one stays.
func TestUnionToolResult_FillsAnInteractionOnlyTheReplayHolds(t *testing.T) {
	proj := marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "allow_always"}
	own := marotte.ToolInteraction{Type: "tool_approval", Outcome: "selected", Choice: "reject_once"}
	for _, tt := range []struct {
		record *marotte.ToolInteraction
		want   marotte.ToolInteraction
		name   string
	}{
		{name: "the record has none", want: proj},
		{name: "the record has its own", record: &own, want: own},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := toolResultEntry(marotte.EntryToolResult{Status: marotte.ToolCompleted, Interaction: tt.record})
			unionToolResult(rec, toolResultEntry(marotte.EntryToolResult{Status: marotte.ToolCompleted, Interaction: &proj}))
			var got marotte.EntryToolResult
			decodePayload(t, *rec, &got)
			if got.Interaction == nil || *got.Interaction != tt.want {
				t.Errorf("interaction after union = %+v, want %+v", got.Interaction, tt.want)
			}
		})
	}
}

// The placeholder takes KAS's completion facts; a process-written closer keeps its own.
func TestUnionTurnClose_PlaceholderTakesTheCompletionFacts(t *testing.T) {
	proj := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:          marotte.TurnOutcomeFailed,
		StopReasonRaw:    "error",
		Throughput:       &marotte.TurnThroughput{EstimatedTokens: 9, ActiveStreamingMs: 90},
		RequestIDs:       []string{"req-1"},
		Recoveries:       []string{"empty"},
		Steering:         []string{"file:///w/a.md"},
		EngineErrorClass: "ModelThrottleError",
	})
	placeholder := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeInterrupted,
		StopReasonRaw: string(marotte.StopReasonUnterminated),
	})
	unionTurnClose(placeholder, proj)
	var got marotte.EntryTurnClose
	decodePayload(t, *placeholder, &got)
	if got.Throughput == nil || got.Throughput.EstimatedTokens != 9 || !slices.Equal(got.RequestIDs, []string{"req-1"}) ||
		!slices.Equal(got.Recoveries, []string{"empty"}) || !slices.Equal(got.Steering, []string{"file:///w/a.md"}) ||
		got.EngineErrorClass != "ModelThrottleError" {
		t.Errorf("placeholder after union = %+v, want the replay's completion facts", got)
	}

	real := turnCloseEntry(marotte.EntryTurnClose{
		Outcome:       marotte.TurnOutcomeCompleted,
		StopReasonRaw: string(marotte.StopReasonEndTurn),
		RequestIDs:    []string{"req-own"},
	})
	unionTurnClose(real, proj)
	var kept marotte.EntryTurnClose
	decodePayload(t, *real, &kept)
	if !slices.Equal(kept.RequestIDs, []string{"req-own"}) || kept.Throughput != nil {
		t.Errorf("a real closer after union = %+v, want its own facts kept", kept)
	}
}
