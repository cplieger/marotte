package trigger

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
)

// launchCall is one LaunchTriggered call the fake launcher took.
type launchCall struct {
	at       time.Time
	inputs   map[string]string
	ruleID   string
	key      string
	coalesce string
	// n numbers the call from 1.
	n int
}

// fakeLauncher stands in for the agent's LaunchTriggered: by default it names a new run wf_<n> through the
// Confirmer, as workflow/new and the grant do, and returns its id with NameAttempt's error. answer, when set,
// decides the call instead.
type fakeLauncher struct {
	tc     *testConfirmer
	answer func(n int, call launchCall) (string, error)
	calls  []launchCall
	mu     sync.Mutex
}

var _ Launcher = (*fakeLauncher)(nil)

func (f *fakeLauncher) LaunchTriggered(
	_ context.Context, _, ruleID, eventKey, coalesceKey string, inputs map[string]string, _ marotte.RunDefaults,
) (string, error) {
	call := launchCall{
		at: time.Now(), ruleID: ruleID, key: eventKey, coalesce: coalesceKey, inputs: maps.Clone(inputs),
	}
	f.mu.Lock()
	call.n = len(f.calls) + 1
	f.calls = append(f.calls, call)
	n, answer := call.n, f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(n, call)
	}
	return f.named(n, call)
}

// named is the default answer: a new run named for the call's key.
func (f *fakeLauncher) named(n int, call launchCall) (string, error) {
	wf := fmt.Sprintf("wf_%d", n)
	return wf, f.tc.NameAttempt(call.ruleID, call.key, wf)
}

func (f *fakeLauncher) taken() []launchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// startLauncher runs the launcher of tc's rules over a new fake until the test ends, with the Confirmer's effects
// running; nothing launches before tc.engineReady or tc.answerCancels. It runs inside synctest.Test only.
func (tc *testConfirmer) startLauncher(answer func(n int, call launchCall) (string, error)) *fakeLauncher {
	f := &fakeLauncher{tc: tc, answer: answer}
	tc.run()
	ll := NewLauncher(tc.Confirmer, f)
	ctx, cancel := context.WithCancel(context.WithoutCancel(tc.t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ll.Run(ctx, tc.ready)
	}()
	tc.t.Cleanup(func() { cancel(); <-done })
	return f
}

// busyWith answers every call with a BusyError naming wf, as a recipe held by that run refuses the launch.
func busyWith(wf string) func(int, launchCall) (string, error) {
	return func(int, launchCall) (string, error) {
		return "", &schedule.BusyError{WorkflowID: wf}
	}
}

// letLaunch lets the launcher start, answering every cancel with err from now on.
func (tc *testConfirmer) letLaunch(err error) { tc.answerCancels(err) }

// answerWith makes answer decide every later call, nil restoring the default.
func (f *fakeLauncher) answerWith(answer func(int, launchCall) (string, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = answer
}

// dropQueue empties the rule's queue in memory, so its abandoned records hold no entry.
func (tc *testConfirmer) dropQueue(ruleID string) {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.es.queue = nil
}

func TestBusyErrorNamingAbandonedRunCounts(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := tc.startLauncher(nil)
		tc.letLaunch(errCancelRefused)
		f.answerWith(busyWith("wf_1"))
		advance(launchConfirmBound + launchBackoff[0])
		for range 2 {
			advance(pollTick)
		}
		if n := len(f.taken()); n != 4 {
			t.Errorf("%d calls, want the first, the one after the abandon and one at each of two poll intervals", n)
		}
		if ran, _ := tc.fx.calls(); len(ran) != 4 {
			t.Errorf("cancels run = %v, want the bound's and one resend per busy", ran)
		}
		_, st := tc.memory(r.ID)
		if st.FailingSince.IsZero() || !strings.Contains(st.RowText, "its cancel has not landed; retrying") {
			t.Errorf("status %+v, want the busys counted and the retrying row text", st)
		}
	})
}

