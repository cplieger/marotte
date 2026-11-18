package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/cplieger/go/filesafe"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/runlease"
)

// hostFileName is the file under a run's directory that names the session marotte opened to host its parentless
// launch. It lives with the run's log, so every removal of the run's directory (a delete, the finished-run purge, the
// orphan sweep) drops it.
const hostFileName = "host"

// maxHostFileBytes bounds the read: the file holds one session id.
const maxHostFileBytes = 256

// hostSecurityFileName is the file beside the host record holding the security the host session opened with at
// launch, which a retry after the run's terminal frame released its lease reloads the session with.
const hostSecurityFileName = "host-security"

// maxHostSecurityBytes bounds the read: the file holds a list of preset ids.
const maxHostSecurityBytes = 64 << 10

// recordHost writes session as workflowID's host session, and the security it opened with. A tombstoned run writes
// nothing, so a write racing the run's delete cannot recreate its directory. The security is written first, so this
// build never leaves a host record without its security; a record an older build wrote may lack it (retryHostSecurity).
func (r *runLog) recordHost(ctx context.Context, workflowID, session string, host *runlease.HostSecurity) error {
	dir, err := r.dir(workflowID)
	if err != nil {
		return err
	}
	security, err := json.Marshal(host)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.runs[workflowID]; ok && rec.removed {
		return errRunLogRemoved
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{hostSecurityFileName, security}, {hostFileName, []byte(session)}} {
		if _, err := filesafe.WriteFile(ctx, filepath.Join(dir, f.name), f.data,
			filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
			return err
		}
	}
	return nil
}

// hostSecurity is the security workflowID's host session opened with at launch; found is false when none is recorded.
func (r *runLog) hostSecurity(ctx context.Context, workflowID string) (host runlease.HostSecurity, found bool, err error) {
	dir, err := r.dir(workflowID)
	if err != nil {
		return host, false, err
	}
	data, err := filesafe.ReadBounded(ctx, filepath.Join(dir, hostSecurityFileName), maxHostSecurityBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return host, false, nil
	}
	if err != nil {
		return host, false, fmt.Errorf("read run %s host security: %w", workflowID, err)
	}
	if err := json.Unmarshal(data, &host); err != nil || host.Presets == nil {
		return host, false, fmt.Errorf("read run %s host security: undecodable record", workflowID)
	}
	return host, true, nil
}

// hostSession is the host session recorded for workflowID, "" when none is recorded. A record that exists but cannot
// be read is an error: answering "" for it would let a chat bind the session (RunHostedBy).
func (r *runLog) hostSession(ctx context.Context, workflowID string) (string, error) {
	dir, err := r.dir(workflowID)
	if err != nil {
		return "", err
	}
	data, err := filesafe.ReadBounded(ctx, filepath.Join(dir, hostFileName), maxHostFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read run %s host record: %w", workflowID, err)
	}
	return string(data), nil
}

// recordHost records session as the host marotte opened for workflowID's parentless launch (parentlessHost), with the
// security it opened with (retryHostSecurity).
func (rs *Runs) recordHost(ctx context.Context, workflowID, session string, host *runlease.HostSecurity) {
	if rs.log == nil || !ids.ValidSessionID(session) || host == nil {
		return
	}
	if err := rs.log.recordHost(ctx, workflowID, session, host); err != nil {
		slog.Warn("a parentless run's host session was not recorded on disk; an unreadable chat file blocks its retry",
			"workflow_id", workflowID, "error", err)
	}
}

// hostedBy is the run whose recorded host is session, "" for none. A record that cannot be read is an error, never
// "hosts nothing".
func (r *runLog) hostedBy(ctx context.Context, session string) (string, error) {
	runs, err := r.recorded()
	if err != nil {
		return "", err
	}
	for _, id := range runs {
		host, err := r.hostSession(ctx, id)
		if err != nil {
			return "", err
		}
		if host == session {
			return id, nil
		}
	}
	return "", nil
}

