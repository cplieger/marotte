package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/version"
)

func scrape() *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	return rec
}

// TestHandler_ServesClientGolangsDefaultFamilies names families client_golang's
// default Go and process collectors serve, which stock Go dashboards read.
func TestHandler_ServesClientGolangsDefaultFamilies(t *testing.T) {
	body := scrape().Body.String()
	for _, want := range []string{
		"# TYPE go_goroutines gauge",
		"# TYPE go_info gauge",
		"# TYPE go_threads gauge",
		"# TYPE go_gc_duration_seconds summary",
		"# TYPE go_memstats_alloc_bytes gauge",
		"# TYPE process_cpu_seconds_total counter",
		"# TYPE process_resident_memory_bytes gauge",
		"# TYPE process_start_time_seconds gauge",
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("the page lacks %q", want)
		}
	}
}

// TestCounters_EveryKnownValueIsOnThePageBeforeItsFirstEvent: increase() over a series born at its first event misses
// that event, so every cause, class and reason a rule reads is on the page from the start.
func TestCounters_EveryKnownValueIsOnThePageBeforeItsFirstEvent(t *testing.T) {
	SignInBackend("obs-test")
	body := scrape().Body.String()
	var want []string
	for _, cause := range []string{
		"restart_confirmed", "restart_auto", "shutdown", "stopped", "stdin_stalled", "parse_breaker",
		"lane_overflow", "external_stop", "crash",
	} {
		want = append(want, `marotte_engine_exits_total{cause="`+cause+`"} `)
	}
	for _, class := range []string{"fatal", "pipe_death", "busy", "transient", "throttled", "rejected", "auth"} {
		want = append(want, `marotte_prompt_failures_total{class="`+class+`"} `)
	}
	for _, reason := range []string{"staged_copy_left", "write_failed"} {
		want = append(want, `marotte_signin_save_failures_total{backend="obs-test",reason="`+reason+`"} `)
	}
	for _, origin := range []string{"scheduled", "triggered"} {
		for _, reason := range []string{
			"needs_approval", "overran", "backstop", "stalled", "paused", "orphaned", "cancel_given_up",
		} {
			want = append(want, `marotte_unattended_run_failures_total{origin="`+origin+`",reason="`+reason+`"} `)
		}
		want = append(want, `marotte_unattended_approvals_total{origin="`+origin+`"} `)
	}
	for _, event := range []string{"launch_failed", "signin_held", "engine_busy"} {
		want = append(want, `marotte_schedule_slots_not_started_total{event="`+event+`"} `)
	}
	for _, series := range want {
		if !strings.Contains(body, "\n"+series) {
			t.Errorf("the page lacks %q before any event", series)
		}
	}
}

// TestHandler_DeclaresEachFamilyOnce guards the page as a whole: a repeated
// TYPE line, from a second writer of the process families for example, makes
// Prometheus reject the entire scrape.
func TestHandler_DeclaresEachFamilyOnce(t *testing.T) {
	body := scrape().Body.String()
	seen := map[string]int{}
	for line := range strings.Lines(body) {
		if name, ok := strings.CutPrefix(line, "# TYPE "); ok {
			seen[strings.Fields(name)[0]]++
		}
	}
	if len(seen) == 0 {
		t.Fatal("the page declares no family")
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("family %s is declared %d times, want once", name, n)
		}
	}
}

