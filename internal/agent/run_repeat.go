package agent

import (
	"context"
	"errors"
	"fmt"
)

// maxExtendIterations caps one extend request; KAS caps a repeat's total at 1000 and refuses beyond.
const maxExtendIterations = 1000

// extendRepeat gives a repeat paused at its cap n more iterations and re-drives the run; KAS validates nodeID.
func (rs *Runs) extendRepeat(ctx context.Context, workflowID, nodeID string, n int) (err error) {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if n < 1 || n > maxExtendIterations {
		return fmt.Errorf("iterations must be between 1 and %d", maxExtendIterations)
	}
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	defer func() { host.release(err) }()
	if _, err = rs.callReclaiming(ctx, host.sb.bridge, workflowID, methodKiroWorkflowResume, map[string]any{
		keyWorkflowID: workflowID,
		"extendRepeat": map[string]any{
			"nodeId":               nodeID,
			"additionalIterations": n,
		},
	}); err != nil {
		return err
	}
	rs.armDeadline(ctx, workflowID)
	return nil
}

// finishRepeat ends a repeat paused at its cap and lets the run continue. Separate from SetStepStatus:
// KAS refuses `nodeId` on an ordinary step write.
func (rs *Runs) finishRepeat(ctx context.Context, workflowID, nodeID string) (err error) {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	host, err := rs.hostRun(ctx, workflowID)
	if err != nil {
		return err
	}
	// A decline releases with a cause too: no run_complete follows.
	defer func() { host.release(err) }()
	resp, err := rs.callReclaiming(ctx, host.sb.bridge, workflowID, methodKiroWorkflowUpdate, map[string]any{
		keyWorkflowID: workflowID,
		"action":      updateStatusAction,
		"status":      runStepCompleted,
		"nodeId":      nodeID,
	})
	if err != nil {
		return err
	}
	if refusal := stepStatusRefusal(resp); refusal != "" {
		err = fmt.Errorf("%w: %s", errStepStatusRefused, refusal)
		return err
	}
	rs.armDeadline(ctx, workflowID)
	return nil
}