// RunHostedBy answers whether sessionID hosts a parentless run (command.runOwner): its recorded host, or one a launch
// opened and has not recorded yet, whose workflowID is "" until workflow/new names the run. An unreadable runs/
// directory or host record is an error.
func (rs *Runs) RunHostedBy(ctx context.Context, sessionID string) (workflowID string, hosted bool, err error) {
	if wf, ok := rs.launchHosts.lookup(sessionID); ok {
		return wf, true, nil
	}
	if rs.log == nil || !ids.ValidSessionID(sessionID) {
		return "", false, nil
	}
	wf, err := rs.log.hostedBy(ctx, sessionID)
	return wf, wf != "", err
}

// launchHosts holds each host session a parentless launch has opened, mapped to its run's id once workflow/new named
// it, until launchAs returns. From the mint to recordHost no run, record or chat names the session, so without this
// entry nothing would refuse binding it into a chat (RunHostedBy). The zero value is ready.
type launchHosts struct {
	m  map[string]string
	mu sync.Mutex
}

// claim enters session and answers its drop.
func (l *launchHosts) claim(session string) (drop func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]string{}
	}
	l.m[session] = ""
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.m, session)
	}
}

// name records workflowID as the run session hosts.
func (l *launchHosts) name(session, workflowID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[session]; ok {
		l.m[session] = workflowID
	}
}

func (l *launchHosts) lookup(session string) (workflowID string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	workflowID, ok = l.m[session]
	return workflowID, ok
}

// parentlessHost reports whether session is the host session marotte opened for workflowID's parentless launch, which
// no chat launched and no chat can bind (RunHostedBy): an owner scan that could not read every chat hides no launcher
// of it. This record, not the lease, is the signal: a parentless run always names a parent session (its host), and
// observeComplete releases its lease on the terminal frame, so a failed run usually has no lease left to consult. A
// record that cannot be read proves nothing, so the caller keeps its incomplete-scan refusal.
func (rs *Runs) parentlessHost(ctx context.Context, workflowID, session string) bool {
	if rs.log == nil || !ids.ValidSessionID(session) {
		return false
	}
	host, err := rs.log.hostSession(ctx, workflowID)
	if err != nil {
		slog.Warn("a run's host record could not be read, so the run is not taken for marotte's parentless launch",
			"workflow_id", workflowID, "error", err)
		return false
	}
	return host == session
}

// retryHostSecurity sets the security a parentless retry's host session reloads with: the launch's own for a run
// marotte launched parentless (parentlessHost), so a retry after the terminal frame released the lease keeps the
// launch's permissions. A chat-parented run, and a parentless one marotte did not launch, keep o.host nil. A run
// marotte launched parentless whose launch security is not on disk is refused with runlease.ErrNoHostSecurity.
func (rs *Runs) retryHostSecurity(ctx context.Context, workflowID string, o *runOrigin) error {
	if o.parent.chat != "" {
		return nil
	}
	host, err := rs.launchHostSecurity(ctx, workflowID, o.parent.session)
	if err != nil {
		return err
	}
	o.host = host
	return nil
}

// launchHostSecurity is the security session opened with at the parentless launch of workflowID marotte made
// (parentlessHost), nil for a run marotte did not launch parentless. A launch whose security is not on disk is
// runlease.ErrNoHostSecurity; one whose record cannot be read is errRunHostStart.
func (rs *Runs) launchHostSecurity(ctx context.Context, workflowID, session string) (*runlease.HostSecurity, error) {
	if !rs.parentlessHost(ctx, workflowID, session) {
		return nil, nil
	}
	host, found, err := rs.log.hostSecurity(ctx, workflowID)
	if err != nil {
		slog.Warn("a parentless run's launch security could not be read, so its session was not loaded",
			"workflow_id", workflowID, "error", err)
		return nil, fmt.Errorf("%w: %w", errRunHostStart, err)
	}
	if !found {
		return nil, runlease.ErrNoHostSecurity
	}
	return &host, nil
}
