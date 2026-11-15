package agent

import "context"

// ListedConversation reports whether KAS lists sessionID among this workspace's sessions that are not a workflow
// step's: a session read, which unlike a run read reconciles no run. False covers a step's session and one KAS
// does not list.
func (rt *Runtime) ListedConversation(ctx context.Context, sessionID string) (bool, error) {
	rows, err := rt.workspaceSessionRows(ctx)
	if err != nil {
		return false, err
	}
	for i := range rows {
		if rows[i].SessionID == sessionID {
			// toResumable's guard: a step row KAS let through is still a step's.
			return len(rows[i].Meta.Kiro.Workflow) == 0, nil
		}
	}
	return false, nil
}
