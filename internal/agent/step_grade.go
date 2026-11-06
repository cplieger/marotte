package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

type stepClose struct {
	refusal *marotte.RefusalInfo
	outcome marotte.TurnOutcome
	reason  string
	kind    marotte.FailureKind
	rawStop marotte.StopReason
	// unanswered is the close's EntryTurnClose.Unanswered.
	unanswered bool
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
		a.text.Reset()
		var c marotte.EntryToolCall
		a.signalled = json.Unmarshal(e.Payload, &c) == nil && (c.ToolName == "send_message" || c.ToolName == "update_workflow")
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

func stepCloseOf(c *marotte.EntryTurnClose) stepClose {
	return stepClose{
		refusal:    c.Refusal,
		outcome:    c.Outcome,
		reason:     c.FailureReason,
		kind:       c.FailureKind,
		rawStop:    marotte.StopReason(c.StopReasonRaw),
		unanswered: c.Unanswered,
	}
}

// stoppedOnToolUse is the turn-level tell of kiro-cli's model-call limit. It is not proof on its own: kiro-cli also
// ends a turn whose last tool asked for it with the model's tool_use stop (update_workflow's handoff_ends_turn,
// kiro-cli 2.32.0), and only KAS's node record tells the two apart.
func (c *stepClose) stoppedOnToolUse() bool {
	return marotte.ConcludeStopReason(c.rawStop).FailureKind == marotte.FailureKindModelCallLimit
}

// answeredNothing is whether a close that reads as an ordinary end is one its own turn ended with nothing said.
func (c *stepClose) answeredNothing() bool {
	return c.unanswered && (c.outcome == marotte.TurnOutcomeCompleted || c.outcome == marotte.TurnOutcomeEmpty)
}

// mayBeBroken is whether KAS's record could change or confirm this close's grade, so a reader needs it.
func (c *stepClose) mayBeBroken() bool {
	return marotte.SeverityOf(c.outcome) == marotte.TurnSeverityBroken || c.stoppedOnToolUse() || c.answeredNothing()
}

// mayNote is whether this close's grade could call for a step note: graded never makes a close refused, and the only
// kinds it adds are the model-call limit on a tool_use stop and the empty answer.
func (c *stepClose) mayNote() bool {
	return c.outcome == marotte.TurnOutcomeRefused || c.kind == marotte.FailureKindModelCallLimit ||
		c.stoppedOnToolUse() || c.answeredNothing()
}

// graded is the step's end under KAS's record of the node, the authority (known is false when KAS's record was not
// read). A step KAS completed on a success signal ended completed whatever its turn's stop. A step KAS completed with
// no signal is the model-call limit when its turn stopped on tool_use, and an empty answer when its own turn ended it
// saying nothing: KAS retries an empty answer only when the model sent no stop reason, so one ending end_turn
// completes the step.
func (c *stepClose) graded(kas workflow.StepRecord, known bool) stepClose {
	g := *c
	switch {
	case !known:
		return g
	case kas.Succeeded():
		if marotte.SeverityOf(g.outcome) == marotte.TurnSeverityBroken {
			return stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: g.rawStop}
		}
		return g
	case kas.Completed() && g.stoppedOnToolUse():
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

func (c *stepClose) end() (marotte.RunStepEnd, bool) {
	if marotte.SeverityOf(c.outcome) != marotte.TurnSeverityBroken {
		return marotte.RunStepEnd{}, false
	}
	return marotte.RunStepEnd{Outcome: c.outcome, FailureReason: c.reason, FailureKind: c.kind}, true
}

func kasSteps(in workflow.Inspect) map[string]workflow.StepRecord {
	if !in.OK {
		return nil
	}
	return workflow.StepRecords(in.State)
}

func (rs *Runs) kasStep(ctx context.Context, workflowID, nodePath string) (workflow.StepRecord, bool) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		slog.Warn("a step's end could not be checked against the run's state", "workflow_id", logsafe.Field(workflowID),
			"node_path", logsafe.Field(nodePath), "error", err)
		return workflow.StepRecord{}, false
	}
	return kasStepIn(workflowID, raw, nodePath)
}

func kasStepIn(workflowID string, raw json.RawMessage, nodePath string) (workflow.StepRecord, bool) {
	in, err := workflow.DecodeInspect(workflowID, raw)
	if err != nil {
		return workflow.StepRecord{}, false
	}
	st, ok := workflow.StepRecords(in.State)[nodePath]
	return st, ok
}

// gradeNewestClose regrades the newest turn_close in a step's entries under KAS's record, rewriting its payload in
// place when the grade moves. read answers KAS's record, and is called only for a close KAS's record could move.
func gradeNewestClose(entries []marotte.Entry, read func() (workflow.StepRecord, bool)) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != marotte.EntryKindTurnClose {
			continue
		}
		var tc marotte.EntryTurnClose
		if json.Unmarshal(entries[i].Payload, &tc) != nil {
			return
		}
		c := stepCloseOf(&tc)
		if !c.mayBeBroken() {
			return
		}
		g := c.graded(read())
		if g.outcome == c.outcome && g.kind == c.kind && g.reason == c.reason {
			return
		}
		tc.Outcome, tc.FailureKind, tc.FailureReason, tc.Refusal = g.outcome, g.kind, g.reason, g.refusal
		if payload, err := json.Marshal(&tc); err == nil {
			entries[i].Payload = payload
		}
		return
	}
}
