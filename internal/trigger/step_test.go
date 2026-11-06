package trigger

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
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
	tc := newTestConfirmer(t, withWriter(func(w launchrule.StateWriter) launchrule.StateWriter {
		guard.StateWriter = w
		return guard
	}))
	// The entry points whose rows are not built yet land on A0.
	tc.expectA0()
	r := tc.createRule(runSpec("success"))
	tc.run()
	tc.engineReady()

	h := tc.pollerOn()
	h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(-time.Minute)))
	h.poll()
	_ = changeRule(t, tc.Confirmer, r.ID, 0, moveRepo)

	tc.setStatus(r.ID, func(st *launchrule.Status) {
		st.LastRunID, st.LastStatus = "wf_0", launchrule.StatusStarted
		st.Gaps = []launchrule.Gap{{Cause: launchrule.GapOutage}}
	})
	tc.RecordEnding(r.ID, "wf_0", launchrule.Outcome{Status: launchrule.StatusFailed})
	if _, st := tc.memory(r.ID); st.LastStatus != launchrule.StatusFailed {
		t.Errorf("status after RecordEnding = %q, want failed", st.LastStatus)
	}
	tc.RecordOutcome(r.ID, "wf_0", launchrule.Outcome{Status: launchrule.StatusUnknown})
	if _, st := tc.memory(r.ID); st.LastStatus != launchrule.StatusUnknown {
		t.Errorf("status after RecordOutcome = %q, want unknown", st.LastStatus)
	}
	if err := tc.DismissGap(r.ID); err != nil {
		t.Errorf("DismissGap = %v", err)
	}
	if _, st := tc.memory(r.ID); len(st.Gaps) != 0 {
		t.Errorf("gaps after DismissGap = %+v, want none", st.Gaps)
	}

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
	for _, m := range []string{"WriteState", "RemoveState"} {
		if _, ok := reflect.TypeFor[*launchrule.Store]().MethodByName(m); ok {
			t.Errorf("*launchrule.Store has the exported state write %s, want it on the store's writer only", m)
		}
	}
}
