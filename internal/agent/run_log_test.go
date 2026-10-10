package agent

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

// Turn answers the open turn for a step, or nil.
func (r *runLog) Turn(workflowID, nodePath string) *turnlog.Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.openLocked(workflowID, nodePath); t != nil {
		return t.turn
	}
	return nil
}

// runLogEntries reads a run's log back as decoded entries.
func runLogEntries(t *testing.T, r *runLog, workflowID string) []marotte.Entry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, workflowID, "entries.jsonl"))
	if err != nil {
		t.Fatalf("read the run log: %v", err)
	}
	var out []marotte.Entry
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e marotte.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func closeEntryOf(t *testing.T, e marotte.Entry) marotte.EntryTurnClose {
	t.Helper()
	var c marotte.EntryTurnClose
	if err := json.Unmarshal(e.Payload, &c); err != nil {
		t.Fatalf("decode turn_close: %v", err)
	}
	return c
}

func openOf(t *testing.T, e marotte.Entry) marotte.EntryTurnOpen {
	t.Helper()
	var o marotte.EntryTurnOpen
	if err := json.Unmarshal(e.Payload, &o); err != nil {
		t.Fatalf("decode turn_open: %v", err)
	}
	return o
}

func kinds(entries []marotte.Entry) []marotte.EntryKind {
	out := make([]marotte.EntryKind, len(entries))
	for i, e := range entries {
		out[i] = e.Kind
	}
	return out
}

func TestRunLog_ANodeStartOpensOneTurnPerStepInstance(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	turn, opened, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "root/step", SessionID: "sess-a"}, "c-1")
	if err != nil || turn == nil || opened == nil {
		t.Fatalf("Open(first) = %v, %v, %v; want a turn and its turn_open", turn, opened, err)
	}
	o := openOf(t, *opened)
	if o.Source != marotte.TurnOpenNameWorkflowStep || o.Run != "wf1" || o.NodePath != "root/step" || o.SessionID != "sess-a" || o.N != 1 {
		t.Fatalf("turn_open payload = %+v, want workflow_step/wf1/root/step/sess-a/n=1", o)
	}
	again, reopened, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "root/step", SessionID: "sess-a"}, "c-1")
	if err != nil || reopened != nil || again != turn {
		t.Fatalf("Open(second) = %v, %v, %v; want the same turn and no second turn_open", again == turn, reopened, err)
	}
	if got := r.Turn("wf1", "root/step"); got != turn {
		t.Fatalf("Turn() = %v, want the open turn", got)
	}
	if !r.hostsOpen("c-1") || r.hostsOpen("c-2") {
		t.Fatal("the run's host is not the chat the frame arrived on")
	}
	if got := r.OpenSeqs("wf1"); len(got) != 1 {
		t.Fatalf("OpenSeqs() = %v, want the one open turn", got)
	} else if _, ok := got[turn.ID()]; !ok {
		t.Fatalf("OpenSeqs() = %v, want it keyed by %s", got, turn.ID())
	}
}

// TestRunLog_OpenNodeIDsNamesEachOpenStepsOwnID pins the id answered whole: one holding "/", and one whose
// path is over keyenc.MaxComponentBytes, so its key is a hash.
func TestRunLog_OpenNodeIDsNamesEachOpenStepsOwnID(t *testing.T) {
	r := newRunLog(t.TempDir())
	long := strings.Repeat("x", 9<<10)
	for _, path := range [][]string{{"wf1", "grp", "a/b"}, {"wf1", "b"}, {"wf1", long}} {
		step := translate.RunStep{RunID: "wf1", NodePath: workflow.PathKey(path), NodeID: path[len(path)-1]}
		if _, _, err := r.Open(t.Context(), step, "c-1"); err != nil {
			t.Fatalf("Open(%.40q): %v", path, err)
		}
	}
	got := r.OpenNodeIDs("wf1")
	if want := map[string]struct{}{"a/b": {}, "b": {}, long: {}}; !maps.Equal(got, want) {
		t.Errorf("OpenNodeIDs() = %d ids, has the %d-byte id: %t; has a/b: %t; want a/b, b and the long id",
			len(got), len(long), hasNodeID(got, long), hasNodeID(got, "a/b"))
	}
}

