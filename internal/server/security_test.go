package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cplieger/webhttp/v3"
)

func helloMux() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestSecurityMiddleware_SetsCSP(t *testing.T) {
	h := securityMiddleware(baseCSPPolicy, nil, helloMux())
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP missing default-src: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options not set")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSecurityMiddleware_OriginCheck(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		origin   string
		wantCode int
	}{
		{"GET cross-origin allowed", http.MethodGet, "http://attacker.example", http.StatusOK},
		{"POST same-origin allowed", http.MethodPost, "http://example.com", http.StatusOK},
		{"POST missing origin allowed", http.MethodPost, "", http.StatusOK},
		{"POST cross-origin blocked", http.MethodPost, "http://attacker.example", http.StatusForbidden},
		{"PUT cross-origin blocked", http.MethodPut, "http://attacker.example", http.StatusForbidden},
		{"PATCH cross-origin blocked", http.MethodPatch, "http://attacker.example", http.StatusForbidden},
		{"DELETE cross-origin blocked", http.MethodDelete, "http://attacker.example", http.StatusForbidden},
	}

	h := securityMiddleware(baseCSPPolicy, nil, helloMux())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.method != http.MethodGet {
				body = strings.NewReader("")
			}
			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tc.method, "http://example.com/x", body)
			} else {
				req = httptest.NewRequest(tc.method, "http://example.com/", http.NoBody)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("got status %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestSecurityMiddleware_HostAllowlist pins the ALLOWED_HOSTS
// anti-DNS-rebinding gate inside the real security middleware: a rebinding
// attack makes an attacker-controlled hostname resolve to this server, so
// Origin and Host AGREE and the CSRF layer alone admits the request — the
// exact-Host allowlist must reject it (with the baseline security headers
// still applied), while an allowed Host passes through to the CSRF check
// (which still rejects a forged cross-origin POST). The loopback peer+Host
// carve-out keeps the image's own healthcheck working under a browser-facing
// allowlist, a forged loopback Host from a remote peer stays rejected, and a
// nil policy is a pass-through (unset ALLOWED_HOSTS stays backward
// compatible).
func TestSecurityMiddleware_HostAllowlist(t *testing.T) {
	policy, invalid := webhttp.ParseHostList([]string{"marotte.example.com"},
		webhttp.WithLoopbackExempt(true),
		webhttp.WithHostAllowlistError("",
			"host not allowed; add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	h := securityMiddleware(baseCSPPolicy, policy, helloMux())

	do := func(method, host, origin, remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+"/x", strings.NewReader(""))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("rebound host rejected even though Origin agrees", func(t *testing.T) {
		rec := do(http.MethodPost, "attacker.evil:8080", "http://attacker.evil:8080", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (Origin/Host agreement must not admit a rebound host)", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ALLOWED_HOSTS") {
			t.Errorf("403 body = %q, want it to name ALLOWED_HOSTS", rec.Body.String())
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("host-gate 403 lost the baseline security headers")
		}
	})

	t.Run("allowed host passes through to the handler", func(t *testing.T) {
		rec := do(http.MethodPost, "marotte.example.com", "http://marotte.example.com", "192.168.1.50:44444")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("allowed host still gets the CSRF check", func(t *testing.T) {
		rec := do(http.MethodPost, "marotte.example.com", "http://attacker.evil", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (the host gate must not swallow the cross-origin rejection)", rec.Code)
		}
	})

	t.Run("healthcheck shape: loopback peer + loopback Host admitted", func(t *testing.T) {
		rec := do(http.MethodGet, "127.0.0.1:8080", "", "127.0.0.1:54321")
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (the loopback carve-out must keep the container healthcheck working)", rec.Code)
		}
	})

	t.Run("forged loopback Host from remote peer rejected", func(t *testing.T) {
		rec := do(http.MethodGet, "127.0.0.1:8080", "", "192.168.1.50:44444")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (a remote peer forging a loopback Host must not ride the carve-out)", rec.Code)
		}
	})

	t.Run("nil policy is a pass-through", func(t *testing.T) {
		open := securityMiddleware(baseCSPPolicy, nil, helloMux())
		req := httptest.NewRequest(http.MethodGet, "http://anything.example/x", http.NoBody)
		rec := httptest.NewRecorder()
		open.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (unset ALLOWED_HOSTS must stay backward compatible)", rec.Code)
		}
	})
}

func BenchmarkSecurityMiddleware(b *testing.B) {
	h := securityMiddleware(baseCSPPolicy, nil, helloMux())

	b.Run("GET_headers_only", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", http.NoBody)
		rec := httptest.NewRecorder()
		b.ReportAllocs()
		for b.Loop() {
			rec.Body.Reset()
			h.ServeHTTP(rec, req)
		}
	})

	b.Run("POST_same_origin", func(b *testing.B) {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/chat", http.NoBody)
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		b.ReportAllocs()
		for b.Loop() {
			rec.Body.Reset()
			h.ServeHTTP(rec, req)
		}
	})
}

// scriptSrc returns the script-src directive's source list, or fails the test
// when the policy has none.
func scriptSrc(t *testing.T, policy string) string {
	t.Helper()
	for part := range strings.SplitSeq(policy, "; ") {
		if v, ok := strings.CutPrefix(part, "script-src "); ok {
			return v
		}
	}
	t.Fatalf("policy %q has no script-src directive", policy)
	return ""
}

// TestBuildCSPPolicy_ScriptSrcIsSelfOnly: a page whose only scripts are
// external files gets script-src 'self' exactly — no hash token and no
// 'unsafe-inline', either of which would admit an inline script.
func TestBuildCSPPolicy_ScriptSrcIsSelfOnly(t *testing.T) {
	html := []byte(`<html><head><script src="/prepaint.js"></script></head>` +
		`<script type="module" src="/app.js"></script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}

	policy, err := buildCSPPolicy(staticFS)
	if err != nil {
		t.Fatalf("buildCSPPolicy(external scripts only) = %v, want nil", err)
	}
	if got := scriptSrc(t, policy); got != "'self'" {
		t.Errorf("script-src = %q, want exactly 'self'", got)
	}
	if strings.Contains(policy, "sha256-") {
		t.Errorf("policy %q carries a hash token, want none", policy)
	}
	if strings.Contains(scriptSrc(t, policy), "'unsafe-inline'") {
		t.Errorf("script-src %q allows inline scripts", scriptSrc(t, policy))
	}
}

// TestBuildCSPPolicy_RefusesAnInlineScript: script-src 'self' would block an
// inline script at runtime, so construction fails and names the count rather
// than serving a page whose script never runs.
func TestBuildCSPPolicy_RefusesAnInlineScript(t *testing.T) {
	html := []byte(`<html><script>console.log("unreviewed")</script>` +
		`<script src="/prepaint.js"></script></html>`)
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	_, err := buildCSPPolicy(staticFS)
	if err == nil {
		t.Fatal("buildCSPPolicy(one inline script) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "1 inline script") {
		t.Errorf("buildCSPPolicy error = %q, want it to name the inline-script count", err)
	}
}

// TestBuildCSPPolicy_RealEmbeddedHTML: the committed index.html carries no
// inline script, so the real startup path builds the policy.
func TestBuildCSPPolicy_RealEmbeddedHTML(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", "static", "index.html"))
	if err != nil {
		t.Fatalf("read static/index.html: %v", err)
	}
	if n := len(webhttp.InlineScriptHashes(html)); n != 0 {
		t.Fatalf("static/index.html carries %d inline script(s), want 0", n)
	}
	staticFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: html}}
	policy, err := buildCSPPolicy(staticFS)
	if err != nil {
		t.Fatalf("buildCSPPolicy over the real index.html: %v", err)
	}
	if got := scriptSrc(t, policy); got != "'self'" {
		t.Errorf("script-src = %q, want exactly 'self'", got)
	}
}

func TestBuildCSPPolicy_NilFS(t *testing.T) {
	if _, err := buildCSPPolicy(nil); err == nil {
		t.Error("expected error for nil FS")
	}
}

func FuzzSecurityMiddleware_OriginCheck(f *testing.F) {
	f.Add("POST", "http://example.com", "example.com")
	f.Add("GET", "http://attacker.example", "example.com")
	f.Add("POST", "http://attacker.example", "example.com")
	f.Add("POST", "", "example.com")
	f.Add("DELETE", "http://example.com:8080", "example.com")
	f.Add("PUT", "null", "example.com")
	f.Add("PATCH", "http://example.com", "example.com:443")

	h := securityMiddleware(baseCSPPolicy, nil, helloMux())

	f.Fuzz(func(t *testing.T, method, origin, host string) {
		if method == "" {
			return
		}
		// Skip methods that would cause httptest.NewRequest to panic
		// (contains spaces, control characters, or other invalid bytes).
		for _, b := range []byte(method) {
			if b <= 0x20 || b == 0x7f {
				t.Skip("invalid HTTP method character")
			}
		}
		if host == "" {
			t.Skip("empty host")
		}
		for _, b := range []byte(host) {
			if b < 0x20 || b == 0x7f {
				t.Skip("control char in host")
			}
		}
		req, err := http.NewRequest(method, "http://"+host+"/x", http.NoBody)
		if err != nil {
			t.Skip("invalid request:", err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		// Invariant 1: no panics (implicit).
		// Invariant 2: GET always 200.
		if method == http.MethodGet && rec.Code != http.StatusOK {
			t.Errorf("GET with origin=%q got %d, want 200", origin, rec.Code)
		}
	})
}

func TestCSPPolicy_StructuralInvariants(t *testing.T) {
	policy := baseCSPPolicy
	directives := make(map[string]string)
	for part := range strings.SplitSeq(policy, "; ") {
		fields := strings.SplitN(part, " ", 2)
		name := fields[0]
		if _, dup := directives[name]; dup {
			t.Errorf("duplicate directive: %s", name)
		}
		val := ""
		if len(fields) > 1 {
			val = fields[1]
		}
		directives[name] = val
	}

	required := []string{"default-src", "script-src", "style-src", "connect-src", "frame-ancestors", "img-src", "font-src"}
	for _, d := range required {
		if _, ok := directives[d]; !ok {
			t.Errorf("missing required directive: %s", d)
		}
	}

	if fa := directives["frame-ancestors"]; fa != "'none'" {
		t.Errorf("frame-ancestors = %q, want 'none'", fa)
	}

	if ds := directives["default-src"]; ds != "'self'" {
		t.Errorf("default-src = %q, want 'self'", ds)
	}

	if ss := directives["script-src"]; ss != "'self'" {
		t.Errorf("script-src = %q, want 'self'", ss)
	}
}
