package agent

import (
	"context"
	"errors"
	"sync"
)

// runLifecycle owns every step-session mutation of a run against the run's deletion. A message, an
// answer, a steer or a clear-and-resubmit runs under a lease issued only while the run is open; a
// Delete closes the run to new leases and waits, holding no lock, for the admitted ones to end before
// KAS sees it. The zero value is ready.
type runLifecycle struct {
	runs map[string]*runLife
	mu   sync.Mutex
}

type runLife struct {
	// drained is closed when the last admitted lease ends while a Delete waits on it.
	drained chan struct{}
	// settled is closed when a Delete leaves the run: reopened, or deleted and torn down.
	settled chan struct{}
	leases  int
	closed  bool
}

var (
	errRunClosing  = errors.New("the run is being deleted")
	errRunDeleting = errors.New("this run is already being deleted")
)

// runLease is one admitted mutation of a run's step sessions; end it exactly once.
type runLease struct {
	life       *runLifecycle
	workflowID string
}

// The caller holds l.mu.
func (l *runLifecycle) entry(workflowID string) *runLife {
	if l.runs == nil {
		l.runs = make(map[string]*runLife)
	}
	r := l.runs[workflowID]
	if r == nil {
		r = &runLife{}
		l.runs[workflowID] = r
	}
	return r
}

// The caller holds l.mu.
func (l *runLifecycle) forgetIdle(workflowID string, r *runLife) {
	if r.leases == 0 && !r.closed {
		delete(l.runs, workflowID)
	}
}

// admit issues a lease while the run is open, and errRunClosing while a Delete holds it.
func (l *runLifecycle) admit(workflowID string) (runLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.entry(workflowID)
	if r.closed {
		return runLease{}, errRunClosing
	}
	r.leases++
	return runLease{life: l, workflowID: workflowID}, nil
}

// admitWhenSettled is admit for background work nobody is waiting on: it waits out a Delete, then
// takes its lease from whatever the Delete left.
func (l *runLifecycle) admitWhenSettled(ctx context.Context, workflowID string) (runLease, error) {
	for {
		l.mu.Lock()
		r := l.entry(workflowID)
		if !r.closed {
			r.leases++
			l.mu.Unlock()
			return runLease{life: l, workflowID: workflowID}, nil
		}
		settled := r.settled
		l.mu.Unlock()
		select {
		case <-settled:
		case <-ctx.Done():
			return runLease{}, ctx.Err()
		}
	}
}

func (lease runLease) end() {
	l := lease.life
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.runs[lease.workflowID]
	r.leases--
	if r.leases == 0 && r.drained != nil {
		close(r.drained)
		r.drained = nil
	}
	l.forgetIdle(lease.workflowID, r)
}

// runDeletion is a Delete's hold on its run: closed to new leases until settle.
type runDeletion struct {
	life       *runLifecycle
	workflowID string
}

// beginDelete closes the run to new leases and waits for the admitted ones. A run another Delete
// already holds is refused with errRunDeleting; a ctx ending first reopens the run.
func (l *runLifecycle) beginDelete(ctx context.Context, workflowID string) (*runDeletion, error) {
	l.mu.Lock()
	r := l.entry(workflowID)
	if r.closed {
		l.mu.Unlock()
		return nil, errRunDeleting
	}
	r.closed = true
	r.settled = make(chan struct{})
	var drained chan struct{}
	if r.leases > 0 {
		r.drained = make(chan struct{})
		drained = r.drained
	}
	l.mu.Unlock()
	d := &runDeletion{life: l, workflowID: workflowID}
	if drained == nil {
		return d, nil
	}
	select {
	case <-drained:
		return d, nil
	case <-ctx.Done():
		d.settle()
		return nil, ctx.Err()
	}
}

// settle reopens the run to leases: KAS kept it, or it is gone and torn down here.
func (d *runDeletion) settle() {
	l := d.life
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.runs[d.workflowID]
	r.closed = false
	r.drained = nil
	close(r.settled)
	r.settled = nil
	l.forgetIdle(d.workflowID, r)
}
