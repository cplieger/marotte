package trigger

import (
	"github.com/cplieger/marotte/internal/marotte"
)

// eventKind is an attempt table event. The record events come first and carry a workflow id; the rule events,
// from evEditFilter on, carry none and act on every record the rule holds.
type eventKind int

const (
	evNamed eventKind = iota + 1
	evInvokeOK
	evInvokeError
	evInvokeReplyLost
	evRunStart
	evBoundReached
	evEngineReady
	evCancelOK
	evCancelError
	evBusy
	evRetried
	evWriteFailed

	evEditFilter
	evEditRebaseline
	evDelete
	evPause
	evResume
	evRunGone
	evTTL
	evLaunchEnded
	evWriteRetry
	evBoot
)

var eventNames = [...]string{
	evNamed:           "named",
	evInvokeOK:        "invoke ok",
	evInvokeError:     "invoke error",
	evInvokeReplyLost: "invoke reply lost",
	evRunStart:        "run_start",
	evBoundReached:    "bound reached",
	evEngineReady:     "engine ready",
	evCancelOK:        "cancel ok",
	evCancelError:     "cancel error",
	evBusy:            "busy",
	evRetried:         "retried",
	evWriteFailed:     "write failed",
	evEditFilter:      "edit (filter)",
	evEditRebaseline:  "edit (rebaseline)",
	evDelete:          "delete",
	evPause:           "pause",
	evResume:          "resume",
	evRunGone:         "run gone",
	evTTL:             "ttl",
	evLaunchEnded:     "launch ended",
	evWriteRetry:      "write retry",
	evBoot:            "boot",
}

func (k eventKind) String() string {
	if k <= 0 || int(k) >= len(eventNames) {
		return "unknown"
	}
	return eventNames[k]
}

func (k eventKind) isRecord() bool { return k < evEditFilter }

// attemptEvent is one event delivered to step.
type attemptEvent struct {
	// err is a launch's, a cancel's or a launch ending's error.
	err error
	// statuses is run gone's run list.
	statuses map[string]marotte.RunStatus
	// boot is the boot load's view of the disk.
	boot       *bootLoad
	workflowID string
	// key is the event key named, or whose launch ended.
	key string
	// launchingRule is a busy's rule whose launch the run blocked, when the step runs on the rule holding the run.
	launchingRule string
	kind          eventKind
}
