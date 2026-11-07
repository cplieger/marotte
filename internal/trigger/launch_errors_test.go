package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/workflow"
)

var (
	errUnlisted   = errors.New("the recipe catalog answered something new")
	errEngineSlow = Classed(ErrLaunchRetryable,
		fmt.Errorf("%w: %w", schedule.ErrEngineBusy, errors.New("the agent engine is busy, try again in a few seconds")))
	errSignIn = Classed(ErrLaunchRetryable,
		fmt.Errorf("%w: %w", schedule.ErrNoRunCanStart, errors.New("sign in to start runs")))
	errUnknownRecipe = Classed(ErrLaunchBlocked, errors.New(`unknown recipe source "bundled://gone"`))
	invalidParams    = &marotte.RPCError{Code: -32602, Message: "inputs.run_id: Required"}
	// justClaimed is KAS's run-claim refusal as workflow.Classify marks it.
	justClaimed = workflow.Classify(&marotte.RPCError{
		Code: -32603, Message: "Internal error",
		Data: json.RawMessage(`{"details":"run wf_1 was just claimed by another process"}`),
	})
)

// failBefore answers every call with err and no run, as LaunchTriggered does for an error before workflow/new.
func failBefore(err error) func(int, launchCall) (string, error) {
	return func(int, launchCall) (string, error) { return "", err }
}

// refusing answers each call by naming its run wf_<n>, as workflow/new and the grant do, then with err from
// invoke.
func refusing(tc *testConfirmer, err error) func(int, launchCall) (string, error) {
	return func(n int, call launchCall) (string, error) {
		wf := fmt.Sprintf("wf_%d", n)
		if nErr := tc.NameAttempt(call.ruleID, call.key, wf); nErr != nil {
			return wf, nErr
		}
		return wf, err
	}
}

// gaps are the waits between successive calls.
func gaps(calls []launchCall) []time.Duration {
	out := make([]time.Duration, 0, len(calls))
	for i := 1; i < len(calls); i++ {
		out = append(out, calls[i].at.Sub(calls[i-1].at))
	}
	return out
}

func (tc *testConfirmer) classes() []string {
	return tc.log.AttrValuesExact(logMsgRunNotStarted, "class")
}

func fifoRun() *launchrule.EventSpec {
	ev := runSpec("success")
	ev.Hold = &launchrule.Hold{Policy: launchrule.HoldFIFO, Cap: 10}
	return ev
}

// launchErrorCases are the TestAttemptTable lines whose launch fails, classed through the error table.
var launchErrorCases = []attemptCase{
	refusedThenRelaunched(),
	supersededOnLaunchEnded(),
	busyOnAbandoned(),
	unlistedLaunchError(),
	blockedLaunchError(),
}

func refusedThenRelaunched() attemptCase {
	var f *fakeLauncher
	var start time.Time
	return attemptCase{
		name: "8 invoking, invoke error (*marotte.RPCError): A5, then relaunched after the backoff's first step",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			f = tc.startLauncher(refusing(tc, refusal))
			start = time.Now()
			tc.letLaunch(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || !rec.state.abandoned() || rec.cause != causeInvokeError {
				t.Errorf("record of wf_1 = %+v, want abandoned with cause invoke_error", rec)
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want one for wf_1", ran)
			}
			_, st := tc.memory(r.ID)
			if !st.FailingSince.Equal(start) || st.BoundAbandons != 0 || st.Blocked != "" {
				t.Errorf("status %+v, want the clock started, the bound count unchanged, no block", st)
			}
			if want := "the launch of run wf_1 failed: session not idle; retrying at "; !strings.HasPrefix(st.RowText, want) {
				t.Errorf("row text = %q, want it to start %q", st.RowText, want)
			}
			advance(launchBackoff[0] - time.Second)
			if n := len(f.taken()); n != 1 {
				t.Errorf("%d calls inside the backoff's first step, want 1", n)
			}
			advance(time.Second)
			if calls := f.taken(); len(calls) != 2 || calls[1].key != "k1" {
				t.Errorf("calls = %+v, want k1 relaunched after the backoff's first step", calls)
			}
		},
	}
}

