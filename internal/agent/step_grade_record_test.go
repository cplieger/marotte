package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// recordedGrade is one step_graded entry of a run log: the turn it grades and its payload.
type recordedGrade struct {
	Turn          string
	Outcome       string `json:"outcome"`
	FailureKind   string `json:"failure_kind"`
	FailureReason string `json:"failure_reason"`
}

func gradesOf(t *testing.T, h *Runtime, workflowID string) []recordedGrade {
	t.Helper()
	l, err := h.runs.log.log(t.Context(), workflowID)
	if err != nil || l == nil {
		t.Fatalf("the run log of %s = %v, %v", workflowID, l, err)
	}
	all, err := l.All()
	if err != nil {
		t.Fatalf("read the run log: %v", err)
	}
	return gradesIn(t, all)
}

func gradesIn(t *testing.T, entries []marotte.Entry) []recordedGrade {
	t.Helper()
	var out []recordedGrade
	for _, e := range entries {
		if e.Kind != marotte.EntryKindStepGraded {
			continue
		}
		g := recordedGrade{Turn: e.Turn}
		if err := json.Unmarshal(e.Payload, &g); err != nil {
			t.Fatalf("decode step_graded %s: %v", e.ID, err)
		}
		out = append(out, g)
	}
	return out
}

// closeTurnOf is the turn id of the run log's newest turn_close.
func closeTurnOf(t *testing.T, h *Runtime, workflowID string) string {
	t.Helper()
	l, err := h.runs.log.log(t.Context(), workflowID)
	if err != nil || l == nil {
		t.Fatalf("the run log of %s = %v, %v", workflowID, l, err)
	}
	all, err := l.All()
	if err != nil {
		t.Fatalf("read the run log: %v", err)
	}
	for _, e := range slices.Backward(all) {
		if e.Kind == marotte.EntryKindTurnClose {
			return e.Turn
		}
	}
	t.Fatalf("the run log of %s holds no turn_close", workflowID)
	return ""
}

func steerIDs(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []string {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.Kind == marotte.EntryKindSteer {
			out = append(out, e.ID)
		}
	}
	return out
}

func leaseToChat(t *testing.T, h *Runtime, cs *testChatStore, workflowID string, chatID marotte.ChatID) {
	t.Helper()
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), workflowID, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
}

// gradedAppends counts the step_graded entries the bus carried after before.
func gradedAppends(t *testing.T, h *Runtime, before uint64) int {
	t.Helper()
	n := 0
	for _, p := range payloadsOfType[marotte.EntryAppendedPayload](t, bufferedSince(h, before), marotte.EventEntryAppended) {
		if p.Entry.Kind == marotte.EntryKindStepGraded {
			n++
		}
	}
	return n
}

// KAS completed kiro-cli 2.32.0's create-workflow step on a success signal, so a refusal its turn sealed is graded
// completed, once, and every reader reads that one grade.
func TestStepGrade_ARefusedStepKASCompletedOnASuccessSignalIsRecordedCompletedOnce(t *testing.T) {
	const (
		id     = "wf_04b7a7ba1affa505"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "create-workflow"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "creator_232_inspect.json"))
	leaseToChat(t, h, cs, id, chatID)
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	completed := recordedGrade{Turn: closeTurnOf(t, h, id), Outcome: string(marotte.TurnOutcomeCompleted)}
	t.Run("the run log holds one completed grade of the step's turn", func(t *testing.T) {
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != completed {
			t.Errorf("step_graded entries = %+v, want only %+v", got, completed)
		}
	})
	t.Run("the run read names the recorded completed end", func(t *testing.T) {
		ends := stepEndsOf(t, h, id)
		if want := (marotte.RunStepEnd{Outcome: marotte.TurnOutcomeCompleted}); len(ends) != 1 || ends[nodePath] != want {
			t.Errorf("step_ends = %+v, want only %s = %+v", ends, nodePath, want)
		}
	})
	t.Run("more reads record no second grade", func(t *testing.T) {
		stepEndsOf(t, h, id)
		if _, err := h.runs.stepTranscript(t.Context(), id, nodePath); err != nil {
			t.Fatalf("step transcript: %v", err)
		}
		joinInflight(t, h)
		if got := gradesOf(t, h, id); len(got) != 1 {
			t.Errorf("step_graded entries after more reads = %+v, want one", got)
		}
	})
	t.Run("the launching chat gets no note", func(t *testing.T) {
		if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
			t.Errorf("the launching chat got %d notes, want none: %+v", len(steers), steers)
		}
	})
}

