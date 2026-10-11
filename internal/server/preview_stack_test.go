package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/preview"
	"github.com/cplieger/webhttp/v3"
)

// previewStack is the full middleware stack over a mux holding the preview
// routes plus one SPA and one API stand-in, which is what the preview headers
// have to win against and must not leak onto.
func previewStack(t *testing.T) (http.Handler, *preview.Handler, string) {
	t.Helper()
	ws := t.TempDir()
	demo := filepath.Join(ws, "demo")
	if err := os.MkdirAll(demo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(demo, "index.html"), []byte("<p>hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := preview.NewSigner()
	if err != nil {
		t.Fatal(err)
	}
	ph := preview.New(ws, filebrowse.Sensitive{}, signer, slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("spa")) }))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) { webhttp.Ok(w) })
	ph.RegisterRoutes(mux)
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	s := &Server{}
	return webhttp.Chain(mux, s.middlewareStack(baseCSPPolicy, idem)...), ph, filepath.Join(demo, "index.html")
}

func serveStack(h http.Handler, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "box.test:9847"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPreviewStack_PreviewHeadersWinOnThePreviewRouteOnly(t *testing.T) {
	h, ph, page := previewStack(t)
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{g.URL, g.Base + "missing.png"} {
		rec := serveStack(h, http.MethodGet, target, "")
		if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox ") ||
			rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("GET %s (%d): XFO %q CSP %q ACAO %q, want the preview header set", target, rec.Code,
				rec.Header().Get("X-Frame-Options"), rec.Header().Get("Content-Security-Policy"), rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	for _, target := range []string{"/", "/api/health"} {
		rec := serveStack(h, http.MethodGet, target, "")
		if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Content-Security-Policy") != baseCSPPolicy ||
			rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("GET %s: XFO %q CSP %q ACAO %q, want the global policy untouched", target,
				rec.Header().Get("X-Frame-Options"), rec.Header().Get("Content-Security-Policy"), rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
}

func TestPreviewStack_GrantAndStampStayOrdinaryAPIResponses(t *testing.T) {
	h, _, page := previewStack(t)
	body := `{"path":"` + page + `"}`
	stamp := "/api/preview/stamp?path=" + page
	checks := []struct {
		name, method, target, body string
		hdr                        []string
		status                     int
	}{
		{"grant same-origin", http.MethodPost, "/api/preview/grant", body, []string{"Sec-Fetch-Site", "same-origin"}, 200},
		{"stamp", http.MethodGet, stamp, "", nil, 200},
		{"grant cross-site", http.MethodPost, "/api/preview/grant", body, []string{"Sec-Fetch-Site", "cross-site", "Origin", "null"}, 403},
		{"stamp cross-site", http.MethodGet, stamp, "", []string{"Sec-Fetch-Site", "cross-site", "Origin", "null"}, 403},
	}
	for _, c := range checks {
		rec := serveStack(h, c.method, c.target, c.body, c.hdr...)
		if rec.Code != c.status || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s = %d ACAO %q XFO %q, want %d, no ACAO, DENY", c.name, rec.Code,
				rec.Header().Get("Access-Control-Allow-Origin"), rec.Header().Get("X-Frame-Options"), c.status)
		}
	}
}

func TestPreviewStack_AnOverlongHostBuildsNoSourceList(t *testing.T) {
	h, ph, page := previewStack(t)
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, g.URL, http.NoBody)
	req.Host = strings.Repeat("a", 1<<20-64)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusBadRequest || strings.Contains(csp, "http://") || len(csp) > 1024 {
		t.Errorf("near-MiB Host through the stack = %d, CSP %d bytes, want 400 and a short source-free policy", rec.Code, len(csp))
	}
}

func TestPreviewStack_AccessLogOmitsTheToken(t *testing.T) {
	buf := captureLogs(t)
	h, ph, page := previewStack(t)
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	serveStack(h, http.MethodGet, g.URL, "")
	tok := strings.TrimSuffix(strings.TrimPrefix(g.Base, preview.PathPrefix), "/")
	if !strings.Contains(buf.String(), "msg=http") {
		t.Fatalf("no access-log line captured: %s", buf)
	}
	if strings.Contains(buf.String(), tok) {
		t.Errorf("access log carries the preview token: %s", buf)
	}
}
