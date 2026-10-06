package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

var _ translate.RunAppender = (*Runs)(nil)

// RunNodeStart opens the step path's turn and announces its turn_open under the
// run scope; a path already open appends nothing.
func (rs *Runs) RunNodeStart(ctx context.Context, runID, nodePath, sessionID string, chatID marotte.ChatID) {
	if rs.log == nil {
		return
	}
	_, opened, err := rs.log.Open(ctx, runID, nodePath, sessionID, chatID)
	if err != nil {
		slog.Warn("run log: open step turn", "run", runID, "node_path", nodePath, "error", err)
		return
	}
	if opened != nil {
		translate.PublishAppended(ctx, rs.bus, "", runID, opened)
	}
}

// RunNodeComplete closes the step path's turn with KAS's status and announces what
// the close sealed.
func (rs *Runs) RunNodeComplete(ctx context.Context, runID, nodePath, status, reason string) {
	if rs.log == nil {
		return
	}
	sealed, closed, err := rs.log.CloseNode(ctx, runID, nodePath, status, reason)
	if err != nil {
		slog.Warn("run log: close step turn", "run", runID, "node_path", nodePath, "error", err)
	}
	if !closed {
		slog.Debug("run log: node_complete for a path with no open turn", "run", runID, "node_path", nodePath)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
	rs.noteStepRefused(ctx, runID, nodePath, sealed)
}

// stepRefusalNoteID is the note's steer id, shaped like runEndNoteID; the prefix decides nothing.
// Keyed on the node path, which names one step instance (iterations carry iter-N).
func stepRefusalNoteID(workflowID, nodePath string) string {
	return "notify-" + workflowID + ":refused:" + nodePath
}

// noteStepRefused leaves the launching chat a row saying a step was declined, read off the close's
// turn_close. It reaches the human reading the chat, not the agent (recordSteer never calls
// _session/steer). Only CloseNode's close gets here.
func (rs *Runs) noteStepRefused(ctx context.Context, workflowID, nodePath string, sealed []turnlog.Sealed) {
	c, refused := refusedClose(sealed)
	if !refused {
		return
	}
	l, held := rs.lease(workflowID)
	if !held || l.ChatID == "" || rs.coord == nil {
		return
	}
	producedTs := time.Now().UnixMilli()
	rs.coord.recordSteer(ctx, marotte.ChatID(l.ChatID), stepRefusalNoteID(workflowID, nodePath), &marotte.EntrySteer{
		Text:       stepRefusalNoteText(cmp.Or(l.Recipe, "Workflow run"), nodePath, c.Refusal),
		Origin:     marotte.SteerOriginAgent,
		State:      marotte.SteerStateRead,
		Severity:   "warning",
		OriginRun:  workflowID,
		ProducedTs: producedTs,
	})
}

// refusedClose answers a close's turn_close and whether it graded refused; an undecodable payload is not a refusal.
func refusedClose(sealed []turnlog.Sealed) (marotte.EntryTurnClose, bool) {
	for _, s := range sealed {
		if s.Entry == nil || s.Entry.Kind != marotte.EntryKindTurnClose {
			continue
		}
		var c marotte.EntryTurnClose
		if json.Unmarshal(s.Entry.Payload, &c) != nil {
			return marotte.EntryTurnClose{}, false
		}
		return c, c.Outcome == marotte.TurnOutcomeRefused
	}
	return marotte.EntryTurnClose{}, false
}

// stepRefusalNoteText names the step and does not invite a re-run: a refusal is deterministic.
// Category and explanation are optional. The total length is unbounded, accepted as in noteRunEnd.
func stepRefusalNoteText(recipe, nodePath string, r *marotte.RefusalInfo) string {
	var b strings.Builder
	b.WriteString(recipe)
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
func (rs *Runs) RunFoldTarget(ctx context.Context, runID, nodePath, sessionID string, chatID marotte.ChatID) (*turnlog.Turn, bool) {
	if rs.log == nil {
		return nil, false
	}
	if t := rs.log.Turn(runID, nodePath); t != nil {
		return t, true
	}
	if rs.log.hasClosed(runID, nodePath) {
		return nil, false
	}
	t, opened, err := rs.log.Open(ctx, runID, nodePath, sessionID, chatID)
	if err != nil {
		slog.Warn("run log: open step turn for a content frame", "run", runID, "node_path", nodePath, "error", err)
		return nil, false
	}
	if opened != nil {
		translate.PublishAppended(ctx, rs.bus, "", runID, opened)
	}
	return t, true
}

// RunAppendAfterClosed files an entry after the path's newest closed turn and announces it; no closed turn is an error.
func (rs *Runs) RunAppendAfterClosed(ctx context.Context, runID, nodePath string, e *marotte.Entry) error {
	if rs.log == nil {
		return errRunLogRemoved
	}
	filed, err := rs.log.AppendAfterClosed(ctx, runID, nodePath, e)
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
	return rs.log != nil && rs.log.Meter(runID, nodePath, credits, elapsedMs)
}

// RunStopReason records a step's last turn_end stop reason on its open turn.
func (rs *Runs) RunStopReason(runID, nodePath string, raw marotte.StopReason) bool {
	return rs.log != nil && rs.log.StopReason(runID, nodePath, raw)
}

// closeRun closes every open turn of the run with the outcome KAS's status maps to and announces the sealed entries.
func (rs *Runs) closeRun(ctx context.Context, runID, status string) {
	if rs.log == nil {
		return
	}
	sealed, err := rs.log.CloseRun(ctx, runID, runOutcome(status), true)
	if err != nil {
		slog.Warn("run log: close run", "run", runID, "error", err)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
}

// deleteRunLog is Delete's second step: open turns close cancelled and maps drop, so the death
// closer finds nothing to close interrupted. RemoveDir is last.
func (rs *Runs) deleteRunLog(ctx context.Context, runID string) {
	if rs.log == nil {
		return
	}
	sealed, err := rs.log.Delete(ctx, runID)
	if err != nil {
		slog.Warn("run log: delete run", "run", runID, "error", err)
	}
	translate.PublishSealed(ctx, rs.bus, "", runID, sealed)
}

// hostsLiveRun reports whether the chat's bridge hosts an open step turn (the retire busy rule).
func (rs *Runs) hostsLiveRun(chatID marotte.ChatID) bool {
	return rs.log != nil && rs.log.hostsOpen(chatID)
}

// openSeq answers an open step turn's newest sealed seq, for the digest's run_turn arm.
func (rs *Runs) openSeq(runID, turn string) (uint64, bool) {
	if rs.log == nil {
		return 0, false
	}
	seq, open := rs.log.OpenSeqs(runID)[turn]
	return seq, open
}
