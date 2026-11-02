package agent

// One workflow step's transcript from KAS's session log via `session/load`. Nothing is stored; the step is
// addressed by path (repeat iterations share a node id); the load is an engine call under its own budget.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepTranscriptBudget bounds one step read, RPC and barrier: a bridge Call has no client timeout. A var for tests.
var stepTranscriptBudget = 60 * time.Second

// Every other outcome is a 200 with a verdict.
var errStepUnknown = errors.New("this run has no step at that path")

// errRunStateUndecodable means the inspect reply would not decode: `unavailable`, not a 404 blaming the caller.
var errRunStateUndecodable = errors.New("this run's state could not be decoded")

// stepTranscript reads one step's transcript from the run's log when it holds a turn for the path (`log`, with
// open tails and stamps), else KAS's replay (`replay`). `ready`, `gone` (session gone or never started) or
// `unavailable` (worth retrying). nodePath is a workflow.PathKey, looked up among workflowID's own steps only,
// so another run's step, like a missing key, is errStepUnknown. The newest turn_close reads graded under KAS's
// record of the step (stepClose.graded), as `step_ends` does.
func (rs *Runs) stepTranscript(ctx context.Context, workflowID, nodePath string) (marotte.RunStepTranscript, error) {
	out := marotte.RunStepTranscript{
		Entries:     []marotte.Entry{},
		OpenEntries: []marotte.OpenEntry{},
		Subject:     []*marotte.SubjectStamp{},
		WorkflowID:  workflowID,
		NodePath:    nodePath,
		State:       marotte.RunStepTranscriptUnavailable,
		Source:      marotte.RunStepTranscriptSourceReplay,
	}
	if rs.log != nil && rs.fromRunLog(ctx, &out) {
		gradeNewestClose(out.Entries, func() (workflow.StepRecord, bool) {
			return rs.kasStep(ctx, workflowID, nodePath)
		})
		return out, nil
	}
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		// A missing workflow engine is the run endpoint's to report.
		slog.Warn("step transcript: run state unreadable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err, "detail", rpcerr.Details(err))
		return out, nil
	}
	// A KAS refusal errors above, and an empty reply fails to decode below.
	sessionID, err := stepSessionAt(raw, nodePath)
	if errors.Is(err, errRunStateUndecodable) {
		// Not a 404: the path may be real in bytes this build cannot read.
		slog.Warn("step transcript: run state undecodable", "workflow_id", workflowID,
			"node_path", nodePath, "error", err)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if sessionID == "" {
		// A step that never ran has no session.
		out.State = marotte.RunStepTranscriptGone
		return out, nil
	}

	turns, state := rs.replayStepSession(ctx, sessionID)
	// A terminal run's step session is closed once its replay settled; a live run's never is, since close quiesces
	// the session and would abort its step.
	if workflowTerminal(workflowID, raw) {
		rs.closeStepSession(sessionID)
	}
	out.State = state
	for i := range turns {
		out.Entries = append(out.Entries, turns[i].Entries...)
	}
	gradeNewestClose(out.Entries, func() (workflow.StepRecord, bool) { return kasStepIn(workflowID, raw, nodePath) })
	return out, nil
}

// stepSessionAt resolves the step path's ACP session off one inspect reply: errStepUnknown for no such path,
// "" for a step not yet run (hence workflow.Steps).
func stepSessionAt(raw json.RawMessage, nodePath string) (string, error) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil {
		return "", errRunStateUndecodable
	}
	for _, st := range workflow.Steps(res.State) {
		if workflow.PathKey(st.Path) == nodePath {
			return st.SessionID, nil
		}
	}
	return "", errStepUnknown
}

