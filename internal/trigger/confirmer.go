package trigger

import (
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// Confirmer owns every event rule's in-memory state and decides every launch attempt through step, the only writer
// of an event rule's state file. It implements agent.TriggerConfirmer.
type Confirmer struct {
	store  *launchrule.Store
	writer launchrule.StateWriter
	rules  map[string]*ruleValue
	// leases is the run-lease store, read by a busy naming a run no rule holds a record of (A24).
	leases Leases
	// bootHeld is each boot-held run's rule, by workflow id.
	bootHeld map[string]string
	effects  *effectQueue
	// marks are the queue entries a launch is in flight for, shared by the poller and the launcher.
	marks *launchMarks
	// wake is the launcher's coalescing signal.
	wake chan struct{}
	// bootSettled closes once Run has answered every cancel queued before the engine was ready.
	bootSettled chan struct{}
	mu          sync.Mutex
}

// NewConfirmer returns a Confirmer over the rules in store, persisting their state through writer, with every
// state file loaded against the leases on disk (row E7). The cancels the load owes wait for Run.
func NewConfirmer(store *launchrule.Store, writer launchrule.StateWriter, leases Leases) *Confirmer {
	c := &Confirmer{
		store: store, writer: writer, leases: leases, rules: map[string]*ruleValue{}, bootHeld: map[string]string{},
		effects: newEffectQueue(), marks: newLaunchMarks(), wake: make(chan struct{}, 1),
		bootSettled: make(chan struct{}),
	}
	c.load(leases)
	return c
}

// ruleValue is one event rule's in-memory state; mu covers all of it and every write of the rule's state file.
type ruleValue struct {
	retryAt time.Time
	// label is the filter.label the labeled cursor was built for.
	label        string
	writeRetry   *time.Timer
	writeBlocked string
	// unreadable is the load error of a state file that would not load. The value holds nothing from the file, and
	// nothing writes over it until E2 replaces it or E3 removes it.
	unreadable *launchrule.UnreadableStateError
	blocker    blocker
	records    []*record
	es         eventState
	retries    int
	// generation counts the edits that change what a poll reads (E1, E2); a poll that read under an older one
	// applies nothing.
	generation int
	mu         sync.Mutex
	dropped    bool
	newest     bool
}

// markUnreadable blocks the rule on a state file the store read but whose content does not decode.
func (rv *ruleValue) markUnreadable(ruleID string, err error) {
	rv.unreadable = &launchrule.UnreadableStateError{RuleID: ruleID, Err: err}
	slog.Error(logMsgStateUnreadable, "rule_id", ruleID, "error", rv.unreadable.Error())
}

func (rv *ruleValue) find(workflowID string) *record {
	for _, r := range rv.records {
		if r.workflowID == workflowID {
			return r
		}
	}
	return nil
}

// rule is the rule's in-memory value, loaded from the store on first use.
func (c *Confirmer) rule(ruleID string) *ruleValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rv := c.rules[ruleID]; rv != nil {
		return rv
	}
	rv := &ruleValue{}
	if def, ok := c.store.Get(ruleID); ok {
		rv.newest, rv.label = isNewest(&def), labelOf(&def)
	}
	st, err := c.store.LoadedState(ruleID)
	if ue, ok := errors.AsType[*launchrule.UnreadableStateError](err); ok {
		rv.unreadable = ue
	} else if err == nil {
		if es, derr := decodeState(&st); derr != nil {
			rv.markUnreadable(ruleID, derr)
		} else {
			rv.es = es
		}
	}
	c.rules[ruleID] = rv
	return rv
}

// lock is the rule's current value with its mutex held.
func (c *Confirmer) lock(ruleID string) *ruleValue {
	for {
		rv := c.rule(ruleID)
		rv.mu.Lock()
		if !rv.dropped {
			return rv
		}
		rv.mu.Unlock()
	}
}

// forget drops a deleted rule's value once it holds no record (A32); rv.mu is held.
func (c *Confirmer) forget(ruleID string, rv *ruleValue) {
	rv.dropped = true
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rules[ruleID] == rv {
		delete(c.rules, ruleID)
	}
}

// loaded is a snapshot of the rules with an in-memory value, for a lookup by workflow id.
func (c *Confirmer) loaded() map[string]*ruleValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.rules)
}

// deliver runs ev through step and then its effects.
func (c *Confirmer) deliver(ruleID string, ev *attemptEvent) stepResult {
	res := c.step(ruleID, ev)
	c.dispatch(ruleID, res.effects)
	return res
}

// NameAttempt records the run workflow/new created for the rule's queued event. An error means the run must not be
// invoked.
func (c *Confirmer) NameAttempt(ruleID, eventKey, workflowID string) error {
	return c.deliver(ruleID, &attemptEvent{kind: evNamed, key: eventKey, workflowID: workflowID}).err
}

// ConfirmEvent is the run's run_start.
func (c *Confirmer) ConfirmEvent(ruleID, eventKey, workflowID string) {
	c.deliver(ruleID, &attemptEvent{kind: evRunStart, key: eventKey, workflowID: workflowID})
}

