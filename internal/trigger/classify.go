package trigger

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/workflow"
)

// launchClass is a launch error's row of the error table.
type launchClass int

const (
	// classRetryable waits the error out on the backoff and counts nothing.
	classRetryable launchClass = iota + 1
	// classBlocked blocks the rule with the error's sentence.
	classBlocked
	// classCounted is every error the table does not list: retried on the backoff and counted on the rule's
	// 24-hour clock, so a new error never wedges a rule silently and never retries forever.
	classCounted
)

// classify is the error table, matched through every wrap. internal/agent marks its own errors with
// ErrLaunchRetryable or ErrLaunchBlocked, since this package cannot read them.
func classify(err error) launchClass {
	if errors.Is(err, ErrLaunchBlocked) {
		return classBlocked
	}
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok && rpc.Code == codeInvalidParams {
		return classBlocked
	}
	if _, ok := errors.AsType[*schedule.BusyError](err); ok {
		return classRetryable
	}
	for _, retryable := range []error{
		ErrLaunchRetryable, workflow.ErrJustClaimed, schedule.ErrNoRunCanStart, context.DeadlineExceeded,
		ErrLeaseNotGranted,
	} {
		if errors.Is(err, retryable) {
			return classRetryable
		}
	}
	return classCounted
}

// launchBackoff is the wait before each retry of a failed or abandoned launch; its last step repeats.
var launchBackoff = []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute}

// backOff holds the rule's launches for the backoff's next step.
func (s *stepper) backOff() {
	rv := s.rv
	rv.retryAt = s.now.Add(launchBackoff[min(rv.retries, len(launchBackoff)-1)])
	rv.retries++
}

// launchFailed is E9's classing of an error LaunchTriggered returned before workflow/new created a run.
func (s *stepper) launchFailed(err error) {
	class := classify(err)
	if _, busy := errors.AsType[*schedule.BusyError](err); busy {
		// The busy rows named the blocking run; the launch is attempted again at the rule's next poll.
		s.rv.retryAt = s.now.Add(pollInterval(&s.def))
		return
	}
	text := reasonText(err)
	if class == classBlocked {
		s.block("", text)
		return
	}
	s.backOff()
	st := s.status()
	switch {
	case errors.Is(err, schedule.ErrNoRunCanStart):
		st.RowText = "waiting for the sign-in"
	case errors.Is(err, schedule.ErrEngineBusy):
		st.RowText = "waiting on the engine"
	default:
		st.RowText = "the launch failed: " + text + "; retrying at " + s.rv.retryAt.Local().Format("15:04")
	}
	if class == classCounted {
		s.failedAttempt("", text)
	}
	if st.Blocked == "" {
		warnRetryable(s.ruleID, "", text)
	}
}

// reasonText is an error as a rule's row shows it, sanitized and capped: for a refusal of KAS's, its own sentence
// without marotte's wrapping.
func reasonText(err error) string {
	if rpc, ok := errors.AsType[*marotte.RPCError](err); ok {
		err = rpc
	}
	return launchrule.ReasonText(rpcerr.Text(err))
}

func warnRetryable(ruleID, workflowID, text string) {
	slog.Warn(logMsgRunNotStarted, "rule_id", ruleID, "workflow_id", workflowID, "class", "retryable", "error", text)
}
