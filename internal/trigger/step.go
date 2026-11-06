package trigger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// attemptState is a record's state in the attempt table; none is a workflow id the rule holds no record for.
type attemptState int

const (
	stateNone attemptState = iota
	stateInvoking
	stateInvokedUnconfirmed
	stateBootHeld
	stateAbandonedCancelling
	stateAbandonedCancelled
)

var stateNames = [...]string{
	stateNone:                "none",
	stateInvoking:            "invoking",
	stateInvokedUnconfirmed:  "invoked-unconfirmed",
	stateBootHeld:            "boot-held",
	stateAbandonedCancelling: "abandoned-cancelling",
	stateAbandonedCancelled:  "abandoned-cancelled",
}

func (s attemptState) String() string {
	if s < 0 || int(s) >= len(stateNames) {
		return "unknown"
	}
	return stateNames[s]
}

// record is one launch attempt of an event rule: the run marotte created for the event key.
type record struct {
	workflowID string
	key        string
	state      attemptState
}

// stepResult is what one step decided.
type stepResult struct {
	// err is NameAttempt's, Retried's or DismissGap's answer.
	err     error
	effects []attemptEffect
	// write is set by a row whose change the state file must carry.
	write bool
	// handedBack is Retried's answer.
	handedBack bool
}

// errImpossibleTransition answers a call whose cell no row of the attempt table names.
var errImpossibleTransition = errors.New(logMsgImpossible)

// step applies the attempt table's row for ev to the rule and writes the rule's state file at most once. It is the
// only writer of an event rule's state; no caller holds another rule's mutex while it calls step.
func (c *Confirmer) step(ruleID string, ev attemptEvent) stepResult {
	rv := c.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	var rec *record
	if ev.kind.isRecord() {
		rec = rv.find(ev.workflowID)
	}
	res := impossible(ruleID, rec, &ev)
	if res.write {
		if err := c.writeLocked(ruleID, rv); err != nil {
			slog.Error(logMsgNotWritten, "rule_id", ruleID, "error", err)
		}
	}
	return res
}

// impossible is row A0: a cell no other row names, where an implementation bug lands, loudly.
func impossible(ruleID string, rec *record, ev *attemptEvent) stepResult {
	state := stateNone
	if rec != nil {
		state = rec.state
	}
	slog.Error(logMsgImpossible, "rule_id", ruleID, "state", state.String(), "event", ev.kind.String(),
		"workflow_id", ev.workflowID)
	return stepResult{err: errImpossibleTransition}
}

// writeLocked writes the rule's whole value as its state file; rv.mu is held.
func (c *Confirmer) writeLocked(ruleID string, rv *ruleValue) error {
	st, err := rv.es.encode()
	if err != nil {
		return fmt.Errorf("encode the state of launch rule %s: %w", ruleID, err)
	}
	// A local file write that must land whatever the caller's lifetime; it carries no caller context.
	return c.writer.WriteState(context.Background(), ruleID, &st)
}
