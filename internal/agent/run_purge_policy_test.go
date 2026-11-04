package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// runFinishedPurge runs one finished-run purge pass under p.
func runFinishedPurge(t *testing.T, h *Runtime, p RunRetention) RunPurgeResult {
	t.Helper()
	return h.PurgeFinishedRuns(t.Context(), p)
}

// sweepOrphanLogs runs one orphaned run-log sweep pass under p.
func sweepOrphanLogs(t *testing.T, h *Runtime, p RunRetention) RunPurgeResult {
	t.Helper()
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
