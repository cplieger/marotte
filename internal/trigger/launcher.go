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
	// caps are the fleet caps the runner installs; nil holds nothing.
	caps *fleetCaps
	// mu is the launch mutex, held across each caps check and the LaunchTriggered call it lets through.
	mu      sync.Mutex
	running bool
}

// errLauncherRunning is a Run on a launcher whose Run is already running: one goroutine launches every rule.
var errLauncherRunning = errors.New("the trigger launcher is already running")

// NewLauncher returns the launcher of c's rules, starting their runs through l.
func NewLauncher(c *Confirmer, l Launcher) *LaunchLoop {
	return &LaunchLoop{c: c, l: l}
}

// Run launches until ctx ends, scanning every rule at its start and again at each wake of the Confirmer. Its first
// scan waits for engineReady and for the Confirmer's Run to answer the cancels queued before then, since a run the
// boot load cancels may still hold its recipe. It returns nil once ctx ends and its launch in flight returned, and
// errLauncherRunning at once while another Run of ll runs.
func (ll *LaunchLoop) Run(ctx context.Context, engineReady <-chan struct{}) error {
	ll.mu.Lock()
	if ll.running {
		ll.mu.Unlock()
		return errLauncherRunning
	}
	ll.running = true
	ll.mu.Unlock()
	defer func() {
		ll.mu.Lock()
		ll.running = false
		ll.mu.Unlock()
	}()
	for _, ready := range []<-chan struct{}{engineReady, ll.c.bootSettled} {
		select {
		case <-ready:
		case <-ctx.Done():
			return nil
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
			return nil
		}
		due.Stop()
	}
}

// setCaps installs the fleet caps every later launch is checked against.
func (ll *LaunchLoop) setCaps(fc *fleetCaps) {
	ll.mu.Lock()
	defer ll.mu.Unlock()
	ll.caps = fc
}

// scan launches the next launchable entry of each rule, again after any launch, since a launch's return can leave
// another entry launchable, and answers how long until the first held rule may launch again.
func (ll *LaunchLoop) scan(ctx context.Context) (time.Duration, bool) {
	for {
		launched, capWait := ll.pass(ctx)
		if ctx.Err() != nil {
			return 0, false
		}
		if launched {
			continue
		}
		retry, ok := ll.c.nextRetry()
		if capWait > 0 && (!ok || capWait < retry) {
			return capWait, true
		}
		return retry, ok
	}
}

// pass offers every enabled event rule one launch, answering whether any launched and the shortest wait of those
// the fleet caps held, zero when none was.
func (ll *LaunchLoop) pass(ctx context.Context) (launched bool, capWait time.Duration) {
	for _, r := range ll.c.store.List() {
		if ctx.Err() != nil {
			return launched, capWait
		}
		if r.When.Event == nil || !r.Enabled {
			continue
		}
		ok, wait := ll.launchNext(ctx, &r)
		launched = launched || ok
		if wait > 0 && (capWait == 0 || wait < capWait) {
			capWait = wait
		}
	}
	return launched, capWait
}

// launchNext launches the rule's next launchable entry, answering whether it did and, when the fleet caps hold the
// entry, how long until they may let it through. The caps are checked before the claim, under the launch mutex the
// call then runs under, so a held entry is never marked launching.
func (ll *LaunchLoop) launchNext(ctx context.Context, r *launchrule.Rule) (bool, time.Duration) {
	ll.mu.Lock()
	if hold, held := ll.caps.hold(ctx, time.Now()); held {
		ll.mu.Unlock()
		if ll.c.launchable(r) {
			ll.c.capHeld(r.ID, hold.text)
			return false, hold.wait
		}
		return false, 0
	}
	e, done, ok := ll.c.claim(r)
	if !ok {
		ll.mu.Unlock()
		return false, 0
	}
	ll.launch(ctx, r, &e, done)
	return true, 0
}

// launch makes one call for the rule's entry and delivers what its return calls for, then clears the entry's
// launching mark and delivers launch ended (E9), which classes an error from before workflow/new; a call the
// launcher's own shutdown ended is no failed launch. ll.mu is held, and released once the call returned.
func (ll *LaunchLoop) launch(ctx context.Context, r *launchrule.Rule, e *queueEntry, done func()) {
	wf, err := ll.l.LaunchTriggered(ctx, r.Source, r.ID, e.Key, e.CoalesceKey, launchInputs(r, e),
		marotte.RunDefaults{Model: r.Model, Effort: r.Effort})
	if wf != "" {
		ll.caps.launched(time.Now())
	}
	ll.mu.Unlock()
	if wf != "" {
		ll.c.LaunchReturned(r.ID, wf, err)
	} else if busy, ok := errors.AsType[*schedule.BusyError](err); ok && busy.WorkflowID != "" {
		ll.c.busy(r.ID, busy.WorkflowID)
	}
	done()
	var ended error
	if wf == "" && ctx.Err() == nil {
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
	key, ok := c.nextKey(r, rv)
	if !ok {
		return queueEntry{}, nil, false
	}
	i := slices.IndexFunc(rv.es.queue, func(e queueEntry) bool { return e.Key == key })
	e := rv.es.queue[i]
	e.Inputs = maps.Clone(e.Inputs)
	return e, c.marks.mark(r.ID, e.Key), true
}

// launchable reports whether the rule has an entry claim would take.
func (c *Confirmer) launchable(r *launchrule.Rule) bool {
	if c.store.Refused(r.ID) != nil {
		return false
	}
	rv := c.lock(r.ID)
	defer rv.mu.Unlock()
	_, ok := c.nextKey(r, rv)
	return ok
}

// nextKey is the key of the rule's next launchable entry; rv.mu is held.
func (c *Confirmer) nextKey(r *launchrule.Rule, rv *ruleValue) (string, bool) {
	if rv.writeBlocked != "" || rv.es.raw.Status.Blocked != "" || time.Now().Before(rv.retryAt) {
		return "", false
	}
	pr := pollRule{rule: r, ev: r.When.Event, feed: ruleFeed(r), st: rv.es, marks: c.marks}
	keys := pr.eligible()
	if len(keys) == 0 {
		return "", false
	}
	return keys[0], true
}

func ruleFeed(r *launchrule.Rule) Feed {
	return Feed{Connection: r.When.Event.Connection, Selector: r.When.Event.Repo}
}