func supersededOnLaunchEnded() attemptCase {
	var f *fakeLauncher
	before := -1
	return attemptCase{
		name: "57 none, k1 launching on a newest rule with k2 of its coalesce key queued meanwhile, the launch fails " +
			"before NameAttempt (errEngineSlow): E9 supersedes k1 by k2 in one write, and only k2 launches",
		spec: newest,
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			f = tc.startLauncher(func(n int, call launchCall) (string, error) {
				if n == 1 {
					tc.enqueue(r.ID, held("k2", "c1"))
					before = tc.written()
					return "", errEngineSlow
				}
				wf := fmt.Sprintf("wf_%d", n)
				return wf, tc.NameAttempt(call.ruleID, call.key, wf)
			})
			tc.letLaunch(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := tc.written() - before; before < 0 || n != 1 {
				t.Errorf("%d writes after the launch ended, want E9's one", n)
			}
			es, _ := tc.file(r.ID)
			if !eq(es.superseded, []supersededEntry{{Key: "k1", By: "k2"}}) || !eq(keysOf(es.queue), []string{"k2"}) {
				t.Errorf("file superseded %+v, queue %v; want k1 superseded by k2 and k2 queued", es.superseded, keysOf(es.queue))
			}
			advance(launchBackoff[0])
			calls := f.taken()
			if len(calls) != 2 || calls[1].key != "k2" {
				t.Errorf("calls = %+v, want k2 launched after the backoff, and k1 never again", calls)
			}
		},
	}
}

func busyOnAbandoned() attemptCase {
	var f *fakeLauncher
	var since time.Time
	return attemptCase{
		name: "68 abandoned-cancelling, LaunchTriggered answers BusyError naming its run: A16 only, then E9 classes it " +
			"retryable and counts nothing",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			f = tc.startLauncher(nil)
			tc.letLaunch(errCancelRefused)
			advance(launchConfirmBound)
			_, st := tc.memory(r.ID)
			since = st.FailingSince
			f.answerWith(busyWith("wf_1"))
			advance(launchBackoff[0])
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := len(f.taken()); n != 2 {
				t.Errorf("%d calls, want the first and the busy one after the backoff", n)
			}
			rec := tc.recordOf(r.ID, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.inFlight || rec.owesRelease {
				t.Errorf("record = %+v, want abandoned-cancelling, not in flight, owing no release", rec)
			}
			cancels, releases := tc.fx.calls()
			if !slices.Equal(cancels, []string{"wf_1", "wf_1"}) || len(releases) != 0 {
				t.Errorf("cancels %v, releases %v; want A6's cancel and A16's resend, and no release", cancels, releases)
			}
			_, st := tc.memory(r.ID)
			if since.IsZero() || !st.FailingSince.Equal(since) || st.BoundAbandons != 1 || st.Blocked != "" {
				t.Errorf("status %+v, want A6's clock and count unchanged and no block", st)
			}
			advance(launchBackoff[1])
			if n := len(f.taken()); n != 2 {
				t.Errorf("%d calls at the backoff's second step, want 2: a busy recipe waits for the poll interval", n)
			}
			advance(pollTick - launchBackoff[1])
			if n := len(f.taken()); n != 3 {
				t.Errorf("%d calls at the poll interval, want 3", n)
			}
		},
	}
}

