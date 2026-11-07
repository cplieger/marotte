package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