func TestBusyErrorNamingOtherRulesRun(t *testing.T) {
	t.Run("an abandoned run of another rule, refused every time", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			x, y := tc.createRule(runSpec("success")), otherRule(tc)
			tc.seedRecord(y.ID, &record{
				workflowID: "wf_y", key: "k9", state: stateAbandonedCancelling, cause: causeBound, at: time.Now(),
			})
			tc.enqueue(x.ID, held("k1", "c1"))
			f := tc.startLauncher(busyWith("wf_y"))
			tc.letLaunch(errCancelRefused)
			for range 2 {
				advance(pollTick)
			}
			if n := len(f.taken()); n != 3 {
				t.Errorf("%d calls, want one at each of three poll intervals", n)
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_y", "wf_y", "wf_y"}) {
				t.Errorf("cancels run = %v, want Y's three resends", ran)
			}
			if _, st := tc.memory(y.ID); st.FailingSince.IsZero() {
				t.Errorf("Y status %+v, want the busys counted on Y's clock", st)
			}
			want := "waiting for run wf_y, abandoned by rule nightly, to be cancelled"
			if _, st := tc.memory(x.ID); st.RowText != want {
				t.Errorf("X row text = %q, want %q", st.RowText, want)
			}
			if n := tc.warns(); n != 0 {
				t.Errorf("%d WARN lines, want none", n)
			}
			unchangedBy(t, tc, x.ID)
		})
	})
	t.Run("another rule's invoking run, through its bound", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			x, y := tc.createRule(runSpec("success")), otherRule(tc)
			invoking(tc, &y)
			tc.dropQueue(y.ID)
			advance(time.Minute)
			tc.enqueue(x.ID, held("k1", "c1"))
			tc.startLauncher(busyWith("wf_1"))
			tc.letLaunch(errCancelRefused)
			advance(3 * time.Minute)
			if ran, _ := tc.fx.calls(); len(ran) != 0 {
				t.Errorf("cancels before the bound = %v, want none", ran)
			}
			if _, st := tc.memory(x.ID); st.RowText != "waiting for run wf_1, launched by rule nightly" {
				t.Errorf("X row text before the bound = %q, want Y's launch named", st.RowText)
			}
			advance(90 * time.Second)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels at the bound = %v, want A6's one", ran)
			}
			advance(time.Minute)
			want := "waiting for run wf_1, abandoned by rule nightly, to be cancelled"
			if _, st := tc.memory(x.ID); st.RowText != want {
				t.Errorf("X row text after the bound = %q, want %q", st.RowText, want)
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1", "wf_1"}) {
				t.Errorf("cancels after the next busy = %v, want A16's resend", ran)
			}
			if n := tc.warns(); n != 0 {
				t.Errorf("%d WARN lines, want none", n)
			}
			unchangedBy(t, tc, x.ID)
		})
	})
}

func TestBusyErrorDeliversNoLaunchReturned(t *testing.T) {
	t.Run("a busy recipe creates no record and leaves the entry queued", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.manualLease("wf_9")
			tc.startLauncher(busyWith("wf_9"))
			tc.letLaunch(nil)
			rv := tc.rule(r.ID)
			rv.mu.Lock()
			records := len(rv.records)
			rv.mu.Unlock()
			if records != 0 || !tc.relaunchable(r.ID, "k1") {
				t.Errorf("%d records, k1 relaunchable %v; want none and k1 still launchable", records, tc.relaunchable(r.ID, "k1"))
			}
			if n := tc.log.CountExact(logMsgImpossible); n != 0 {
				t.Errorf("%d A0 lines, want none: no LaunchReturned names a run that was not created", n)
			}
		})
	})
	t.Run("the error from before workflow/new is classed", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.manualLease("wf_9")
			f := tc.startLauncher(busyWith("wf_9"))
			tc.letLaunch(nil)
			if _, st := tc.memory(r.ID); !st.FailingSince.IsZero() || st.Blocked != "" {
				t.Errorf("status %+v, want E9 to count nothing for a busy recipe", st)
			}
			advance(launchBackoff[0])
			if n := len(f.taken()); n != 1 {
				t.Errorf("%d calls at the backoff's first step, want 1: a busy recipe waits for the poll interval", n)
			}
			advance(pollTick - launchBackoff[0])
			if n := len(f.taken()); n != 2 {
				t.Errorf("%d calls at the poll interval, want 2", n)
			}
		})
	})
}

func TestBusyErrorOnOwnAttemptNotCancelled(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		unconfirmed(tc, &r)
		for range 4 {
			tc.busy(r.ID, "wf_1")
			advance(time.Minute)
		}
		if got := tc.cancels(); len(got) != 0 {
			t.Errorf("cancels = %v, want none: the bound decides the rule's own live run", got)
		}
		if _, st := tc.memory(r.ID); !st.FailingSince.IsZero() || st.BoundAbandons != 0 {
			t.Errorf("status %+v, want nothing counted", st)
		}
	})
}