func TestStepGrade_TheModelCallLimitIsRecordedOnTheStepsTurn(t *testing.T) {
	const id = "wf_42066808f4c28538"
	path := []string{id, "p2-loop", "iter-0", "p2-build"}
	nodePath := workflow.PathKey(path)
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))

	runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))

	limit := recordedGrade{
		Turn: closeTurnOf(t, h, id), Outcome: string(marotte.TurnOutcomeFailed),
		FailureKind: string(marotte.FailureKindModelCallLimit), FailureReason: marotte.ModelCallLimitStepReason,
	}
	t.Run("the run log holds the limit", func(t *testing.T) {
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != limit {
			t.Errorf("step_graded entries = %+v, want only %+v", got, limit)
		}
	})
	t.Run("the transcript serves the sealed close, then its grade", func(t *testing.T) {
		out, err := h.runs.stepTranscript(t.Context(), id, nodePath)
		if err != nil {
			t.Fatalf("step transcript: %v", err)
		}
		c := newestClose(t, out.Entries)
		if c.Outcome != marotte.TurnOutcomeCompleted || c.StopReasonRaw != string(marotte.StopReasonToolUse) {
			t.Errorf("turn_close = {%s %q}, want the sealed {completed tool_use}", c.Outcome, c.StopReasonRaw)
		}
		if n := len(out.Entries); n == 0 || out.Entries[n-1].Kind != marotte.EntryKindStepGraded {
			t.Fatalf("the transcript's last entry is not the step's grade: %v", kinds(out.Entries))
		}
		if got := gradesIn(t, out.Entries); len(got) != 1 || got[0] != limit {
			t.Errorf("the transcript's step_graded = %+v, want only %+v", got, limit)
		}
	})
}

func TestStepGrade_ARecordedGradeOutlivesARestartAndAnUnreadableRecord(t *testing.T) {
	const id = "wf_42066808f4c28538"
	path := []string{id, "p2-loop", "iter-0", "p2-build"}
	nodePath := workflow.PathKey(path)
	h, _, br := runLoggingHub(t)
	dir := t.TempDir()
	h.runs.log = newRunLog(dir)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))
	runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))

	h.runs.log = newRunLog(dir)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))

	ends, _, err := h.runs.log.stepEnds(t.Context(), id, nil)
	if err != nil {
		t.Fatalf("stepEnds: %v", err)
	}
	want := marotte.RunStepEnd{
		Outcome: marotte.TurnOutcomeFailed, FailureKind: marotte.FailureKindModelCallLimit,
		FailureReason: marotte.ModelCallLimitStepReason,
	}
	if len(ends) != 1 || ends[nodePath] != want {
		t.Errorf("stepEnds with no KAS record after a restart = %+v, want only %s = %+v", ends, nodePath, want)
	}
}

func TestStepTranscript_AGradedStepReadsNoRecordAgain(t *testing.T) {
	const id = "wf_42066808f4c28538"
	path := []string{id, "p2-loop", "iter-0", "p2-build"}
	nodePath := workflow.PathKey(path)
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))
	runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))
	br.forgetCalls()

	if _, err := h.runs.stepTranscript(t.Context(), id, nodePath); err != nil {
		t.Fatalf("step transcript: %v", err)
	}
	if br.called(methodKiroWorkflowInspect) {
		t.Errorf("the transcript of a graded step read KAS's record again: calls %v", br.callLog())
	}
}

