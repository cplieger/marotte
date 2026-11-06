package trigger

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/launchrule"
)

// testConfirmer is a Confirmer over a real launch-rule store in a temporary config dir, its log captured. Its
// cleanup fails the test on any ERROR logMsgImpossible line unless the test called expectA0, so a cell that reaches
// a row no task has built yet fails loudly instead of passing for the wrong reason.
type testConfirmer struct {
	*Confirmer
	t     *testing.T
	store *launchrule.Store
	log   *capture.Recorder
	fx    *recordingEffects
	ready chan struct{}
	dir   string
	a0    bool
}

type testConfirmerOption func(*testConfirmerConfig)

type testConfirmerConfig struct {
	writer func(*launchrule.Store) launchrule.StateWriter
}

// withWriter puts the writer w returns between the Confirmer and the store.
func withWriter(w func(*launchrule.Store) launchrule.StateWriter) testConfirmerOption {
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
	st, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: NewStore: %v", err)
	}
	var w launchrule.StateWriter = st
	if cfg.writer != nil {
		w = cfg.writer(st)
	}
	tc := &testConfirmer{
		Confirmer: NewConfirmer(st, w), t: t, store: st, log: rec, fx: &recordingEffects{}, ready: make(chan struct{}),
		dir: dir,
	}
	t.Cleanup(func() {
		if n := rec.CountExact(logMsgImpossible); n > 0 && !tc.a0 {
			t.Errorf("%d ERROR %q lines, want none: a cell landed on A0", n, logMsgImpossible)
		}
	})
	return tc
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
