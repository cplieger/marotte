package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// verbEndStep names a person's end of a paused step in its refusals.
const verbEndStep = "end step"

// The refusals of an end about the step itself.
var (
	errStepNotPaused = command.StatusErrorReason(http.StatusConflict, string(marotte.RunStepEndNotPaused),
		errors.New("this step is not paused, so it was not ended"))
	errStepEndUnnamed = command.StatusErrorReason(http.StatusConflict, string(marotte.RunStepEndUnnamed),
		errors.New("another paused execution of this step has the same id, so this one cannot be ended on its own"))
)

// endStep ends the paused step at nodePath, one workflow.PathKey, with status, a person's end runTransitions decides.
func (rs *Runs) endStep(ctx context.Context, workflowID, nodePath, status string) error {
	switch {
	case nodePath == "":
		return errors.New("missing step path")
	case status != runStepCompleted && status != runStepFailed:
		return fmt.Errorf("status must be %q or %q", runStepCompleted, runStepFailed)
	}
	_, err := rs.transition(ctx, workflowID, &runRequest{want: personEnd, verb: verbEndStep, path: nodePath, status: status})
	return err
}

// endStepAsked is transition's end of the step at req.path, decided on r's tree and sent behind marotte's own sends to
// the run, as a pause is.
func (rs *Runs) endStepAsked(ctx context.Context, workflowID string, req *runRequest, r *pauseRead) error {
	nodeID, err := endableStep(r.tree, req.path)
	if err != nil {
		return err
	}
	host, err := rs.ensureResident(ctx, workflowID)
	if err != nil {
		return err
	}
	return rs.controlSerialized(ctx, workflowID, func(cctx context.Context) error {
		return rs.updateNodeStatus(cctx, host, workflowID, nodeID, req.status)
	})
}

// endableStep is the node id of the paused step at nodePath in tree. KAS ends the first paused step its node id names
// in pre-order (applyPausedStepFinish), so an id another paused execution shares is refused: the end could land on it.
func endableStep(tree workflow.Inspect, nodePath string) (string, error) {
	if !tree.OK {
		return "", errRunStateUnread
	}
	steps := workflow.Steps(tree.State)
	at := false
	nodeID := ""
	for _, st := range steps {
		if workflow.PathKey(st.Path) != nodePath {
			continue
		}
		at = true
		if marotte.RunNodeStatus(st.Status) == marotte.RunNodeStatusPaused {
			nodeID = st.NodeID
		}
	}
	switch {
	case !at:
		return "", errStepUnknown
	case nodeID == "":
		return "", errStepNotPaused
	}
	paused := 0
	for _, st := range steps {
		if st.NodeID == nodeID && marotte.RunNodeStatus(st.Status) == marotte.RunNodeStatusPaused {
			paused++
		}
	}
	if paused > 1 {
		return "", errStepEndUnnamed
	}
	return nodeID, nil
}