// KAS's record was unreadable at node_complete, so a later read records the grade, and the launching chat's note
// follows that grade rather than the seal.
func TestStepGrade_TheRunReadRecordsAGradeTheNodeCompleteMissed(t *testing.T) {
	const chatID = marotte.ChatID("c1")
	down := errors.New("the engine is gone")

	t.Run("refused, then completed on a success signal", func(t *testing.T) {
		const id = "wf_04b7a7ba1affa505"
		path := []string{id, "create-workflow"}
		h, cs, br := runLoggingHub(t)
		leaseToChat(t, h, cs, id, chatID)
		br.setCallErr(methodKiroWorkflowInspect, down)
		refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
		h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
		joinInflight(t, h)
		if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
			t.Errorf("an unread record left %d notes, want none: %+v", len(steers), steers)
		}

		br.clearCallErr(methodKiroWorkflowInspect)
		br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "creator_232_inspect.json"))
		before := h.bus.fanout.Position().Head
		stepEndsOf(t, h, id)
		joinInflight(t, h)

		want := recordedGrade{Turn: closeTurnOf(t, h, id), Outcome: string(marotte.TurnOutcomeCompleted)}
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
			t.Errorf("step_graded entries = %+v, want only %+v", got, want)
		}
		if n := gradedAppends(t, h, before); n != 1 {
			t.Errorf("the bus carried %d step_graded appends, want one", n)
		}
		if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
			t.Errorf("a step KAS completed on its signal left %d notes, want none: %+v", len(steers), steers)
		}
	})

	limitStep := func(t *testing.T, read func(h *Runtime, id, nodePath string)) {
		t.Helper()
		const id = "wf_42066808f4c28538"
		path := []string{id, "p2-loop", "iter-0", "p2-build"}
		nodePath := workflow.PathKey(path)
		h, cs, br := runLoggingHub(t)
		leaseToChat(t, h, cs, id, chatID)
		br.setCallErr(methodKiroWorkflowInspect, down)
		runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))
		if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
			t.Errorf("an unread record left %d notes, want none: %+v", len(steers), steers)
		}

		br.clearCallErr(methodKiroWorkflowInspect)
		br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))
		read(h, id, nodePath)
		joinInflight(t, h)

		want := recordedGrade{
			Turn: closeTurnOf(t, h, id), Outcome: string(marotte.TurnOutcomeFailed),
			FailureKind: string(marotte.FailureKindModelCallLimit), FailureReason: marotte.ModelCallLimitStepReason,
		}
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
			t.Errorf("step_graded entries = %+v, want only %+v", got, want)
		}
		ids := steerIDs(t, cs, chatID)
		if wantID := stepNoteID(id, stepNoteModelCallLimit, nodePath); len(ids) != 1 || ids[0] != wantID {
			t.Fatalf("launching chat notes = %v, want only %s", ids, wantID)
		}
		// The inspect fixture's workflowName outranks the lease's name.
		if text, want := chatSteers(t, cs, chatID)[0].Text, stepModelCallLimitNoteText("one-engine-build", nodePath); text != want {
			t.Errorf("note text = %q, want %q", text, want)
		}
	}
	t.Run("a tool_use stop KAS completed with no signal", func(t *testing.T) {
		limitStep(t, func(h *Runtime, id, _ string) { stepEndsOf(t, h, id) })
	})
	t.Run("the step transcript read records it too", func(t *testing.T) {
		limitStep(t, func(h *Runtime, id, nodePath string) {
			if _, err := h.runs.stepTranscript(t.Context(), id, nodePath); err != nil {
				t.Fatalf("step transcript: %v", err)
			}
		})
	})
}

// A grade node_complete's KAS read missed is recorded at the run's next lifecycle frame, with no reader: another
// step's node_complete or the run's run_complete. It is recorded and noted once, and a run read after adds neither.
func TestStepGrade_TheRunsNextFrameRecordsAGradeTheNodeCompleteMissed(t *testing.T) {
	const (
		id     = "wf_42066808f4c28538"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "p2-loop", "iter-0", "p2-build"}
	nodePath := workflow.PathKey(path)
	retry := func(t *testing.T, frame func(h *Runtime)) {
		t.Helper()
		h, cs, br := runLoggingHub(t)
		leaseToChat(t, h, cs, id, chatID)
		br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))
		runRecordedStep(t, h, id, nodePath, path, recordedStop(t, "limit_231_session_tail.jsonl"))
		if got := gradesOf(t, h, id); len(got) != 0 {
			t.Fatalf("step_graded entries under an unread record = %+v, want none", got)
		}
		turn := closeTurnOf(t, h, id)

		br.clearCallErr(methodKiroWorkflowInspect)
		br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "limit_231_inspect.json"))
		frame(h)
		joinInflight(t, h)

		want := recordedGrade{
			Turn: turn, Outcome: string(marotte.TurnOutcomeFailed),
			FailureKind: string(marotte.FailureKindModelCallLimit), FailureReason: marotte.ModelCallLimitStepReason,
		}
		wantNote := stepNoteID(id, stepNoteModelCallLimit, nodePath)
		check := func(when string) {
			t.Helper()
			if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
				t.Errorf("%s: step_graded entries = %+v, want only %+v", when, got, want)
			}
			if ids := steerIDs(t, cs, chatID); len(ids) != 1 || ids[0] != wantNote {
				t.Errorf("%s: launching chat notes = %v, want only %s", when, ids, wantNote)
			}
		}
		check("after the frame")
		stepEndsOf(t, h, id)
		joinInflight(t, h)
		check("after a run read")
	}
	t.Run("another step's node_complete", func(t *testing.T) {
		retry(t, func(h *Runtime) {
			next := []string{id, "p2-review"}
			runRecordedStep(t, h, id, workflow.PathKey(next), next, marotte.StopReasonEndTurn)
		})
	})
	t.Run("a terminal run_complete", func(t *testing.T) {
		retry(t, func(h *Runtime) { h.runs.observeComplete(t.Context(), "", runComplete(id, "completed")) })
	})
	t.Run("a paused run_complete", func(t *testing.T) {
		retry(t, func(h *Runtime) { h.runs.observeComplete(t.Context(), "", runComplete(id, "paused")) })
	})
}