// A content frame can open a step's turn before node_start names the node; the id still lands.
func TestRunLog_ALaterOpenNamesATurnOpenedWithNoNodeID(t *testing.T) {
	r := newRunLog(t.TempDir())
	step := translate.RunStep{RunID: "wf1", NodePath: workflow.PathKey([]string{"wf1", "a"})}
	if _, _, err := r.Open(t.Context(), step, "c-1"); err != nil {
		t.Fatalf("Open(no id): %v", err)
	}
	step.NodeID = "a"
	if _, reopened, err := r.Open(t.Context(), step, "c-1"); err != nil || reopened != nil {
		t.Fatalf("Open(with id) = %v, %v; want the open turn and no second turn_open", reopened, err)
	}
	if got := r.OpenNodeIDs("wf1"); !maps.Equal(got, map[string]struct{}{"a": {}}) {
		t.Errorf("OpenNodeIDs() = %v, want [a]", got)
	}
}

func hasNodeID(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
}

func TestRunLog_AnEmptyChatIDRecordsTheParentlessBridgeAsHost(t *testing.T) {
	r := newRunLog(t.TempDir())
	if _, _, err := r.Open(t.Context(), translate.RunStep{RunID: "wf1", NodePath: "root/step"}, ""); err != nil {
		t.Fatal(err)
	}
	if !r.hostsOpen(runChatID("wf1")) {
		t.Fatalf("an empty chat id did not record %q as the host", runChatID("wf1"))
	}
}

func TestRunLog_TheHostIsRecordedByTheFirstOpenAndNeverMoved(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "a"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "b"}, "c-2"); err != nil {
		t.Fatal(err)
	}
	if !r.hostsOpen("c-1") || r.hostsOpen("c-2") {
		t.Fatal("the host moved to the second opener's chat")
	}
}

func TestRunLog_NodeCompleteMapsKASStatusOntoTheOutcome(t *testing.T) {
	cases := []struct {
		status  string
		outcome marotte.TurnOutcome
		raw     marotte.StopReason
	}{
		{"completed", marotte.TurnOutcomeCompleted, "completed"},
		{"failed", marotte.TurnOutcomeFailed, "failed"},
		{"cancelled", marotte.TurnOutcomeCancelled, "cancelled"},
		{"aborted", marotte.TurnOutcomeUnknown, "aborted"},
		{"skipped", marotte.TurnOutcomeUnknown, "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			r := newRunLog(t.TempDir())
			ctx := t.Context()
			if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
				t.Fatal(err)
			}
			sealed, closed, err := r.CloseNode(ctx, "wf1", "s", tc.status, "why")
			if err != nil || !closed {
				t.Fatalf("CloseNode(%q) = %v, %v, %v", tc.status, len(sealed), closed, err)
			}
			entries := runLogEntries(t, r, "wf1")
			c := closeEntryOf(t, entries[len(entries)-1])
			if c.Outcome != tc.outcome || c.StopReasonRaw != string(tc.raw) || c.FailureReason != "why" {
				t.Fatalf("turn_close = {%s %s %q}, want {%s %s why}", c.Outcome, c.StopReasonRaw, c.FailureReason, tc.outcome, tc.raw)
			}
			if r.Turn("wf1", "s") != nil {
				t.Fatal("the path is still open after its node_complete")
			}
		})
	}
}

