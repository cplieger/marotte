package trigger

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// blockReasons are the reasons the blocked series names, the alert rules' label values.
var blockReasons = []string{"launch_failed", "unnamed_run", "too_many_items", "state_unreadable", "state_unwritten"}

// blocked answers the rule's blocked series by reason, and false when the page has none of them.
func blocked(t *testing.T, ruleID string) (map[string]float64, bool) {
	t.Helper()
	out := map[string]float64{}
	for _, reason := range blockReasons {
		v, ok := testsupport.MetricSample(t, `marotte_trigger_rule_blocked{reason="`+reason+`",rule_id="`+ruleID+`"}`)
		if !ok {
			return nil, false
		}
		out[reason] = v
	}
	return out, true
}

func wantBlocked(t *testing.T, ruleID string, want ...string) {
	t.Helper()
	got, ok := blocked(t, ruleID)
	if !ok {
		t.Fatalf("no blocked series for rule %s, want one per reason", ruleID)
	}
	for _, reason := range blockReasons {
		if w := float64(boolInt(slices.Contains(want, reason))); got[reason] != w {
			t.Errorf("blocked{reason=%q} = %v, want %v", reason, got[reason], w)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ruleGauge(t *testing.T, family, ruleID string) (float64, bool) {
	t.Helper()
	return testsupport.MetricSample(t, family+`{rule_id="`+ruleID+`"}`)
}

// abandon abandons the rule's queued k1, launched as wf, at the bound.
func abandon(tc *testConfirmer, ruleID, wf string) {
	tc.name(ruleID, "k1", wf)
	tc.LaunchReturned(ruleID, wf, nil)
	advance(launchConfirmBound + launchBackoff[0])
}

// A rule its launches blocked shows launch_failed until it is switched off and on; a rule switched off or deleted
// shows no series.
func TestBlockedRuleSeries(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.enqueue(r.ID, held("k1", "c1"))
		abandon(tc, r.ID, "wf_1")
		wantBlocked(t, r.ID)
		abandon(tc, r.ID, "wf_2")
		wantBlocked(t, r.ID, "launch_failed")

		if err := changeRule(t, tc.Confirmer, r.ID, 0, func(r *launchrule.Rule) { r.Enabled = false }); err != nil {
			t.Fatalf("pause: %v", err)
		}
		if _, ok := blocked(t, r.ID); ok {
			t.Error("a paused rule still shows its blocked series")
		}
		if err := changeRule(t, tc.Confirmer, r.ID, 0, func(r *launchrule.Rule) { r.Enabled = true }); err != nil {
			t.Fatalf("resume: %v", err)
		}
		wantBlocked(t, r.ID)
		deleteRule(t, tc.Confirmer, r.ID)
		if _, ok := blocked(t, r.ID); ok {
			t.Error("a deleted rule still shows its blocked series")
		}
		if _, ok := ruleGauge(t, "marotte_trigger_queue_full", r.ID); ok {
			t.Error("a deleted rule still shows its queue full series")
		}
	})
}

// A run no rule launched that holds the recipe past the bound shows unnamed_run until a run list no longer shows
// it.
func TestUnnamedRunSeries(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.busy(r.ID, "wf_9")
		wantBlocked(t, r.ID)
		advance(launchConfirmBound)
		tc.busy(r.ID, "wf_9")
		wantBlocked(t, r.ID, "unnamed_run")
		tc.PruneAbandoned(r.ID, map[string]marotte.RunStatus{})
		wantBlocked(t, r.ID)
	})
}

// A state file write that keeps failing shows state_unwritten until a write lands.
func TestUnwrittenStateSeries(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		r := tc.createRule(runSpec("success"))
		tc.failWrites(errDiskFull)
		tc.RuleChanged(r.ID, launchrule.ChangeResume)
		wantBlocked(t, r.ID, "state_unwritten")
		tc.failWrites(nil)
		advance(writeTick)
		wantBlocked(t, r.ID)
	})
}

// A poll that leaves a match at the forge shows the queue full until a poll queues every match.
func TestQueueFullSeries(t *testing.T) {
	h := newHarness(t)
	r := h.create(fifo(labeledSpec(), 2))
	h.issuePage(issue(1, "a"), issue(2, "a"), issue(3, "a"))
	h.poll()
	if v, ok := ruleGauge(t, "marotte_trigger_queue_full", r.ID); !ok || v != 0 {
		t.Errorf("queue full after the baseline = %v (%v), want 0", v, ok)
	}
	all := []forgeapi.Issue{issue(1, "a", testLabel), issue(2, "a", testLabel), issue(3, "a", testLabel)}
	h.issuePage(all...)
	h.poll()
	if v, _ := ruleGauge(t, "marotte_trigger_queue_full", r.ID); v != 1 {
		t.Errorf("queue full after #3 was left at the forge = %v, want 1", v)
	}
	h.drain(r.ID)
	h.issuePage(all...)
	h.poll()
	if v, _ := ruleGauge(t, "marotte_trigger_queue_full", r.ID); v != 0 {
		t.Errorf("queue full after #3 was queued = %v, want 0", v)
	}
}

// The rule's poll failures count its list's failed polls in a row, and a good poll sets them back to 0.
func TestPollFailuresSeries(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage()
	h.poll()
	h.src.errs = map[string]error{listIssues: &forgeapi.Error{Kind: forgeapi.KindTransient, Status: 502}}
	for range 3 {
		h.poll()
	}
	if v, _ := ruleGauge(t, "marotte_trigger_poll_failures", r.ID); v != 3 {
		t.Errorf("poll failures after three failed polls = %v, want 3", v)
	}
	h.src.errs = nil
	h.poll()
	if v, ok := ruleGauge(t, "marotte_trigger_poll_failures", r.ID); !ok || v != 0 {
		t.Errorf("poll failures after a good poll = %v (%v), want 0", v, ok)
	}
}

// A catch-up gap counts once under its cause, and every cause a row records is pre-set.
func TestCatchUpGapCounted(t *testing.T) {
	for _, cause := range []string{launchrule.GapOutage, launchrule.GapAgedOut, launchrule.GapResultWindow} {
		if _, ok := testsupport.MetricSample(t, `marotte_trigger_catch_up_gaps_total{cause="`+cause+`"}`); !ok {
			t.Errorf("no catch-up gap series for cause %s, want every cause a row records pre-set", cause)
		}
	}
	series := `marotte_trigger_catch_up_gaps_total{cause="` + launchrule.GapOutage + `"}`
	before := testsupport.MetricValue(t, series)
	h, id, runs := overForge(t, runSpec("success"))
	for _, at := range []time.Duration{40 * time.Hour, 80 * time.Hour} {
		h.pollAt(pollStart.Add(at))
	}
	last := pollStart.Add(100 * time.Hour)
	h.pollAt(last)
	back := last.Add(60 * time.Hour)
	*runs = append(*runs, run(41, forgeapi.CheckPassing, back.Add(-time.Hour)))
	h.pollAt(back)
	h.poll()
	if g := h.gaps(id); len(g) != 1 {
		t.Fatalf("gaps = %+v, want one outage", g)
	}
	if d := testsupport.MetricValue(t, series) - before; d != 1 {
		t.Errorf("the outage counter moved by %v, want 1", d)
	}
}
