// Package trigger launches recipes on forge events, at least once per event.
package trigger

import "errors"

// ErrLeaseNotGranted is a triggered launch stopped before its invoke because its run lease was not granted or not
// persisted: after a restart nothing would name the run, so it is never invoked.
var ErrLeaseNotGranted = errors.New("the run's lease could not be written, so the run was not started")

// ErrAttemptGone is a named run whose event is no longer queued (an edit reset it, or the rule is gone): the run
// is recorded abandoned, its cancel queued, and it must not be invoked.
var ErrAttemptGone = errors.New("the event this run was created for is no longer queued")

// ErrAttemptNotWritten wraps the state file write that failed to record a named run: the run is recorded abandoned
// in memory, its cancel queued, and it must not be invoked.
var ErrAttemptNotWritten = errors.New("the launch attempt could not be recorded")