func TestRunLog_ARecordedRefusalOutranksTheNodeCompleteStatus(t *testing.T) {
	// The close is the only durable carrier of a refusal's metadata.
	refused := &marotte.RefusalInfo{
		Category:         "policy",
		Explanation:      "I will not continue with this request.",
		RecommendedModel: "claude-sonnet-5",
	}
	cases := []struct {
		desc    string
		status  string
		raw     marotte.StopReason
		refusal *marotte.RefusalInfo
		outcome marotte.TurnOutcome
	}{
		// KAS reports a refused step as run; only the turn_end says it declined.
		{"a refused step KAS reports as completed", "completed", marotte.StopReasonRefusal, refused, marotte.TurnOutcomeRefused},
		{"a content filter is the same refusal", "completed", marotte.StopReasonContentFiltered, refused, marotte.TurnOutcomeRefused},
		// Refused names why rather than relabelling a failure.
		{"a refusal on a failed step still reads refused", "failed", marotte.StopReasonRefusal, refused, marotte.TurnOutcomeRefused},
		// Every metadata field is optional.
		{"a refusal with no metadata still reads refused", "completed", marotte.StopReasonRefusal, nil, marotte.TurnOutcomeRefused},
		{"a completed step with no refusal is unchanged", "completed", marotte.StopReasonEndTurn, nil, marotte.TurnOutcomeCompleted},
		{"a failed step with no refusal is unchanged", "failed", marotte.StopReasonError, nil, marotte.TurnOutcomeFailed},
		{"a cancelled step with no refusal is unchanged", "cancelled", marotte.StopReasonCancelled, nil, marotte.TurnOutcomeCancelled},
		{"a step with no turn_end at all is unchanged", "completed", "", nil, marotte.TurnOutcomeCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			r := newRunLog(t.TempDir())
			ctx := t.Context()
			turn, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.refusal != nil {
				turn.SetRefusal(tc.refusal)
			}
			if tc.raw != "" && !r.StopReason("wf1", "s", tc.raw) {
				t.Fatalf("%s: StopReason(%q) refused on an open turn", tc.desc, tc.raw)
			}
			if _, closed, err := r.CloseNode(ctx, "wf1", "s", tc.status, "why"); err != nil || !closed {
				t.Fatalf("%s: CloseNode(%q) = %v, %v", tc.desc, tc.status, closed, err)
			}
			entries := runLogEntries(t, r, "wf1")
			c := closeEntryOf(t, entries[len(entries)-1])
			wantRaw := string(tc.raw)
			if wantRaw == "" {
				wantRaw = tc.status
			}
			if c.Outcome != tc.outcome || c.StopReasonRaw != wantRaw || c.FailureReason != "why" {
				t.Errorf("%s: CloseNode(%q) with turn_end %q wrote turn_close {%s %s %q}, want {%s %s why}",
					tc.desc, tc.status, tc.raw, c.Outcome, c.StopReasonRaw, c.FailureReason, tc.outcome, wantRaw)
			}
			switch {
			case tc.refusal == nil && c.Refusal != nil:
				t.Errorf("%s: turn_close carries refusal %+v, want none", tc.desc, c.Refusal)
			case tc.refusal != nil && c.Refusal == nil:
				t.Errorf("%s: turn_close dropped the recorded refusal %+v", tc.desc, tc.refusal)
			case tc.refusal != nil && *c.Refusal != *tc.refusal:
				t.Errorf("%s: turn_close carries refusal %+v, want %+v", tc.desc, *c.Refusal, *tc.refusal)
			}
		})
	}
}

func TestRunLog_AStepStoppedAtTheModelCallLimitNeverReadsCompleted(t *testing.T) {
	cases := []struct {
		desc    string
		status  string
		reason  string
		outcome marotte.TurnOutcome
		kind    marotte.FailureKind
		want    string
	}{
		// The measured shape: KAS reports `completed` with no reason after a last turn_end of tool_use.
		{"a step KAS reports as completed", "completed", "", marotte.TurnOutcomeFailed, marotte.FailureKindModelCallLimit, marotte.ModelCallLimitStepReason},
		{"KAS's own failure keeps its account", "failed", "boom", marotte.TurnOutcomeFailed, "", "boom"},
		{"a cancel keeps its account", "cancelled", "", marotte.TurnOutcomeCancelled, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			r := newRunLog(t.TempDir())
			ctx := t.Context()
			if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
				t.Fatal(err)
			}
			if !r.StopReason("wf1", "s", marotte.StopReasonToolUse) {
				t.Fatal("StopReason(tool_use) refused on an open turn")
			}
			if _, closed, err := r.CloseNode(ctx, "wf1", "s", tc.status, tc.reason); err != nil || !closed {
				t.Fatalf("CloseNode(%q) = %v, %v", tc.status, closed, err)
			}
			entries := runLogEntries(t, r, "wf1")
			c := closeEntryOf(t, entries[len(entries)-1])
			if c.Outcome != tc.outcome || c.FailureKind != tc.kind || c.FailureReason != tc.want || c.StopReasonRaw != "tool_use" {
				t.Errorf("CloseNode(%q, %q) after turn_end tool_use wrote turn_close {%s %q %q %s}, want {%s %q %q tool_use}",
					tc.status, tc.reason, c.Outcome, c.FailureKind, c.FailureReason, c.StopReasonRaw, tc.outcome, tc.kind, tc.want)
			}
		})
	}
}

