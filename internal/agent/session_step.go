package agent

import (
	"context"
	"encoding/json"
)

// StepSessionOf reports whether a KAS session is a workflow step's, and whose, from the workspace's
// session list: a session read, which unlike a run read reconciles no run. The step row is the one
// carrying `_meta.kiro.workflow`.
func (rt *Runtime) StepSessionOf(ctx context.Context, sessionID string) (workflowID string, found bool, err error) {
	rows, err := rt.workspaceSessionRows(ctx)
	if err != nil {
		return "", false, err
	}
	for i := range rows {
		if rows[i].SessionID != sessionID || len(rows[i].Meta.Kiro.Workflow) == 0 {
			continue
		}
		var wf struct {
			WorkflowID string `json:"workflowId"`
		}
		// The row's presence is the fact; an unreadable id still names a step.
		_ = json.Unmarshal(rows[i].Meta.Kiro.Workflow, &wf)
		return wf.WorkflowID, true, nil
	}
	return "", false, nil
}