func unlistedLaunchError() attemptCase {
	var f *fakeLauncher
	var start time.Time
	before := 0
	return attemptCase{
		name: "74 none, k1 launching, launch ended with an unlisted error from before workflow/new: E9 starts the " +
			"clock in one write, and the rule is blocked 24 hours later",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			f = tc.startLauncher(failBefore(errUnlisted))
			before, start = tc.written(), time.Now()
			tc.letLaunch(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			if n := tc.written() - before; n != 1 {
				t.Errorf("%d writes, want E9's one", n)
			}
			if _, st := tc.memory(r.ID); !st.FailingSince.Equal(start) || st.Blocked != "" {
				t.Errorf("status %+v, want the clock started and no block", st)
			}
			advance(failingBlockAfter - time.Second)
			if _, st := tc.memory(r.ID); st.Blocked != "" {
				t.Errorf("blocked %q before 24 hours, want not yet", st.Blocked)
			}
			advance(time.Second)
			if _, st := tc.memory(r.ID); st.Blocked != errUnlisted.Error() {
				t.Errorf("blocked %q at 24 hours, want the last error %q", st.Blocked, errUnlisted)
			}
			if got := tc.classes(); !slices.Contains(got, "blocked") || slices.Index(got, "blocked") != len(got)-1 {
				t.Errorf("WARN classes %v, want retryable failures, then one blocked last", got)
			}
			n := len(f.taken())
			advance(time.Hour)
			if m := len(f.taken()); m != n {
				t.Errorf("%d calls an hour after the block, want %d: a blocked rule launches nothing", m, n)
			}
		},
	}
}

func blockedLaunchError() attemptCase {
	var f *fakeLauncher
	return attemptCase{
		name: "74 none, k1 launching, launch ended with errUnknownRecipe: E9 blocks the rule at once with its sentence",
		deliver: func(tc *testConfirmer, r *launchrule.Rule) {
			tc.enqueue(r.ID, held("k1", "c1"))
			f = tc.startLauncher(failBefore(errUnknownRecipe))
			tc.letLaunch(nil)
		},
		check: func(t *testing.T, tc *testConfirmer, r *launchrule.Rule) {
			es, _ := tc.file(r.ID)
			if st := es.raw.Status; st.Blocked != `unknown recipe source "bundled://gone"` || !st.FailingSince.IsZero() {
				t.Errorf("file status %+v, want blocked with the refusal's own sentence and no clock", st)
			}
			if got := tc.classes(); !slices.Equal(got, []string{"blocked"}) {
				t.Errorf("WARN classes %v, want one blocked", got)
			}
			if !eq(keysOf(es.queue), []string{"k1"}) {
				t.Errorf("file queue %v, want k1 kept", keysOf(es.queue))
			}
			advance(time.Hour)
			if n := len(f.taken()); n != 1 {
				t.Errorf("%d calls, want 1: no launch until the operator edits or resumes the rule", n)
			}
		},
	}
}

func TestFifoHeadBackoffBlocksLaterEntries(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(fifoRun())
		tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c2"))
		f := tc.startLauncher(failBefore(errUnlisted))
		tc.letLaunch(nil)
		advance(launchBackoff[0] - time.Second)
		if calls := f.taken(); len(calls) != 1 {
			t.Errorf("calls inside the head's backoff = %+v, want k1's one: k2 waits behind it", calls)
		}
		advance(time.Second)
		f.answerWith(nil)
		advance(launchBackoff[1])
		var keys []string
		for _, c := range f.taken() {
			keys = append(keys, c.key)
		}
		if !slices.Equal(keys, []string{"k1", "k1", "k1"}) {
			t.Errorf("keys launched = %v, want the head k1 alone, then its live attempt holding k2 back", keys)
		}
	})
}