// closeStep runs one step turn through its turn_end and node_complete.
func closeStep(t *testing.T, r *runLog, path string, raw marotte.StopReason, status string) {
	t.Helper()
	if _, _, err := r.Open(t.Context(), translate.RunStep{RunID: "wf1", NodePath: path}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if !r.StopReason("wf1", path, raw) {
		t.Fatalf("StopReason(%q) refused on the open turn %s", raw, path)
	}
	if _, closed, err := r.CloseNode(t.Context(), "wf1", path, status, ""); err != nil || !closed {
		t.Fatalf("CloseNode(%s, %q) = %v, %v", path, status, closed, err)
	}
}

func TestRunLog_StepEndsNamesTheStepsWhoseNewestTurnClosedBroken(t *testing.T) {
	var (
		capped = workflow.PathKey([]string{"wf1", "capped"})
		clean  = workflow.PathKey([]string{"wf1", "clean"})
		rerun  = workflow.PathKey([]string{"wf1", "rerun"})
	)
	limit := marotte.RunStepEnd{
		Outcome:       marotte.TurnOutcomeFailed,
		FailureReason: marotte.ModelCallLimitStepReason,
		FailureKind:   marotte.FailureKindModelCallLimit,
	}
	t.Run("a fresh registry reads them off the log on disk", func(t *testing.T) {
		dir := t.TempDir()
		w := newRunLog(dir)
		closeStep(t, w, capped, marotte.StopReasonToolUse, "completed")
		closeStep(t, w, clean, marotte.StopReasonEndTurn, "completed")
		// A rerun of a path that once failed: its newest turn is what the step says now.
		closeStep(t, w, rerun, marotte.StopReasonToolUse, "completed")
		closeStep(t, w, rerun, marotte.StopReasonEndTurn, "completed")

		got, err := newRunLog(dir).StepEnds(t.Context(), "wf1")
		if err != nil {
			t.Fatalf("StepEnds: %v", err)
		}
		want := map[string]marotte.RunStepEnd{capped: limit}
		if !maps.Equal(got, want) {
			t.Errorf("StepEnds = %+v, want %+v", got, want)
		}
	})
	t.Run("a close after the first read keeps the answer current", func(t *testing.T) {
		r := newRunLog(t.TempDir())
		closeStep(t, r, clean, marotte.StopReasonEndTurn, "completed")
		if got, err := r.StepEnds(t.Context(), "wf1"); err != nil || len(got) != 0 {
			t.Fatalf("StepEnds before any broken close = %+v, %v, want none", got, err)
		}
		closeStep(t, r, capped, marotte.StopReasonToolUse, "completed")

		got, err := r.StepEnds(t.Context(), "wf1")
		if err != nil {
			t.Fatalf("StepEnds: %v", err)
		}
		if want := map[string]marotte.RunStepEnd{capped: limit}; !maps.Equal(got, want) {
			t.Errorf("StepEnds after the capped close = %+v, want %+v", got, want)
		}
	})
	t.Run("a run with no log answers nothing", func(t *testing.T) {
		got, err := newRunLog(t.TempDir()).StepEnds(t.Context(), "wf1")
		if err != nil || got != nil {
			t.Errorf("StepEnds with no log = %+v, %v, want nil, nil", got, err)
		}
	})
}

func TestRunLog_ANodeCompleteForAClosedPathClosesNothing(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if _, closed, err := r.CloseNode(ctx, "wf1", "s", "completed", ""); err != nil || !closed {
		t.Fatal(err)
	}
	before := len(runLogEntries(t, r, "wf1"))
	sealed, closed, err := r.CloseNode(ctx, "wf1", "s", "completed", "")
	if err != nil || closed || len(sealed) != 0 {
		t.Fatalf("second CloseNode = %v, %v, %v; want nothing closed", len(sealed), closed, err)
	}
	if _, closed, _ := r.CloseNode(ctx, "wf-unknown", "s", "completed", ""); closed {
		t.Fatal("an unknown run closed a turn")
	}
	if after := len(runLogEntries(t, r, "wf1")); after != before {
		t.Fatalf("the log grew from %d to %d on a no-op close", before, after)
	}
}

func TestRunLog_MeteringFoldsIntoTheOpenTurnAndDropsOtherwise(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if r.Meter("wf1", "s", 1, 1) || r.StopReason("wf1", "s", "end_turn") {
		t.Fatal("metering landed on a run with no open turn")
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	// Two KAS turns in one step: credits and elapsed sum, the last stop reason wins.
	if !r.Meter("wf1", "s", 0.25, 100) || !r.Meter("wf1", "s", 0.5, 200) {
		t.Fatal("metering refused on an open turn")
	}
	if !r.StopReason("wf1", "s", "max_tokens") || !r.StopReason("wf1", "s", "end_turn") {
		t.Fatal("stop reason refused on an open turn")
	}
	if _, _, err := r.CloseNode(ctx, "wf1", "s", "completed", ""); err != nil {
		t.Fatal(err)
	}
	entries := runLogEntries(t, r, "wf1")
	c := closeEntryOf(t, entries[len(entries)-1])
	if c.Credits != 0.75 || c.ElapsedMs != 300 {
		t.Fatalf("aggregate = %v credits, %v ms; want 0.75, 300", c.Credits, c.ElapsedMs)
	}
	if c.Outcome != marotte.TurnOutcomeCompleted || c.StopReasonRaw != string(marotte.StopReasonEndTurn) {
		t.Fatalf("turn_close = {%s %s}; want completed with the last turn_end's stop reason", c.Outcome, c.StopReasonRaw)
	}
	if r.Meter("wf1", "s", 1, 1) {
		t.Fatal("a metering frame after node_complete found an aggregate to join")
	}
}

func TestRunLog_AnUnmappedStatusKeepsTheStatusAsTheRawStop(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	r.StopReason("wf1", "s", "end_turn")
	if _, _, err := r.CloseNode(ctx, "wf1", "s", "aborted", ""); err != nil {
		t.Fatal(err)
	}
	entries := runLogEntries(t, r, "wf1")
	if c := closeEntryOf(t, entries[len(entries)-1]); c.StopReasonRaw != "aborted" {
		t.Fatalf("stop_reason_raw = %q, want the unmapped status itself", c.StopReasonRaw)
	}
}

func TestRunLog_ABetweenTurnsAppendLandsAfterThePathsNewestClosedTurn(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	late := &marotte.Entry{ID: "call-1:result", Kind: marotte.EntryKindToolResult, Payload: json.RawMessage(`{"status":"completed"}`)}
	if ok, err := r.AppendAfterClosed(ctx, "wf1", "a", late); ok || err != nil {
		t.Fatalf("AppendAfterClosed on an unknown run = %v, %v; want false", ok, err)
	}
	turnA, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "a"}, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.AppendAfterClosed(ctx, "wf1", "a", late); ok || err != nil {
		t.Fatalf("AppendAfterClosed while the path is open = %v, %v; want false", ok, err)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "b"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.CloseNode(ctx, "wf1", "a", "completed", ""); err != nil {
		t.Fatal(err)
	}
	// The late frame for path a lands after a's close, not the open b's.
	if ok, err := r.AppendAfterClosed(ctx, "wf1", "a", late); !ok || err != nil {
		t.Fatalf("AppendAfterClosed after the close = %v, %v; want true", ok, err)
	}
	entries := runLogEntries(t, r, "wf1")
	last := entries[len(entries)-1]
	if last.Turn != turnA.ID() || last.Kind != marotte.EntryKindToolResult {
		t.Fatalf("late entry landed in turn %q as %s; want %q", last.Turn, last.Kind, turnA.ID())
	}
	var closeSeq uint64
	for _, e := range entries {
		if e.Turn == turnA.ID() && e.Kind == marotte.EntryKindTurnClose {
			closeSeq = e.Seq
		}
	}
	if last.Seq != closeSeq+1 {
		t.Fatalf("late entry seq = %d, want the close's %d plus one", last.Seq, closeSeq)
	}
}

func TestRunLog_CloseRunClosesEveryOpenTurnAndDropsHostWhenTerminal(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	for _, p := range []string{"par/b1", "par/b2"} {
		if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: p}, "c-1"); err != nil {
			t.Fatal(err)
		}
	}
	c := runOutcome("failed")
	sealed, err := r.CloseRun(ctx, "wf1", c, false)
	if err != nil || len(sealed) != 2 {
		t.Fatalf("CloseRun(non-terminal) = %d sealed, %v; want the two closers", len(sealed), err)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "par/b3"}, "c-9"); err != nil {
		t.Fatal(err)
	}
	if !r.hostsOpen("c-1") || r.hostsOpen("c-9") {
		t.Fatal("a non-terminal run_complete dropped the host")
	}
	if _, err := r.CloseRun(ctx, "wf1", c, true); err != nil {
		t.Fatal(err)
	}
	if open := r.OpenSeqs("wf1"); open != nil {
		t.Fatalf("after the terminal close open=%v; want none", open)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "par/b4"}, "c-9"); err != nil {
		t.Fatal(err)
	}
	if !r.hostsOpen("c-9") || r.hostsOpen("c-1") {
		t.Fatal("the terminal close kept the host; a re-entered step must record its own")
	}
	for _, e := range runLogEntries(t, r, "wf1") {
		if e.Kind == marotte.EntryKindTurnClose && closeEntryOf(t, e).Outcome != marotte.TurnOutcomeFailed {
			t.Fatalf("closer %s carries %s, want failed", e.Turn, closeEntryOf(t, e).Outcome)
		}
	}
	if _, err := r.CloseRun(ctx, "wf-unknown", c, true); err != nil {
		t.Fatalf("CloseRun on an unknown run = %v, want nil", err)
	}
}

