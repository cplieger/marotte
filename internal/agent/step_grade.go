package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/workflow"
)

type stepClose struct {
	refusal *marotte.RefusalInfo
	outcome marotte.TurnOutcome
	reason  string
	kind    marotte.FailureKind
	rawStop marotte.StopReason
	// turn is the closed turn's id, which a step_graded names.
	turn string
	// misses counts the reads of KAS's record that left the close due (runLog.gradeLocked); in memory only.
	misses int
	// unanswered is the close's EntryTurnClose.Unanswered.
	unanswered bool
	// offTurn is the close's EntryTurnClose.EndedOffTurn.
	offTurn bool
	// byHand is the grade's EntryStepGraded.EndedByHand.
	byHand bool
	// hasGrade is whether a step_graded for turn is folded in.
	hasGrade bool
	// settled is whether no read of KAS's record is spent on the close's grade again (runLog.gradeLocked); in
	// memory only, so a restart starts the count again.
	settled bool
}

// stepGrade is one step_graded gradeLocked appended, and what the launching chat's note reads from it.
type stepGrade struct {
	entry *marotte.Entry
	// note is the lease node_complete held for the close (runRecord.noteLeases), nil when it held none.
	note      *runlease.Lease
	nodePath  string
	sessionID string    // KAS's record's step session, named by the empty-answer note
	grade     stepClose // the close under the grade (withGrade)
}

// kasNoAnswerText opens the text KAS records in place of a model answer that came back empty (kiro-cli 3.0.0).
const kasNoAnswerText = "No assistant response was recorded for the preceding turn"

// answerFold folds one KAS turn's entries, in order, into whether it said anything after its last tool call: answer
// text, or a send_message or update_workflow call, the tools a step reports through.
type answerFold struct {
	text      strings.Builder
	signalled bool
}

func (a *answerFold) see(e *marotte.Entry) {
	switch e.Kind {
	case marotte.EntryKindToolCall:
		var c marotte.EntryToolCall
		ok := json.Unmarshal(e.Payload, &c) == nil
		// marotte's own cards are no model call: kiro-cli fires a Stop hook after the model's answer.
		if ok && (c.Kind == marotte.ToolKindHook || c.Kind == marotte.ToolKindSteering) {
			return
		}
		a.text.Reset()
		a.signalled = ok && (c.ToolName == "send_message" || c.ToolName == "update_workflow")
	case marotte.EntryKindToolResult:
		a.text.Reset()
	case marotte.EntryKindText:
		var t marotte.EntryText
		if json.Unmarshal(e.Payload, &t) == nil {
			a.text.WriteString(t.Text)
		}
	}
}

// reset starts the fold over at a turn_start: an earlier turn's answer is not this one's.
func (a *answerFold) reset() {
	a.text.Reset()
	a.signalled = false
}

// silent is whether the turn said nothing: no signal call, and a blank answer or KAS's stand-in for an empty one.
func (a *answerFold) silent() bool {
	t := strings.TrimSpace(a.text.String())
	return !a.signalled && (t == "" || strings.HasPrefix(strings.TrimPrefix(t, "("), kasNoAnswerText))
}

func stepCloseOf(c *marotte.EntryTurnClose, turn string) stepClose {
	return stepClose{
		refusal:    c.Refusal,
		outcome:    c.Outcome,
		reason:     c.FailureReason,
		kind:       c.FailureKind,
		rawStop:    marotte.StopReason(c.StopReasonRaw),
		unanswered: c.Unanswered,
		offTurn:    c.EndedOffTurn,
		turn:       turn,
	}
}

// withGrade is c under its step_graded entry: the grade's outcome, kind and reason, the close's refusal only while
// the grade is refused.
func (c *stepClose) withGrade(g *marotte.EntryStepGraded) stepClose {
	out := *c
	out.outcome, out.kind, out.reason, out.byHand, out.hasGrade = g.Outcome, g.FailureKind, g.FailureReason, g.EndedByHand, true
	if g.Outcome != marotte.TurnOutcomeRefused {
		out.refusal = nil
	}
	return out
}

// stoppedAtCallLimit is the turn-level tell of kiro-cli's model-call limit (a tool_use or max_turn_requests stop). It
// is not proof on its own: kiro-cli also ends a turn whose last tool asked for it with the model's tool_use stop
// (update_workflow's handoff_ends_turn, kiro-cli 2.32.0), and only KAS's node record tells the two apart.
func (c *stepClose) stoppedAtCallLimit() bool {
	return marotte.ConcludeStopReason(c.rawStop).FailureKind == marotte.FailureKindModelCallLimit
}

// answeredNothing is whether a close that reads as an ordinary end is one its own turn ended with nothing said.
func (c *stepClose) answeredNothing() bool {
	return c.unanswered && (c.outcome == marotte.TurnOutcomeCompleted || c.outcome == marotte.TurnOutcomeEmpty)
}

// mayBeBroken is whether KAS's record could change or confirm this close's grade.
func (c *stepClose) mayBeBroken() bool {
	return marotte.SeverityOf(c.outcome) == marotte.TurnSeverityBroken || c.stoppedAtCallLimit() || c.answeredNothing() ||
		c.offTurn
}