// LaunchReturned is the return of the LaunchTriggered call that created workflowID, with the error it returned.
// It is delivered only for a call that created a run.
func (c *Confirmer) LaunchReturned(ruleID, workflowID string, err error) {
	c.deliver(ruleID, &attemptEvent{kind: launchReturnKind(err), workflowID: workflowID, err: err})
}

// launchReturnKind reads what a launch's error proves. An in-band refusal, a lease never granted, or a run the
// confirmer already abandoned prove KAS did not take the invoke; any other error (a deadline, the bridge ending)
// proves nothing, and the bound decides.
func launchReturnKind(err error) eventKind {
	switch {
	case err == nil:
		return evInvokeOK
	case errors.Is(err, ErrLeaseNotGranted), errors.Is(err, errAttemptGone), errors.Is(err, errAttemptNotWritten):
		return evInvokeError
	}
	if _, ok := errors.AsType[*marotte.RPCError](err); ok {
		return evInvokeError
	}
	return evInvokeReplyLost
}

// BootHeld reports a run whose attempt the boot load holds until its bound.
func (c *Confirmer) BootHeld(workflowID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.bootHeld[workflowID]
	return ok
}

func (c *Confirmer) releaseBootHeld(workflowID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.bootHeld, workflowID)
}

// PruneAbandoned hands over a triggered launch's run list.
func (c *Confirmer) PruneAbandoned(ruleID string, statuses map[string]marotte.RunStatus) {
	c.deliver(ruleID, &attemptEvent{kind: evRunGone, statuses: statuses})
}

// errUnknownChange is a RuleChanged whose change this build does not know.
var errUnknownChange = errors.New("unknown launch rule change")

var changeEvents = map[launchrule.Change]eventKind{
	launchrule.ChangeFilter:     evEditFilter,
	launchrule.ChangeRebaseline: evEditRebaseline,
	launchrule.ChangeDelete:     evDelete,
	launchrule.ChangePause:      evPause,
	launchrule.ChangeResume:     evResume,
}

// RuleChanged is a landed definitions write of an event rule.
func (c *Confirmer) RuleChanged(ruleID string, change launchrule.Change) {
	kind, ok := changeEvents[change]
	if !ok {
		slog.Error(logMsgImpossible, "rule_id", ruleID, "event", string(change), "error", errUnknownChange)
		return
	}
	c.deliver(ruleID, &attemptEvent{kind: kind})
}

// RetryAnswer is Retried's answer.
type RetryAnswer struct {
	// Event is set for a run that is still its event's live attempt: a retry finding no lease held re-leases the
	// run as this event's, so its run_start confirms the event.
	Event *RetriedEvent
	// HandedBack means the rule abandoned the run and handed it to the person's retry.
	HandedBack bool
}

// RetriedEvent is the event a live attempt's run was launched for.
type RetriedEvent struct {
	RuleID      string
	EventKey    string
	CoalesceKey string
	Label       string
}

// Retried is a person's retry of the run; an error refuses the retry. A run no rule holds a record of answers the
// zero RetryAnswer.
func (c *Confirmer) Retried(workflowID string) (RetryAnswer, error) {
	for id, rv := range c.loaded() {
		rv.mu.Lock()
		held := rv.find(workflowID) != nil
		rv.mu.Unlock()
		if held {
			res := c.deliver(id, &attemptEvent{kind: evRetried, workflowID: workflowID})
			return res.retry, res.err
		}
	}
	return RetryAnswer{}, nil
}

// RecordOutcome writes the run's outcome on its rule's row, only while the row names the run as its last.
func (c *Confirmer) RecordOutcome(ruleID, workflowID string, o launchrule.Outcome) {
	c.update(ruleID, anyGeneration, func(es *eventState) bool { return setOutcome(&es.raw.Status, workflowID, o, false) })
}

// RecordEnding is RecordOutcome for how the run ended on its own, landing only while the row still reads it started.
func (c *Confirmer) RecordEnding(ruleID, workflowID string, o launchrule.Outcome) {
	c.update(ruleID, anyGeneration, func(es *eventState) bool { return setOutcome(&es.raw.Status, workflowID, o, true) })
}

func setOutcome(st *launchrule.Status, workflowID string, o launchrule.Outcome, ending bool) bool {
	if st.LastRunID != workflowID || (ending && st.LastStatus != launchrule.StatusStarted) {
		return false
	}
	st.LastStatus, st.LastReason = o.Status, o.Reason
	return true
}

// DismissGap clears the rule's catch-up gap segment. launchrule.ErrNotFound when no event rule has the id.
func (c *Confirmer) DismissGap(ruleID string) error {
	if def, ok := c.store.Get(ruleID); !ok || def.When.Event == nil {
		return launchrule.ErrNotFound
	}
	c.update(ruleID, anyGeneration, func(es *eventState) bool {
		had := len(es.raw.Status.Gaps) > 0
		es.raw.Status.Gaps = nil
		return had
	})
	return nil
}
