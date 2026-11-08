package agent

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// engineDownFor is how long marotte_engine_up == 0 may last before the engine-down rule pages; it mirrors that rule's for
// in alerts/promql.yaml.
const engineDownFor = 10 * time.Minute

func engineUp(t *testing.T) float64 {
	t.Helper()
	return testsupport.MetricValue(t, "marotte_engine_up")
}

// upRecord is one published engine state with the gauge as the publish left it.
type upRecord struct {
	state marotte.EngineState
	up    float64
}

type upRecorder struct {
	t    *testing.T
	recs []upRecord
	mu   sync.Mutex
}

func (r *upRecorder) record(st *marotte.EngineStatus) {
	up := engineUp(r.t)
	r.mu.Lock()
	r.recs = append(r.recs, upRecord{state: st.State, up: up})
	r.mu.Unlock()
}

func (r *upRecorder) all() []upRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]upRecord(nil), r.recs...)
}

// waitReadyPublished waits for n ready snapshots to be recorded. waitReady returns once the state is ready, before that
// publish's hook has read the gauge, so a state change made after waitReady alone could be read as the ready record's.
func (r *upRecorder) waitReadyPublished(n int) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ready := 0
		for _, rec := range r.all() {
			if rec.state == marotte.EngineReady {
				ready++
			}
		}
		if ready >= n {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%d ready snapshots recorded, want %d; records = %v", ready, n, r.all())
		}
		time.Sleep(time.Millisecond)
	}
}

// Every snapshot an engine status publish carries leaves the gauge at 1 for ready and 0 for every other state, through a
// first start and a requested restart. Not parallel: the gauge is process-wide.
func TestEngineUp_FollowsEveryPublishedState(t *testing.T) {
	rec := &upRecorder{t: t}
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	o := ownerFixture(t, src, engineHooks{publish: rec.record})
	o.start()
	waitReady(t, o, 1)
	rec.waitReadyPublished(1)
	if !o.requestRestart(restartCause{Kind: restartConfirmed, Reasons: []marotte.EngineReason{"k"}}) {
		t.Fatal("Setup: the restart was refused")
	}
	waitReady(t, o, 2)
	rec.waitReadyPublished(2)

	seen := map[marotte.EngineState]bool{}
	for _, r := range rec.all() {
		seen[r.state] = true
		want := 0.0
		if r.state == marotte.EngineReady {
			want = 1
		}
		if r.up != want {
			t.Errorf("a published %q left marotte_engine_up at %v, want %v", r.state, r.up, want)
		}
	}
	for _, s := range []marotte.EngineState{marotte.EngineStarting, marotte.EngineReady, marotte.EngineRestarting} {
		if !seen[s] {
			t.Errorf("no %q snapshot was published; states seen = %v", s, rec.all())
		}
	}
	if got := engineUp(t); got != 1 {
		t.Errorf("marotte_engine_up after the restart = %v, want 1", got)
	}
}

// A requested restart whose doors never idle waits its whole door budget and still has the engine up again well inside
// the engine-down rule's for, so a restart alone never pages. Not parallel: the gauge is process-wide.
func TestEngineUp_ARequestedRestartIsUpAgainInsideTheRulesFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		o := ownerFixture(t, src, engineHooks{})
		o.start()
		synctest.Wait()
		waitReady(t, o, 1)
		release := holdEngineDoor(t, o)
		t.Cleanup(release)

		asked := time.Now()
		if !o.requestRestart(restartCause{Kind: restartConfirmed, Reasons: []marotte.EngineReason{"k"}}) {
			t.Fatal("Setup: the restart was refused")
		}
		synctest.Wait()
		if got := engineUp(t); got != 0 {
			t.Errorf("marotte_engine_up while the restart waits for its door = %v, want 0", got)
		}
		for engineUp(t) != 1 {
			if time.Since(asked) >= engineDownFor {
				t.Fatalf("marotte_engine_up still 0 %v after the restart was asked, the rule's for", time.Since(asked))
			}
			time.Sleep(time.Second)
		}
		if h, gen := o.current(); h == nil || gen != 2 {
			t.Errorf("the engine back up is generation %d (ready %v), want generation 2", gen, h != nil)
		}
	})
}

// ownerState is o's engine state read under its lock, so reading it publishes nothing and leaves the gauge alone.
func ownerState(o *engineOwner) marotte.EngineState {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

// Nothing raises the gauge from its boot value of 0 while the engine is starting or down: only a ready publish does. Not
// parallel: the gauge is process-wide.
func TestEngineUp_StaysAtItsBootValueUntilTheFirstReadyPublish(t *testing.T) {
	t.Run("while the managed install is awaited", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// The boot value; an earlier test in this package leaves the gauge at 1.
			obs.EngineUp(false)
			src := managedSource{fakeEngineSource: fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), inst: make(chan struct{})}
			o := ownerFixture(t, src, engineHooks{})
			o.start()
			synctest.Wait()
			if st := ownerState(o); st != marotte.EngineStarting {
				t.Fatalf("Setup: state = %q, want starting", st)
			}
			if got := engineUp(t); got != 0 {
				t.Errorf("marotte_engine_up while the install is awaited = %v, want 0", got)
			}
			close(src.inst)
			synctest.Wait()
			if got := engineUp(t); got != 1 {
				t.Errorf("marotte_engine_up after the first ready publish = %v, want 1", got)
			}
		})
	})
	t.Run("with no kiro-cli", func(t *testing.T) {
		obs.EngineUp(false)
		o := ownerFixture(t, noEngine{}, engineHooks{})
		o.start()
		if st := ownerState(o); st != marotte.EngineDown {
			t.Fatalf("Setup: state = %q, want down", st)
		}
		if got := engineUp(t); got != 0 {
			t.Errorf("marotte_engine_up with no kiro-cli = %v, want 0", got)
		}
	})
}
