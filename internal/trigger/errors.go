// Package trigger launches recipes on forge events, at least once per event. Every launch attempt follows the
// attempt table in internal/trigger/attempt-table.md, whose row ids (A0 to A38, W1, W2, E1 to E10, R, Q) the
// package's comments and tests cite.
package trigger

import "errors"

// ErrLeaseNotGranted is a triggered launch stopped before its invoke because its run lease was not granted or not
// persisted: after a restart nothing would name the run, so it is never invoked.
var ErrLeaseNotGranted = errors.New("the run's lease could not be written, so the run was not started")

// errAttemptGone is a named run whose event is no longer queued (an edit reset it, or the rule is gone): the run
// is recorded abandoned, its cancel queued, and it must not be invoked.
var errAttemptGone = errors.New("the event this run was created for is no longer queued")

// errAttemptNotWritten wraps the state file write that failed to record a named run: the run is recorded abandoned
// in memory, its cancel queued, and it must not be invoked.
var errAttemptNotWritten = errors.New("the launch attempt could not be recorded")

// ErrLaunchRetryable marks a launch error the event waits out: it stays queued and launches again on the backoff,
// counting nothing on the rule's 24-hour clock.
var ErrLaunchRetryable = errors.New("the launch can be retried")

// ErrLaunchBlocked marks a launch error no retry can fix: the rule is blocked until the operator edits or resumes
// it.
var ErrLaunchBlocked = errors.New("the launch cannot succeed until the rule is edited")

// Classed marks err with class, ErrLaunchRetryable or ErrLaunchBlocked, for errors.Is. Its text stays err's, so a
// rule's row shows the refusal's own sentence.
func Classed(class, err error) error { return &classedError{class: class, err: err} }

type classedError struct {
	class error
	err   error
}

func (e *classedError) Error() string   { return e.err.Error() }
func (e *classedError) Unwrap() []error { return []error{e.class, e.err} }
