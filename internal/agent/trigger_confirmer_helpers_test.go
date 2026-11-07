package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/trigger"
)

const testEventCoalesce = "github.com/o/r/run/7"

// confirmerRig is the real trigger.Confirmer over a launch-rule store holding one enabled event rule whose state
// file queues testEventKey, as a poll would have left it.
type confirmerRig struct {
	*trigger.Confirmer
	st   *launchrule.Store
	w    *faultyStateWriter
	rule launchrule.Rule
}

// faultyStateWriter fails every state write with err while it is set.
type faultyStateWriter struct {
	launchrule.StateWriter
	err error
	mu  sync.Mutex
}

func (f *faultyStateWriter) WriteState(ctx context.Context, ruleID string, st *launchrule.State) error {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.StateWriter.WriteState(ctx, ruleID, st)
}

func (f *faultyStateWriter) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func newConfirmerRig(t *testing.T) *confirmerRig {
	t.Helper()
	st, w, err := launchrule.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("launchrule.NewStore: %v", err)
	}
	r, err := st.Create(t.Context(), eventRuleOf("o/r"))
	if err != nil {
		t.Fatalf("Setup: Create: %v", err)
	}
	entry, err := json.Marshal(map[string]any{
		"key": testEventKey, "coalesce_key": testEventCoalesce, "observed_at": "2026-11-01T00:00:00Z",
		"inputs": map[string]string{"run_id": "7", "event_id": "e7"},
	})
	if err != nil {
		t.Fatalf("Setup: queue entry: %v", err)
	}
	seed := launchrule.State{Population: launchrule.Population(&r), Queue: []json.RawMessage{entry}}
	if err := w.WriteState(t.Context(), r.ID, &seed); err != nil {
		t.Fatalf("Setup: WriteState: %v", err)
	}
	fw := &faultyStateWriter{StateWriter: w}
	return &confirmerRig{Confirmer: trigger.NewConfirmer(st, fw, nil), st: st, w: fw, rule: r}
}

// launch is the rule's triggered launch origin for testEventKey.
func (cr *confirmerRig) launch() launchOrigin {
	return triggeredLaunch(cr.rule.ID, testEventKey, testEventCoalesce, "run 7")
}

// named names wf for testEventKey and has its call return, as a launch whose invoke KAS took leaves it.
func (cr *confirmerRig) named(t *testing.T, wf string) {
	t.Helper()
	if err := cr.NameAttempt(cr.rule.ID, testEventKey, wf); err != nil {
		t.Fatalf("Setup: NameAttempt: %v", err)
	}
	cr.LaunchReturned(cr.rule.ID, wf, nil)
}

// state is the rule's state file as the store reads it.
func (cr *confirmerRig) state(t *testing.T) launchrule.State {
	t.Helper()
	st, err := cr.st.State(cr.rule.ID)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	return st
}

// launched is the event keys the state file holds as confirmed.
func (cr *confirmerRig) launched(t *testing.T) []string {
	t.Helper()
	var entries []struct {
		Key string `json:"key"`
	}
	if raw := cr.state(t).Launched; len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("launched: %v", err)
		}
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	return keys
}

// attempts is the workflow ids of the attempts the state file holds.
func (cr *confirmerRig) attempts(t *testing.T) []string {
	t.Helper()
	var entries []struct {
		WorkflowID string `json:"workflow_id"`
	}
	if raw := cr.state(t).Attempts; len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("attempts: %v", err)
		}
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.WorkflowID)
	}
	return ids
}

// confirmedBy checks the state file reads the event confirmed by wf and no attempt left.
func (cr *confirmerRig) confirmedBy(t *testing.T, wf string) {
	t.Helper()
	if got := cr.launched(t); !slices.Equal(got, []string{testEventKey}) {
		t.Errorf("launched = %q, want the event confirmed", got)
	}
	if got := cr.attempts(t); len(got) != 0 {
		t.Errorf("attempts = %q, want none", got)
	}
	if st := cr.state(t).Status; st.LastRunID != wf || st.LastStatus != launchrule.StatusStarted {
		t.Errorf("status = %+v, want %s started", st, wf)
	}
}