func TestRunLog_TheDeathArmClosesTheDeadHostsRunsOnly(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf2", NodePath: "s"}, "c-2"); err != nil {
		t.Fatal(err)
	}
	c := marotte.ConcludeStopReason(marotte.StopReasonInterrupted)
	out, err := r.CloseHost(ctx, "c-1", c)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out["wf1"]) != 1 {
		t.Fatalf("CloseHost = %v, want wf1's one closer", out)
	}
	if r.Turn("wf1", "s") != nil || r.Turn("wf2", "s") == nil {
		t.Fatal("the death arm closed the wrong run's turn")
	}
	if c := closeEntryOf(t, runLogEntries(t, r, "wf1")[1]); c.Outcome != marotte.TurnOutcomeInterrupted {
		t.Fatalf("outcome = %s, want interrupted", c.Outcome)
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s2"}, "c-9"); err != nil {
		t.Fatal(err)
	}
	if !r.hostsOpen("c-1") || r.hostsOpen("c-9") {
		t.Fatal("the death arm dropped the host; a re-entered step must find it")
	}
}

func TestRunLog_DeleteClosesCancelledTombstonesAndRemovesLast(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	turn, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.TextDelta(ctx, "", "say-1", "hello"); err != nil {
		t.Fatal(err)
	}
	sealed, err := r.Delete(ctx, "wf1")
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(sealedEntries(sealed)); len(got) != 2 || got[0] != marotte.EntryKindText || got[1] != marotte.EntryKindTurnClose {
		t.Fatalf("Delete sealed %v, want the held text then the closer", got)
	}
	entries := runLogEntries(t, r, "wf1")
	if c := closeEntryOf(t, entries[len(entries)-1]); c.Outcome != marotte.TurnOutcomeCancelled {
		t.Fatalf("outcome = %s, want cancelled", c.Outcome)
	}
	if open := r.OpenSeqs("wf1"); open != nil {
		t.Fatalf("Delete left the maps populated: %v", open)
	}
	// A frame after the cancel meets the tombstone.
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s2"}, "c-1"); !errors.Is(err, errRunLogRemoved) {
		t.Fatalf("Open after Delete = %v, want errRunLogRemoved", err)
	}
	if _, err := r.AppendAfterClosed(ctx, "wf1", "s", &marotte.Entry{Kind: marotte.EntryKindToolResult, Payload: json.RawMessage(`{}`)}); !errors.Is(err, errRunLogRemoved) {
		t.Fatalf("AppendAfterClosed after Delete = %v, want errRunLogRemoved", err)
	}
	if l, err := r.Log(ctx, "wf1"); l != nil || err != nil {
		t.Fatalf("Log after Delete = %v, %v; want nil", l, err)
	}
	if err := r.RemoveDir("wf1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "wf1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory after RemoveDir: %v, want gone", err)
	}
	if err := r.RemoveDir("wf1"); err != nil {
		t.Fatalf("second RemoveDir = %v, want nil", err)
	}
}

