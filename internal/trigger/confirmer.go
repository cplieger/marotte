package trigger

import (
	"errors"
	"log/slog"
	"maps"
	"sync"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// Confirmer owns every event rule's in-memory state and decides every launch attempt through step, the only writer
// of an event rule's state file. It implements agent.TriggerConfirmer.
type Confirmer struct {
	store    *launchrule.Store
	writer   launchrule.StateWriter
	rules    map[string]*ruleValue
	bootHeld map[string]bool
	effects  *effectQueue
	// wake is the launcher's coalescing signal.
	wake chan struct{}
	mu   sync.Mutex
}

// NewConfirmer returns a Confirmer over the rules in store, persisting their state through writer.
func NewConfirmer(store *launchrule.Store, writer launchrule.StateWriter) *Confirmer {
	return &Confirmer{
		store: store, writer: writer, rules: map[string]*ruleValue{}, bootHeld: map[string]bool{},
		effects: newEffectQueue(), wake: make(chan struct{}, 1),
	}
}

// ruleValue is one event rule's in-memory state; mu covers all of it and every write of the rule's state file.
type ruleValue struct {
	records []*record
	es      eventState
	mu      sync.Mutex
	// dropped is set when the Confirmer forgets the value; a step that waited for mu takes the rule's current one.
	dropped bool
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
	if st, err := c.store.State(ruleID); err == nil {
		if es, derr := decodeState(&st); derr == nil {
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
//
//deadset:ignore DS1102 -- the launcher delivers each call's return through it; until that lands only the tests do
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
	case errors.Is(err, ErrLeaseNotGranted), errors.Is(err, ErrAttemptGone), errors.Is(err, ErrAttemptNotWritten):
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
	return c.bootHeld[workflowID]
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

// Retried is a person's retry of the run: true means the rule abandoned it and handed it to the person, an error
// refuses the retry. A run no rule holds a record of answers false, nil.
func (c *Confirmer) Retried(workflowID string) (bool, error) {
	for id, rv := range c.loaded() {
		rv.mu.Lock()
		held := rv.find(workflowID) != nil
		rv.mu.Unlock()
		if held {
			res := c.deliver(id, &attemptEvent{kind: evRetried, workflowID: workflowID})
			return res.handedBack, res.err
		}
	}
	return false, nil
}

// RecordOutcome writes the run's outcome on its rule's row.
func (c *Confirmer) RecordOutcome(ruleID, _ string, _ launchrule.Outcome) {
	c.deliver(ruleID, &attemptEvent{kind: evWriteRetry})
}

// RecordEnding is RecordOutcome for how the run ended on its own.
func (c *Confirmer) RecordEnding(ruleID, _ string, _ launchrule.Outcome) {
	c.deliver(ruleID, &attemptEvent{kind: evWriteRetry})
}

// DismissGap clears the rule's catch-up gap segment.
func (c *Confirmer) DismissGap(ruleID string) error {
	return c.deliver(ruleID, &attemptEvent{kind: evWriteRetry}).err
}