func TestHandleTurnRange_ServesTheStepsRecordedGrade(t *testing.T) {
	const id = "wf_04b7a7ba1affa505"
	path := []string{id, "create-workflow"}
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, fixtureFile(t, "creator_232_inspect.json"))
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	stepEndsOf(t, h, id)
	turn := closeTurnOf(t, h, id)

	rr := &runRoutes{runs: h.runs}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/"+id+"/turns/"+turn, nil)
	req.SetPathValue("id", id)
	req.SetPathValue("turn", turn)
	rec := httptest.NewRecorder()
	rr.handleTurnRange(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries []marotte.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the reply: %v", err)
	}
	if n := len(got.Entries); n == 0 || got.Entries[n-1].Kind != marotte.EntryKindStepGraded {
		t.Fatalf("the turn's entries end %v, want its step_graded last", kinds(got.Entries))
	}
	want := recordedGrade{Turn: turn, Outcome: string(marotte.TurnOutcomeCompleted)}
	if g := gradesIn(t, got.Entries); len(g) != 1 || g[0] != want {
		t.Errorf("the turn's step_graded = %+v, want only %+v", g, want)
	}
}

// On resume KAS completes a step the death closer closed interrupted, with a node_complete that closes nothing.
func TestStepGrade_AResumedStepKASCompletedIsGradedWithoutANewClose(t *testing.T) {
	const id = "wf_1"
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, _, br := runLoggingHub(t)
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
	if _, err := h.runs.log.closeHost(t.Context(), runChatID(id), marotte.ConcludeStopReason(marotte.StopReasonInterrupted)); err != nil {
		t.Fatalf("closeHost: %v", err)
	}
	interrupted := closeTurnOf(t, h, id)

	t.Run("a step KAS still runs is not graded", func(t *testing.T) {
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "running", ""))
		stepEndsOf(t, h, id)
		joinInflight(t, h)
		if got := gradesOf(t, h, id); len(got) != 0 {
			t.Errorf("step_graded entries for a running step = %+v, want none", got)
		}
	})
	t.Run("its completion grades the interrupted turn", func(t *testing.T) {
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", "success"))
		h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
		joinInflight(t, h)
		want := recordedGrade{Turn: interrupted, Outcome: string(marotte.TurnOutcomeCompleted)}
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
			t.Errorf("step_graded entries = %+v, want only %+v", got, want)
		}
	})
}

// KAS's record describes the path's newest execution, so while a retry is open it is not the earlier attempt's.
func TestStepGrade_AnEarlierAttemptIsNotGradedWhileTheNextIsOpen(t *testing.T) {
	const id = "wf_1"
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	step := &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}

	t.Run("a turn this process reopened", func(t *testing.T) {
		h, _, br := runLoggingHub(t)
		h.runs.RunNodeStart(t.Context(), step, "")
		h.runs.RunNodeComplete(t.Context(), id, path, "failed", "")
		joinInflight(t, h)
		first := closeTurnOf(t, h, id)
		h.runs.RunNodeStart(t.Context(), step, "")
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", "success"))

		stepEndsOf(t, h, id)
		joinInflight(t, h)
		if got := gradesOf(t, h, id); len(got) != 0 {
			t.Errorf("step_graded entries while the retry is open = %+v, want none", got)
		}

		h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
		joinInflight(t, h)
		for _, g := range gradesOf(t, h, id) {
			if g.Turn == first {
				t.Errorf("the earlier attempt %s was graded %+v after the retry closed", first, g)
			}
		}
	})
	t.Run("a retry a restart left open is the one graded", func(t *testing.T) {
		h, _, br := runLoggingHub(t)
		dir := t.TempDir()
		h.runs.log = newRunLog(dir)
		h.runs.RunNodeStart(t.Context(), step, "")
		h.runs.RunNodeComplete(t.Context(), id, path, "failed", "")
		joinInflight(t, h)
		first := closeTurnOf(t, h, id)
		h.runs.RunNodeStart(t.Context(), step, "")
		h.runs.log = newRunLog(dir)
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", "success"))

		stepEndsOf(t, h, id)
		joinInflight(t, h)
		// Opening the log closes the retry unterminated, so the retry is the newest close KAS's record describes.
		retry := closeTurnOf(t, h, id)
		if retry == first {
			t.Fatalf("the newest close is the earlier attempt %s, want the retry's unterminated close", first)
		}
		want := recordedGrade{Turn: retry, Outcome: string(marotte.TurnOutcomeCompleted)}
		if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
			t.Errorf("step_graded entries = %+v, want only %+v", got, want)
		}
	})
}

// A step KAS did not complete keeps its sealed close, so its note needs no read of KAS's record.
func TestRunNodeComplete_ARefusedStepKASDidNotCompleteIsNotedWithoutReadingItsRecord(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	h, cs, br := runLoggingHub(t)
	leaseToChat(t, h, cs, id, chatID)
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	br.forgetCalls()

	h.runs.RunNodeComplete(t.Context(), id, path, "failed", "")
	joinInflight(t, h)

	if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteRefused, workflow.PathKey(path)); len(ids) != 1 || ids[0] != want {
		t.Errorf("launching chat notes = %v, want only %s", ids, want)
	}
	if br.called(methodKiroWorkflowInspect) {
		t.Errorf("a step KAS did not complete read KAS's record for its note: calls %v", br.callLog())
	}
	if got := gradesOf(t, h, id); len(got) != 0 {
		t.Errorf("step_graded entries = %+v, want none", got)
	}
}

