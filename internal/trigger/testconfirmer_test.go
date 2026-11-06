package trigger

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/launchrule"
)

// testConfirmer is a Confirmer over a real launch-rule store in a temporary config dir, its log captured. Its
// cleanup fails the test on any ERROR logMsgImpossible line unless the test called expectA0, so a cell that reaches
// a row no task has built yet fails loudly instead of passing for the wrong reason.
type testConfirmer struct {
	*Confirmer
	t      *testing.T
	store  *launchrule.Store
	log    *capture.Recorder
	fx     *recordingEffects
	writes *countingWriter
	ready  chan struct{}
	dir    string
	a0     bool
}

type testConfirmerOption func(*testConfirmerConfig)

type testConfirmerConfig struct {
	writer func(launchrule.StateWriter) launchrule.StateWriter
}

// withWriter puts the writer w returns, given the store's own, between the Confirmer and the store.
func withWriter(w func(launchrule.StateWriter) launchrule.StateWriter) testConfirmerOption {
	return func(c *testConfirmerConfig) { c.writer = w }
}

func newTestConfirmer(t *testing.T, opts ...testConfirmerOption) *testConfirmer {
	t.Helper()
	var cfg testConfirmerConfig
	for _, o := range opts {
		o(&cfg)
	}
	rec := capture.Default(t)
	dir := t.TempDir()
	st, w, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore: %v", err)
	}
	if cfg.writer != nil {
		w = cfg.writer(w)
	}
	counted := &countingWriter{StateWriter: w}
	tc := &testConfirmer{
		Confirmer: NewConfirmer(st, counted), t: t, store: st, log: rec, fx: &recordingEffects{},
		ready: make(chan struct{}), dir: dir, writes: counted,
	}
	t.Cleanup(func() {
		if n := rec.CountExact(logMsgImpossible); n > 0 && !tc.a0 {
			t.Errorf("%d ERROR %q lines, want none: a cell landed on A0", n, logMsgImpossible)
		}
	})
	return tc
}

// countingWriter counts the state file writes and removals that landed.
type countingWriter struct {
	launchrule.StateWriter
	writes, removals int
	mu               sync.Mutex
}

func (w *countingWriter) WriteState(ctx context.Context, ruleID string, st *launchrule.State) error {
	err := w.StateWriter.WriteState(ctx, ruleID, st)
	if err == nil {
		w.mu.Lock()
		w.writes++
		w.mu.Unlock()
	}
	return err
}

func (w *countingWriter) RemoveState(ruleID string) error {
	err := w.StateWriter.RemoveState(ruleID)
	if err == nil {
		w.mu.Lock()
		w.removals++
		w.mu.Unlock()
	}
	return err
}

// written is the count of landed writes since the last call.
func (tc *testConfirmer) written() int {
	tc.writes.mu.Lock()
	defer tc.writes.mu.Unlock()
	n := tc.writes.writes
	tc.writes.writes = 0
	return n
}

// expectA0 lets the test end with ERROR logMsgImpossible lines.
func (tc *testConfirmer) expectA0() { tc.a0 = true }

// run starts the Confirmer's effects over tc.fx until the test ends; nothing runs before tc.engineReady.
func (tc *testConfirmer) run() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(tc.t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		tc.Run(ctx, tc.ready, tc.fx)
	}()
	tc.t.Cleanup(func() { cancel(); <-done })
}

func (tc *testConfirmer) engineReady() { close(tc.ready) }

// createRule stores an enabled event rule of spec.
func (tc *testConfirmer) createRule(spec *launchrule.EventSpec) launchrule.Rule {
	tc.t.Helper()
	r, err := tc.store.Create(tc.t.Context(), &launchrule.Rule{
		Source: "bundled://weekly", Name: "weekly", Enabled: true, When: launchrule.When{Event: spec},
	})
	if err != nil {
		tc.t.Fatalf("Setup: Create: %v", err)
	}
	return r
}

// changeRule replaces the rule with fn's edit of it, as the launch-rule PUT does, and hands the change to c once
// the store took it.
func changeRule(t *testing.T, c *Confirmer, ruleID string, confirmDrop int, fn func(r *launchrule.Rule)) error {
	t.Helper()
	r, ok := c.store.Get(ruleID)
	if !ok {
		t.Fatalf("Setup: no rule %s", ruleID)
	}
	fn(&r)
	_, change, err := c.store.Replace(t.Context(), ruleID, &r, confirmDrop)
	if err == nil && change != "" {
		c.RuleChanged(ruleID, change)
	}
	return err
}

// deleteRule deletes the event rule, as the launch-rule DELETE does, and hands the delete to c.
func deleteRule(t *testing.T, c *Confirmer, ruleID string) {
	t.Helper()
	if _, err := c.store.Delete(t.Context(), ruleID); err != nil {
		t.Fatalf("Setup: Delete: %v", err)
	}
	c.RuleChanged(ruleID, launchrule.ChangeDelete)
}

