package agent

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// RunRetention is how the retention side passes treat runs and run logs, read from chat_retention_days.
type RunRetention struct {
	Window   time.Duration
	Enabled  bool // false under Keep forever and for a present but unreadable config.json
	ChatRuns bool // a living chat's finished runs age out too; only with a positive Window
}

// RunPurgeResult reports one retention side pass over runs or run logs; NextDeadline is when the earliest keep that
// lapses by itself (a run's age, a retry in flight) lapses, or the next pass's turn for work it deferred, zero for
// none.
type RunPurgeResult struct {
	NextDeadline time.Time
	Purged       int
	Kept         int
	Errors       int
}

// keep counts a kept item and folds the instant its keep lapses by itself, zero for never, into NextDeadline.
func (res *RunPurgeResult) keep(until time.Time) {
	res.Kept++
	if !until.IsZero() && (res.NextDeadline.IsZero() || until.Before(res.NextDeadline)) {
		res.NextDeadline = until
	}
}

// runPurgePace is the least gap between two run deletes, and between two sweep passes that remove logs, whatever
// wakes the passes sooner. Each delete rewrites KAS's session index once per step session (about 5.5 s of KAS
// main-thread time for a 238-step run), so one delete a minute keeps those rewrites paced.
const runPurgePace = time.Minute

// deletePacer holds when a retention pass last reached a delete. The zero value is ready.
type deletePacer struct {
	last time.Time
	mu   sync.Mutex
}

func (d *deletePacer) mark(at time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last = at
}

// next is when the next delete may run, zero when it may run at now.
func (d *deletePacer) next(now time.Time) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if due := d.last.Add(runPurgePace); now.Before(due) {
		return due
	}
	return time.Time{}
}

// runPurgeFailBackoff is how long a run whose delete failed waits before the purge tries it again, so one failing
// delete cannot take every pass's single delete.
const runPurgeFailBackoff = time.Hour

// ReattributeRuns is Runs.reattributeRuns, a retention side pass.
func (rt *Runtime) ReattributeRuns(ctx context.Context) { rt.runs.reattributeRuns(ctx) }

// PurgeFinishedRuns deletes, oldest first and at most one per runPurgePace, finished runs older than p.Window (from
// last update) with no retry in flight, judged on the list and again on a fresh read before the delete, whether or not
// a chat claims them; with p.ChatRuns false a claimed run is left to its chat. Shown, live or paused runs, unreadable
// ages, runs naming no valid launching session and runs whose launching chat is in teardown are kept, and nothing is
// deleted while the tab arrangement is unread or a chat file is.
func (rt *Runtime) PurgeFinishedRuns(ctx context.Context, p RunRetention) RunPurgeResult {
	if !p.Enabled {
		return RunPurgeResult{}
	}
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
	// After the run list, so a chat created between the reads counts as its run's owner.
	scan := rt.chatStore.SessionOwners(ctx)
	if !scan.Complete {
		slog.Warn("run purge: a chat file could not be read, purging nothing this pass")
		return RunPurgeResult{}
	}
	now := time.Now()
	pass := finishedRunPass{p: p, now: now, waitUntil: rt.runs.purgePace.next(now)}
	for _, r := range oldestFinishedFirst(runs) {
		rt.purgeStep(ctx, &pass, r, len(scan.Owners.Of(r.ParentSessionID)) > 0)
	}
	pass.report()
	return pass.res
}

// finishedRunPass is one PurgeFinishedRuns pass's state across its candidates.
type finishedRunPass struct {
	now time.Time
	// waitUntil is when the next delete may run, zero while this pass may still reach one.
	waitUntil   time.Time
	unreadAside string
	unparented  []string
	res         RunPurgeResult
	p           RunRetention
	deferred    int
}

// purgeStep takes one finished run's list row: kept, skipped, deferred while a delete is runPurgePace away, or tried.
func (rt *Runtime) purgeStep(ctx context.Context, pass *finishedRunPass, r *kasWorkflowRun, owned bool) {
	switch {
	case !ids.ValidSessionID(r.ParentSessionID):
		// A run whose launcher is unknown is not a run no chat owns.
		pass.unparented = append(pass.unparented, r.WorkflowID)
		pass.res.Kept++
		return
	case owned && !pass.p.ChatRuns:
		return
	case !pass.waitUntil.IsZero():
		if why, until := rt.runs.purgeKeep(r.WorkflowID, listedRead(r), pass.p.Window, pass.now); why != "" {
			pass.res.keep(until)
			return
		}
		pass.deferred++
		pass.res.keep(pass.waitUntil)
		return
	}
	if failedAt, ok := rt.runs.purgeFailed.recent(r.WorkflowID, pass.now); ok {
		slog.Warn("run purge: a run's delete failed recently, trying the next one",
			"workflow_id", r.WorkflowID, "failed_at", failedAt)
		pass.res.keep(failedAt.Add(runPurgeFailBackoff))
		return
	}
	tried, refused := rt.purgeCandidate(ctx, r, pass.p.Window, pass.now, owned, &pass.res)
	if tried {
		at := time.Now()
		rt.runs.purgePace.mark(at)
		pass.waitUntil = at.Add(runPurgePace)
	}
	if unread, ok := errors.AsType[*command.ArrangementUnreadError](refused); ok {
		pass.unreadAside = unread.Aside
	}
}

