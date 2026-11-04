package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// runFinishedPurge runs one finished-run purge pass under p, as runPurgePace after the last delete.
func runFinishedPurge(t *testing.T, h *Runtime, p RunRetention) RunPurgeResult {
	t.Helper()
	h.runs.purgePace.mark(time.Time{})
	return h.PurgeFinishedRuns(t.Context(), p)
}

// sweepOrphanLogs runs one orphaned run-log sweep pass under p, as runPurgePace after the last removing pass.
func sweepOrphanLogs(t *testing.T, h *Runtime, p RunRetention) RunPurgeResult {
	t.Helper()
	h.runs.logSweep.pace.mark(time.Time{})
	return h.SweepOrphanRunLogs(t.Context(), p)
}

// failLogRemoval makes removing workflowID's run log fail, as a directory the process cannot remove does.
func failLogRemoval(t *testing.T, h *Runtime, workflowID string) {
	t.Helper()
	h.runs.log.removeAll = func(path string) error {
		if filepath.Base(path) == workflowID {
			return &fs.PathError{Op: "unlinkat", Path: path, Err: syscall.EBUSY}
		}
		return os.RemoveAll(path)
	}
}
