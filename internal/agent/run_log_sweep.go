package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
)

// orphanRunLogGuard is the youngest run log the sweep removes, whatever the window: KAS writes a run's state before
// its first step opens a log, and a list or disk read can race a run that young.
const orphanRunLogGuard = 10 * time.Minute

// orphanRunLogCap is the removals one sweep pass attempts; the rest wait runPurgePace for the next pass.
const orphanRunLogCap = 50

// logSweepState is the sweep's process-lifetime memory. The zero value is ready.
type logSweepState struct {
	// poisoned holds the logs whose removal failed; later passes skip them until restart.
	poisoned map[string]struct{}
	// unwired warns once that the sweep has no run logs or no KAS store to read.
	unwired sync.Once
	mu      sync.Mutex
}

func (s *logSweepState) poison(workflowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned == nil {
		s.poisoned = map[string]struct{}{}
	}
	s.poisoned[workflowID] = struct{}{}
}

func (s *logSweepState) isPoisoned(workflowID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.poisoned[workflowID]
	return ok
}

// orphanLog is one run log the sweep may remove: its last write and its size.
type orphanLog struct {
	modified time.Time
	id       string
	bytes    int64
}

// logSweepTally is one pass's counts, for its completion line and its result.
type logSweepTally struct {
	bytes                                                                        int64
	removed, inList, onDisk, young, held, unreadable, poisoned, deferred, errors int
}

func (t *logSweepTally) result(now time.Time) RunPurgeResult {
	res := RunPurgeResult{
		Purged: t.removed,
		Kept:   t.inList + t.onDisk + t.young + t.held + t.unreadable + t.poisoned + t.deferred,
		Errors: t.errors,
	}
	if t.deferred > 0 {
		res.NextDeadline = now.Add(runPurgePace)
	}
	return res
}

// SweepOrphanRunLogs removes, oldest first and at most orphanRunLogCap a pass, the run logs under the config dir's
// runs/ older than p.Window (and orphanRunLogGuard) whose run is absent from a complete workflow/list and from KAS's
// run state on disk. A failed or partial list, an unreadable KAS store or lease file removes nothing; a log marotte
// leases or has a step open in is kept; a log whose removal fails is skipped until restart.
func (rt *Runtime) SweepOrphanRunLogs(ctx context.Context, p RunRetention) RunPurgeResult {
	if !p.Enabled {
		return RunPurgeResult{}
	}
	release, ok := rt.lifecycle.enterUnlessDraining()
	if !ok {
		return RunPurgeResult{}
	}
	defer release()
	rs := rt.runs
	if rs.log == nil || rt.sessionReaper == nil {
		// With no reaper RunStates answers none for every run, which is no evidence a run is gone.
		rs.logSweep.unwired.Do(func() {
			slog.Warn("run log sweep: no run log store or KAS session store is wired, sweeping nothing")
		})
		return RunPurgeResult{}
	}
	now := time.Now()
	var t logSweepTally
	defer func() {
		slog.Info("run log sweep: complete", "removed", t.removed, "bytes", t.bytes, "in_list", t.inList,
			"on_disk", t.onDisk, "young", t.young, "leased", t.held, "unreadable_age", t.unreadable,
			"poisoned", t.poisoned+t.errors, "deferred", t.deferred)
	}()
	candidates, total := rt.orphanLogCandidates(now, max(p.Window, orphanRunLogGuard), &t)
	slog.Info("run log sweep: started", "run_logs", total, "candidates", len(candidates), "window", p.Window)
	if len(candidates) == 0 {
		return t.result(now)
	}
	absent, err := rt.runsAbsent(ctx, &t)
	if err != nil {
		slog.Warn("run log sweep: removing nothing this pass", "error", err)
		return t.result(now)
	}
	attempts := 0
	for _, c := range candidates {
		if !absent(c.id) {
			continue
		}
		if attempts == orphanRunLogCap {
			t.deferred++
			continue
		}
		attempts++
		if err := rs.removeOrphanLog(ctx, c.id); err != nil {
			rs.logSweep.poison(c.id)
			t.errors++
			slog.Warn("run log sweep: could not remove a run log, skipping it until restart",
				"workflow_id", c.id, "error", err)
			continue
		}
		t.removed++
		t.bytes += c.bytes
		slog.Info("run log sweep: removed an orphaned run log", "workflow_id", c.id,
			"age", now.Sub(c.modified).Round(time.Second), "bytes", c.bytes)
	}
	return t.result(now)
}

