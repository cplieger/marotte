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

// sample is series' value on the page, and whether the page has it.
func sample(series string) (string, bool) {
	for line := range strings.Lines(scrape().Body.String()) {
		if v, ok := strings.CutPrefix(line, series+" "); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
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
