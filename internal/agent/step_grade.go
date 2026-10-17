package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepClose is the run log's account of how a step's turn ended: its turn_close facts.
type stepClose struct {
	refusal *marotte.RefusalInfo
	outcome marotte.TurnOutcome
	reason  string
	kind    marotte.FailureKind
	rawStop marotte.StopReason
}

func stepCloseOf(c *marotte.EntryTurnClose) stepClose {
	return stepClose{
		refusal: c.Refusal,
		outcome: c.Outcome,
		reason:  c.FailureReason,
		kind:    c.FailureKind,
		rawStop: marotte.StopReason(c.StopReasonRaw),
	}
}

// stoppedOnToolUse is the turn-level tell of kiro-cli's model-call limit. It is not proof on its own: kiro-cli also
// ends a turn whose last tool asked for it with the model's tool_use stop (update_workflow's handoff_ends_turn,
// kiro-cli 2.32.0), and only KAS's node record tells the two apart.
func (c stepClose) stoppedOnToolUse() bool {
	return marotte.ConcludeStopReason(c.rawStop).FailureKind == marotte.FailureKindModelCallLimit
}

// mayBeBroken is whether KAS's record could change or confirm this close's grade, so a reader needs it.
func (c stepClose) mayBeBroken() bool {
	return marotte.SeverityOf(c.outcome) == marotte.TurnSeverityBroken || c.stoppedOnToolUse()
}

// graded is the step's end under KAS's record of the node, the authority (known is false when KAS's record was not
// read). A step KAS completed on a success signal ended completed whatever its turn's stop. The model-call limit is
// named only for a step KAS completed with no success signal whose turn stopped on tool_use.
func (c stepClose) graded(kas workflow.StepRecord, known bool) stepClose {
	switch {
	case !known:
		return c
	case kas.Succeeded():
		if marotte.SeverityOf(c.outcome) == marotte.TurnSeverityBroken {
			return stepClose{outcome: marotte.TurnOutcomeCompleted, rawStop: c.rawStop}
		}
		return c
	case kas.Completed() && c.stoppedOnToolUse():
		c.outcome = marotte.TurnOutcomeFailed
		c.kind = marotte.FailureKindModelCallLimit
		c.reason = marotte.ModelCallLimitStepReason
	}
	return c
}

// end is the close as a `step_ends` row, false for a close that is not broken.
func (c stepClose) end() (marotte.RunStepEnd, bool) {
	if marotte.SeverityOf(c.outcome) != marotte.TurnSeverityBroken {
		return marotte.RunStepEnd{}, false
	}
	return marotte.RunStepEnd{Outcome: c.outcome, FailureReason: c.reason, FailureKind: c.kind}, true
}

// kasSteps indexes a decoded inspect's steps by node path, nil when the read did not decode.
func kasSteps(in workflow.Inspect) map[string]workflow.StepRecord {
	if !in.OK {
		return nil
	}
	return workflow.StepRecords(in.State)
}

// kasStep reads KAS's record of one step; false when the run cannot be read or names no such step.
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