// mayNote is whether this close's grade could call for a step note: graded never makes a close refused, and the only
// kinds it adds are the model-call limit on a tool_use or max_turn_requests stop and the empty answer.
func (c *stepClose) mayNote() bool {
	return c.outcome == marotte.TurnOutcomeRefused || c.kind == marotte.FailureKindModelCallLimit ||
		c.stoppedAtCallLimit() || c.answeredNothing()
}

// recordedGrade is c's step_graded entry under kas, KAS's record of a node it completed, and c under that entry.
// The run log's grade and a replay's both fold through it, so a grade reads one way wherever it is served.
func (c *stepClose) recordedGrade(kas workflow.StepRecord) (marotte.EntryStepGraded, stepClose) {
	g := c.graded(kas)
	p := marotte.EntryStepGraded{Outcome: g.outcome, FailureReason: g.reason, FailureKind: g.kind, EndedByHand: g.byHand}
	under := c.withGrade(&p)
	return p, under
}

// graded is the step's end under KAS's record of the node, the authority, and the one rule every surface's colour
// reads; a turn that failed before the step's last says nothing here. A step KAS completed on a success signal ended
// completed whatever its turn's stop. A step KAS completed with no signal ended completed by hand when its own turn
// did not end it, unless it refused; else it is the model-call limit when its turn stopped on tool_use or
// max_turn_requests, and an empty answer when its turn said nothing: KAS retries an empty answer only when the model
// sent no stop reason, so one ending end_turn completes the step.
func (c *stepClose) graded(kas workflow.StepRecord) stepClose {
	g := *c
	switch {
	case kas.Succeeded():
		if marotte.SeverityOf(g.outcome) == marotte.TurnSeverityBroken {
			return stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: g.rawStop}
		}
		return g
	case kas.Completed() && kas.CompletionSignal == "" && g.offTurn && g.outcome != marotte.TurnOutcomeRefused:
		g.outcome, g.kind, g.reason, g.byHand = marotte.TurnOutcomeCompleted, "", "", true
	case kas.Completed() && g.stoppedAtCallLimit():
		g.outcome = marotte.TurnOutcomeFailed
		g.kind = marotte.FailureKindModelCallLimit
		g.reason = marotte.ModelCallLimitStepReason
	case kas.Completed() && kas.CompletionSignal == "" && g.answeredNothing():
		g.outcome = marotte.TurnOutcomeFailed
		g.kind = marotte.FailureKindEmptyAnswer
		g.reason = marotte.EmptyAnswerStepReason
	}
	return g
}

// end is the close's step_ends row: one for a recorded grade, a completed one included, or a broken close.
func (c *stepClose) end() (marotte.RunStepEnd, bool) {
	if !c.hasGrade && marotte.SeverityOf(c.outcome) != marotte.TurnSeverityBroken {
		return marotte.RunStepEnd{}, false
	}
	return marotte.RunStepEnd{Outcome: c.outcome, FailureReason: c.reason, FailureKind: c.kind, EndedByHand: c.byHand}, true
}

func kasSteps(in workflow.Inspect) map[string]workflow.StepRecord {
	if !in.OK {
		return nil
	}
	return workflow.StepRecords(in.State)
}

// kasRunSteps is KAS's record of the run's steps by PathKey; false when it could not be read.
func (rs *Runs) kasRunSteps(ctx context.Context, workflowID string) (map[string]workflow.StepRecord, bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	var in workflow.Inspect
	if err == nil {
		in, err = workflow.DecodeInspect(workflowID, raw)
	}
	if err != nil {
		slog.Warn("a step's end could not be checked against the run's state", "workflow_id", logsafe.Field(workflowID),
			"error", err)
		return nil, false
	}
	return kasSteps(in), true
}

func kasStepIn(workflowID string, raw json.RawMessage, nodePath string) (workflow.StepRecord, bool) {
	in, err := workflow.DecodeInspect(workflowID, raw)
	if err != nil {
		return workflow.StepRecord{}, false
	}
	st, ok := workflow.StepRecords(in.State)[nodePath]
	return st, ok
}

// gradeNewestClose regrades the newest turn_close of a replayed step, one the run log holds no turn for, under KAS's
// record, rewriting its payload in place when the grade moves. read answers KAS's record, and is called only for a
// close KAS's record could move. A step the run log holds reads its recorded grade (runLog.gradeLocked) instead.
func gradeNewestClose(entries []marotte.Entry, read func() (workflow.StepRecord, bool)) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != marotte.EntryKindTurnClose {
			continue
		}
		var tc marotte.EntryTurnClose
		if json.Unmarshal(entries[i].Payload, &tc) != nil {
			return
		}
		c := stepCloseOf(&tc, entries[i].Turn)
		if !c.mayBeBroken() {
			return
		}
		kas, known := read()
		if !known || !kas.Completed() {
			return
		}
		_, g := c.recordedGrade(kas)
		if g.outcome == c.outcome && g.kind == c.kind && g.reason == c.reason && g.refusal == c.refusal {
			return
		}
		tc.Outcome, tc.FailureKind, tc.FailureReason, tc.Refusal = g.outcome, g.kind, g.reason, g.refusal
		if payload, err := json.Marshal(&tc); err == nil {
			entries[i].Payload = payload
		}
		return
	}
}