func TestAbandonedAttemptsBlockAfter24h(t *testing.T) {
	t.Run("each abandon backs off 1, 2, 4, 8, then 15 minutes", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			f := tc.startLauncher(refusing(tc, refusal))
			tc.letLaunch(nil)
			advance(45 * time.Minute)
			want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
			if got := gaps(f.taken()); !slices.Equal(got, want) {
				t.Errorf("gaps between launches = %v, want %v", got, want)
			}
		})
	})
	t.Run("a confirmed launch resets the clock and the backoff", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			f := tc.startLauncher(refusing(tc, refusal))
			tc.letLaunch(nil)
			advance(3 * time.Minute)
			f.answerWith(nil)
			advance(4 * time.Minute)
			tc.ConfirmEvent(r.ID, "k1", "wf_4")
			if _, st := tc.memory(r.ID); !st.FailingSince.IsZero() {
				t.Errorf("failing since %v after the confirm, want zero", st.FailingSince)
			}
			f.answerWith(refusing(tc, refusal))
			tc.enqueue(r.ID, held("k2", "c2"))
			tc.persist(r.ID)
			advance(launchBackoff[0])
			calls := f.taken()
			if len(calls) != 6 || calls[5].at.Sub(calls[4].at) != launchBackoff[0] {
				t.Errorf("gaps after the confirm = %v, want k2's relaunch at the backoff's first step again", gaps(calls))
			}
		})
	})
	t.Run("a nil invoke reply does not reset the clock", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.name(r.ID, "k1", "wf_1")
			tc.LaunchReturned(r.ID, "wf_1", refusal)
			_, st := tc.memory(r.ID)
			since := st.FailingSince
			advance(time.Minute)
			tc.name(r.ID, "k1", "wf_2")
			tc.LaunchReturned(r.ID, "wf_2", nil)
			if _, st := tc.memory(r.ID); since.IsZero() || !st.FailingSince.Equal(since) {
				t.Errorf("failing since %v after a nil reply, want %v kept", st.FailingSince, since)
			}
		})
	})
	t.Run("a restart keeps the clock", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.FailingSince = time.Now().Add(-23 * time.Hour) })
			tc.persist(r.ID)
			tc.restart()
			advance(time.Hour)
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.name(r.ID, "k1", "wf_1")
			tc.LaunchReturned(r.ID, "wf_1", refusal)
			if _, st := tc.memory(r.ID); st.Blocked == "" {
				t.Error("not blocked by a failure 24 hours into a clock started before the restart")
			}
			if got := tc.classes(); !slices.Equal(got, []string{"blocked"}) {
				t.Errorf("WARN classes %v, want one blocked", got)
			}
		})
	})
}

// retriedWithoutCount checks a retryable launch error: the event stays queued, nothing is counted and it launches
// again after the backoff's first step.
func retriedWithoutCount(t *testing.T, err error, rowText string) {
	t.Helper()
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := tc.startLauncher(failBefore(err))
		tc.letLaunch(nil)
		_, st := tc.memory(r.ID)
		if !st.FailingSince.IsZero() || st.Blocked != "" || !tc.relaunchable(r.ID, "k1") {
			t.Errorf("status %+v, k1 launchable %v; want nothing counted and k1 still queued", st, tc.relaunchable(r.ID, "k1"))
		}
		if !strings.HasPrefix(st.RowText, rowText) {
			t.Errorf("row text = %q, want it to start %q", st.RowText, rowText)
		}
		if got := tc.classes(); !slices.Equal(got, []string{"retryable"}) {
			t.Errorf("WARN classes %v, want one retryable", got)
		}
		if ran, _ := tc.fx.calls(); len(ran) != 0 {
			t.Errorf("cancels run = %v, want none: no run was created", ran)
		}
		advance(launchBackoff[0])
		if n := len(f.taken()); n != 2 {
			t.Errorf("%d calls, want k1 again after the backoff's first step", n)
		}
	})
}

func TestSignInGateHoldsEvent(t *testing.T) {
	retriedWithoutCount(t, errSignIn, "waiting for the sign-in")
}

func TestListRecipesTimeoutRetries(t *testing.T) {
	slow := Classed(ErrLaunchRetryable, fmt.Errorf("%w: %w",
		errors.New("the recipe list is still loading, try again in a few seconds"), context.DeadlineExceeded))
	retriedWithoutCount(t, slow, "the launch failed: the recipe list is still loading")
}

func TestEngineSlowRetries(t *testing.T) {
	retriedWithoutCount(t, errEngineSlow, "waiting on the engine")
}

