package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

var _ translate.RunAppender = (*Runs)(nil)

// RunNodeStart opens the step path's turn and announces its turn_open under the
// run scope; a path already open appends nothing.
func (rs *Runs) RunNodeStart(ctx context.Context, step *translate.RunStep, chatID marotte.ChatID) {
	rs.signals.nodeStarted(step.RunID, step.NodePath, step.Path)
	if rs.log == nil {
		return
	}
	turn, opened, err := rs.log.open(ctx, step, chatID)
	if err != nil {
		slog.Warn("run log: open step turn", "run", step.RunID, "node_path", step.NodePath, "error", err)
		return
	}
	if opened != nil {
		translate.PublishAppended(ctx, rs.bus, "", step.RunID, opened)
	}
	rs.steers.bind(step.RunID, step.NodePath, step.SessionID, hostFor(step.RunID, chatID), turn.ID())
}

// RunNodeComplete closes the step path's turn with KAS's status and announces what
// the close sealed.
func (rs *Runs) RunNodeComplete(ctx context.Context, runID string, path []string, status, reason string) {
	nodePath := workflow.PathKey(path)
	rs.signals.nodeStopped(runID, nodePath)
	if rs.log == nil {
		return
	}
	// One gated step with the close, so no message binds the turn between them. Before the close: the
	// unread steers' notes belong inside the step's turn.
	unlock := rs.steers.lockEnd(ctx, runID)
	rs.steers.endLocked(runID, nodePath, true)
	sealed, closed, err := rs.log.closeNode(ctx, runID, nodePath, status, reason)
	unlock()
	if err != nil {
		slog.Warn("run log: close step turn", "run", runID, "node_path", nodePath, "error", err)
	}
	if !closed {
		slog.Debug("run log: node_complete for a path with no open turn", "run", runID, "node_path", nodePath)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
	rs.gradeStepEnd(runID, nodePath, sealed, status)
	if closed {
		rs.quietEdge()
	}
}

// stepNoteRefused and stepNoteModelCallLimit name why a step left the launching chat a note, in its steer id.
const (
	stepNoteRefused        = "refused"
	stepNoteModelCallLimit = "model-call-limit"
	stepNoteEmptyAnswer    = "empty-answer"
)

// The prefix decides nothing. Keyed on the node path, which names one step instance (iterations
// carry iter-N).
func stepNoteID(workflowID, cause, nodePath string) string {
	return "notify-" + workflowID + ":" + cause + ":" + nodePath
}

// gradeStepEnd leaves what a step's node_complete calls for. Under a status other than completed the sealed close is
// the step's end (stepClose.graded is the identity there), so its note needs no read of KAS's record. Under completed
// the close is graded off the frame's goroutine, an engine read, and noted only from the grade recorded.
// A note reaches the human reading the chat, not the agent (recordSteer never calls _session/steer).
func (rs *Runs) gradeStepEnd(workflowID, nodePath string, sealed []turnlog.Sealed, status string) {
	if rs.lifecycle == nil {
		return
	}
	// Read here: run_complete's forgetBounds releases the lease right after a run's last node_complete.
	l, held := rs.lease(workflowID)
	if marotte.RunStatus(status) != marotte.RunStatusCompleted {
		tc, ok := turnCloseOf(sealed)
		if !ok {
			return
		}
		c := stepCloseOf(&tc, "")
		if !c.mayNote() || !held || l.ChatID == "" || rs.coord == nil {
			return
		}
		rs.lifecycle.goUnlessDraining(func() {
			ctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.noteGradedStepEnd(ctx, &l, nodePath, &c, "")
		})
		return
	}
	rs.lifecycle.goUnlessDraining(func() {
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		if g := rs.gradeStep(ctx, workflowID, nodePath); g != nil {
			rs.recordGrade(ctx, workflowID, g, &l, held)
		}
	})
}

// gradeStep records the path's due grade (runLog.gradeStep), reading KAS's record only for a close due one.
func (rs *Runs) gradeStep(ctx context.Context, workflowID, nodePath string) *stepGrade {
	g, err := rs.log.gradeStep(ctx, workflowID, nodePath, func() (workflow.StepRecord, bool) {
		return rs.kasStep(ctx, workflowID, nodePath)
	})
	if err != nil {
		slog.Warn("run log: record a step's grade", "run", workflowID, "node_path", nodePath, "error", err)
	}
	return g
}

// recordGrade announces a step's grade and leaves the launching chat the note it calls for. Every trigger of a grade
// calls it, so a close is noted once, by the trigger that recorded its grade. l and held are the caller's lease read.
func (rs *Runs) recordGrade(ctx context.Context, workflowID string, g *stepGrade, l *runlease.Lease, held bool) {
	translate.PublishAppended(ctx, rs.bus, "", workflowID, g.entry)
	if !g.mayNote || rs.coord == nil || rs.lifecycle == nil || !held || l.ChatID == "" {
		return
	}
	// Off the caller's context: a run read's request ending must not cut the note's write.
	rs.lifecycle.goUnlessDraining(func() {
		nctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		rs.noteGradedStepEnd(nctx, l, g.nodePath, &g.grade, g.sessionID)
	})
}

// stepEnds is runLog.stepEnds, announcing and noting each grade it recorded.
func (rs *Runs) stepEnds(ctx context.Context, workflowID string, kas map[string]workflow.StepRecord) (map[string]marotte.RunStepEnd, error) {
	ends, graded, err := rs.log.stepEnds(ctx, workflowID, kas)
	if len(graded) > 0 {
		l, held := rs.lease(workflowID)
		for _, g := range graded {
			rs.recordGrade(ctx, workflowID, g, &l, held)
		}
	}
	return ends, err
}

// noteGradedStepEnd leaves the note a graded step end calls for, if any. sessionID is the step's session, "" when
// KAS's record was not read.
func (rs *Runs) noteGradedStepEnd(ctx context.Context, l *runlease.Lease, nodePath string, c *stepClose, sessionID string) {
	name := cmp.Or(rs.leasedRunName(l), "Workflow run")
	var cause, text string
	switch {
	case c.outcome == marotte.TurnOutcomeRefused:
		cause, text = stepNoteRefused, stepRefusalNoteText(name, nodePath, c.refusal)
	case c.kind == marotte.FailureKindModelCallLimit:
		cause, text = stepNoteModelCallLimit, stepModelCallLimitNoteText(name, nodePath)
	case c.kind == marotte.FailureKindEmptyAnswer:
		cause, text = stepNoteEmptyAnswer, stepEmptyAnswerNoteText(name, nodePath, sessionID)
	default:
		return
	}
	workflowID := l.WorkflowID
	rs.coord.recordSteer(ctx, marotte.ChatID(l.ChatID), stepNoteID(workflowID, cause, nodePath), &marotte.EntrySteer{
		Text:       text,
		Origin:     marotte.SteerOriginAgent,
		State:      marotte.SteerStateRead,
		Severity:   "warning",
		OriginRun:  workflowID,
		ProducedTs: time.Now().UnixMilli(),
	})
}

// turnCloseOf answers a close's turn_close; false when it sealed none or its payload does not decode.
func turnCloseOf(sealed []turnlog.Sealed) (marotte.EntryTurnClose, bool) {
	for _, s := range sealed {
		if s.Entry == nil || s.Entry.Kind != marotte.EntryKindTurnClose {
			continue
		}
		var c marotte.EntryTurnClose
		if json.Unmarshal(s.Entry.Payload, &c) != nil {
			return marotte.EntryTurnClose{}, false
		}
		return c, true
	}
	return marotte.EntryTurnClose{}, false
}

// stepModelCallLimitNoteText names the step by its workflow.PathKey, as an included node's id holds a '/'.
func stepModelCallLimitNoteText(run, nodePath string) string {
	return run + " step " + nodePath + " stopped early. " + marotte.ModelCallLimitStepReason
}

// stepEmptyAnswerNoteText names the step and its session, which holds the empty answer.
func stepEmptyAnswerNoteText(run, nodePath, sessionID string) string {
	session := ""
	if sessionID != "" {
		session = " (session " + sessionID + ")"
	}
	return run + " step " + nodePath + session + " ended with no answer and no signal. " +
		marotte.EmptyAnswerStepReason
}

// stepRefusalNoteText names the step and does not invite a re-run: a refusal is deterministic.
// Category and explanation are optional. The total length is unbounded, accepted as in noteRunEnd.
func stepRefusalNoteText(run, nodePath string, r *marotte.RefusalInfo) string {
	var b strings.Builder
	b.WriteString(run)
	b.WriteString(" step ")
	b.WriteString(nodePath)
	b.WriteString(" was declined by the model")
	if r != nil && r.Category != "" {
		b.WriteString(" (")
		b.WriteString(r.Category)
		b.WriteString(")")
	}
	b.WriteString(". Re-running it unchanged will be declined again. Change the step's prompt or the model it runs on.")
	if r != nil && r.Explanation != "" {
		b.WriteString(" The model said: ")
		b.WriteString(r.Explanation)
	}
	return b.String()
}

// RunFoldTarget answers the step path's open turn, opening one when the path has neither an open nor a closed turn.
func (rs *Runs) RunFoldTarget(ctx context.Context, step *translate.RunStep, chatID marotte.ChatID) (*turnlog.Turn, bool) {
	if rs.log == nil {
		return nil, false
	}
	if t := rs.log.turn(step.RunID, step.NodePath); t != nil {
		return t, true
	}
	if rs.log.hasClosed(step.RunID, step.NodePath) {
		return nil, false
	}
	t, opened, err := rs.log.open(ctx, step, chatID)
	if err != nil {
		slog.Warn("run log: open step turn for a content frame", "run", step.RunID, "node_path", step.NodePath, "error", err)
		return nil, false
	}
	if opened != nil {
		translate.PublishAppended(ctx, rs.bus, "", step.RunID, opened)
	}
	return t, true
}

// RunOpenTurn answers the step path's open turn, never opening one.
func (rs *Runs) RunOpenTurn(runID, nodePath string) (*turnlog.Turn, bool) {
	if rs.log == nil {
		return nil, false
	}
	t := rs.log.turn(runID, nodePath)
	return t, t != nil
}

// RunAppendAfterClosed files an entry after the path's newest closed turn and announces it; no closed turn is an error.
func (rs *Runs) RunAppendAfterClosed(ctx context.Context, runID, nodePath string, e *marotte.Entry) error {
	if rs.log == nil {
		return errRunLogRemoved
	}
	filed, err := rs.log.appendAfterClosed(ctx, runID, nodePath, e)
	if err != nil {
		return err
	}
	if !filed {
		return errRunNoClosedTurn
	}
	translate.PublishAppended(ctx, rs.bus, "", runID, e)
	return nil
}

// RunMeter folds a step's turn_completion into its open turn's aggregate.
func (rs *Runs) RunMeter(runID, nodePath string, credits, elapsedMs float64) bool {
	return rs.log != nil && rs.log.meter(runID, nodePath, credits, elapsedMs)
}

// RunStopReason records a step's last turn_end stop reason on its open turn.
func (rs *Runs) RunStopReason(runID, nodePath string, raw marotte.StopReason) bool {
	return rs.log != nil && rs.log.stopReason(runID, nodePath, raw)
}

// RunTurnStart records a step's turn_start on its open turn.
func (rs *Runs) RunTurnStart(runID, nodePath string) bool {
	return rs.log != nil && rs.log.turnStarted(runID, nodePath)
}

// closeRun closes every open turn of the run with the outcome KAS's status maps to and announces the sealed entries.
func (rs *Runs) closeRun(ctx context.Context, runID, status string) {
	if rs.log == nil {
		return
	}
	unlock := rs.steers.lockEnd(ctx, runID)
	rs.steers.endLocked(runID, "", true)
	sealed, err := rs.log.closeRunTurns(ctx, runID, runOutcome(status), true)
	unlock()
	if err != nil {
		slog.Warn("run log: close run", "run", runID, "error", err)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
	rs.quietEdge()
	// Words KAS took but never opened a turn for still belong to the step's record.
	staged := rs.log.takeStaged(runID)
	for _, path := range slices.Sorted(maps.Keys(staged)) {
		p := staged[path]
		rs.RunSteer(ctx, runID, path, p.ID, &marotte.EntrySteer{
			Text: p.Text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead, Caller: p.Caller,
		})
	}
}

// deleteRunLog is Delete's second step: open turns close cancelled and maps drop, so the death
// closer finds nothing to close interrupted. RemoveDir is last.
func (rs *Runs) deleteRunLog(ctx context.Context, runID string) {
	unlock := rs.steers.lockEnd(ctx, runID)
	rs.unconfirmed.forget(runID)
	if rs.log == nil {
		unlock()
		return
	}
	rs.steers.endLocked(runID, "", true)
	sealed, err := rs.log.delete(ctx, runID)
	unlock()
	if err != nil {
		slog.Warn("run log: delete run", "run", runID, "error", err)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
}

// The targets stay: the run's next message on a reloaded host session binds them.
func (rs *Runs) endHostedSteers(ctx context.Context, host marotte.ChatID) {
	for _, workflowID := range rs.steers.runsHostedBy(host) {
		rs.steers.end(ctx, workflowID, "", false)
	}
}

func (rs *Runs) hostsLiveRun(chatID marotte.ChatID) bool {
	return rs.log != nil && rs.log.hostsOpen(chatID)
}

func (rs *Runs) openSeq(runID, turn string) (uint64, bool) {
	if rs.log == nil {
		return 0, false
	}
	seq, open := rs.log.openSeqs(runID)[turn]
	return seq, open
}
