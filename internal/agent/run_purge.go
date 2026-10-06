package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// RunPurgeResult reports one parentless-run purge pass; NextDeadline is when the earliest age-kept run becomes purgeable, zero for none.
type RunPurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// PurgeParentlessRuns deletes finished runs no chat claims once older than window (from last update); zero
// deletes them all. Shown, live or paused runs and unreadable ages are kept; a chat-launched run goes with its chat.
func (rt *Runtime) PurgeParentlessRuns(ctx context.Context, window time.Duration) RunPurgeResult {
	if rt.sessionRefs == nil {
		return RunPurgeResult{}
	}
	runs, err := rt.runs.listRaw(ctx)
	if err != nil {
		slog.Warn("run purge: run list unavailable, purging nothing this pass", "error", err)
		return RunPurgeResult{}
	}
	// After the run list, so a chat created between the reads still claims its run.
	claimed, complete := rt.sessionRefs(ctx)
	if !complete {
		slog.Warn("run purge: a chat file could not be read, purging nothing this pass")
		return RunPurgeResult{}
	}
	now := time.Now()
	var res RunPurgeResult
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" || !marotte.RunStatus(r.Status).Terminal() {
			continue
		}
		// A candidate filter only: admission re-reads under the membership lock.
		if _, owned := claimed[r.ParentSessionID]; owned {
			continue
		}
		rt.purgeCandidate(ctx, r, window, now, &res)
	}
	if res.Purged > 0 || res.Errors > 0 {
		slog.Info("run purge: pass complete",
			"purged", res.Purged, "kept", res.Kept, "errors", res.Errors, "window", window)
	}
	return res
}

// purgeCandidate deletes one due, apparently unclaimed finished run, recording the outcome.
func (rt *Runtime) purgeCandidate(ctx context.Context, r *kasWorkflowRun, window time.Duration, now time.Time, res *RunPurgeResult) {
	updated := parseKASTime(r.UpdatedAt)
	if updated <= 0 {
		res.Kept++
		return
	}
	if due := time.UnixMilli(updated).Add(window); due.After(now) {
		res.Kept++
		if res.NextDeadline.IsZero() || due.Before(res.NextDeadline) {
			res.NextDeadline = due
		}
		return
	}
	done, admitted := rt.admitRunPurge(ctx, r.WorkflowID, r.ParentSessionID)
	if !admitted {
		res.Kept++
		return
	}
	err := rt.runs.deleteOn(ctx, r.WorkflowID, runStop{}, nil)
	done()
	if err != nil {
		res.Errors++
		slog.Warn("run purge: delete failed", "workflow_id", r.WorkflowID, "error", err)
		return
	}
	res.Purged++
}

func (rt *Runtime) admitRunPurge(ctx context.Context, workflowID, parentSessionID string) (done func(), ok bool) {
	if rt.membership == nil {
		return func() {}, true
	}
	return rt.membership.AdmitRunPurge(ctx, workflowID, parentSessionID)
}
