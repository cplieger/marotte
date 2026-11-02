package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cplieger/marotte/internal/marotte"
)

// errRunNotNamed: an inspect reply that does not name the run it was asked about.
var errRunNotNamed = errors.New("the inspect reply does not name the run")

// RunRow is one run as the keep-state reconcile reads it from KAS.
type RunRow struct {
	WorkflowID      string
	ParentSessionID string
	Terminal        bool
}

// RunRows lists KAS's runs for this workspace with their launching session and whether each is terminal.
func (rs *Runs) RunRows(ctx context.Context) ([]RunRow, error) {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RunRow, 0, len(runs))
	for i := range runs {
		if runs[i].WorkflowID == "" {
			continue
		}
		out = append(out, RunRow{
			WorkflowID:      runs[i].WorkflowID,
			ParentSessionID: runs[i].ParentSessionID,
			Terminal:        marotte.RunStatus(runs[i].Status).Terminal(),
		})
	}
	return out, nil
}

// InspectRunRow reads one run by inspect. "Workflow not found", a failed call and a reply that does not name the
// run are all errors: none of them shows the run's status or its launcher.
func (rs *Runs) InspectRunRow(ctx context.Context, workflowID string) (RunRow, error) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return RunRow{}, err
	}
	var res inspectRunState
	if err := json.Unmarshal(raw, &res); err != nil {
		return RunRow{}, err
	}
	if !res.names(workflowID) {
		return RunRow{}, errRunNotNamed
	}
	return RunRow{WorkflowID: workflowID, ParentSessionID: res.State.ParentSessionID, Terminal: res.State.Status.Terminal()}, nil
}

// EndedThisBoot reports whether an evidence end of the run was seen in this process and no retry began a new life
// since. The reconcile re-checks it under a record lock before adding the run's fact.
func (rs *Runs) EndedThisBoot(workflowID string) bool { return rs.lives.endedThisBoot(workflowID) }

// SetRecordedRuns records the live runs the keep-state reconcile found only under runs/, workflow id to launching
// session, and marks runs/ read for RunsParentedBy.
func (rs *Runs) SetRecordedRuns(parents map[string]string) { rs.recorded.set(parents) }

// RecordLeaseHolders adds holders to a held lease's KeepChats, a union; a lease released meanwhile is left alone. The
// unattributed mark stays for ReattributeRuns, which holds the facts on every chat before it clears the mark.
func (rs *Runs) RecordLeaseHolders(ctx context.Context, workflowID string, holders []marotte.ChatID) {
	unlock := rs.lives.lock(workflowID)
	defer unlock()
	if l, held := rs.lease(workflowID); held {
		rs.recordKeepChats(ctx, &l, holders)
	}
}
