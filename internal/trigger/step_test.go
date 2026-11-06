package trigger

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// stepGuard is a state writer that fails every write and records which of them did not come from step.
type stepGuard struct {
	launchrule.StateWriter
	outside []string
	mu      sync.Mutex
}

var errGuardWrite = errors.New("the state file write failed")

func (g *stepGuard) note(op string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !calledFromStep() {
		g.outside = append(g.outside, op)
	}
}

func (g *stepGuard) WriteState(_ context.Context, ruleID string, _ *launchrule.State) error {
	g.note("write " + ruleID)
	return errGuardWrite
}

func (g *stepGuard) RemoveState(ruleID string) error {
	g.note("remove " + ruleID)
	return errGuardWrite
}

func calledFromStep() bool {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	for {
		f, more := frames.Next()
		if strings.HasSuffix(f.Function, ".(*Confirmer).step") {
			return true
		}
		if !more {
			return false
		}
	}
}

// Every write of an event rule's state the Confirmer makes, through any of its entry points, is step's.
func TestEveryAttemptWriteGoesThroughStep(t *testing.T) {
	guard := &stepGuard{}
	tc := newTestConfirmer(t, withWriter(func(st *launchrule.Store) launchrule.StateWriter {
		guard.StateWriter = st
		return guard
	}))
	// No row is built yet, so every entry point below lands on A0.
	tc.expectA0()
	r := tc.createRule(runSpec("success"))
	tc.run()
	tc.engineReady()

	_ = tc.NameAttempt(r.ID, "k1", "wf_1")
	tc.ConfirmEvent(r.ID, "k1", "wf_1")
	tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{"wf_1": marotte.RunStatusAborted})
	for _, change := range []launchrule.Change{
		launchrule.ChangeFilter, launchrule.ChangeRebaseline, launchrule.ChangePause, launchrule.ChangeResume,
		launchrule.ChangeDelete,
	} {
		tc.RuleChanged(r.ID, change)
	}
	_, _ = tc.Retried("wf_1")
	tc.RecordOutcome(r.ID, "wf_1", launchrule.Outcome{Status: launchrule.StatusCompleted})
	tc.RecordEnding(r.ID, "wf_1", launchrule.Outcome{Status: launchrule.StatusCompleted})
	_ = tc.DismissGap(r.ID)

	guard.mu.Lock()
	defer guard.mu.Unlock()
	if len(guard.outside) != 0 {
		t.Errorf("state writes outside step: %v", guard.outside)
	}
	assertNoStateFile(t, tc, r.ID)
}
