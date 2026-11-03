package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// RunPurgeResult reports one parentless-run purge pass; NextDeadline is when the earliest age-kept run becomes purgeable, zero for none.
type RunPurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// ReattributeRuns is Runs.ReattributeRuns, a retention side pass.
func (rt *Runtime) ReattributeRuns(ctx context.Context) { rt.runs.ReattributeRuns(ctx) }

// PurgeParentlessRuns deletes finished runs no chat claims once older than window (from last update); zero
// deletes them all. Shown, live or paused runs, unreadable ages and runs naming no valid launching session are
// kept, and nothing is deleted while the tab arrangement is unread; a chat-launched run goes with its chat.
func (rt *Runtime) PurgeParentlessRuns(ctx context.Context, window time.Duration) RunPurgeResult {
	// The purge scheduler outlives Shutdown; once draining, the next boot's pass purges.
	release, ok := rt.lifecycle.enterUnlessDraining()
	if !ok {
		return RunPurgeResult{}
	}
	defer release()
	runs, err := rt.runs.listRaw(ctx)
	if err != nil {
		slog.Warn("run purge: run list unavailable, purging nothing this pass", "error", err)
		return RunPurgeResult{}
	}
	// After the run list, so a chat created between the reads still claims its run.
	scan := rt.chatStore.SessionOwners(ctx)
	if !scan.Complete {
		slog.Warn("run purge: a chat file could not be read, purging nothing this pass")
		return RunPurgeResult{}
	}
	now := time.Now()
	var res RunPurgeResult
	var unparented []string
	unreadAside := ""
	for i := range runs {
		r := &runs[i]
		switch {
		case r.WorkflowID == "" || !marotte.RunStatus(r.Status).Terminal():
			continue
		case !ids.ValidSessionID(r.ParentSessionID):
			// A run whose launcher is unknown is not a run no chat owns.
			unparented = append(unparented, r.WorkflowID)
			res.Kept++
			continue
		case len(scan.Owners.Of(r.ParentSessionID)) > 0:
			// A candidate filter only: admission re-reads under the membership lock.
			continue
		}
		var unread *command.ArrangementUnreadError
		if refused := rt.purgeCandidate(ctx, r, window, now, &res); errors.As(refused, &unread) {
			unreadAside = unread.Aside
		}
	}
	if len(unparented) > 0 {
		slog.Warn("run purge: finished runs name no valid launching session, keeping them", "workflow_ids", unparented)
	}
	if unreadAside != "" {
		slog.Warn("run purge: the tab arrangement was unreadable, deleting no run", "aside", unreadAside)
	}
	if res.Purged > 0 || res.Errors > 0 {
		slog.Info("run purge: pass complete",
			"purged", res.Purged, "kept", res.Kept, "errors", res.Errors, "window", window)
	}
	return res
}

// purgeCandidate deletes one unclaimed finished run once past window, answering admission's refusal, if any.
func (rt *Runtime) purgeCandidate(ctx context.Context, r *kasWorkflowRun, window time.Duration, now time.Time, res *RunPurgeResult) (refused error) {
	updated := parseKASTime(r.UpdatedAt)
	if updated <= 0 {
		res.Kept++
		return nil
	}
	if due := time.UnixMilli(updated).Add(window); due.After(now) {
		res.Kept++
		if res.NextDeadline.IsZero() || due.Before(res.NextDeadline) {
			res.NextDeadline = due
		}
		return nil
	}
	done, refused := rt.admitRunPurge(ctx, r.WorkflowID, r.ParentSessionID)
	if refused != nil {
		res.Kept++
		return refused
	}
	err := rt.runs.deleteAs(ctx, r.WorkflowID, runStop{})
	done()
	if err != nil {
		res.Errors++
		slog.Warn("run purge: delete failed", "workflow_id", r.WorkflowID, "error", err)
		return nil
	}
	res.Purged++
	return nil
}

func (rt *Runtime) admitRunPurge(ctx context.Context, workflowID, parentSessionID string) (done func(), refused error) {
	if rt.membership == nil {
		return func() {}, nil
	}
	return rt.membership.AdmitRunPurge(ctx, workflowID, parentSessionID)
}
