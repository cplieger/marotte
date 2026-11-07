package testsupport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/obs"
)

// metricsPage scrapes marotte's metrics page.
func metricsPage(ctx context.Context) string {
	rec := httptest.NewRecorder()
	obs.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", http.NoBody))
	return rec.Body.String()
}

// PresetLabelValues returns the label values on the page of family, a family with the one label label, in page
// order. Assigned to a package-level var it runs after obs's init and before any test moves a series, so it reads
// the values obs pre-sets and nothing a test added. It panics when the page has no series of family.
func PresetLabelValues(family, label string) []string {
	var out []string
	for line := range strings.Lines(metricsPage(context.Background())) {
		if rest, ok := strings.CutPrefix(line, family+"{"+label+`="`); ok {
			if v, _, ok := strings.Cut(rest, `"} `); ok {
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		panic("testsupport: the metrics page has no series of " + family + " by " + label)
	}
	return out
}

// MetricValue scrapes marotte's metrics page and returns one series' sample. series is written as the page writes
// it, the family name followed by any label set, such as `marotte_prompt_failures_total{class="auth"}`. It fails tb
// when the page lacks the series. The series are process-wide, so a counter test reads a delta, never an absolute, and
// no test runs in parallel with another test that moves the same series.
func MetricValue(tb testing.TB, series string) float64 {
	tb.Helper()
	v, ok := MetricSample(tb, series)
	if !ok {
		tb.Fatalf("the metrics page has no series %s", series)
	}
	return v
}

// MetricSample is MetricValue for a series that may be absent: ok is false when the page lacks it.
func MetricSample(tb testing.TB, series string) (v float64, ok bool) {
	tb.Helper()
	for line := range strings.Lines(metricsPage(tb.Context())) {
		if raw, found := strings.CutPrefix(line, series+" "); found {
			f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				tb.Fatalf("series %s has the sample %q: %v", series, raw, err)
			}
			return f, true
		}
	}
	return 0, false
}
