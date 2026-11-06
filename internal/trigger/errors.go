// Package trigger launches recipes on forge events, at least once per event.
package trigger

import "errors"

// ErrLeaseNotGranted is a triggered launch stopped before its invoke because its run lease was not granted or not
// persisted: after a restart nothing would name the run, so it is never invoked.
var ErrLeaseNotGranted = errors.New("the run's lease could not be written, so the run was not started")