// sample is series' value on the page, and whether the page has it.
func sample(series string) (string, bool) {
	for line := range strings.Lines(scrape().Body.String()) {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func TestBuildInfo_IsOneForTheRunningVersionFromTheStart(t *testing.T) {
	series := `marotte_build_info{version="` + version.Build + `"}`
	if v, ok := sample(series); !ok || v != "1" {
		t.Errorf("%s = %q (on the page %v), want 1", series, v, ok)
	}
}

func TestEngineUp_IsOneOnlyForAReadyEngine(t *testing.T) {
	EngineUp(true)
	if v, ok := sample("marotte_engine_up"); !ok || v != "1" {
		t.Errorf("marotte_engine_up after a ready engine = %q (on the page %v), want 1", v, ok)
	}
	EngineUp(false)
	if v, ok := sample("marotte_engine_up"); !ok || v != "0" {
		t.Errorf("marotte_engine_up after an engine that is not ready = %q (on the page %v), want 0", v, ok)
	}
}

// A schedule's next slot is a Unix time on the page; a slot nobody can name leaves no series rather than a 0 a
// "time() > next_due" rule would read as long overdue.
func TestSchedule_ExportsTheCountAndTheNextSlotUntilTheScheduleGoes(t *testing.T) {
	const failed, due = `marotte_schedule_failed_slots{rule_id="obs-a"}`, `marotte_schedule_next_due_timestamp_seconds{rule_id="obs-a"}`
	next := time.Date(2026, time.November, 9, 2, 0, 0, 0, time.UTC)
	Schedule("obs-a", 3, next)
	if v, ok := sample(failed); !ok || v != "3" {
		t.Errorf("%s = %q (on the page %v), want 3", failed, v, ok)
	}
	if v, ok := sample(due); !ok || v != "1794189600" {
		t.Errorf("%s = %q (on the page %v), want 1794189600", due, v, ok)
	}
	Schedule("obs-a", 0, time.Time{})
	if v, ok := sample(failed); !ok || v != "0" {
		t.Errorf("%s after a success = %q (on the page %v), want 0", failed, v, ok)
	}
	if v, ok := sample(due); ok {
		t.Errorf("%s with no nameable slot = %q, want no series", due, v)
	}
	Schedule("obs-a", 1, next)
	Schedule("obs-b", 2, next)
	ScheduleGone("obs-a")
	for _, s := range []string{failed, due} {
		if v, ok := sample(s); ok {
			t.Errorf("%s after ScheduleGone = %q, want no series", s, v)
		}
	}
	if v, ok := sample(`marotte_schedule_failed_slots{rule_id="obs-b"}`); !ok || v != "2" {
		t.Errorf("another schedule's count after ScheduleGone(obs-a) = %q (on the page %v), want 2", v, ok)
	}
}

// A ratio rule divides the resident set by the limit, so an unknown limit leaves no limit series to divide by, and a
// limit known earlier does not stay behind.
func TestEngineMemory_AnUnknownLimitLeavesNoLimitSeries(t *testing.T) {
	const rss, limit = "marotte_engine_resident_memory_bytes", "marotte_engine_heap_limit_bytes"
	EngineMemory(3<<20, 8<<20)
	if v, ok := sample(rss); !ok || v != "3145728" {
		t.Errorf("%s = %q (on the page %v), want 3145728", rss, v, ok)
	}
	if v, ok := sample(limit); !ok || v != "8388608" {
		t.Errorf("%s = %q (on the page %v), want 8388608", limit, v, ok)
	}
	EngineMemory(4<<20, 0)
	if v, ok := sample(rss); !ok || v != "4194304" {
		t.Errorf("%s with an unknown limit = %q (on the page %v), want 4194304", rss, v, ok)
	}
	if v, ok := sample(limit); ok {
		t.Errorf("%s with an unknown limit = %q, want no series", limit, v)
	}
	EngineMemory(4<<20, 8<<20)
	EngineMemoryGone()
	for _, s := range []string{rss, limit} {
		if v, ok := sample(s); ok {
			t.Errorf("%s after EngineMemoryGone = %q, want no series", s, v)
		}
	}
}

// An event rule shows every blocked reason at 0 or 1, so a rule matching one reason sees the others' series, and a
// gone rule's series leave without another rule's.
func TestTriggerRule_ExportsEveryReasonUntilTheRuleGoes(t *testing.T) {
	TriggerRule("obs-a", &TriggerRuleState{UnnamedRun: true, QueueFull: true, PollFailures: 4, CatchUpGaps: 2})
	TriggerRule("obs-b", &TriggerRuleState{StateUnwritten: true})
	for reason, want := range map[string]string{
		"launch_failed": "0", "unnamed_run": "1", "too_many_items": "0", "state_unreadable": "0", "state_unwritten": "0",
	} {
		series := `marotte_trigger_rule_blocked{reason="` + reason + `",rule_id="obs-a"}`
		if v, ok := sample(series); !ok || v != want {
			t.Errorf("%s = %q (on the page %v), want %s", series, v, ok, want)
		}
	}
	if v, ok := sample(`marotte_trigger_queue_full{rule_id="obs-a"}`); !ok || v != "1" {
		t.Errorf("queue full = %q (on the page %v), want 1", v, ok)
	}
	if v, ok := sample(`marotte_trigger_poll_failures{rule_id="obs-a"}`); !ok || v != "4" {
		t.Errorf("poll failures = %q (on the page %v), want 4", v, ok)
	}
	if v, ok := sample(`marotte_trigger_catch_up_gaps{rule_id="obs-a"}`); !ok || v != "2" {
		t.Errorf("catch-up gaps = %q (on the page %v), want 2", v, ok)
	}
	TriggerRuleGone("obs-a")
	if body := scrape().Body.String(); strings.Contains(body, `rule_id="obs-a"`) {
		t.Errorf("obs-a's series are on the page after TriggerRuleGone")
	}
	if v, ok := sample(`marotte_trigger_rule_blocked{reason="state_unwritten",rule_id="obs-b"}`); !ok || v != "1" {
		t.Errorf("another rule's series after TriggerRuleGone(obs-a) = %q (on the page %v), want 1", v, ok)
	}
}
