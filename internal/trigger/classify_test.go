package trigger

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/workflow"
)

func TestClassifyTable(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("workflow new: %w", err) }
	for _, c := range []struct {
		err  error
		name string
		want launchClass
	}{
		{name: "marked retryable", err: wrap(ErrLaunchRetryable), want: classRetryable},
		{name: "marked blocked", err: wrap(ErrLaunchBlocked), want: classBlocked},
		{name: "a run claim in flight", err: wrap(workflow.ErrJustClaimed), want: classRetryable},
		{
			name: "invalid params", err: wrap(&marotte.RPCError{Code: -32602, Message: "inputs.run_id: required"}),
			want: classBlocked,
		},
		{
			name: "another in-band refusal", err: wrap(&marotte.RPCError{Code: -32603, Message: "Internal error"}),
			want: classCounted,
		},
		{name: "a busy recipe", err: wrap(&schedule.BusyError{WorkflowID: "wf_9"}), want: classRetryable},
		{name: "no run can start", err: wrap(schedule.ErrNoRunCanStart), want: classRetryable},
		{name: "a deadline", err: wrap(context.DeadlineExceeded), want: classRetryable},
		{name: "a lease not granted", err: wrap(ErrLeaseNotGranted), want: classCounted},
		{name: "an unmarked error", err: errors.New("something new"), want: classCounted},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.err); got != c.want {
				t.Errorf("classify(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}