// run_complete's forgetBounds releases the lease first (run_bounds.go), so a run's last step's grade can finish
// after the lease is gone; the note still reaches the chat the lease named at node_complete.
func TestRunNodeComplete_ALastStepIsNotedThoughTheRunEndsDuringItsGrade(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	endDuringGrade := func(t *testing.T, h *Runtime, br *fakeBridge) {
		t.Helper()
		gate := make(chan struct{})
		br.setBlockOn(methodKiroWorkflowInspect, gate)
		h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
		h.runs.releaseLease(t.Context(), id)
		close(gate)
		joinInflight(t, h)
	}
	t.Run("a refused step KAS completed with no signal", func(t *testing.T) {
		h, cs, br := runLoggingHub(t)
		leaseToChat(t, h, cs, id, chatID)
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
		refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
		endDuringGrade(t, h, br)
		if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteRefused, nodePath); len(ids) != 1 || ids[0] != want {
			t.Errorf("launching chat notes = %v, want only %s", ids, want)
		}
	})
	t.Run("a step stopped at the model-call limit", func(t *testing.T) {
		h, cs, br := runLoggingHub(t)
		leaseToChat(t, h, cs, id, chatID)
		br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
		h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
		if !h.runs.log.stopReason(id, nodePath, marotte.StopReasonToolUse) {
			t.Fatal("StopReason(tool_use) refused on the open turn")
		}
		endDuringGrade(t, h, br)
		if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteModelCallLimit, nodePath); len(ids) != 1 || ids[0] != want {
			t.Errorf("launching chat notes = %v, want only %s", ids, want)
		}
	})
}

// A run read whose own inspect answered first can record the grade while node_complete's read is still out and after
// run_complete released the lease; the note still reaches the chat the lease named at node_complete, once.
func TestRunNodeComplete_ALastStepIsNotedThoughARunReadRecordsItsGradeAfterTheRunEnds(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	leaseToChat(t, h, cs, id, chatID)
	rec := inspectWithStep(t, id, "build", "completed", "")
	br.setCallResult(methodKiroWorkflowInspect, rec)
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	br.forgetCalls()
	gate := make(chan struct{})
	br.setBlockOn(methodKiroWorkflowInspect, gate)

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	waitForCall(t, br, methodKiroWorkflowInspect)
	h.runs.releaseLease(t.Context(), id)
	st, ok := kasStepIn(id, rec, nodePath)
	if !ok {
		t.Fatal("the inspect reply holds no record of the step")
	}
	if _, err := h.runs.stepEnds(t.Context(), id, map[string]workflow.StepRecord{nodePath: st}); err != nil {
		t.Fatalf("stepEnds: %v", err)
	}
	close(gate)
	joinInflight(t, h)

	if got := gradesOf(t, h, id); len(got) != 1 || got[0].Outcome != string(marotte.TurnOutcomeRefused) {
		t.Errorf("step_graded entries = %+v, want one refused", got)
	}
	if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteRefused, nodePath); len(ids) != 1 || ids[0] != want {
		t.Errorf("launching chat notes = %v, want only %s", ids, want)
	}
}

// turnClosedHook runs fn on the publisher's goroutine at the first turn_closed the run bus carries: after the close
// released runLog.mu and before node_complete's grade goroutine is spawned.
type turnClosedHook struct {
	next runBroadcaster
	fn   func()
	once sync.Once
}

func (b *turnClosedHook) Broadcast(ctx context.Context, evt marotte.ServerEvent) {
	if evt.Type == marotte.EventTurnClosed {
		b.once.Do(b.fn)
	}
	b.next.Broadcast(ctx, evt)
}

