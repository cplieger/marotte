package agent

import (
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// TriggerConfirmer decides every event rule's launch attempts (trigger.Confirmer). Runs holds nil when no rule store
// is wired, and every call site checks.
type TriggerConfirmer interface {
	// NameAttempt records the run workflow/new created for the rule's queued event, before its invoke. An error
	// means the attempt is already abandoned and its cancel queued: the launch must not invoke the run.
	NameAttempt(ruleID, eventKey, workflowID string) error
	// ConfirmEvent is the run's run_start: the event counts as launched only when a live attempt names the run.
	ConfirmEvent(ruleID, eventKey, workflowID string)
	// BootHeld reports a run whose attempt the boot load holds until its bound, which the orphan sweep leaves alone.
	BootHeld(workflowID string) bool
	// PruneAbandoned hands the run list a triggered launch read, so abandoned runs no longer listed are dropped.
	PruneAbandoned(ruleID string, statuses map[string]marotte.RunStatus)
	// RuleChanged reports a landed definitions write of an event rule.
	RuleChanged(ruleID string, change launchrule.Change)
	// Retried is a person's retry of the run. true means the rule abandoned the run, so the retry is the person's
	// and takes a manual lease; an error refuses the retry.
	Retried(workflowID string) (bool, error)
	// RecordOutcome writes the run's outcome on its rule's row, only for the run that confirmed the rule's event.
	RecordOutcome(ruleID, workflowID string, outcome launchrule.Outcome)
	// RecordEnding is RecordOutcome for how the run ended on its own, landing only while the row still reads it
	// started: an outcome RecordOutcome wrote first (a denial, a bound stop) names the cause, and an ending says less.
	RecordEnding(ruleID, workflowID string, outcome launchrule.Outcome)
	// DismissGap clears the rule's catch-up gap segment.
	DismissGap(ruleID string) error
}

// WithTriggerConfirmer wires the event rules' confirmer; without it no launch is triggered.
//
//deadset:ignore DS1004 -- composition wires it with the trigger runner; until that lands only the tests do
func WithTriggerConfirmer(c TriggerConfirmer) Option {
	return func(rt *Runtime) { rt.runs.confirmer = c }
}