// orphanLogCandidates lists the run logs old enough to sweep, oldest first, counting the ones it keeps into t, and
// answers how many logs it listed. A log marotte leases, has a step open in or failed to remove is kept.
func (rt *Runtime) orphanLogCandidates(now time.Time, minAge time.Duration, t *logSweepTally) (out []orphanLog, total int) {
	rs := rt.runs
	ids, err := rs.log.recorded()
	if err != nil {
		slog.Warn("run log sweep: the run logs could not be listed, removing nothing this pass", "error", err)
		return nil, 0
	}
	if aside := rs.leaseStore().Unread(); aside != "" {
		slog.Warn("run log sweep: the run lease file is unreadable, so no log is known unleased", "aside", aside)
		t.held += len(ids)
		return nil, len(ids)
	}
	var unreadable []string
	for _, id := range ids {
		l, err := rs.log.lastWrite(id)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			unreadable = append(unreadable, id)
			t.unreadable++
		case rs.logSweep.isPoisoned(id):
			t.poisoned++
		case now.Sub(l.modified) < minAge:
			t.young++
		case rs.log.hasOpen(id):
			t.held++
		default:
			if _, leased := rs.lease(id); leased {
				t.held++
				continue
			}
			out = append(out, l)
		}
	}
	if len(unreadable) > 0 {
		slog.Warn("run log sweep: run logs whose age could not be read, keeping them", "workflow_ids", unreadable)
	}
	slices.SortFunc(out, func(a, b orphanLog) int { return cmp.Compare(a.modified.UnixNano(), b.modified.UnixNano()) })
	return out, len(ids)
}

// runsAbsent reads KAS's run list, then its run state on disk, and answers whether a run is absent from both, counting
// each present run it is asked about into t. Any read that cannot prove absence is an error.
func (rt *Runtime) runsAbsent(ctx context.Context, t *logSweepTally) (func(workflowID string) bool, error) {
	runs, err := rt.runs.listRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("the run list was failed or partial: %w", err)
	}
	listed := make(map[string]struct{}, len(runs))
	for i := range runs {
		if runs[i].WorkflowID == "" {
			return nil, fmt.Errorf("the run list was failed or partial: %w: a row names no workflow id", errRunListPartial)
		}
		listed[runs[i].WorkflowID] = struct{}{}
	}
	// After the list, so a run KAS left out because it could not read its state still shows here.
	onDisk, err := rt.sessionReaper.RunStates()
	if err != nil {
		return nil, fmt.Errorf("KAS's run state could not be listed: %w", err)
	}
	return func(workflowID string) bool {
		if _, ok := listed[workflowID]; ok {
			t.inList++
			return false
		}
		if _, ok := onDisk[workflowID]; ok {
			t.onDisk++
			return false
		}
		return true
	}, nil
}

// lastWrite is a run log's age source: the later of its entries file's and its directory's modification time, since
// every append writes the file and the log has no header. A missing entries file reads the directory alone.
func (r *runLog) lastWrite(workflowID string) (orphanLog, error) {
	dir, err := r.dir(workflowID)
	if err != nil {
		return orphanLog{}, err
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return orphanLog{}, err
	}
	l := orphanLog{id: workflowID, modified: dirInfo.ModTime()}
	fileInfo, err := os.Stat(filepath.Join(dir, chat.EntriesFileName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return l, nil
	case err != nil:
		return orphanLog{}, err
	}
	l.bytes = fileInfo.Size()
	if fileInfo.ModTime().After(l.modified) {
		l.modified = fileInfo.ModTime()
	}
	return l, nil
}

// removeOrphanLog tombstones a run log no run backs, so a late frame cannot recreate it, then removes its directory.
// It skips teardownDeleted: there is no run to report deleted and no bridge to close.
func (rs *Runs) removeOrphanLog(ctx context.Context, workflowID string) error {
	if _, err := rs.log.delete(ctx, workflowID); err != nil {
		return err
	}
	return rs.log.removeDir(workflowID)
}