func TestRunLog_LogReadsAnExistingDirectoryAndCreatesNone(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	if l, err := r.Log(ctx, "wf-none"); l != nil || err != nil {
		t.Fatalf("Log(unknown) = %v, %v; want nil, nil", l, err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "wf-none")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a read created the run's directory")
	}
	if _, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.CloseNode(ctx, "wf1", "s", "completed", ""); err != nil {
		t.Fatal(err)
	}
	// A second registry over the same root, as the next process.
	fresh := newRunLog(r.root[:len(r.root)-len("/"+runLogDir)])
	l, err := fresh.Log(ctx, "wf1")
	if err != nil || l == nil {
		t.Fatalf("Log(existing) = %v, %v; want the log", l, err)
	}
	if rows := l.RailRows(); len(rows) != 0 {
		t.Fatalf("RailRows = %d rows, want 0: a turn with no content is undrawn", len(rows))
	}
	if count, _ := l.Counters(); count != 1 {
		t.Fatalf("Counters = %d turns, want 1", count)
	}
}

func TestRunLog_OpenSeqsTracksTheNewestSealedSeq(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	turn, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "s"}, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.OpenSeqs("wf1"); got[turn.ID()] != 0 {
		t.Fatalf("OpenSeqs before any seal = %v, want 0", got)
	}
	if _, err := turn.TextDelta(ctx, "", "say-1", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.ToolCall(ctx, "", &marotte.EntryToolCall{ID: "call-1"}); err != nil {
		t.Fatal(err)
	}
	// Text sealed at seq 1, the tool_call at 2.
	if got := r.OpenSeqs("wf1"); got[turn.ID()] != 2 {
		t.Fatalf("OpenSeqs after two seals = %v, want 2", got)
	}
}

