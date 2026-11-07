package schedule

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// slotsNotStarted reads every event's count of schedule slots that produced no run.
func slotsNotStarted(t *testing.T) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, event := range []string{"launch_failed", "signin_held", "engine_busy"} {
		out[event] = testsupport.MetricValue(t, `marotte_schedule_slots_not_started_total{event="`+event+`"}`)
	}
	return out
}

func failedSlotsSeries(id string) string {
	return `marotte_schedule_failed_slots{rule_id="` + id + `"}`
}

func nextDueSeries(id string) string {
	return `marotte_schedule_next_due_timestamp_seconds{rule_id="` + id + `"}`
}

// A weekly and a monthly schedule have no interval to measure lateness by, so each exports the time its next slot
// falls due.
func TestSweep_AWeeklyAndAMonthlyScheduleExportTheirNextSlot(t *testing.T) {
	st := mustStore(t)
	anchor := at(2026, time.August, 4, 12, 0) // a Tuesday
	weekly := putClock(t, st, clockSeed{
		Source: "bundled://weekly", Anchor: anchor,
		Spec: launchrule.Spec{Freq: launchrule.FreqWeekly, Weekdays: []int{int(time.Monday)}, Hour: 2},
	})
	monthly := putClock(t, st, clockSeed{
		Source: "bundled://monthly", Anchor: anchor,
		Spec: launchrule.Spec{Freq: launchrule.FreqMonthly, MonthDay: 15, Hour: 3, Minute: 30},
	})
	r := NewRunner(st, &fakeLauncher{})
	r.now = func() time.Time { return at(2026, time.August, 5, 9, 0) }
	r.sweep(t.Context())

	for name, tc := range map[string]struct {
		id   string
		want time.Time
	}{
		"weekly, the Monday after the anchor": {weekly.ID, at(2026, time.August, 10, 2, 0)},
		"monthly, the 15th after the anchor":  {monthly.ID, at(2026, time.August, 15, 3, 30)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := testsupport.MetricValue(t, nextDueSeries(tc.id)); got != float64(tc.want.Unix()) {
				t.Errorf("next due = %v, want %v (%v)", got, tc.want.Unix(), tc.want)
			}
			if got := testsupport.MetricValue(t, failedSlotsSeries(tc.id)); got != 0 {
				t.Errorf("failed slots = %v, want 0 before any slot", got)
			}
		})
	}
}

// Three slots in a row that produced no run set the count to 3; a slot whose run completes sets it back to 0. The
// count is the row's, so it survives a restart.
func TestSweep_ThreeFailedSlotsCountThreeAndASuccessResetsIt(t *testing.T) {
	dir := t.TempDir()
	st, l, r := newFixtureIn(t, dir, time.Time{})
	id := st.Clocks()[0].ID
	l.failWith(errors.New("workflow new: no such recipe"))
	before := slotsNotStarted(t)
	for day := 4; day <= 6; day++ {
		r.now = func() time.Time { return at(2026, time.August, day, 2, 0).Add(30 * time.Second) }
		r.sweep(t.Context())
	}
	if got := testsupport.MetricValue(t, failedSlotsSeries(id)); got != 3 {
		t.Errorf("failed slots after three failed launches = %v, want 3", got)
	}
	if got := slotsNotStarted(t)["launch_failed"] - before["launch_failed"]; got != 3 {
		t.Errorf("launch_failed slots moved by %v, want 3", got)
	}

	// A restart: the count is the row's, so the new runner exports it before any slot.
	reopened, err := launchrule.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	l.failWith(nil)
	r = NewRunner(reopened, l)
	r.now = func() time.Time { return at(2026, time.August, 7, 2, 0).Add(30 * time.Second) }
	r.sweep(t.Context())
	if got := testsupport.MetricValue(t, failedSlotsSeries(id)); got != 3 {
		t.Errorf("failed slots while the fourth slot's run is going = %v, want 3", got)
	}
	run := launchrule.Run{ScheduleID: id, WorkflowID: reopened.Clocks()[0].LastRunID}
	if err := reopened.RecordEnding(t.Context(), run, launchrule.Outcome{Status: launchrule.StatusCompleted}); err != nil {
		t.Fatalf("RecordEnding: %v", err)
	}
	r.now = func() time.Time { return at(2026, time.August, 7, 2, 1).Add(30 * time.Second) }
	r.sweep(t.Context())
	if got := testsupport.MetricValue(t, failedSlotsSeries(id)); got != 0 {
		t.Errorf("failed slots after a completed run = %v, want 0", got)
	}
}