// A run read waiting on runLog.mu when node_complete's close releases it can record the grade before node_complete's
// goroutine takes the lock, after run_complete released the lease; the note still reaches the chat the lease named
// at node_complete, once. The test holds runLog.mu from the close's publish to stand in for that read.
func TestRunNodeComplete_ALastStepIsNotedThoughARunReadRecordsItsGradeBeforeTheStepEndDoes(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	leaseToChat(t, h, cs, id, chatID)
	rec := inspectWithStep(t, id, "build", "completed", "")
	br.setCallResult(methodKiroWorkflowInspect, rec)
	st, ok := kasStepIn(id, rec, nodePath)
	if !ok {
		t.Fatal("the inspect reply holds no record of the step")
	}
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	locked := false
	h.runs.bus = &turnClosedHook{next: h.runs.bus, fn: func() {
		h.runs.log.mu.Lock()
		locked = true
	}}

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	if !locked {
		t.Fatal("node_complete published no turn_closed")
	}
	h.runs.releaseLease(t.Context(), id)
	r := h.runs.log.runs[id]
	if err := scanStepFactsLocked(r, r.log); err != nil {
		h.runs.log.mu.Unlock()
		t.Fatalf("scan the run log: %v", err)
	}
	graded, err := h.runs.log.gradeDueLocked(t.Context(), r, []string{nodePath},
		kasRead{steps: map[string]workflow.StepRecord{nodePath: st}, ok: true})
	h.runs.log.mu.Unlock()
	if err != nil {
		t.Fatalf("grade as the run read: %v", err)
	}
	for _, g := range graded {
		h.runs.recordGrade(t.Context(), id, g)
	}
	joinInflight(t, h)

	if got := gradesOf(t, h, id); len(got) != 1 || got[0].Outcome != string(marotte.TurnOutcomeRefused) {
		t.Errorf("step_graded entries = %+v, want one refused", got)
	}
	if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteRefused, nodePath); len(ids) != 1 || ids[0] != want {
		t.Errorf("launching chat notes = %v, want only %s", ids, want)
	}
}

// node_complete's own grade notes the close; a run read before and after run_complete finds it graded and leaves no
// second note.
func TestRunNodeComplete_AStepEndThatRecordsTheGradeNotesItOnce(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	leaseToChat(t, h, cs, id, chatID)
	rec := inspectWithStep(t, id, "build", "completed", "")
	br.setCallResult(methodKiroWorkflowInspect, rec)
	st, ok := kasStepIn(id, rec, nodePath)
	if !ok {
		t.Fatal("the inspect reply holds no record of the step")
	}
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	kas := map[string]workflow.StepRecord{nodePath: st}
	if _, err := h.runs.stepEnds(t.Context(), id, kas); err != nil {
		t.Fatalf("stepEnds: %v", err)
	}
	h.runs.releaseLease(t.Context(), id)
	if _, err := h.runs.stepEnds(t.Context(), id, kas); err != nil {
		t.Fatalf("stepEnds: %v", err)
	}
	joinInflight(t, h)

	if got := gradesOf(t, h, id); len(got) != 1 || got[0].Outcome != string(marotte.TurnOutcomeRefused) {
		t.Errorf("step_graded entries = %+v, want one refused", got)
	}
	if ids, want := steerIDs(t, cs, chatID), stepNoteID(id, stepNoteRefused, nodePath); len(ids) != 1 || ids[0] != want {
		t.Errorf("launching chat notes = %v, want only %s", ids, want)
	}
}

// Only the lease node_complete captured, in process memory, decides a close's note: a grade a run read records after
// a restart leaves none, though the run's lease is still held.
func TestStepGrade_AGradeRecordedAfterARestartLeavesNoNote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	leaseToChat(t, h, cs, id, chatID)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	h.runs.log = newRunLog(filepath.Dir(h.runs.log.root))
	rec := inspectWithStep(t, id, "build", "completed", "")
	st, ok := kasStepIn(id, rec, nodePath)
	if !ok {
		t.Fatal("the inspect reply holds no record of the step")
	}
	if _, err := h.runs.stepEnds(t.Context(), id, map[string]workflow.StepRecord{nodePath: st}); err != nil {
		t.Fatalf("stepEnds: %v", err)
	}
	joinInflight(t, h)

	if got := gradesOf(t, h, id); len(got) != 1 || got[0].Outcome != string(marotte.TurnOutcomeRefused) {
		t.Errorf("step_graded entries = %+v, want one refused", got)
	}
	if ids := steerIDs(t, cs, chatID); len(ids) != 0 {
		t.Errorf("launching chat notes = %v, want none", ids)
	}
}

