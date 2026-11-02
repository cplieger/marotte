package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

var (
	// errRunOwnersUnread: an owner scan that could not read every chat found no holder, so one of the unread chats
	// may own the run.
	errRunOwnersUnread = errors.New("a chat file could not be read, so which chat owns the run is unknown")
	// errRunNoLauncher: nothing names the run's launcher, and the run could be a chat's.
	errRunNoLauncher = errors.New("the run names no launching session")
	// errRunRecordsUnread: the runs recorded under runs/ were not attributed yet this boot.
	errRunRecordsUnread = errors.New("the recorded runs were not read yet")
)

// runHolders names the chats a run's keep fact goes to: every chat whose session chain holds one of the run's
// parent sessions (the test the purge teardown deletes runs by), plus the holders its lease already records.
// known is false when nothing names a launcher and the run could be a chat's: no valid parent session, and a
// lease that is absent or of OriginAgent with no holder.
func runHolders(parents []string, l *runlease.Lease, owners chat.SessionOwners) (holders []marotte.ChatID, known bool) {
	set := map[marotte.ChatID]struct{}{}
	parented := false
	for _, p := range parents {
		if !ids.ValidSessionID(p) {
			continue
		}
		parented = true
		for _, h := range owners.Of(p) {
			set[h] = struct{}{}
		}
	}
	if l != nil {
		for _, h := range append(slices.Clone(l.KeepChats), l.ChatID) {
			if ids.ValidChatID(h) {
				set[marotte.ChatID(h)] = struct{}{}
			}
		}
	}
	holders = slices.Sorted(maps.Keys(set))
	known = len(holders) > 0 || parented || (l != nil && l.Origin != runlease.OriginAgent)
	return holders, known
}

// RunOwner answers a run's owners by runHolders (command.runOwner). A leased run answers from its lease and the
// chats; an unleased one from KAS's inventory row, or its inspect reply when the inventory omits it, and the chats.
// RunUnknown when no lease, row or reply names the run: the list leaves out a run whose state it could not read, so
// an omission is not parentless. The error is an unreadable inventory, an owner scan that could not read every chat
// and found no holder, or a run that names no launcher.
func (rs *Runs) RunOwner(ctx context.Context, workflowID string) (command.RunOwner, error) {
	if l, held := rs.lease(workflowID); held {
		return rs.ownersOf(ctx, []string{l.ParentSessionID}, &l)
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return command.RunOwner{}, fmt.Errorf("%w: reading the run inventory: %w", errRunHostStart, err)
	}
	if i := slices.IndexFunc(runs, func(r kasWorkflowRun) bool { return r.WorkflowID == workflowID }); i >= 0 {
		return rs.ownersOf(ctx, []string{runs[i].ParentSessionID}, nil)
	}
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return command.RunOwner{Verdict: command.RunUnknown}, nil
	}
	var res inspectRunState
	if json.Unmarshal(raw, &res) != nil || !res.names(workflowID) {
		return command.RunOwner{Verdict: command.RunUnknown}, nil
	}
	return rs.ownersOf(ctx, []string{res.State.ParentSessionID}, nil)
}

// ownersOf is RunOwner's verdict over one fresh owner scan.
func (rs *Runs) ownersOf(ctx context.Context, parents []string, l *runlease.Lease) (command.RunOwner, error) {
	scan := rs.chats.SessionOwners(ctx)
	holders, known := runHolders(parents, l, scan.Owners)
	out := command.RunOwner{Holders: holders, ParentSessions: slices.DeleteFunc(slices.Clone(parents), func(p string) bool {
		return !ids.ValidSessionID(p)
	})}
	switch {
	case len(holders) > 0:
		out.Verdict = command.RunOwned
	case !scan.Complete:
		return command.RunOwner{}, fmt.Errorf("%w: %w", errRunHostStart, errRunOwnersUnread)
	case !known:
		return command.RunOwner{}, errRunNoLauncher
	default:
		out.Verdict = command.RunParentless
	}
	return out, nil
}

// RunsParentedBy answers the runs sessionID launched (command.runOwner): live holds every lease naming it as parent,
// every listed non-terminal row with that parent and every run recorded under runs/ that the keep-state reconcile
// attributed to it; all adds the listed terminal rows. Until the reconcile has read runs/ this boot, and on an
// unreadable inventory, it errors.
func (rs *Runs) RunsParentedBy(ctx context.Context, sessionID string) (live, all []string, err error) {
	recorded, read := rs.recorded.parentedBy(sessionID)
	if !read {
		return nil, nil, errRunRecordsUnread
	}
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the run inventory: %w", err)
	}
	liveSet := map[string]struct{}{}
	for _, id := range recorded {
		liveSet[id] = struct{}{}
	}
	leases := rs.leaseStore().List()
	for i := range leases {
		if leases[i].ParentSessionID == sessionID {
			liveSet[leases[i].WorkflowID] = struct{}{}
		}
	}
	allSet := map[string]struct{}{}
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" || r.ParentSessionID != sessionID {
			continue
		}
		allSet[r.WorkflowID] = struct{}{}
		if !marotte.RunStatus(r.Status).Terminal() {
			liveSet[r.WorkflowID] = struct{}{}
		}
	}
	maps.Copy(allSet, liveSet)
	return slices.Sorted(maps.Keys(liveSet)), slices.Sorted(maps.Keys(allSet)), nil
}

// RecordedRuns answers every workflow id with a run record under runs/, the fill-in's candidates beside the leases
// and the run list. No run log answers none.
func (rs *Runs) RecordedRuns(context.Context) ([]string, error) {
	if rs.log == nil {
		return nil, nil
	}
	return rs.log.recorded()
}

// recordedRuns holds the live runs the keep-state reconcile found only under runs/, by parent session, and whether it
// has read runs/ this boot.
type recordedRuns struct {
	parents map[string]string
	mu      sync.Mutex
	read    bool
}

// set replaces the attributed runs, workflow id to parent session, and marks runs/ read.
func (r *recordedRuns) set(parents map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parents, r.read = maps.Clone(parents), true
}

func (r *recordedRuns) parentedBy(sessionID string) (workflowIDs []string, read bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for wf, parent := range r.parents {
		if parent == sessionID {
			workflowIDs = append(workflowIDs, wf)
		}
	}
	return workflowIDs, r.read
}
