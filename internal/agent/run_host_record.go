package agent

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/ids"
)

// hostFileName is the file under a run's directory that names the session marotte opened to host its parentless
// launch. It lives with the run's log, so every removal of the run's directory (a delete, the finished-run purge, the
// orphan sweep) drops it.
const hostFileName = "host"

// maxHostFileBytes bounds the read: the file holds one session id.
const maxHostFileBytes = 256

// recordHost writes session as workflowID's host session. A tombstoned run writes nothing, so a write racing the run's
// delete cannot recreate its directory.
func (r *runLog) recordHost(ctx context.Context, workflowID, session string) error {
	dir, err := r.dir(workflowID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok && rec.removed {
		return errRunLogRemoved
	}
	_, err = filesafe.WriteFile(ctx, filepath.Join(dir, hostFileName), []byte(session),
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700))
	return err
}

// hostSession is the host session recorded for workflowID, "" when none is recorded or the record cannot be read.
func (r *runLog) hostSession(ctx context.Context, workflowID string) string {
	dir, err := r.dir(workflowID)
	if err != nil {
		return ""
	}
	data, err := filesafe.ReadBounded(ctx, filepath.Join(dir, hostFileName), maxHostFileBytes)
	if err != nil || !ids.ValidSessionID(string(data)) {
		return ""
	}
	return string(data)
}

// recordHost records session as the host marotte opened for workflowID's parentless launch (parentlessHost).
func (rs *Runs) recordHost(ctx context.Context, workflowID, session string) {
	if rs.log == nil || !ids.ValidSessionID(session) {
		return
	}
	if err := rs.log.recordHost(ctx, workflowID, session); err != nil {
		slog.Warn("a parentless run's host session was not recorded on disk; an unreadable chat file blocks its retry",
			"workflow_id", workflowID, "error", err)
	}
}

// parentlessHost reports whether session is the host session marotte opened for workflowID's parentless launch, which
// no chat launched: an owner scan that could not read every chat hides no launcher of it. This record, not the lease,
// is the signal: a parentless run always names a parent session (its host), and observeComplete releases its lease on
// the terminal frame, so a failed run usually has no lease left to consult.
func (rs *Runs) parentlessHost(ctx context.Context, workflowID, session string) bool {
	return rs.log != nil && ids.ValidSessionID(session) && rs.log.hostSession(ctx, workflowID) == session
}