func (pass *finishedRunPass) report() {
	if len(pass.unparented) > 0 {
		slog.Warn("run purge: finished runs name no valid launching session, keeping them", "workflow_ids", pass.unparented)
	}
	if pass.unreadAside != "" {
		slog.Warn("run purge: the tab arrangement was unreadable, deleting no run", "aside", pass.unreadAside)
	}
	res := pass.res
	if res.Purged > 0 || res.Errors > 0 || pass.deferred > 0 {
		slog.Info("run purge: pass complete", "purged", res.Purged, "kept", res.Kept, "errors", res.Errors,
			"deferred", pass.deferred, "window", pass.p.Window, "chat_runs", pass.p.ChatRuns)
	}
}

// oldestFinishedFirst is runs' finished rows with a workflow id, oldest last update first; an unreadable age sorts last.
func oldestFinishedFirst(runs []kasWorkflowRun) []*kasWorkflowRun {
	var out []*kasWorkflowRun
	for i := range runs {
		if r := &runs[i]; r.WorkflowID != "" && marotte.RunStatus(r.Status).Terminal() {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b *kasWorkflowRun) int {
		ua, ub := parseKASTime(a.UpdatedAt), parseKASTime(b.UpdatedAt)
		if (ua <= 0) != (ub <= 0) {
			if ua <= 0 {
				return 1
			}
			return -1
		}
		return cmp.Compare(ua, ub)
	})
	return out
}

// listedRead is a run's list row as purgeKeep judges it.
func listedRead(r *kasWorkflowRun) purgeRead {
	return purgeRead{status: marotte.RunStatus(r.Status), updated: parseKASTime(r.UpdatedAt)}
}

// purgeCandidate deletes one finished run that purgeKeep finds purgeable on its list row and again under the delete's
// hold, owned when a chat claims its launching session. It answers whether it reached the delete's hold, and
// admission's refusal, if any.
func (rt *Runtime) purgeCandidate(
	ctx context.Context, r *kasWorkflowRun, window time.Duration, now time.Time, owned bool, res *RunPurgeResult,
) (attempted bool, refused error) {
	if why, until := rt.runs.purgeKeep(r.WorkflowID, listedRead(r), window, now); why != "" {
		res.keep(until)
		return false, nil
	}
	done, refused := rt.admitRunPurge(ctx, r.WorkflowID, r.ParentSessionID, owned)
	if refused != nil {
		res.Kept++
		return false, refused
	}
	deleted, until, err := rt.runs.deleteIfPurgeable(ctx, r.WorkflowID, window)
	done()
	switch {
	case err != nil:
		res.Errors++
		rt.runs.purgeFailed.record(r.WorkflowID, time.Now())
		slog.Warn("run purge: delete failed", "workflow_id", r.WorkflowID, "error", err)
	case deleted:
		rt.runs.purgeFailed.clear(r.WorkflowID)
		res.Purged++
		slog.Info("run purge: deleted a finished run", "workflow_id", r.WorkflowID, "chat_owned", owned,
			"updated_at", r.UpdatedAt)
	default:
		res.keep(until)
	}
	return true, nil
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

func (rt *Runtime) admitRunPurge(ctx context.Context, workflowID, parentSessionID string, owned bool) (done func(), refused error) {
	if rt.membership == nil {
		return func() {}, nil
	}
	return rt.membership.AdmitRunPurge(ctx, workflowID, parentSessionID, owned)
}

// purgeFailures holds when each run's last purge delete failed, until it succeeds or runPurgeFailBackoff lapses. The
// zero value is ready.
type purgeFailures struct {
	at map[string]time.Time
	mu sync.Mutex
}

func (f *purgeFailures) record(workflowID string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.at == nil {
		f.at = map[string]time.Time{}
	}
	f.at[workflowID] = now
}

func (f *purgeFailures) clear(workflowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.at, workflowID)
}

// recent reports when workflowID's delete last failed, while that is within runPurgeFailBackoff of now; a lapsed
// failure is dropped.
func (f *purgeFailures) recent(workflowID string, now time.Time) (failedAt time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	failedAt, ok = f.at[workflowID]
	if ok && !now.Before(failedAt.Add(runPurgeFailBackoff)) {
		delete(f.at, workflowID)
		return time.Time{}, false
	}
	return failedAt, ok
}
