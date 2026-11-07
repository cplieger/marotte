package agent

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/testsupport"
)

// unattendedFailureReasons are the reasons an unattended run's failure is counted under.
var unattendedFailureReasons = []string{
	"needs_approval", "overran", "backstop", "stalled", "paused", "orphaned", "cancel_given_up",
}

// failureCounts reads marotte_unattended_run_failures_total for every unattended origin and reason, keyed
// "origin/reason".
func failureCounts(t *testing.T) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, origin := range []runlease.Origin{runlease.OriginScheduled, runlease.OriginTriggered} {
		for _, reason := range unattendedFailureReasons {
			out[string(origin)+"/"+reason] = testsupport.MetricValue(t,
				`marotte_unattended_run_failures_total{origin="`+string(origin)+`",reason="`+reason+`"}`)
		}
	}
	return out
}

// wantFailureDeltas checks that, since before, the series in want moved by their value, every other one stayed, and
// no run someone launched was counted at all.
func wantFailureDeltas(t *testing.T, before, want map[string]float64) {
	t.Helper()
	for key, n := range failureCounts(t) {
		if d := n - before[key]; d != want[key] {
			t.Errorf("unattended run failures %s moved by %v, want %v", key, d, want[key])
		}
	}
	for _, origin := range []runlease.Origin{runlease.OriginManual, runlease.OriginAgent} {
		for _, reason := range unattendedFailureReasons {
			series := `marotte_unattended_run_failures_total{origin="` + string(origin) + `",reason="` + reason + `"}`
			if v, ok := testsupport.MetricSample(t, series); ok {
				t.Errorf("a run someone launched was counted: %s = %v", series, v)
			}
		}
	}
}

// awaitFailureDelta waits for the failure series key to move by want since before, for a count written off the
// test's goroutine.
func awaitFailureDelta(t *testing.T, before map[string]float64, key string, want float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for failureCounts(t)[key]-before[key] < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func unattendedApprovals(t *testing.T, origin runlease.Origin) float64 {
	t.Helper()
	return testsupport.MetricValue(t, `marotte_unattended_approvals_total{origin="`+string(origin)+`"}`)
}

// scheduledLease is the lease a scheduled fire of the rule scheduleID grants run workflowID.
func scheduledLease(scheduleID, workflowID string) *runlease.Lease {
	return &runlease.Lease{
		WorkflowID: workflowID, ScheduleID: scheduleID, Origin: runlease.OriginScheduled, Unattended: true,
	}
}

var (
	presetFailureOrigins  = testsupport.PresetLabelValues("marotte_unattended_run_failures_total", "origin")
	presetApprovalOrigins = testsupport.PresetLabelValues("marotte_unattended_approvals_total", "origin")
)

// The unattended counters are pre-set for exactly the origins a lease marks unattended: a missing origin's first
// failure is missed by increase(), and an extra one is a series no run moves.
func TestUnattendedCounters_PresetEveryOriginALeaseMarksUnattended(t *testing.T) {
	h := &Runs{chats: newTestChatStore()}
	launches := map[string]launchOrigin{
		"wf_scheduled": scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}),
		"wf_triggered": eventLaunch(),
		"wf_manual":    manualLaunch(),
		"wf_agent":     agentLaunch(),
	}
	unattended := map[string]bool{}
	for wf, o := range launches {
		h.grantLease(t.Context(), wf, "publish", o, hostSec())
		l, held := h.lease(wf)
		if !held {
			t.Fatalf("no lease for %s", wf)
		}
		if l.Unattended {
			unattended[string(l.Origin)] = true
		}
	}
	want := slices.Sorted(maps.Keys(unattended))
	for family, got := range map[string][]string{
		"marotte_unattended_run_failures_total": presetFailureOrigins,
		"marotte_unattended_approvals_total":    presetApprovalOrigins,
	} {
		if got = slices.Sorted(slices.Values(got)); !slices.Equal(got, want) {
			t.Errorf("%s is pre-set for origins %v, want the unattended ones %v", family, got, want)
		}
	}
}
