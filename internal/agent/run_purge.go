package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// RunPurgeResult reports one parentless-run purge pass; NextDeadline is when the earliest keep that lapses by itself
// (a run's age, a retry in flight) lapses, zero for none.
type RunPurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// ReattributeRuns is Runs.reattributeRuns, a retention side pass.
func (rt *Runtime) ReattributeRuns(ctx context.Context) { rt.runs.reattributeRuns(ctx) }

// PurgeParentlessRuns deletes finished runs no chat claims once older than window (from last update) with no retry
// in flight, judged on the list and again on a fresh read before each delete; zero window deletes them all. Shown,
// live or paused runs, unreadable ages and runs naming no valid launching session are kept, and nothing is deleted
// while the tab arrangement is unread; a chat-launched run goes with its chat.
func (rt *Runtime) PurgeParentlessRuns(ctx context.Context, window time.Duration) RunPurgeResult {
	// The purge scheduler outlives Shutdown; once draining, the next boot's pass purges.
	release, ok := rt.lifecycle.enterUnlessDraining()
	if !ok {
		return RunPurgeResult{}
	}
	defer release()
	rt.runs.retries.sweep(time.Now())
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

// purgeCandidate deletes one unclaimed finished run that purgeKeep finds purgeable on its list row and again under the
// delete's hold, answering admission's refusal, if any.
func (rt *Runtime) purgeCandidate(ctx context.Context, r *kasWorkflowRun, window time.Duration, now time.Time, res *RunPurgeResult) (refused error) {
	listed := purgeRead{status: marotte.RunStatus(r.Status), updated: parseKASTime(r.UpdatedAt)}
	if why, until := rt.runs.purgeKeep(r.WorkflowID, listed, window, now); why != "" {
		res.keep(until)
		return nil
	}
	done, refused := rt.admitRunPurge(ctx, r.WorkflowID, r.ParentSessionID)
	if refused != nil {
		res.Kept++
		return refused
	}
	deleted, until, err := rt.runs.deleteIfPurgeable(ctx, r.WorkflowID, window)
	done()
	switch {
	case err != nil:
		res.Errors++
		slog.Warn("run purge: delete failed", "workflow_id", r.WorkflowID, "error", err)
	case deleted:
		res.Purged++
	default:
		res.keep(until)
	}
	return nil
}

// keep counts a kept run and folds the instant its keep lapses by itself, zero for never, into NextDeadline.
func (res *RunPurgeResult) keep(until time.Time) {
	res.Kept++
	if !until.IsZero() && (res.NextDeadline.IsZero() || until.Before(res.NextDeadline)) {
		res.NextDeadline = until
	}
}

// purgeRead is one read of a run, from KAS's list or its inspect, as purgeKeep judges it.
type purgeRead struct {
	status marotte.RunStatus
	// updated is the run's last activity in epoch millis (the list's updatedAt, workflow.State.UpdatedAt), 0 unreadable.
	updated int64
}

// purgeKeep is the purge's one eligibility rule: a run is purgeable once finished, older than window by its own last
// activity, and with no retry in flight. It answers why the run is kept, "" for purgeable, and when that reason lapses
// by itself, zero for never.
func (rs *Runs) purgeKeep(workflowID string, r purgeRead, window time.Duration, now time.Time) (why string, until time.Time) {
	switch {
	case !r.status.Terminal():
		return "it is not finished", time.Time{}
	case r.updated <= 0:
		return "its last activity is unreadable", time.Time{}
	}
	last := time.UnixMilli(r.updated)
	if due := last.Add(window); due.After(now) {
		return "it was active within the retention window", due
	}
	if until, ok := rs.retries.inFlight(workflowID, last, now); ok {
		return "a retry marotte sent has not shown taking effect", until
	}
	return "", time.Time{}
}

// deleteIfPurgeable deletes a run its list row showed purgeable only when, under the delete's hold that marotte's
// retry is admitted through, purgeKeep finds it purgeable again on a fresh inspect; until is when a keep lapses. A run
// KAS no longer finds is torn down here, since no later list names it; any other answer keeps the run, logged with
// why. Another KAS client's retry does not pass the hold.
func (rs *Runs) deleteIfPurgeable(ctx context.Context, workflowID string, window time.Duration) (deleted bool, until time.Time, err error) {
	d, err := rs.life.beginDelete(ctx, workflowID)
	if err != nil {
		return false, time.Time{}, err
	}
	read, err := rs.inspectPurgeRead(ctx, workflowID)
	switch {
	case workflowNotFound(err):
		slog.Info("run purge: KAS no longer knows a run it listed finished, dropping marotte's record of it",
			"workflow_id", workflowID)
		rs.teardownDeleted(ctx, workflowID)
		d.settle()
		return true, time.Time{}, nil
	case err != nil:
		slog.Warn("run purge: could not re-read a finished run before deleting it, keeping it",
			"workflow_id", workflowID, "error", err)
		d.settle()
		return false, time.Time{}, nil
	}
	if read.updated > 0 {
		rs.retries.dropOvertaken(workflowID, time.UnixMilli(read.updated))
	}
	why, until := rs.purgeKeep(workflowID, read, window, time.Now())
	if why == "" {
		err = rs.deleteHeld(ctx, d, runStop{})
		if workflowNotFound(err) {
			return true, time.Time{}, nil
		}
		return err == nil, time.Time{}, err
	}
	slog.Info("run purge: a fresh read no longer finds a listed run purgeable, keeping it",
		"workflow_id", workflowID, "reason", why)
	d.settle()
	return false, until, nil
}

// inspectPurgeRead is a run's status and last activity as KAS's inspect reports them, refusing a reply with no state
// or about another run.
func (rs *Runs) inspectPurgeRead(ctx context.Context, workflowID string) (purgeRead, error) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return purgeRead{}, err
	}
	in, err := workflow.DecodeInspect(workflowID, raw)
	if err != nil {
		return purgeRead{}, err
	}
	return purgeRead{status: marotte.RunStatus(in.State.Status), updated: parseKASTime(in.State.UpdatedAt())}, nil
}

