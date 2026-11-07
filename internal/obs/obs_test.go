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
