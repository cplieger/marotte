package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
)

// healthzStack is the production route table and middleware stack with one allowed host.
func healthzStack(t *testing.T) http.Handler {
	t.Helper()
	policy, invalid := httpserver.ParseHostList([]string{"marotte.example.com"},
		httpserver.WithLoopbackExempt(true),
		httpserver.WithHostAllowlistError("", "host not allowed. Add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	s := docServer(t.TempDir(), WithHostPolicy(policy))
	s.ready.Store(true)
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	return httpserver.Chain(s.routes(), s.middlewareStack(baseCSPPolicy, idem)...)
}

func probe(h http.Handler, method, remote, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+path, http.NoBody)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const lanPeer = "192.168.1.50:44444"

func TestHealthzRoute_AnswersGETAndHEAD(t *testing.T) {
	h := healthzStack(t)

	get := probe(h, http.MethodGet, lanPeer, "marotte.example.com", "/healthz")
	if get.Code != http.StatusOK || strings.TrimSpace(get.Body.String()) != `{"status":"ok"}` {
		t.Errorf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", get.Code, get.Body.String())
	}
	if got := get.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (the single-page app answered instead)", got)
	}

	head := probe(h, http.MethodHead, lanPeer, "marotte.example.com", "/healthz")
	if head.Code != http.StatusOK {
		t.Errorf("HEAD /healthz = %d, want 200", head.Code)
	}
}

func TestHealthzRoute_RefusesOtherMethods(t *testing.T) {
	h := healthzStack(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := probe(h, method, lanPeer, "marotte.example.com", "/healthz")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /healthz = %d, want 405", method, rec.Code)
		}
	}
}

// The old path is gone with no alias: it answers what any unknown API path answers.
func TestHealthzRoute_OldPathIsAnUnknownPath(t *testing.T) {
	h := healthzStack(t)
	old := probe(h, http.MethodGet, lanPeer, "marotte.example.com", "/api/health")
	unknown := probe(h, http.MethodGet, lanPeer, "marotte.example.com", "/api/no-such-endpoint")

	if old.Code != unknown.Code || old.Body.String() != unknown.Body.String() {
		t.Errorf("/api/health = %d %q, want what an unknown path answers: %d %q",
			old.Code, old.Body.String(), unknown.Code, unknown.Body.String())
	}
	if old.Code != http.StatusNotFound {
		t.Errorf("/api/health = %d, want 404", old.Code)
	}
}

// The allowlist still guards /healthz, and the in-container probe is always admitted.
func TestHealthzRoute_HostAllowlist(t *testing.T) {
	h := healthzStack(t)

	if rec := probe(h, http.MethodGet, lanPeer, "prober.elsewhere", "/healthz"); rec.Code != http.StatusForbidden {
		t.Errorf("an unlisted host got %d, want 403", rec.Code)
	}
	if rec := probe(h, http.MethodGet, "127.0.0.1:50000", "127.0.0.1:9847", "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("the in-container probe got %d, want 200", rec.Code)
	}
}

// curl -f reads a redirect as success, so a non-canonical spelling must not 301 to /healthz.
func TestHealthzRoute_RefusesANonCanonicalSpelling(t *testing.T) {
	h := healthzStack(t)
	for _, path := range []string{"//healthz", "/x/../healthz", "/./healthz"} {
		if rec := probe(h, http.MethodGet, lanPeer, "marotte.example.com", path); rec.Code < 400 {
			t.Errorf("GET %s = %d, want a refusal (>= 400)", path, rec.Code)
		}
	}
}

// A prober configured with a trailing slash must not read the single-page app's 200 as healthy.
func TestHealthzRoute_TrailingSlashIsAnUnknownPath(t *testing.T) {
	h := healthzStack(t)
	for _, path := range []string{"/healthz/", "/healthz/x"} {
		if rec := probe(h, http.MethodGet, lanPeer, "marotte.example.com", path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// A prober calls every 15 to 60 seconds, so a served probe logs at DEBUG.
func TestHealthzRoute_LogsAtProbeLevel(t *testing.T) {
	logs := captureLogs(t)
	h := healthzStack(t)

	probe(h, http.MethodGet, lanPeer, "marotte.example.com", "/healthz")

	var lines []string
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, "msg=http ") && strings.Contains(line, "path=/healthz") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "level=DEBUG") {
		t.Errorf("access lines for /healthz = %q, want one at level=DEBUG", lines)
	}
}
