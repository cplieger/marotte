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

// stepRefusalNoteID is the note's steer id, runEndNoteID's notify- shape so the two
// run-origin notes read alike wherever a human or a grep meets them. The prefix decides
// nothing: this entry sets Origin explicitly and recordSteer writes it verbatim, reaching
// neither translate.steerOrigin (whose only prefix arm is `steer-`) nor stampRunNotice
// (which keys on the hyphenated `notify-wf-`). Keyed on the NODE PATH rather than on an
// instant: a path is one step instance (a repeat's iterations carry their own iter-N
// segment), so the id is stable for that step and distinct from every other step of the
// run.
func stepRefusalNoteID(workflowID, nodePath string) string {
	return "notify-" + workflowID + ":refused:" + nodePath
}

// noteStepRefused leaves the launching chat a row saying a step was DECLINED, read off
// the close's own turn_close.
//
// recordSteer appends to this server's own log and never calls _session/steer, so the
// launching AGENT is not told; the human reading the conversation is who this is for.
//
// Reached from CloseNode's close alone. closeAllLocked carries no refusal override, so a
// refused step closed by closeRun or deleteRunLog neither grades refused nor arrives here.
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

// refusedClose answers a close's turn_close payload and whether it graded the step
// refused. A payload that will not decode is not a refusal: the note would have nothing
// true to say, and the close itself has already been announced.
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

// stepRefusalNoteText names the step and refuses to invite a re-run: a refusal is
// deterministic, so the same step against the same model declines again. Category and
// explanation are each optional on the wire, so the sentence stands without either.
//
// Explanation arrives displayText-ed from translate.refusalFrom (single-line, under 512
// bytes); recipe, node path and category are raw and unbounded, which is noteRunEnd's
// posture for the recipe too. So the composed text carries no total bound, accepted.
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

// RunFoldTarget answers the step path's open turn, opening one when the path has
// none and no closed turn to file a late entry after.
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

// RunAppendAfterClosed files an entry after the path's newest closed turn and
// announces it; a path with no closed turn is an error the caller logs.
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

// closeRun closes every open turn of the run with the terminal outcome KAS's
// status maps to and announces the sealed entries: the run_complete closer.
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

// deleteRunLog is Delete's second step: the run's open turns close cancelled and
// its maps drop, so the death closer that follows finds no hosted turn to close
// interrupted. RemoveDir is the caller's last step.
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

// hostsLiveRun reports whether the chat's bridge hosts an open step turn: the
// retire door's busy rule.
func (rs *Runs) hostsLiveRun(chatID marotte.ChatID) bool {
	return rs.log != nil && rs.log.hostsOpen(chatID)
}

// openSeq answers an open step turn's newest sealed seq: the digest's run_turn arm.
func (rs *Runs) openSeq(runID, turn string) (uint64, bool) {
	if rs.log == nil {
		return 0, false
	}
	seq, open := rs.log.OpenSeqs(runID)[turn]
	return seq, open
}