// retryInFlightBound is how long a retry marotte sent keeps its run from the purge while KAS shows no sign of it.
// KAS loads the run from disk before it marks it running, under no deadline marotte knows, so the bound sits far past
// retryTimeout: holding a finished run's delete that long costs at most one purge pass.
const retryInFlightBound = 10 * time.Minute

// retryMarks holds, per run, when marotte last sent it a retry KAS has not shown taking effect: the purge's "retry in
// flight". Only the purge reads it, so quiescence and the interrupted snapshot never see it. A mark is dropped only by
// the facts purgeKeep reads (dropOvertaken, the bound) or by the run ceasing to exist (clear): KAS emits a retry's
// run_start before it records the start, so a frame proves nothing, and a mark is per run, so a refused attempt says
// nothing of an earlier one KAS may still be running (its just-claimed refusal means one is). The zero value is ready.
type retryMarks struct {
	at map[string]time.Time
	mu sync.Mutex
}

// mark records a retry of workflowID about to be sent at now, to KAS's millisecond, so an end KAS stamps in the same
// millisecond reads as after it.
func (m *retryMarks) mark(workflowID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at == nil {
		m.at = map[string]time.Time{}
	}
	m.at[workflowID] = time.UnixMilli(now.UnixMilli())
}

// clear drops workflowID's mark: the run is gone.
func (m *retryMarks) clear(workflowID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.at, workflowID)
}

// dropOvertaken drops workflowID's mark once a fresh inspect shows the run's last activity at or after it.
func (m *retryMarks) dropOvertaken(workflowID string, last time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at, ok := m.at[workflowID]; ok && !last.Before(at) {
		delete(m.at, workflowID)
	}
}

// sweep drops every mark retryInFlightBound has passed at now, so a run the purge never judges keeps none.
func (m *retryMarks) sweep(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for wf, at := range m.at {
		if !now.Before(at.Add(retryInFlightBound)) {
			delete(m.at, wf)
		}
	}
}

// inFlight reports workflowID's mark while it is younger than retryInFlightBound at now and last, the run's last
// activity, is before it, with when it lapses. It never drops a mark.
func (m *retryMarks) inFlight(workflowID string, last, now time.Time) (until time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.at[workflowID]
	if !ok {
		return time.Time{}, false
	}
	until = at.Add(retryInFlightBound)
	if !last.Before(at) || !now.Before(until) {
		return time.Time{}, false
	}
	return until, true
}

func (rt *Runtime) admitRunPurge(ctx context.Context, workflowID, parentSessionID string) (done func(), refused error) {
	if rt.membership == nil {
		return func() {}, nil
	}
	return rt.membership.AdmitRunPurge(ctx, workflowID, parentSessionID)
}