func TestUnleasedAbandonedRunCancelRetried(t *testing.T) {
	t.Run("each busy resends the cancel", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			nameFailedCancelling(tc, &r)
			tc.answerCancels(errCancelRefused)
			for i := range 3 {
				tc.busy(r.ID, "wf_1")
				tc.answerCancels(errCancelRefused)
				if ran, _ := tc.fx.calls(); len(ran) != i+2 {
					t.Errorf("after busy %d: cancels run %v, want %d", i+1, ran, i+2)
				}
				advance(pollTick)
			}
			tc.busy(r.ID, "wf_1")
			tc.answerCancels(nil)
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelled {
				t.Errorf("record = %+v, want abandoned-cancelled once a resend lands", rec)
			}
		})
	})
	t.Run("a gone record whose cancel never landed is dropped by E5's A18", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			nameFailedCancelling(tc, &r)
			tc.answerCancels(errCancelRefused)
			advance(abandonedTTL + time.Hour)
			tc.written()
			tc.deliver(r.ID, &attemptEvent{kind: evTTL})
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelling {
				t.Fatalf("after ttl: record = %+v, want it kept: its cancel never landed", rec)
			}
			if n := tc.written(); n != 0 {
				t.Errorf("ttl wrote %d times, want none", n)
			}
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{})
			if rec := tc.recordOf(r.ID, "wf_1"); rec != nil {
				t.Errorf("after run gone: record = %+v, want it dropped", rec)
			}
			if n := tc.written(); n != 1 {
				t.Errorf("run gone wrote %d times, want one", n)
			}
			if _, file := tc.file(r.ID); len(file) != 0 {
				t.Errorf("file attempts = %+v, want none", file)
			}
		})
	})
}

func TestUnnamedBlockerRowText(t *testing.T) {
	t.Run("a different run restarts the wait", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.busy(r.ID, "wf_9")
			advance(launchConfirmBound)
			tc.busy(r.ID, "wf_9")
			tc.busy(r.ID, "wf_8")
			if _, st := tc.memory(r.ID); st.RowText != "waiting for run wf_8 to finish" {
				t.Errorf("row text = %q, want the new run's wait", st.RowText)
			}
			if b := tc.blockerOf(r.ID); b != "wf_8" {
				t.Errorf("blocker = %q, want wf_8", b)
			}
			if n := tc.warns(); n != 1 {
				t.Errorf("%d WARN lines, want wf_9's one", n)
			}
		})
	})
	t.Run("a run with a lease clears the blocker", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.busy(r.ID, "wf_9")
			tc.manualLease("wf_7")
			tc.busy(r.ID, "wf_7")
			if b := tc.blockerOf(r.ID); b != "" {
				t.Errorf("blocker = %q, want none", b)
			}
		})
	})
	t.Run("a run list keeps a text the blocker did not set", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.busy(r.ID, "wf_9")
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.RowText = "starting run wf_2" })
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{})
			if _, st := tc.memory(r.ID); st.RowText != "starting run wf_2" {
				t.Errorf("row text = %q, want the later row's kept", st.RowText)
			}
			if b := tc.blockerOf(r.ID); b != "" {
				t.Errorf("blocker = %q, want it cleared", b)
			}
		})
	})
	t.Run("a run list still showing the run keeps it", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.busy(r.ID, "wf_9")
			tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{"wf_9": marotte.RunStatusRunning})
			if _, st := tc.memory(r.ID); st.RowText != "waiting for run wf_9 to finish" {
				t.Errorf("row text = %q, want the wait kept", st.RowText)
			}
		})
	})
}

func TestBusyRecipeHoldsEvent(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.manualLease("wf_9")
		f := tc.startLauncher(busyWith("wf_9"))
		tc.letLaunch(nil)
		advance(pollTick - time.Second)
		if n := len(f.taken()); n != 1 {
			t.Errorf("%d calls inside the poll interval, want 1", n)
		}
		f.answerWith(nil)
		advance(time.Second)
		calls := f.taken()
		if len(calls) != 2 || calls[1].key != "k1" {
			t.Fatalf("calls = %+v, want k1 again at the poll interval", calls)
		}
		if rec := tc.recordOf(r.ID, "wf_2"); rec == nil || rec.key != "k1" {
			t.Errorf("record = %+v, want wf_2 launched for k1", rec)
		}
	})
}

