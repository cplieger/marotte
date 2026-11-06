package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// inBubble runs f with a fresh testConfirmer inside a synctest bubble, so the bound's timers run on its clock.
func inBubble(t *testing.T, f func(t *testing.T, tc *testConfirmer)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		f(t, newTestConfirmer(t))
	})
}

// relaunchable reports whether the launcher may start key again: queued, with no live record and not superseded.
func (tc *testConfirmer) relaunchable(ruleID, key string) bool {
	rv := tc.rule(ruleID)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	i := slices.IndexFunc(rv.es.queue, func(e queueEntry) bool { return e.Key == key })
	if i < 0 || rv.es.queue[i].Live != "" {
		return false
	}
	if slices.ContainsFunc(rv.es.superseded, func(e supersededEntry) bool { return e.Key == key }) {
		return false
	}
	return !slices.ContainsFunc(rv.records, func(r *record) bool { return r.key == key && r.state.live() })
}

// pollerOn is a poll harness over tc's store; poll before the Confirmer first touches a rule, which it loads once.
func (tc *testConfirmer) pollerOn() *harness {
	return &harness{t: tc.t, store: tc.store, src: &fakeSource{}, marks: newLaunchMarks(), now: pollStart, dir: tc.dir}
}

// refusal is an in-band invoke refusal KAS words in its data, as a -32603 does.
var refusal = &marotte.RPCError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"session not idle"}`)}

func TestInBandInvokeRefusalAbandonsAtOnce(t *testing.T) {
	refuse := func(tc *testConfirmer) string {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.setStatus(r.ID, func(st *launchrule.Status) { st.BoundAbandons = 1 })
		tc.name(r.ID, "k1", "wf_1")
		tc.LaunchReturned(r.ID, "wf_1", refusal)
		return r.ID
	}
	t.Run("the attempt is abandoned, cancelled and counted on the 24-hour clock", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			id := refuse(tc)
			rec := tc.recordOf(id, "wf_1")
			if rec == nil || rec.state != stateAbandonedCancelling || rec.cause != causeInvokeError {
				t.Fatalf("record = %+v, want abandoned-cancelling with cause invoke_error", rec)
			}
			if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
				t.Errorf("cancels = %v, want one for wf_1", got)
			}
			if !tc.relaunchable(id, "k1") || !tc.woken() {
				t.Error("k1 is not launchable again with the launcher woken, want it queued with no live record")
			}
			_, st := tc.memory(id)
			if !st.FailingSince.Equal(time.Now()) || st.BoundAbandons != 1 || st.Blocked != "" {
				t.Errorf("failing since %v, count %d, blocked %q; want the clock started now, the count unchanged at 1, no block",
					st.FailingSince, st.BoundAbandons, st.Blocked)
			}
			if want := "the launch of run wf_1 failed: session not idle; retrying"; !strings.HasPrefix(st.RowText, want) {
				t.Errorf("row text = %q, want it to start %q", st.RowText, want)
			}
			if got, _ := tc.log.AttrValueExact(logMsgLaunchAbandoned, "cause"); got != causeInvokeError {
				t.Errorf("INFO %q cause = %q, want invoke_error", logMsgLaunchAbandoned, got)
			}
		})
	})
	t.Run("the event launches again after the backoff's first step", func(t *testing.T) { t.Skip("needs 5.9") })
	t.Run("a refusal 24 hours into the clock blocks the rule", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			since := time.Now().Add(-24 * time.Hour)
			tc.setStatus(r.ID, func(st *launchrule.Status) { st.FailingSince = since })
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.name(r.ID, "k1", "wf_1")
			tc.LaunchReturned(r.ID, "wf_1", refusal)
			_, st := tc.memory(r.ID)
			if st.Blocked != "session not idle" || !st.FailingSince.Equal(since) {
				t.Errorf("blocked %q, failing since %v; want the last error as the block and the clock's start kept", st.Blocked, st.FailingSince)
			}
			if got, _ := tc.log.AttrValueExact(logMsgRunNotStarted, "class"); got != "blocked" {
				t.Errorf("WARN %q class = %q, want blocked", logMsgRunNotStarted, got)
			}
		})
	})
}

func TestDeadlineInvokeErrorWaitsForBound(t *testing.T) {
	for name, lost := range map[string]error{"deadline": context.DeadlineExceeded, "bridge exited": marotte.ErrBridgeExited} {
		t.Run(name+" then a run_start 2 s later confirms", func(t *testing.T) {
			inBubble(t, func(t *testing.T, tc *testConfirmer) {
				r := tc.createRule(runSpec("success"))
				tc.enqueue(r.ID, held("k1", "c1"))
				tc.name(r.ID, "k1", "wf_1")
				tc.LaunchReturned(r.ID, "wf_1", lost)
				advance(2 * time.Second)
				tc.ConfirmEvent(r.ID, "k1", "wf_1")
				advance(launchConfirmBound)
				es, _ := tc.memory(r.ID)
				if got := tc.cancels(); len(got) != 0 || len(es.queue) != 0 || len(es.launched) != 1 || tc.woken() {
					t.Errorf("cancels %v, queue %v, launched %+v; want no cancel, k1 launched once and nothing to launch again",
						got, keysOf(es.queue), es.launched)
				}
			})
		})
		t.Run(name+" and no run_start: abandoned at the bound, blocked at the second", func(t *testing.T) {
			inBubble(t, func(t *testing.T, tc *testConfirmer) {
				r := tc.createRule(runSpec("success"))
				tc.enqueue(r.ID, held("k1", "c1"))
				tc.name(r.ID, "k1", "wf_1")
				tc.LaunchReturned(r.ID, "wf_1", lost)
				advance(launchConfirmBound - time.Second)
				if got := tc.cancels(); len(got) != 0 {
					t.Fatalf("cancels before 5 minutes = %v, want none", got)
				}
				advance(2 * time.Second)
				if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.cause != causeBound {
					t.Fatalf("record at the bound = %+v, want a cause=bound abandon", rec)
				}
				tc.name(r.ID, "k1", "wf_2")
				tc.LaunchReturned(r.ID, "wf_2", lost)
				advance(launchConfirmBound + time.Second)
				_, st := tc.memory(r.ID)
				if st.Blocked == "" || st.BoundAbandons != 2 {
					t.Errorf("blocked %q, count %d; want the second expiry to block the rule", st.Blocked, st.BoundAbandons)
				}
			})
		})
	}
}

func TestInvokeErrorAfterConfirmNotCounted(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.name(r.ID, "k1", "wf_1")
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		tc.LaunchReturned(r.ID, "wf_1", refusal)
		es, st := tc.memory(r.ID)
		if !st.FailingSince.IsZero() || st.Blocked != "" || len(tc.cancels()) != 0 {
			t.Errorf("failing since %v, blocked %q, cancels %v; want nothing counted or cancelled", st.FailingSince, st.Blocked, tc.cancels())
		}
		if len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" {
			t.Errorf("launched = %+v, want k1 kept under wf_1", es.launched)
		}
	})
}

func TestUnconfirmedAttemptAbandonedAtBound(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.name(r.ID, "k1", "wf_1")
		tc.LaunchReturned(r.ID, "wf_1", nil)
		advance(launchConfirmBound + time.Second)
		if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
			t.Errorf("cancels = %v, want wf_1 once", got)
		}
		_, st := tc.memory(r.ID)
		if want := "run wf_1 was not confirmed within 5 minutes; cancelled it and launching the event again"; st.RowText != want {
			t.Errorf("row text = %q, want %q", st.RowText, want)
		}
		if got, _ := tc.log.AttrValueExact(logMsgLaunchAbandoned, "cause"); got != causeBound {
			t.Errorf("INFO %q cause = %q, want bound", logMsgLaunchAbandoned, got)
		}
		if !tc.relaunchable(r.ID, "k1") || !tc.woken() {
			t.Error("k1 is not launchable again with the launcher woken")
		}
	})
}

func TestRunStartBeforeReturnConfirms(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.name(r.ID, "k1", "wf_1")
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		tc.LaunchReturned(r.ID, "wf_1", nil)
		es, _ := tc.memory(r.ID)
		if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_1" || tc.woken() {
			t.Errorf("queue %v, launched %+v; want k1 confirmed under wf_1 and the launcher not woken", keysOf(es.queue), es.launched)
		}
	})
}

func TestBoundExpiryLosesToConfirm(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.name(r.ID, "k1", "wf_1")
		tc.LaunchReturned(r.ID, "wf_1", nil)
		advance(launchConfirmBound - time.Millisecond)
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		// The timer that lost the mutex delivers after the confirmation.
		tc.deliver(r.ID, &attemptEvent{kind: evBoundReached, workflowID: "wf_1"})
		advance(time.Second)
		es, st := tc.memory(r.ID)
		if got := tc.cancels(); len(got) != 0 || len(es.queue) != 0 || len(es.launched) != 1 || st.BoundAbandons != 0 || tc.woken() {
			t.Errorf("cancels %v, queue %v, launched %d, count %d; want no cancel, no relaunch, k1 launched once",
				got, keysOf(es.queue), len(es.launched), st.BoundAbandons)
		}
	})
}

func TestLateRunStartAfterAbandonDoesNotConfirm(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.abandonAtBound(r.ID, "k1", "wf_1")
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		es, _ := tc.memory(r.ID)
		if len(es.launched) != 0 || !tc.relaunchable(r.ID, "k1") {
			t.Errorf("launched %+v, k1 launchable %v; want nothing confirmed and k1 still queued", es.launched, tc.relaunchable(r.ID, "k1"))
		}
		if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
			t.Errorf("cancels = %v, want wf_1 once", got)
		}
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelling {
			t.Errorf("record = %+v, want wf_1 still abandoned-cancelling", rec)
		}
	})
}

func TestAbandonedRunStartDuringNextLaunchIgnored(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		tc.abandonAtBound(r.ID, "k1", "wf_1")
		tc.name(r.ID, "k1", "wf_2")
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		es, _ := tc.memory(r.ID)
		if rec := tc.recordOf(r.ID, "wf_2"); rec == nil || rec.state != stateInvoking || len(es.launched) != 0 ||
			len(es.queue) != 1 || es.queue[0].Live != "wf_2" {
			t.Fatalf("after the old run's run_start: wf_2 %+v, queue %+v, launched %+v; want wf_2 still the live attempt of k1",
				rec, es.queue, es.launched)
		}
		tc.ConfirmEvent(r.ID, "k1", "wf_2")
		es, _ = tc.memory(r.ID)
		if len(es.queue) != 0 || len(es.launched) != 1 || es.launched[0].WorkflowID != "wf_2" {
			t.Errorf("queue %v, launched %+v; want k1 confirmed under wf_2", keysOf(es.queue), es.launched)
		}
		if rec := tc.recordOf(r.ID, "wf_1"); rec == nil || rec.state != stateAbandonedCancelling {
			t.Errorf("wf_1 = %+v, want its abandoned record kept until its cancel answers", rec)
		}
		if got := tc.cancels(); !slices.Equal(got, []string{"wf_1"}) {
			t.Errorf("cancels = %v, want wf_1 once", got)
		}
	})
}

// abandonAtBound names key as wf, returns the call nil and lets the bound expire.
func (tc *testConfirmer) abandonAtBound(ruleID, key, wf string) {
	tc.t.Helper()
	tc.name(ruleID, key, wf)
	tc.LaunchReturned(ruleID, wf, nil)
	advance(launchConfirmBound + time.Second)
}

func TestTwoBoundAbandonsBlockRule(t *testing.T) {
	t.Run("two bound abandons of one key block the rule", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			if _, st := tc.memory(r.ID); st.BoundAbandons != 1 || st.Blocked != "" {
				t.Fatalf("after one abandon: count %d, blocked %q; want 1 and no block", st.BoundAbandons, st.Blocked)
			}
			tc.abandonAtBound(r.ID, "k1", "wf_2")
			_, st := tc.memory(r.ID)
			want := "run wf_2 did not report its start twice; marotte stopped retrying this event. Check the engine, then resume the rule"
			if st.BoundAbandons != 2 || st.Blocked != want || st.RowText != want {
				t.Errorf("count %d, blocked %q, row %q; want 2 and the rule blocked with %q", st.BoundAbandons, st.Blocked, st.RowText, want)
			}
			if n := tc.log.CountExact(logMsgRunNotStarted); n != 1 {
				t.Errorf("%d WARN %q lines, want 1", n, logMsgRunNotStarted)
			}
		})
	})
	t.Run("on a newest rule the count survives the supersede", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(newest())
			tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			if tc.relaunchable(r.ID, "k1") {
				t.Fatal("k1 is launchable after the abandon, want it superseded by k2")
			}
			tc.abandonAtBound(r.ID, "k2", "wf_2")
			if _, st := tc.memory(r.ID); st.BoundAbandons != 2 || st.Blocked == "" {
				t.Errorf("count %d, blocked %q; want 2 across the supersede and the rule blocked", st.BoundAbandons, st.Blocked)
			}
		})
	})
	t.Run("a confirmation resets the count", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			tc.name(r.ID, "k1", "wf_2")
			tc.ConfirmEvent(r.ID, "k1", "wf_2")
			if _, st := tc.memory(r.ID); st.BoundAbandons != 0 {
				t.Errorf("count = %d after a confirmation, want 0", st.BoundAbandons)
			}
		})
	})
	t.Run("an abandon of another cause neither adds nor resets", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			tc.name(r.ID, "k1", "wf_2")
			tc.LaunchReturned(r.ID, "wf_2", refusal)
			if _, st := tc.memory(r.ID); st.BoundAbandons != 1 {
				t.Errorf("count = %d after an invoke refusal, want 1", st.BoundAbandons)
			}
		})
	})
	t.Run("the operator's resume resets the count", func(t *testing.T) { t.Skip("needs 5.4") })
	t.Run("an edit resets the count", func(t *testing.T) { t.Skip("needs 5.4") })
	t.Run("an abandon after a rebaseline leaves the rule unblocked at 1", func(t *testing.T) { t.Skip("needs 5.4") })
	t.Run("a filter.label change with an attempt in flight keeps it", func(t *testing.T) { t.Skip("needs 5.4") })
	t.Run("a filter.label change after the second abandon lifts the block", func(t *testing.T) { t.Skip("needs 5.4") })
	t.Run("a boot expiry neither adds nor resets", func(t *testing.T) { t.Skip("needs 5.5") })
}

// launchHead names and confirms the rule's queue head as wf, answering the event_id its record and its launched
// entry carry.
func (tc *testConfirmer) launchHead(ruleID, wf string) (onRecord, onLaunched string) {
	tc.t.Helper()
	es, _ := tc.memory(ruleID)
	if len(es.queue) == 0 {
		tc.t.Fatalf("rule %s queued nothing", ruleID)
	}
	key := es.queue[0].Key
	tc.name(ruleID, key, wf)
	if rec := tc.recordOf(ruleID, wf); rec != nil {
		onRecord = rec.inputs["event_id"]
	}
	tc.ConfirmEvent(ruleID, key, wf)
	es, _ = tc.memory(ruleID)
	for i := range es.launched {
		if es.launched[i].WorkflowID == wf {
			onLaunched = es.launched[i].Inputs["event_id"]
		}
	}
	return onRecord, onLaunched
}

func TestEventIDStableAcrossRulesAndRestart(t *testing.T) {
	t.Run("two rules on one run event pass one id", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h := tc.pollerOn()
			a, b := h.create(runSpec("success")), h.create(runSpec("success"))
			h.runPage()
			h.poll()
			h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute)))
			h.poll()
			want := "github.com/cplieger/animap/run/41/success"
			for _, id := range []string{a.ID, b.ID} {
				if rec, launched := tc.launchHead(id, "wf_"+id); rec != want || launched != want {
					t.Errorf("rule %s: record event_id %q, launched %q; want %q on both", id, rec, launched, want)
				}
			}
		})
	})
	t.Run("two rules on one connection pass one id for one opened item", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h := tc.pollerOn()
			a, b := h.create(openedSpec("alice")), h.create(openedSpec("alice"))
			h.issuePage(issue(10, "alice"))
			h.poll()
			h.issuePage(issue(10, "alice"), issue(11, "alice"))
			h.poll()
			want := "github.com/cplieger/animap/issue/11/opened"
			for _, id := range []string{a.ID, b.ID} {
				if rec, _ := tc.launchHead(id, "wf_"+id); rec != want {
					t.Errorf("rule %s: record event_id %q, want %q", id, rec, want)
				}
			}
		})
	})
	t.Run("a failure rule and a success rule on one run pass different ids", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h := tc.pollerOn()
			fail, ok := h.create(runSpec("failure")), h.create(runSpec("success"))
			h.runPage()
			h.poll()
			created := pollStart.Add(time.Minute)
			h.runPage(run(41, forgeapi.CheckFailing, created))
			h.poll()
			h.runPage(run(41, forgeapi.CheckPassing, created))
			h.poll()
			gotFail, _ := tc.launchHead(fail.ID, "wf_f")
			gotOK, _ := tc.launchHead(ok.ID, "wf_s")
			if gotFail != "github.com/cplieger/animap/run/41/failure" || gotOK != "github.com/cplieger/animap/run/41/success" {
				t.Errorf("event ids %q and %q, want run 41 ending in failure and in success", gotFail, gotOK)
			}
		})
	})
	t.Run("one selector on two hosts passes different ids", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h := tc.pollerOn()
			other := runSpec("success")
			other.Connection = "github:ghe.example.com"
			a, b := h.create(runSpec("success")), h.create(other)
			h.runPage()
			h.runPage()
			h.poll()
			r41 := run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute))
			h.runPage(r41)
			h.runPage(r41)
			h.poll()
			gotA, _ := tc.launchHead(a.ID, "wf_a")
			gotB, _ := tc.launchHead(b.ID, "wf_b")
			if gotA != "github.com/cplieger/animap/run/41/success" || gotB != "ghe.example.com/cplieger/animap/run/41/success" {
				t.Errorf("event ids %q and %q, want each naming its own host", gotA, gotB)
			}
		})
	})
	t.Run("a duplicate attempt after a bound abandon passes the same id", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			tc.enqueue(r.ID, held("k1", "c1"))
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			abandoned := tc.recordOf(r.ID, "wf_1")
			es, _ := tc.memory(r.ID)
			if abandoned == nil || len(es.queue) != 1 || es.queue[0].Inputs["event_id"] != abandoned.inputs["event_id"] {
				t.Fatalf("requeued %+v, abandoned record %+v; want the entry's event_id equal to the record's", es.queue, abandoned)
			}
			tc.name(r.ID, "k1", "wf_2")
			if dup := tc.recordOf(r.ID, "wf_2"); dup == nil || dup.inputs["event_id"] != abandoned.inputs["event_id"] {
				t.Errorf("duplicate attempt %+v, want the abandoned record's event_id %q", dup, abandoned.inputs["event_id"])
			}
		})
	})
	t.Run("a restart passes the same id", func(t *testing.T) { t.Skip("needs 5.5") })
	t.Run("retries pass the same id", func(t *testing.T) { t.Skip("needs 5.9") })
}

func TestLabeledEventIDDiffersAcrossRules(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		h := tc.pollerOn()
		a, b := h.create(labeledSpec()), h.create(labeledSpec())
		h.issuePage(issue(12, "alice"))
		h.poll()
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		idA, _ := tc.launchHead(a.ID, "wf_a")
		idB, _ := tc.launchHead(b.ID, "wf_b")
		if idA == idB || !strings.Contains(idA, "/"+a.ID+"/") || !strings.Contains(idB, "/"+b.ID+"/") {
			t.Errorf("event ids %q and %q, want two different ids, each carrying its rule id", idA, idB)
		}
	})
}

func TestDuplicateAfterLostRunStartCarriesSameEventID(t *testing.T) {
	t.Run("after a bound abandon the queued entry keeps its inputs", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			r := tc.createRule(runSpec("success"))
			entry := held("k1", "c1")
			entry.Inputs["run_id"] = "41"
			tc.enqueue(r.ID, entry)
			tc.abandonAtBound(r.ID, "k1", "wf_1")
			es, _ := tc.memory(r.ID)
			if len(es.queue) != 1 || !maps.Equal(es.queue[0].Inputs, entry.Inputs) {
				t.Errorf("queue = %+v, want k1 still queued with inputs %v", es.queue, entry.Inputs)
			}
		})
	})
	t.Run("the relaunch carries the same event_id", func(t *testing.T) { t.Skip("needs 5.8") })
}

func TestSupersededGainsWorkflowIDOnConfirm(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(newest())
		tc.enqueue(r.ID, held("k1", "c1"), held("k2", "c1"))
		tc.abandonAtBound(r.ID, "k1", "wf_1")
		tc.name(r.ID, "k2", "wf_2")
		tc.ConfirmEvent(r.ID, "k2", "wf_2")
		es, _ := tc.file(r.ID)
		if want := []supersededEntry{{Key: "k1", By: "k2", WorkflowID: "wf_2"}}; !slices.Equal(es.superseded, want) {
			t.Errorf("superseded = %+v, want %+v", es.superseded, want)
		}
	})
}

func TestConfirmWritesCoalesceKeyOnLaunched(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		entry := held("k1", "c1")
		tc.enqueue(r.ID, entry)
		tc.name(r.ID, "k1", "wf_1")
		tc.ConfirmEvent(r.ID, "k1", "wf_1")
		es, _ := tc.file(r.ID)
		if len(es.launched) != 1 || es.launched[0].CoalesceKey != "c1" || !maps.Equal(es.launched[0].Inputs, entry.Inputs) {
			t.Errorf("launched = %+v, want k1 with coalesce key c1 and its inputs", es.launched)
		}
	})
}

// A23: every record event for a run the rule holds no record of changes nothing and counts nothing.
func TestUnheldRunEventsChangeNothing(t *testing.T) {
	for _, ev := range []attemptEvent{
		{kind: evRunStart},
		{kind: evInvokeOK},
		{kind: evInvokeError, err: refusal},
		{kind: evInvokeReplyLost, err: context.DeadlineExceeded},
		{kind: evBoundReached},
		{kind: evCancelOK},
		{kind: evCancelError, err: errors.New("refused")},
		{kind: evBusy, launchingRule: "another rule"},
	} {
		t.Run(ev.kind.String(), func(t *testing.T) {
			inBubble(t, func(t *testing.T, tc *testConfirmer) {
				r := tc.createRule(runSpec("success"))
				tc.enqueue(r.ID, held("k1", "c1"))
				ev.workflowID = "wf_9"
				tc.deliver(r.ID, &ev)
				_, st := tc.memory(r.ID)
				if tc.recordOf(r.ID, "wf_9") != nil || len(tc.cancels()) != 0 || tc.woken() ||
					!st.FailingSince.IsZero() || st.RowText != "" || !tc.relaunchable(r.ID, "k1") {
					t.Errorf("status %+v, cancels %v; want no record, no cancel, nothing counted and k1 untouched", st, tc.cancels())
				}
				assertNoStateFile(t, tc, r.ID)
			})
		})
	}
}