// inspectWithSteps is an inspect reply whose tree holds, under the run's root sequence, one unsignalled step per
// {nodeID, status} pair, at path [workflowID, nodeID].
func inspectWithSteps(t *testing.T, workflowID string, steps ...[2]string) json.RawMessage {
	t.Helper()
	children := make([]any, 0, len(steps))
	for _, s := range steps {
		children = append(children, map[string]any{"nodeId": s[0], "type": "step", "status": s[1], "sessionId": "step-session-1"})
	}
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": "running",
			"root": map[string]any{"nodeId": workflowID, "type": "sequence", "status": "running", "children": children},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// completeStep runs one step KAS completes whose own turn ended it with an answer, so its close is not due a grade.
func completeStep(t *testing.T, h *Runtime, workflowID string, path []string) {
	t.Helper()
	step := &translate.RunStep{RunID: workflowID, NodePath: workflow.PathKey(path), SessionID: "step-session-1"}
	h.runs.RunNodeStart(t.Context(), step, "")
	turn, ok := h.runs.RunFoldTarget(t.Context(), step, "")
	if !ok {
		t.Fatal("Setup: the step has no open turn")
	}
	if _, err := turn.TextDelta(t.Context(), "", "say-done", "Done."); err != nil {
		t.Fatalf("Setup: answer: %v", err)
	}
	if !h.runs.RunStopReason(workflowID, step.NodePath, marotte.StopReasonEndTurn) {
		t.Fatal("Setup: RunStopReason refused on the open turn")
	}
	h.runs.RunNodeComplete(t.Context(), workflowID, path, "completed", "")
	joinInflight(t, h)
}

// A close KAS's record ended without completing can never be graded, so the read that finds it so is its last: no
// later step end, run_complete or transcript open asks KAS about it again.
func TestStepGrade_ACloseKASEndedWithoutCompletingIsReadOnce(t *testing.T) {
	const id = "wf_1"
	build, review, ship := []string{id, "build"}, []string{id, "review"}, []string{id, "ship"}
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect,
		inspectWithSteps(t, id, [2]string{"build", "failed"}, [2]string{"review", "completed"}, [2]string{"ship", "completed"}))
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: workflow.PathKey(build), SessionID: "step-session-1"}, "")
	h.runs.RunNodeComplete(t.Context(), id, build, "failed", "")
	joinInflight(t, h)
	br.forgetCalls()

	completeStep(t, h, id, review)
	if n := countCalls(br, methodKiroWorkflowInspect); n != 1 {
		t.Fatalf("the step end after the failed step read KAS's record %d times, want once", n)
	}
	br.forgetCalls()
	completeStep(t, h, id, ship)
	h.runs.observeComplete(t.Context(), "", runComplete(id, "failed"))
	joinInflight(t, h)
	transcriptOf(t, h, id, workflow.PathKey(build))
	transcriptOf(t, h, id, workflow.PathKey(review))

	if n := countCalls(br, methodKiroWorkflowInspect); n != 0 {
		t.Errorf("a step end, a run_complete and two transcript opens read KAS's record %d times, want none: calls %v",
			n, br.callLog())
	}
	if got := gradesOf(t, h, id); len(got) != 0 {
		t.Errorf("step_graded entries = %+v, want none", got)
	}
}

// The run's terminal run_complete closes an open turn failed under a failed record: its own read settles the close,
// and opening the step's transcript after asks KAS nothing.
func TestStepGrade_ATurnTheRunsEndFailedIsReadOnce(t *testing.T) {
	const id = "wf_1"
	build := []string{id, "build"}
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithSteps(t, id, [2]string{"build", "failed"}))
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: workflow.PathKey(build), SessionID: "step-session-1"}, "")
	br.forgetCalls()

	h.runs.observeComplete(t.Context(), "", runComplete(id, "failed"))
	joinInflight(t, h)
	if n := countCalls(br, methodKiroWorkflowInspect); n != 1 {
		t.Fatalf("the terminal run_complete read KAS's record %d times, want once", n)
	}
	br.forgetCalls()
	transcriptOf(t, h, id, workflow.PathKey(build))
	transcriptOf(t, h, id, workflow.PathKey(build))
	if n := countCalls(br, methodKiroWorkflowInspect); n != 0 {
		t.Errorf("two transcript opens read KAS's record %d times, want none: calls %v", n, br.callLog())
	}
}

// KAS's record holds no node at the close's path, so nothing can grade it: the first read settles it.
func TestStepGrade_ACloseKASHoldsNoRecordOfIsReadOnce(t *testing.T) {
	const id = "wf_1"
	build := []string{id, "build"}
	h, _, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithSteps(t, id, [2]string{"review", "completed"}))
	refuseStep(t, h.runs, id, build, &marotte.RefusalInfo{Category: "policy"})
	br.forgetCalls()

	h.runs.RunNodeComplete(t.Context(), id, build, "completed", "")
	joinInflight(t, h)
	transcriptOf(t, h, id, workflow.PathKey(build))
	transcriptOf(t, h, id, workflow.PathKey(build))

	if n := countCalls(br, methodKiroWorkflowInspect); n != 1 {
		t.Errorf("node_complete and two transcript opens read KAS's record %d times, want once: calls %v", n, br.callLog())
	}
}