func TestJustClaimedRetries(t *testing.T) {
	t.Run("refused before workflow/new", func(t *testing.T) {
		retriedWithoutCount(t, fmt.Errorf("workflow new: %w", workflow.ErrJustClaimed), "the launch failed: ")
	})
	t.Run("the invoke refused", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			f := tc.startLauncher(refusing(tc, fmt.Errorf("workflow invoke: %w", justClaimed)))
			tc.letLaunch(nil)
			_, st := tc.memory(r.ID)
			if !st.FailingSince.IsZero() || st.Blocked != "" || !tc.relaunchable(r.ID, "k1") {
				t.Errorf("status %+v, k1 launchable %v; want nothing counted and k1 still queued", st,
					tc.relaunchable(r.ID, "k1"))
			}
			if want := "the launch of run wf_1 failed: "; !strings.HasPrefix(st.RowText, want) {
				t.Errorf("row text = %q, want it to start %q", st.RowText, want)
			}
			if got := tc.classes(); !slices.Equal(got, []string{"retryable"}) {
				t.Errorf("WARN classes %v, want one retryable", got)
			}
			if ran, _ := tc.fx.calls(); !slices.Equal(ran, []string{"wf_1"}) {
				t.Errorf("cancels run = %v, want one for wf_1", ran)
			}
			advance(launchBackoff[0])
			if n := len(f.taken()); n != 2 {
				t.Errorf("%d calls, want k1 again after the backoff's first step", n)
			}
		})
	})
}

// blockedAtOnce checks a blocked launch error: the rule is blocked at the first failure with sentence, and
// launches nothing more.
func blockedAtOnce(t *testing.T, err error, sentence string) {
	t.Helper()
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := tc.startLauncher(failBefore(err))
		tc.letLaunch(nil)
		if _, st := tc.memory(r.ID); st.Blocked != sentence || !st.FailingSince.IsZero() {
			t.Errorf("status %+v, want blocked with %q and no clock", st, sentence)
		}
		if got := tc.classes(); !slices.Equal(got, []string{"blocked"}) {
			t.Errorf("WARN classes %v, want one blocked", got)
		}
		advance(time.Hour)
		if n := len(f.taken()); n != 1 {
			t.Errorf("%d calls, want 1", n)
		}
	})
}

func TestInvalidParamsBlocksRule(t *testing.T) {
	blockedAtOnce(t, invalidParams, "inputs.run_id: Required")
}

func TestInvokeRefusalClassifiedByWrappedError(t *testing.T) {
	t.Run("a wrapped -32602 blocks at once", func(t *testing.T) {
		blockedAtOnce(t, fmt.Errorf("workflow new: %w", invalidParams), "inputs.run_id: Required")
	})
	t.Run("a wrapped deadline is retried", func(t *testing.T) {
		retriedWithoutCount(t, fmt.Errorf("workflow new: %w", context.DeadlineExceeded), "the launch failed: ")
	})
}

func TestUnclassifiedErrorRetriesThenBlocks(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := tc.startLauncher(func(n int, _ launchCall) (string, error) { return "", fmt.Errorf("fault %d", n) })
		tc.letLaunch(nil)
		advance(failingBlockAfter - time.Second)
		if _, st := tc.memory(r.ID); st.Blocked != "" {
			t.Errorf("blocked %q before 24 hours, want not yet", st.Blocked)
		}
		advance(time.Second)
		n := len(f.taken())
		if _, st := tc.memory(r.ID); st.Blocked != fmt.Sprintf("fault %d", n) {
			t.Errorf("blocked %q, want the last error, fault %d", st.Blocked, n)
		}
	})
}

func TestRepeatedInvokeRefusalBlocksAfter24h(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		f := tc.startLauncher(refusing(tc, refusal))
		tc.letLaunch(nil)
		advance(failingBlockAfter - time.Second)
		if _, st := tc.memory(r.ID); st.Blocked != "" || st.BoundAbandons != 0 {
			t.Errorf("status %+v before 24 hours, want no block and no bound abandon", st)
		}
		advance(time.Second)
		if _, st := tc.memory(r.ID); st.Blocked != "session not idle" {
			t.Errorf("blocked %q at 24 hours, want the refusal's sentence", st.Blocked)
		}
		n := len(f.taken())
		advance(time.Hour)
		if m := len(f.taken()); m != n {
			t.Errorf("%d calls an hour after the block, want %d", m, n)
		}
	})
}