// seedRecord puts rec among the rule's records in memory, as an earlier row would have.
func (tc *testConfirmer) seedRecord(ruleID string, rec *record) {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.records = append(rv.records, rec)
}

// recordOf is the rule's in-memory record of workflowID, nil when it holds none.
func (tc *testConfirmer) recordOf(ruleID, workflowID string) *record {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if r := rv.find(workflowID); r != nil {
		c := *r
		return &c
	}
	return nil
}

// held is a queued event of key and coalesce, its event_id derived from key.
func held(key, coalesce string) queueEntry {
	return queueEntry{Key: key, CoalesceKey: coalesce, Inputs: map[string]string{"event_id": "id/" + key}}
}

// enqueue appends entries to the rule's queue in memory, as a poll would have.
func (tc *testConfirmer) enqueue(ruleID string, entries ...queueEntry) {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.es.queue = append(rv.es.queue, entries...)
}

// setStatus changes the rule's in-memory status, as earlier rows would have.
func (tc *testConfirmer) setStatus(ruleID string, set func(st *launchrule.Status)) {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	set(&rv.es.raw.Status)
}

// memory is a copy of the rule's in-memory queue, launched and superseded lists, and status.
func (tc *testConfirmer) memory(ruleID string) (eventState, launchrule.Status) {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	es := eventState{
		queue:      slices.Clone(rv.es.queue),
		launched:   slices.Clone(rv.es.launched),
		superseded: slices.Clone(rv.es.superseded),
	}
	return es, rv.es.raw.Status
}

// file is the rule's state as the store last wrote it, with its attempts decoded.
func (tc *testConfirmer) file(ruleID string) (eventState, []attemptEntry) {
	tc.t.Helper()
	st, err := tc.store.State(ruleID)
	if err != nil {
		tc.t.Fatalf("State(%s): %v", ruleID, err)
	}
	es, err := decodeState(&st)
	if err != nil {
		tc.t.Fatalf("decodeState(%s): %v", ruleID, err)
	}
	var attempts []attemptEntry
	if len(st.Attempts) > 0 {
		if err := json.Unmarshal(st.Attempts, &attempts); err != nil {
			tc.t.Fatalf("attempts of %s: %v", ruleID, err)
		}
	}
	return es, attempts
}

// cancels are the workflow ids of the cancels queued for the effect goroutine and not yet run.
func (tc *testConfirmer) cancels() []string {
	tc.effects.mu.Lock()
	defer tc.effects.mu.Unlock()
	var out []string
	for _, e := range tc.effects.pending {
		if e.effect.kind == effectCancel {
			out = append(out, e.effect.workflowID)
		}
	}
	return out
}

// woken reports, and clears, a wake of the launcher since the last call.
func (tc *testConfirmer) woken() bool {
	select {
	case <-tc.wake:
		return true
	default:
		return false
	}
}

// withRecord changes the rule's in-memory record of workflowID, as a row this test does not drive would have.
func (tc *testConfirmer) withRecord(ruleID, workflowID string, change func(r *record)) {
	tc.t.Helper()
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	r := rv.find(workflowID)
	if r == nil {
		tc.t.Fatalf("Setup: rule %s holds no record of %s", ruleID, workflowID)
	}
	change(r)
}

// answerCancels lets the effect goroutine run every queued cancel and release, answering each cancel with err,
// and waits until every answer is back through step. It runs inside synctest.Test only, after tc.run.
func (tc *testConfirmer) answerCancels(err error) {
	tc.fx.mu.Lock()
	tc.fx.cancelErr = err
	tc.fx.mu.Unlock()
	select {
	case <-tc.ready:
	default:
		close(tc.ready)
	}
	synctest.Wait()
}

// name is NameAttempt for a queued key, failing the test on an error.
func (tc *testConfirmer) name(ruleID, key, workflowID string) {
	tc.t.Helper()
	if err := tc.NameAttempt(ruleID, key, workflowID); err != nil {
		tc.t.Fatalf("NameAttempt(%s, %s) = %v, want nil", key, workflowID, err)
	}
}

// advance moves the bubble's clock on by d and lets every timer due by then finish its delivery. It runs inside
// synctest.Test only.
func advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// recordingEffects records each cancel and release the effect goroutine runs, answering every cancel with
// cancelErr.
type recordingEffects struct {
	cancelErr error
	cancels   []string
	releases  []string
	mu        sync.Mutex
}

var _ Effects = (*recordingEffects)(nil)

func (f *recordingEffects) CancelAbandoned(_ context.Context, workflowID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, workflowID)
	return f.cancelErr
}

func (f *recordingEffects) ReleaseTriggered(_ context.Context, workflowID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, workflowID)
}

func (f *recordingEffects) calls() (cancels, releases []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancels...), append([]string(nil), f.releases...)
}