// A read that fails is retried at the run's next triggers up to gradeReadAttempts reads in all; then the close is
// settled ungraded, and the step's transcript still opens.
func TestStepGrade_AnUnreadableRecordIsAskedAtMostTheBound(t *testing.T) {
	const id = "wf_1"
	build, review := []string{id, "build"}, []string{id, "review"}
	h, _, br := runLoggingHub(t)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))
	refuseStep(t, h.runs, id, build, &marotte.RefusalInfo{Category: "policy"})
	br.forgetCalls()

	h.runs.RunNodeComplete(t.Context(), id, build, "completed", "")
	joinInflight(t, h)
	completeStep(t, h, id, review)
	transcriptOf(t, h, id, workflow.PathKey(build))
	transcriptOf(t, h, id, workflow.PathKey(review))
	h.runs.observeComplete(t.Context(), "", runComplete(id, "paused"))
	joinInflight(t, h)
	entries := transcriptOf(t, h, id, workflow.PathKey(build))

	if n := countCalls(br, methodKiroWorkflowInspect); n != gradeReadAttempts {
		t.Errorf("node_complete and five later triggers read KAS's record %d times, want %d: calls %v",
			n, gradeReadAttempts, br.callLog())
	}
	if c := newestClose(t, entries); c.Outcome != marotte.TurnOutcomeRefused {
		t.Errorf("the settled step's transcript closes %s, want its sealed refused", c.Outcome)
	}
	if got := gradesIn(t, entries); len(got) != 0 {
		t.Errorf("the settled step's transcript holds step_graded %+v, want none", got)
	}
}

// Whatever the bound has left, the run's terminal run_complete is the last read: a close it cannot grade is settled.
func TestStepGrade_TheRunsEndSettlesACloseItCannotRead(t *testing.T) {
	const id = "wf_1"
	build := []string{id, "build"}
	h, _, br := runLoggingHub(t)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))
	refuseStep(t, h.runs, id, build, &marotte.RefusalInfo{Category: "policy"})
	h.runs.RunNodeComplete(t.Context(), id, build, "completed", "")
	joinInflight(t, h)
	br.forgetCalls()

	h.runs.observeComplete(t.Context(), "", runComplete(id, "completed"))
	joinInflight(t, h)
	transcriptOf(t, h, id, workflow.PathKey(build))

	if n := countCalls(br, methodKiroWorkflowInspect); n != 1 {
		t.Errorf("run_complete and a transcript open read KAS's record %d times, want once: calls %v", n, br.callLog())
	}
}

// A completed node_complete for a settled close's path is KAS saying the node moved, so the close is graded again:
// on resume KAS completes a step the death closer closed interrupted, with a node_complete that closes nothing.
func TestStepGrade_ASettledCloseIsGradedWhenKASCompletesItsNode(t *testing.T) {
	const id = "wf_1"
	path := []string{id, "build"}
	nodePath := workflow.PathKey(path)
	h, _, br := runLoggingHub(t)
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
	if _, err := h.runs.log.closeHost(t.Context(), runChatID(id), marotte.ConcludeStopReason(marotte.StopReasonInterrupted)); err != nil {
		t.Fatalf("closeHost: %v", err)
	}
	interrupted := closeTurnOf(t, h, id)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "running", ""))
	br.forgetCalls()
	for range gradeReadAttempts + 1 {
		transcriptOf(t, h, id, nodePath)
	}
	if n := countCalls(br, methodKiroWorkflowInspect); n != gradeReadAttempts {
		t.Fatalf("%d transcript opens of a running step read KAS's record %d times, want %d",
			gradeReadAttempts+1, n, gradeReadAttempts)
	}

	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", "success"))
	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	want := recordedGrade{Turn: interrupted, Outcome: string(marotte.TurnOutcomeCompleted)}
	if got := gradesOf(t, h, id); len(got) != 1 || got[0] != want {
		t.Errorf("step_graded entries = %+v, want only %+v", got, want)
	}
}

// A grade the run log refuses to write counts against the same bound as a failed read: a log that refuses every
// write is not asked KAS about forever.
func TestStepGrade_ARefusedGradeWriteIsRetriedAtMostTheBound(t *testing.T) {
	const id = "wf_1"
	build := []string{id, "build"}
	h, _, br := runLoggingHub(t)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine is gone"))
	refuseStep(t, h.runs, id, build, &marotte.RefusalInfo{Category: "policy"})
	br.forgetCalls()
	h.runs.RunNodeComplete(t.Context(), id, build, "completed", "")
	joinInflight(t, h)
	l, err := h.runs.log.log(t.Context(), id)
	if err != nil || l == nil {
		t.Fatalf("the run log of %s = %v, %v", id, l, err)
	}
	if err := l.Remove(); err != nil {
		t.Fatalf("tombstone the run log: %v", err)
	}
	br.clearCallErr(methodKiroWorkflowInspect)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))

	for range gradeReadAttempts {
		transcriptOf(t, h, id, workflow.PathKey(build))
	}
	if n := countCalls(br, methodKiroWorkflowInspect); n != gradeReadAttempts {
		t.Errorf("node_complete and %d transcript opens read KAS's record %d times, want %d: calls %v",
			gradeReadAttempts, n, gradeReadAttempts, br.callLog())
	}
}