func sealedEntries(s []turnlog.Sealed) []marotte.Entry {
	out := make([]marotte.Entry, len(s))
	for i, x := range s {
		out[i] = *x.Entry
	}
	return out
}

// The counter advances outside the lock, so a stamp read from it can run ahead of the served entries.
func TestRunLog_StepTurnsStampCertifiesTheServedEntries(t *testing.T) {
	r := newRunLog(t.TempDir())
	ctx := t.Context()
	turn, _, err := r.Open(ctx, translate.RunStep{RunID: "wf1", NodePath: "root/step", SessionID: "sess-a"}, "c-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := turn.ModelSwitched(ctx, marotte.EntryModelSwitched{From: "m-old", To: "m-new"}); err != nil {
		t.Fatalf("ModelSwitched: %v", err)
	}
	// A seal after the read: the counter moves, the page does not.
	rec := r.runs["wf1"].open["root/step"]
	served := rec.seq.Load()
	rec.seq.Store(served + 1)

	entries, open, stamps, found, err := r.stepTurns(ctx, "wf1", "root/step")
	if err != nil || !found {
		t.Fatalf("stepTurns = found %v, err %v; want the path's turn", found, err)
	}
	if got := kinds(entries); len(got) != 2 || got[1] != marotte.EntryKindModelSwitched {
		t.Fatalf("entries = %v, want [turn_open model_switched]", got)
	}
	if len(open) != 0 {
		t.Fatalf("open = %v, want none (every lane sealed)", open)
	}
	if len(stamps) != 1 {
		t.Fatalf("stamps = %v, want the one run_turn stamp", stamps)
	}
	want := turnVersion(turn.ID(), entries[len(entries)-1].Seq)
	if stamps[0].Version != want {
		t.Fatalf("run_turn stamp version = %q, want %q (the newest served seq, %d), not the record's counter %d",
			stamps[0].Version, want, served, served+1)
	}
}

// A non-plain workflow id is refused before any path is built.
func TestRunLog_RefusesAWorkflowIDThatIsNotAPlainName(t *testing.T) {
	r := newRunLog(t.TempDir())
	for _, id := range []string{"../chats", "wf/1", "wf 1", "."} {
		if _, err := r.Log(t.Context(), id); !errors.Is(err, errRunIDInvalid) {
			t.Errorf("Log(%q) err = %v, want errRunIDInvalid", id, err)
		}
		if _, _, err := r.Open(t.Context(), translate.RunStep{RunID: id, NodePath: "step", SessionID: "sess"}, "c1"); !errors.Is(err, errRunIDInvalid) {
			t.Errorf("Open(%q) err = %v, want errRunIDInvalid", id, err)
		}
		if err := r.RemoveDir(id); !errors.Is(err, errRunIDInvalid) {
			t.Errorf("RemoveDir(%q) err = %v, want errRunIDInvalid", id, err)
		}
	}
	if entries, _ := os.ReadDir(r.root); len(entries) != 0 {
		t.Errorf("run root holds %d entries after the refusals, want none", len(entries))
	}
}