// replayStepSession loads a step session with an engine-level `session/load`, not as a session with its own lane:
// its replay frames reach the engine lane's replay reader, its live frames stay with the run's owner.
func (rs *Runs) replayStepSession(ctx context.Context, sessionID string) ([]translate.ProjectedTurn, marotte.RunStepTranscriptState) {
	if !rs.stepReplays.open(sessionID, translate.NewEntryProjection(newMessageID, rs.workDir)) {
		// Refused rather than joined; a retry meets a settled registry.
		slog.Debug("step transcript: a read of this session is already in flight", "session_id", sessionID)
		return nil, marotte.RunStepTranscriptUnavailable
	}
	// Taken on every path, expiry included.
	defer func() { _ = rs.stepReplays.take(sessionID) }()

	cctx, cancel := context.WithTimeout(ctx, stepTranscriptBudget)
	defer cancel()

	// A `{"result":null}` reply would otherwise wait out the budget.
	raw, at, err := rs.engine.callRawAt(cctx, "step transcript load", marotte.MethodSessionLoad,
		map[string]any{marotte.KeySessionID: sessionID})
	if err != nil || len(raw) == 0 {
		// KAS's reason is machine prose: logged, not forwarded.
		slog.Warn("step transcript: session load failed", "session_id", sessionID,
			"error", err, "detail", rpcerr.Details(err))
		// Unavailable, never gone: KAS answers an unknown id and a transient fault alike (-32603).
		return nil, marotte.RunStepTranscriptUnavailable
	}
	// callRawAt gives the response's position, which the barrier measures against; recording it attempts one settle.
	rs.stepReplays.markLoadedAt(sessionID, at)

	// The barrier: replay frames precede the result and the channel is buffered (step_replay.go).
	select {
	case <-rs.stepReplays.barrier(sessionID):
	case <-cctx.Done():
		slog.Warn("step transcript: the replay did not settle inside the budget",
			"session_id", sessionID, "budget", stepTranscriptBudget)
		return nil, marotte.RunStepTranscriptUnavailable
	}

	// The turn_open's prompt is the step's instruction; the pane reads its node_path and n.
	return rs.stepReplays.take(sessionID), marotte.RunStepTranscriptReady
}

// turnRange reads one step turn's tail from the run's log for a `run_turn` repair. No replay fallback (a replay
// has no turn ids), so found-or-not; false for a run with no log.
func (rs *Runs) turnRange(ctx context.Context, workflowID, turn string, from uint64) (entries []marotte.Entry, open []marotte.OpenEntry, stamps []*marotte.SubjectStamp, found bool, err error) {
	if rs.log == nil {
		return nil, nil, nil, false, nil
	}
	return rs.log.turnRange(ctx, workflowID, turn, from)
}

func (rs *Runs) fromRunLog(ctx context.Context, out *marotte.RunStepTranscript) bool {
	entries, open, stamps, found, err := rs.log.stepTurns(ctx, out.WorkflowID, out.NodePath)
	if err != nil {
		slog.Warn("step transcript: run log unreadable", "workflow_id", out.WorkflowID,
			"node_path", out.NodePath, "error", err)
		return true
	}
	if !found {
		return false
	}
	out.State, out.Source = marotte.RunStepTranscriptReady, marotte.RunStepTranscriptSourceLog
	out.Entries = entries
	if open != nil {
		out.OpenEntries = open
	}
	if stamps != nil {
		out.Subject = stamps
	}
	return true
}

// workflowTerminal reports whether an inspect reply shows the run ended.
func workflowTerminal(workflowID string, raw json.RawMessage) bool {
	in, err := workflow.DecodeInspect(workflowID, raw)
	return err == nil && marotte.RunStatus(in.State.Status).Terminal()
}

// closeStepSession disposes a loaded step session of an ended run in KAS's memory, off the reader's path; the next
// pane open loads it again. A failed close leaves it resident until the engine restarts.
func (rs *Runs) closeStepSession(sessionID string) {
	rs.lifecycle.goUnlessDraining(func() {
		ctx, cancel := context.WithTimeout(rs.lifecycle.shutdownCtx, stepCloseBudget)
		defer cancel()
		if _, err := rs.engine.rawCall(ctx, "step session close", marotte.MethodSessionClose,
			map[string]any{marotte.KeySessionID: sessionID}); err != nil {
			slog.Warn("a loaded step session of an ended run did not close; it stays until the engine restarts",
				"session_id", sessionID, "error", err)
		}
	})
}

// stepCloseBudget bounds one step session's close.
const stepCloseBudget = 30 * time.Second
