package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// abandonedHold is where marotte's hold on a run an event rule abandoned stands. Zero is a run no rule abandoned in
// this process.
type abandonedHold int

const (
	// holdAbandoned: the rule abandoned the run and no cancel of it is in flight.
	holdAbandoned abandonedHold = iota + 1
	// holdCancelling: CancelAbandoned's cancel is in flight.
	holdCancelling
	// holdCancelled: KAS took the cancel; the run's lease or host session may still be held.
	holdCancelled
	// holdReleased: the hold ended (ReleaseTriggered, or a person's retry took the run), so the rule's cancel and
	// release touch the run no more.
	holdReleased
)

// maxAbandonedHolds caps the holds kept. An evicted released hold would let a cancel still queued for it stop a
// person's retry, so the cap stays far above the cancels the rules can have queued at once.
const maxAbandonedHolds = 256

// runOwners gives each run id one owner at a time. An abandoned run's cancel and its release, a person's retry and a
// triggered launch's naming and lease grant each own the run for their whole call, so no lease is granted, handed
// back or released for a run while its cancel is in flight; only the run's own end (its terminal frame, a read
// finding it over, a delete) releases it then. The zero value is ready.
type runOwners struct {
	turns map[string]*ownerTurn
	holds map[string]abandonedHold
	// order is holds' eviction queue, oldest first.
	order []string
	mu    sync.Mutex
}

type ownerTurn struct {
	serial chan struct{}
	// users counts the callers owning or waiting for the run; the turn is dropped at zero.
	users int
}

// hold makes the caller the run's owner once the owner before it releases, waiting at most until ctx is done.
// release is idempotent.
func (o *runOwners) hold(ctx context.Context, workflowID string) (release func(), err error) {
	t := o.join(workflowID)
	select {
	case t.serial <- struct{}{}:
		return o.releaser(workflowID, t), nil
	case <-ctx.Done():
		o.leave(workflowID, t)
		return nil, ctx.Err()
	}
}

// holdAlways is hold with no limit on the wait, for a caller that has no answer for giving up: every owner bounds its
// own call.
func (o *runOwners) holdAlways(workflowID string) (release func()) {
	t := o.join(workflowID)
	t.serial <- struct{}{}
	return o.releaser(workflowID, t)
}

func (o *runOwners) releaser(workflowID string, t *ownerTurn) func() {
	return sync.OnceFunc(func() {
		<-t.serial
		o.leave(workflowID, t)
	})
}

func (o *runOwners) join(workflowID string) *ownerTurn {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.turns == nil {
		o.turns = map[string]*ownerTurn{}
	}
	t := o.turns[workflowID]
	if t == nil {
		t = &ownerTurn{serial: make(chan struct{}, 1)}
		o.turns[workflowID] = t
	}
	t.users++
	return t
}

func (o *runOwners) leave(workflowID string, t *ownerTurn) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t.users--
	if t.users == 0 && o.turns[workflowID] == t {
		delete(o.turns, workflowID)
	}
}

// state is the run's hold, zero when no rule abandoned it.
func (o *runOwners) state(workflowID string) abandonedHold {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.holds[workflowID]
}

// set moves the run's hold to s. Only the run's owner calls it.
func (o *runOwners) set(workflowID string, s abandonedHold) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.holds == nil {
		o.holds = map[string]abandonedHold{}
	}
	if _, known := o.holds[workflowID]; known {
		o.order = slices.DeleteFunc(o.order, func(id string) bool { return id == workflowID })
	}
	o.holds[workflowID] = s
	o.order = append(o.order, workflowID)
	for len(o.order) > maxAbandonedHolds {
		delete(o.holds, o.order[0])
		o.order = o.order[1:]
	}
}

// errTerminationClaimed is a cancel not sent because another terminator holds the run's claim, which proves nothing
// about whether the run ends.
var errTerminationClaimed = errors.New("another stop of this run is in progress")

// errAbandonHandedBack is a cancel not sent because the rule's hold on the run ended: a person's retry took it, or
// ReleaseTriggered let it go.
var errAbandonHandedBack = errors.New("the run is no longer its launch rule's to cancel")

// CancelAbandoned cancels a run its event rule abandoned, unattributed, under its own 45 s timeout, which also bounds
// the wait for the run's owner. It returns nil only when its own cancel verb did; a lost termination claim is
// errTerminationClaimed, and a run whose hold ended is errAbandonHandedBack.
func (rs *Runs) CancelAbandoned(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return errors.New("missing workflow id")
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionListTimeout)
	defer cancel()
	release, err := rs.owners.hold(cctx, workflowID)
	if err != nil {
		return fmt.Errorf("cancel an abandoned run: wait for its owner: %w", err)
	}
	defer release()
	if rs.owners.state(workflowID) == holdReleased {
		return errAbandonHandedBack
	}
	if !rs.claimTermination(workflowID) {
		return errTerminationClaimed
	}
	rs.owners.set(workflowID, holdCancelling)
	err = rs.finishTermination(cctx, workflowID, "", runStop{}, func(ctx context.Context) error {
		// A never-invoked run is never armed, so its lease, not bounded, says marotte still holds it.
		if _, held := rs.lease(workflowID); !held {
			return nil
		}
		again := rs.CancelAbandoned(ctx, workflowID)
		if errors.Is(again, errTerminationClaimed) || errors.Is(again, errAbandonHandedBack) {
			return nil
		}
		return again
	})
	if err != nil {
		rs.owners.set(workflowID, holdAbandoned)
		return err
	}
	rs.owners.set(workflowID, holdCancelled)
	if _, held := rs.lease(workflowID); !held {
		// releaseIfOver returns at once when no lease is held, so a landed cancel whose lease is already gone has
		// nobody else to drop its claim.
		rs.forgetBounds(cctx, workflowID)
	}
	return nil
}

// ReleaseTriggered ends marotte's hold on an abandoned triggered run whose cancel landed: its lease and bounds, then
// its host session. It waits for the run's owner, and leaves alone a run a person's retry already took.
func (rs *Runs) ReleaseTriggered(ctx context.Context, workflowID string) {
	release := rs.owners.holdAlways(workflowID)
	defer release()
	if rs.owners.state(workflowID) == holdReleased {
		return
	}
	rs.owners.set(workflowID, holdReleased)
	rs.forgetBounds(ctx, workflowID)
	rs.coord.closeBridge(ctx, runChatID(workflowID), marotte.TurnOutcomeInterrupted)
}

// handBackAbandoned reports every retry to the confirmer, whatever lease is held: an abandoned record outlives the
// triggered lease once its cancel lands (ReleaseTriggered), and left behind it would read the person's retry as the
// rule's unfinished cancel. true ends the rule's hold and drops the triggered lease and its bounds, so the retry is
// leased as the person's and a restart cancels nothing; an error refuses the retry with the lease untouched. The
// caller owns the run.
func (rs *Runs) handBackAbandoned(ctx context.Context, workflowID string) error {
	if rs.confirmer == nil {
		return nil
	}
	abandoned, err := rs.confirmer.Retried(workflowID)
	if err != nil {
		return fmt.Errorf("workflow retry: %w", err)
	}
	if abandoned {
		rs.owners.set(workflowID, holdReleased)
		rs.forgetBounds(ctx, workflowID)
	}
	return nil
}
