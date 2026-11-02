package agent

import (
	"context"
	"errors"
	"fmt"
)

// maxExtendIterations caps one extend request; KAS caps a repeat's total at 1000 and refuses beyond.
const maxExtendIterations = 1000

// extendRepeat gives a repeat paused at its cap n more iterations and re-drives the run for caller, a person's
// resume to runTransitions; KAS validates nodeID.
func (rs *Runs) extendRepeat(ctx context.Context, workflowID, nodeID string, n int, caller controlCaller) (err error) {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	if n < 1 || n > maxExtendIterations {
		return fmt.Errorf("iterations must be between 1 and %d", maxExtendIterations)
	}
	_, err = rs.transition(ctx, workflowID, &runRequest{
		want: personResume, verb: verbExtend, caller: caller,
		resume: map[string]any{"extendRepeat": map[string]any{"nodeId": nodeID, "additionalIterations": n}},
	})
	return err
}

// finishRepeat ends a repeat paused at its cap and lets the run continue, a person's finish runTransitions decides;
// KAS validates nodeID.
func (rs *Runs) finishRepeat(ctx context.Context, workflowID, nodeID string) error {
	if nodeID == "" {
		return errors.New("missing node id")
	}
	_, err := rs.transition(ctx, workflowID, &runRequest{want: personFinish, verb: verbFinishLoop, node: nodeID})
	return err
}