// launcherCases are the TestAttemptTable lines that run the launcher over a fake LaunchTriggered.
var launcherCases = []attemptCase{
	{
		name: "4 none, named, write fails: W1, then the launch after E10's write uses a new workflow id",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.failWrites(errDiskFull)
			f := tc.startLauncher(nil)
			tc.letLaunch(nil)
			if calls := f.taken(); len(calls) != 1 {
				tc.t.Fatalf("calls = %+v, want one before the write lands", calls)
			}
			tc.failWrites(nil)
			advance(writeTick)
			tc.answerCancels(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, attempts := tc.file(r.ID)
			if len(attempts) == 0 || attempts[0].WorkflowID != "wf_1" || attempts[0].State != persistedAbandoned {
				t.Errorf("file attempts = %+v, want wf_1 abandoned first", attempts)
			}
			if rec := tc.recordOf(r.ID, "wf_2"); rec == nil || rec.key != "k1" || !rec.state.live() {
				t.Errorf("record of wf_2 = %+v, want k1's new attempt", rec)
			}
			if len(es.queue) != 1 || es.queue[0].Key != "k1" {
				t.Errorf("file queue = %+v, want k1 queued", es.queue)
			}
			tc.ConfirmEvent(r.ID, "k1", "wf_2")
			advance(launchConfirmBound + time.Second)
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want W1's one and no bound for the cancelled run", ran)
			}
		},
	},
	launchAfterBootCancel(),
}

// launchAfterBootCancel is case 50: the boot load's cancel lands before the first launch, so a recipe the
// cancelled run held is free when the event launches.
func launchAfterBootCancel() attemptCase {
	var f *fakeLauncher
	return attemptCase{
		name: "50 pending on disk, no lease, boot: E7 with A28, then one launch, not refused as busy",
		setup: func(tc *testConfirmer, r *launchrule.Rule) {
			unconfirmed(tc, r)
			tc.restart()
		},
		deliver: func(tc *testConfirmer, _ *launchrule.Rule) {
			f = tc.startLauncher(nil)
			f.answerWith(func(_ int, call launchCall) (string, error) {
				if ran, _ := tc.fx.calls(); !slices.Contains(ran, "wf_1") {
					return "", &schedule.BusyError{WorkflowID: "wf_1"}
				}
				return "wf_2", tc.NameAttempt(call.ruleID, call.key, "wf_2")
			})
			synctest.Wait()
			if calls := f.taken(); len(calls) != 0 {
				tc.t.Errorf("calls before engine ready = %+v, want none", calls)
			}
			tc.letLaunch(nil)
			advance(launchBackoff[0])
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want A28's one", ran)
			}
			if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.cause != causeBootUnleased ||
				rec.state != stateAbandonedCancelled {
				t.Errorf("record of wf_1 = %+v, want abandoned with cause boot_unleased, its cancel landed", rec)
			}
			calls := f.taken()
			if len(calls) != 1 || calls[0].key != "k1" {
				t.Errorf("calls = %+v, want one launch of k1", calls)
			}
			if rec := tc.recordOf(r.ID, "wf_2"); rec == nil || rec.key != "k1" || !rec.state.live() {
				t.Errorf("record of wf_2 = %+v, want k1's launch", rec)
			}
		},
	}
}

func TestFleetLiveCapUnderConcurrentRules(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		tc.queuedRules(5)
		var mu sync.Mutex
		most := 0
		f := tc.startCapped(t.TempDir(), func(n int, call launchCall) (string, error) {
			live := 0
			for _, l := range tc.leases.List() {
				if l.Origin == runlease.OriginTriggered {
					live++
				}
			}
			mu.Lock()
			most = max(most, live+1)
			mu.Unlock()
			return tc.startsRuns(n, call)
		})
		tc.letLaunch(nil)
		advance(time.Hour)
		if n := len(f.taken()); n != 3 {
			t.Errorf("%d LaunchTriggered calls with five rules ready against a live cap of 3, want 3", n)
		}
		mu.Lock()
		defer mu.Unlock()
		if most > 3 {
			t.Errorf("a call took the live triggered runs to %d, want never more than 3", most)
		}
	})
}

func TestLauncherRunRefusesSecondStart(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := &fakeLauncher{tc: tc}
		tc.run()
		ll := NewLauncher(tc.Confirmer, f)
		ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
		first := make(chan error, 1)
		go func() { first <- ll.Run(ctx, tc.ready) }()
		synctest.Wait()
		second := make(chan error, 1)
		go func() { second <- ll.Run(ctx, tc.ready) }()
		synctest.Wait()
		select {
		case err := <-second:
			if !errors.Is(err, errLauncherRunning) {
				t.Errorf("a second Run returned %v, want errLauncherRunning", err)
			}
		default:
			t.Error("a second Run is still running, want it refused at once")
		}
		tc.letLaunch(nil)
		if calls := f.taken(); len(calls) != 1 {
			t.Errorf("%d calls after the wake for one queued entry, want 1", len(calls))
		}
		cancel()
		if err := <-first; err != nil {
			t.Errorf("the first Run returned %v at its context's end, want nil", err)
		}
	})
}
