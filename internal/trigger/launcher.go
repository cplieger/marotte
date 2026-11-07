package trigger

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
)

// Launcher starts a triggered run of an event rule's queued event. Once workflow/new created the run it returns
// the run's id with whatever error followed; an error before it returns no id. *agent.Runs implements it.
type Launcher interface {
	LaunchTriggered(
		ctx context.Context, source, ruleID, eventKey, coalesceKey string, inputs map[string]string,
		defaults marotte.RunDefaults,
	) (workflowID string, err error)
}

// LaunchLoop is the one goroutine that launches every event rule's queued events, one LaunchTriggered call at a
// time, delivering each call's return to the Confirmer.
type LaunchLoop struct {
	c *Confirmer
	l Launcher
	// mu is the launch mutex, held across each LaunchTriggered call.
	mu sync.Mutex
}

// NewLauncher returns the launcher of c's rules, starting their runs through l.
func NewLauncher(c *Confirmer, l Launcher) *LaunchLoop {
	return &LaunchLoop{c: c, l: l}
}

// Run launches until ctx ends, scanning every rule at its start and again at each wake of the Confirmer. Its first
// scan waits for engineReady and for the Confirmer's Run to answer the cancels queued before then, since a run the
// boot load cancels may still hold its recipe. It returns once ctx ends and its launch in flight returned.
func (ll *LaunchLoop) Run(ctx context.Context, engineReady <-chan struct{}) {
	for _, ready := range []<-chan struct{}{engineReady, ll.c.bootSettled} {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
	}
	due := time.NewTimer(time.Hour)
	due.Stop()
	defer due.Stop()
	for {
		if wait, ok := ll.scan(ctx); ok {
			due.Reset(wait)
		}
		select {
		case <-ll.c.wake:
		case <-due.C:
		case <-ctx.Done():
			return
		}
		due.Stop()
	}
}

// scan launches the next launchable entry of each rule, again after any launch, since a launch's return can leave
// another entry launchable, and answers how long until the first held rule may launch again.
func (ll *LaunchLoop) scan(ctx context.Context) (time.Duration, bool) {
	for {
		launched := false
		for _, r := range ll.c.store.List() {
			if ctx.Err() != nil {
				return 0, false
			}
			if r.When.Event == nil || !r.Enabled {
				continue
			}
			if e, done, ok := ll.c.claim(&r); ok {
				ll.launch(ctx, &r, &e, done)
				launched = true
			}
		}
		if !launched {
			return ll.c.nextRetry()
		}
	}
}

// launch makes one call for the rule's entry and delivers what its return calls for, then clears the entry's
// launching mark and delivers launch ended (E9), which classes an error from before workflow/new.
func (ll *LaunchLoop) launch(ctx context.Context, r *launchrule.Rule, e *queueEntry, done func()) {
	ll.mu.Lock()
	wf, err := ll.l.LaunchTriggered(ctx, r.Source, r.ID, e.Key, e.CoalesceKey, launchInputs(r, e),
		marotte.RunDefaults{Model: r.Model, Effort: r.Effort})
	ll.mu.Unlock()
	if wf != "" {
		ll.c.LaunchReturned(r.ID, wf, err)
	} else if busy, ok := errors.AsType[*schedule.BusyError](err); ok && busy.WorkflowID != "" {
		ll.c.busy(r.ID, busy.WorkflowID)
	}
	done()
	var ended error
	if wf == "" {
		ended = cmp.Or(err, errNoRun)
	}
	ll.c.launchEnded(r.ID, e.Key, ended)
}

// errNoRun is a LaunchTriggered that returned neither a run nor an error, which the error table counts.
var errNoRun = errors.New("the launch returned no run")

// nextRetry is how long until the first rule held by a failed, refused or abandoned launch may launch again.
func (c *Confirmer) nextRetry() (time.Duration, bool) {
	now := time.Now()
	var first time.Time
	for _, rv := range c.loaded() {
		rv.mu.Lock()
		at := rv.retryAt
		rv.mu.Unlock()
		if at.After(now) && (first.IsZero() || at.Before(first)) {
			first = at
		}
	}
	if first.IsZero() {
		return 0, false
	}
	return first.Sub(now), true
}

// launchInputs are the rule's fixed inputs with the event's over them: an operator's value never stands in for
// the real event's.
func launchInputs(r *launchrule.Rule, e *queueEntry) map[string]string {
	out := maps.Clone(r.Inputs)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, e.Inputs)
	return out
}

// claim is the rule's next launchable entry, marked launching under the rule's mutex so no poll supersedes it
// between the pick and the call. A rule refused, blocked, write-blocked, backing off or holding an entry in flight
// has none.
func (c *Confirmer) claim(r *launchrule.Rule) (queueEntry, func(), bool) {
	if c.store.Refused(r.ID) != nil {
		return queueEntry{}, nil, false
	}
	rv := c.lock(r.ID)
	defer rv.mu.Unlock()
	if rv.writeBlocked != "" || rv.es.raw.Status.Blocked != "" || time.Now().Before(rv.retryAt) {
		return queueEntry{}, nil, false
	}
	pr := pollRule{rule: r, ev: r.When.Event, feed: ruleFeed(r), st: rv.es, marks: c.marks}
	keys := pr.eligible()
	if len(keys) == 0 {
		return queueEntry{}, nil, false
	}
	i := slices.IndexFunc(rv.es.queue, func(e queueEntry) bool { return e.Key == keys[0] })
	e := rv.es.queue[i]
	e.Inputs = maps.Clone(e.Inputs)
	return e, c.marks.mark(r.ID, e.Key), true
}

func ruleFeed(r *launchrule.Rule) Feed {
	return Feed{Connection: r.When.Event.Connection, Selector: r.When.Event.Repo}
}