// pageHas reports whether one scrape of the metrics page carries series, read off the handler directly so a
// goroutine other than the test's can call it.
func pageHas(series string) bool {
	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, series+" ") {
			return true
		}
	}
	return false
}

// A schedule deleted or disabled loses its series at the next sweep, and only its own: a scrape running beside the
// sweep never finds a live schedule's series missing.
func TestSweep_ADeletedScheduleLosesOnlyItsSeriesWhileAScrapeRuns(t *testing.T) {
	st := mustStore(t)
	daily := launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}
	kept := putClock(t, st, clockSeed{Source: "bundled://kept", Spec: daily})
	deleted := putClock(t, st, clockSeed{Source: "bundled://deleted", Spec: daily})
	disabled := putClock(t, st, clockSeed{Source: "bundled://disabled", Spec: daily})
	r := NewRunner(st, &fakeLauncher{})
	r.now = func() time.Time { return at(2026, time.August, 5, 9, 0) }
	r.sweep(t.Context())
	for _, id := range []string{kept.ID, deleted.ID, disabled.ID} {
		if !pageHas(failedSlotsSeries(id)) || !pageHas(nextDueSeries(id)) {
			t.Fatalf("schedule %s has no series after the first sweep", id)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var missing, scrapes int
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			scrapes++
			if !pageHas(failedSlotsSeries(kept.ID)) || !pageHas(nextDueSeries(kept.ID)) {
				missing++
			}
		}
	})
	if err := st.Delete(t.Context(), deleted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	off := disabled
	off.Enabled = false
	if _, err := st.Replace(t.Context(), disabled.ID, &off, 0); err != nil {
		t.Fatalf("Replace(disabled): %v", err)
	}
	r.sweep(t.Context())
	if !pageHas(failedSlotsSeries(kept.ID)) || !pageHas(nextDueSeries(kept.ID)) {
		t.Errorf("the sweep that took the deleted and disabled schedules off the page took the kept one too")
	}
	for name, id := range map[string]string{"deleted": deleted.ID, "disabled": disabled.ID} {
		for _, s := range []string{failedSlotsSeries(id), nextDueSeries(id)} {
			if pageHas(s) {
				t.Errorf("the %s schedule still has %s", name, s)
			}
		}
	}
	for range 200 {
		r.sweep(t.Context())
	}
	close(stop)
	wg.Wait()
	if missing > 0 {
		t.Errorf("%d of %d scrapes beside the sweeps found the kept schedule's series missing", missing, scrapes)
	}
}

// The runner exports every schedule's series when it starts, before its first tick, so a restart leaves no minute
// in which a schedule has none.
func TestRun_ExportsTheSeriesBeforeItsFirstTick(t *testing.T) {
	st := mustStore(t)
	rule := putClock(t, st, clockSeed{Source: "bundled://boot", Spec: launchrule.Spec{Freq: launchrule.FreqDaily, Hour: 2}})
	r := NewRunner(st, &fakeLauncher{})
	r.tick = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !pageHas(nextDueSeries(rule.ID)) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !pageHas(nextDueSeries(rule.ID)) {
		t.Errorf("the runner exported no %s before its first tick", nextDueSeries(rule.ID))
	}
}
