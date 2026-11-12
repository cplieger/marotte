package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/obs"
)

// metricsStack is the production route table and middleware stack with the
// metrics page wired and one allowed host.
func metricsStack(t *testing.T) http.Handler {
	t.Helper()
	policy, invalid := httpserver.ParseHostList([]string{"marotte.example.com"},
		httpserver.WithLoopbackExempt(true),
		httpserver.WithHostAllowlistError("", "host not allowed. Add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	s := docServer(t.TempDir(), WithMetrics(obs.Handler()), WithHostPolicy(policy))
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	return httpserver.Chain(s.routes(), s.middlewareStack(baseCSPPolicy, idem)...)
}

func scrapeMetrics(h http.Handler, method, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+"/metrics", http.NoBody)
	req.RemoteAddr = "192.168.1.50:44444"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsRoute_ServesTheExpositionNotTheApp(t *testing.T) {
	rec := scrapeMetrics(metricsStack(t), http.MethodGet, "marotte.example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; version=0.0.4; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q (the single-page app answered instead)", got, want)
	}
	if body := rec.Body.String(); !strings.Contains(body, "# TYPE go_goroutines gauge\n") {
		t.Errorf("body lacks the go_goroutines family; got %.200q", body)
	}
}

func TestMetricsRoute_RefusesAHostOutsideTheAllowlist(t *testing.T) {
	rec := scrapeMetrics(metricsStack(t), http.MethodGet, "scraper.elsewhere")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "# TYPE") {
		t.Error("a refused host was served the metrics page")
	}
}

func TestMetricsRoute_RefusesAWriteMethod(t *testing.T) {
	rec := scrapeMetrics(metricsStack(t), http.MethodPost, "marotte.example.com")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Errorf("Allow = %q, want GET", got)
	}
}

// A scrape every 15 to 60 seconds at INFO would bury the access log, so its
// access line logs at DEBUG and a refused one still surfaces through the host
// gate's Warn.
func TestMetricsRoute_LogsAtProbeLevel(t *testing.T) {
	logs := captureLogs(t)
	h := metricsStack(t)

	t.Run("served", func(t *testing.T) {
		logs.Reset()
		scrapeMetrics(h, http.MethodGet, "marotte.example.com")
		line := accessLine(t, logs.String())
		if !strings.Contains(line, "level=DEBUG") {
			t.Errorf("a served scrape logged %q, want level=DEBUG", line)
		}
	})

	t.Run("refused", func(t *testing.T) {
		logs.Reset()
		scrapeMetrics(h, http.MethodGet, "scraper.elsewhere")
		out := logs.String()
		if line := accessLine(t, out); !strings.Contains(line, "level=DEBUG") {
			t.Errorf("a refused scrape's access line is %q, want level=DEBUG", line)
		}
		if !strings.Contains(out, `level=WARN msg="httpserver: host not allowed"`) {
			t.Errorf("a refused scrape logged no host-gate Warn; log:\n%s", out)
		}
	})
}

// accessLine returns the one access-log line for /metrics in out.
func accessLine(t *testing.T, out string) string {
	t.Helper()
	var found []string
	for line := range strings.Lines(out) {
		if strings.Contains(line, "msg=http ") && strings.Contains(line, "path=/metrics") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d access lines for /metrics, want 1; log:\n%s", len(found), out)
	}
	return found[0]
}
