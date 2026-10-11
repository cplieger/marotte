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
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
	"github.com/cplieger/marotte/internal/workflow"
)

var _ translate.RunAppender = (*Runs)(nil)

// RunNodeStart opens the step path's turn and announces its turn_open under the
// run scope; a path already open appends nothing.
func (rs *Runs) RunNodeStart(ctx context.Context, step *translate.RunStep, chatID marotte.ChatID) {
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
	if rs.log == nil {
		return
	}
	nodePath := workflow.PathKey(path)
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
	rs.noteStepEnded(ctx, runID, path, sealed)
}

// stepNoteRefused and stepNoteModelCallLimit name why a step left the launching chat a note, in its steer id.
const (
	stepNoteRefused        = "refused"
	stepNoteModelCallLimit = "model-call-limit"
)

// The prefix decides nothing. Keyed on the node path, which names one step instance (iterations
// carry iter-N).
func stepNoteID(workflowID, cause, nodePath string) string {
	return "notify-" + workflowID + ":" + cause + ":" + nodePath
}

// A step KAS graded completed that did not finish: declined, or stopped at kiro-cli's model-call limit.
// It reaches the human reading the chat, not the agent (recordSteer never calls _session/steer).
// Only CloseNode's close gets here.
func (rs *Runs) noteStepEnded(ctx context.Context, workflowID string, path []string, sealed []turnlog.Sealed) {
	c, ok := turnCloseOf(sealed)
	if !ok {
		return
	}
	var cause string
	switch {
	case c.Outcome == marotte.TurnOutcomeRefused:
		cause = stepNoteRefused
	case c.FailureKind == marotte.FailureKindModelCallLimit:
		cause = stepNoteModelCallLimit
	default:
		return
	}
	l, held := rs.lease(workflowID)
	if !held || l.ChatID == "" || rs.coord == nil {
		return
	}
	recipe := cmp.Or(l.Recipe, "Workflow run")
	text := stepModelCallLimitNoteText(recipe, path)
	if cause == stepNoteRefused {
		text = stepRefusalNoteText(recipe, path, c.Refusal)
	}
	rs.coord.recordSteer(ctx, marotte.ChatID(l.ChatID), stepNoteID(workflowID, cause, workflow.PathKey(path)), &marotte.EntrySteer{
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

// stepModelCallLimitNoteText names the step and carries the step close's own remedy.
func stepModelCallLimitNoteText(recipe string, path []string) string {
	return recipe + " step " + strings.Join(path, "/") + " stopped early. " + marotte.ModelCallLimitStepReason
}

// stepRefusalNoteText names the step and does not invite a re-run: a refusal is deterministic.
// Category and explanation are optional. The total length is unbounded, accepted as in noteRunEnd.
func stepRefusalNoteText(recipe string, path []string, r *marotte.RefusalInfo) string {
	var b strings.Builder
	b.WriteString(recipe)
	b.WriteString(" step ")
	b.WriteString(strings.Join(path, "/"))
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
	if t := rs.log.foldTurn(step); t != nil {
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
	// Words KAS took but never opened a turn for still belong to the step's record.
	staged := rs.log.takeStaged(runID)
	for _, path := range slices.Sorted(maps.Keys(staged)) {
		p := staged[path]
		rs.RunSteer(ctx, runID, path, p.ID, &marotte.EntrySteer{
			Text: p.Text, Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead,
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

// The targets stay: a re-hosted run's next message binds them.
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
