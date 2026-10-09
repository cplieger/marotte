package preview

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
)

const testHost = "box.test:9847"

type fixture struct {
	h    *Handler
	mux  *http.ServeMux
	ws   string
	demo string
	now  time.Time
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ws := t.TempDir()
	outside := t.TempDir()
	demo := filepath.Join(ws, "demo")
	write(t, filepath.Join(demo, "index.html"), `<!doctype html><html><head><meta name="marotte-preview" content="phone"></head><body>hi</body></html>`)
	write(t, filepath.Join(demo, "app.js"), "console.log(1)")
	write(t, filepath.Join(demo, "style.css"), "body{}")
	write(t, filepath.Join(demo, "data.json"), `{"a":1}`)
	write(t, filepath.Join(demo, "img.png"), "\x89PNG0123456789")
	write(t, filepath.Join(demo, ".env"), "SECRET=1")
	write(t, filepath.Join(demo, "sub", "inner.html"), "<p>inner")
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	write(t, filepath.Join(outside, "index.html"), "<p>outside")
	write(t, filepath.Join(ws, "root.html"), "<p>root")
	write(t, filepath.Join(demo, "notes.txt"), "notes")
	if err := os.Mkdir(filepath.Join(ws, "dir.html"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlink(t, ".env", filepath.Join(demo, "link.txt"))
	symlink(t, outside, filepath.Join(demo, "escape"))
	symlink(t, "sub", filepath.Join(demo, "subalias"))
	symlink(t, "..", filepath.Join(demo, "up"))
	symlink(t, outside, filepath.Join(ws, "linked"))
	symlink(t, ".", filepath.Join(ws, "alias"))
	now := time.Unix(1_700_000_000, 0)
	h := New(ws, filebrowse.Sensitive{}, newTestSigner(t, now), slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &fixture{h: h, mux: mux, ws: ws, demo: demo, now: now}
}

func (f *fixture) do(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = testHost
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) grant(t *testing.T, p string) marotte.PreviewGrant {
	t.Helper()
	body, _ := json.Marshal(marotte.PreviewGrantRequest{Path: p})
	rec := f.do(http.MethodPost, "/api/preview/grant", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("grant(%q) = %d %s, want 200", p, rec.Code, rec.Body)
	}
	var g marotte.PreviewGrant
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("grant(%q) body: %v", p, err)
	}
	return g
}

func (f *fixture) grantStatus(p string) (int, string) {
	body, _ := json.Marshal(marotte.PreviewGrantRequest{Path: p})
	rec := f.do(http.MethodPost, "/api/preview/grant", string(body))
	return rec.Code, rec.Body.String()
}

func TestGrant_Answers(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	if !strings.HasPrefix(g.URL, g.Base) || !strings.HasSuffix(g.URL, "/index.html") || !strings.HasPrefix(g.Base, PathPrefix) {
		t.Errorf("grant URL = %q base = %q, want base + index.html under %s", g.URL, g.Base, PathPrefix)
	}
	if g.Epoch != f.h.signer.Epoch() || !g.ExpiresAt.Equal(f.now.Add(grantTTL)) {
		t.Errorf("grant epoch/expiry = %q %v, want %q %v", g.Epoch, g.ExpiresAt, f.h.signer.Epoch(), f.now.Add(grantTTL))
	}
	if g.Hint == nil || g.Hint.Preset != marotte.PreviewPresetPhone {
		t.Errorf("grant hint = %+v, want phone", g.Hint)
	}
}

func TestGrant_Refusals(t *testing.T) {
	f := newFixture(t)
	long := filepath.Join(f.ws, strings.Repeat("d", 520), "index.html")
	cases := []struct {
		name, path string
		status     int
	}{
		{"relative", "demo/index.html", 400},
		{"outside", "/etc/x.html", 403},
		{"sibling of the workspace", f.ws + "x/demo/index.html", 403},
		{"workspace root", filepath.Join(f.ws, "root.html"), 400},
		{"too long", long, 400},
		{"missing", filepath.Join(f.demo, "nope.html"), 404},
		{"directory named .html", filepath.Join(f.ws, "dir.html", "x.html"), 404},
		{"folder symlink escaping", filepath.Join(f.ws, "linked", "index.html"), 404},
		{"folder symlink inside", filepath.Join(f.ws, "alias", "demo", "index.html"), 404},
		{"dot component", filepath.Join(f.demo, ".git", "x.html"), 400},
		{"backslash", filepath.Join(f.demo, `a\b.html`), 400},
		{"non-HTML", filepath.Join(f.demo, "app.js"), 400},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			if got, body := f.grantStatus(tc.path); got != tc.status {
				t.Errorf("grant(%s) = %d %s, want %d", tc.name, got, body, tc.status)
			}
		})
	}
	_, body := f.grantStatus(filepath.Join(f.ws, "root.html"))
	if !strings.Contains(body, "its own folder under "+f.ws) {
		t.Errorf("root refusal = %s, want the sentence naming %s and its own folder", body, f.ws)
	}
	if rec := f.do(http.MethodPost, "/api/preview/grant", "{"); rec.Code != 400 {
		t.Errorf("grant(malformed JSON) = %d, want 400", rec.Code)
	}
	rec := f.do(http.MethodGet, "/api/preview/grant", "")
	if rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET grant = %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("grant response carries Access-Control-Allow-Origin")
	}
}

// testdata/page-shapes.json is the page-shape contract preview-page.test.ts reads too: the client
// offers a preview only for a path this handler grants. Its paths are rooted at /workspace, its
// config_dir included. A deny_listed row is a well-formed page the deny list alone refuses, so its
// page exists and the answer must be 403.
func TestGrant_PageShapesMatchTheFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/page-shapes.json")
	if err != nil {
		t.Fatal(err)
	}
	var shapes struct {
		ConfigDir string `json:"config_dir"`
		Pages     []struct {
			Path        string `json:"path"`
			Previewable bool   `json:"previewable"`
			DenyListed  bool   `json:"deny_listed"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(raw, &shapes); err != nil {
		t.Fatal(err)
	}
	if shapes.ConfigDir == "" || len(shapes.Pages) == 0 {
		t.Fatalf("page-shapes.json = %d pages under config_dir %q, want both", len(shapes.Pages), shapes.ConfigDir)
	}
	ws := newFixture(t).ws
	rooted := func(p string) string {
		if rest, ok := strings.CutPrefix(p, "/workspace"); ok {
			return ws + rest
		}
		return p
	}
	f := newDenyFixture(t, ws, rooted(shapes.ConfigDir))
	for i, row := range shapes.Pages {
		p := rooted(row.Path)
		t.Run("row"+strconv.Itoa(i), func(t *testing.T) {
			if row.Previewable || row.DenyListed {
				write(t, p, "<p>page")
			}
			got, body := f.grantStatus(p)
			switch {
			case row.Previewable && row.DenyListed:
				t.Errorf("row %q is both previewable and deny_listed", row.Path)
			case row.Previewable && got != http.StatusOK:
				t.Errorf("grant(%q) = %d %s, want 200", row.Path, got, body)
			case row.DenyListed && got != http.StatusForbidden:
				t.Errorf("grant(%q) = %d %s, want the deny list's 403", row.Path, got, body)
			case !row.Previewable && got != http.StatusBadRequest && got != http.StatusForbidden:
				t.Errorf("grant(%q) = %d %s, want a 400 or 403 shape refusal", row.Path, got, body)
			}
		})
	}
}

func TestGrant_EscapedNamesRoundTrip(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"my page.html", "a#b?.html", "100%.html", "x+y&z=1@$:,;.html", "café-日本.html", "a%2fb.html"} {
		write(t, filepath.Join(f.demo, "names", name), "page "+name)
		g := f.grant(t, filepath.Join(f.demo, "names", name))
		if g.URL != g.Base+url.PathEscape(name) {
			t.Errorf("grant(%q).URL = %q, want base + PathEscape", name, g.URL)
		}
		rec := f.do(http.MethodGet, g.URL, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "page "+name) || !strings.Contains(rec.Body.String(), shimMarker) {
			t.Errorf("GET %q = %d %q, want the page with the shim", g.URL, rec.Code, rec.Body)
		}
	}
	write(t, filepath.Join(f.demo, "names", "my page.png"), "img")
	g := f.grant(t, filepath.Join(f.demo, "names", "my page.html"))
	if rec := f.do(http.MethodGet, g.Base+"my%20page.png", ""); rec.Code != 200 || rec.Body.String() != "img" {
		t.Errorf("GET my%%20page.png = %d %q, want the image", rec.Code, rec.Body)
	}
}

func TestGrant_WorkspaceRootSpellings(t *testing.T) {
	f := newFixture(t)
	page := filepath.Join(f.demo, "index.html")
	for _, root := range []string{"/", f.ws + "/"} {
		t.Run(strings.ReplaceAll(root, "/", "_"), func(t *testing.T) {
			g := &fixture{h: New(root, filebrowse.Sensitive{}, newTestSigner(t, f.now), slog.New(slog.DiscardHandler)), mux: http.NewServeMux()}
			g.h.RegisterRoutes(g.mux)
			grant := g.grant(t, page)
			if rec := g.do(http.MethodGet, grant.URL, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), shimMarker) {
				t.Errorf("GET page under root %q = %d, want 200 with the shim", root, rec.Code)
			}
			if rec := g.do(http.MethodGet, "/api/preview/stamp?path="+url.QueryEscape(page), ""); rec.Code != 200 {
				t.Errorf("stamp under root %q = %d %s, want 200", root, rec.Code, rec.Body)
			}
		})
	}
	g := &fixture{h: New("/", filebrowse.Sensitive{}, newTestSigner(t, f.now), slog.New(slog.DiscardHandler)), mux: http.NewServeMux()}
	g.h.RegisterRoutes(g.mux)
	if code, body := g.grantStatus("/index.html"); code != 400 || !strings.Contains(body, "its own folder under /") {
		t.Errorf("grant(/index.html) under root / = %d %s, want 400 naming its own folder", code, body)
	}
}

func TestServe_HeadersAndInjection(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	rec := f.do(http.MethodGet, g.URL, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), shimMarker) {
		t.Fatalf("GET index = %d, want 200 with the shim", rec.Code)
	}
	tok := strings.TrimSuffix(strings.TrimPrefix(g.Base, PathPrefix), "/")
	src := "http://" + testHost + PathPrefix + tok + "/"
	want := map[string]string{
		"Content-Security-Policy": "sandbox allow-scripts allow-forms allow-modals; default-src 'none'; script-src " + src +
			" https: 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob:; style-src " + src + " https: 'unsafe-inline'; img-src " + src +
			" https: data: blob:; font-src " + src + " https: data:; media-src " + src + " https: data: blob:; connect-src " + src +
			"; worker-src " + src + " blob:; manifest-src 'none'; object-src 'none'; frame-src 'none'; base-uri " + src +
			"; form-action " + src + "; frame-ancestors 'self'",
		"X-Frame-Options": "SAMEORIGIN", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store",
		"Access-Control-Allow-Origin": "*", "Referrer-Policy": "no-referrer", "Content-Type": "text/html; charset=utf-8",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	head := f.do(http.MethodHead, g.URL, "")
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("HEAD index = body %d, Content-Length %q, want 0 and %d", head.Body.Len(), head.Header().Get("Content-Length"), rec.Body.Len())
	}
}

func TestServe_NonHTMLIsByteIdentical(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	for name, ctype := range map[string]string{"style.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "img.png": "image/png", "data.json": "application/json"} {
		want, _ := os.ReadFile(filepath.Join(f.demo, name))
		rec := f.do(http.MethodGet, g.Base+name, "")
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) || rec.Header().Get("Content-Type") != ctype {
			t.Errorf("GET %s = %d %q %q, want the file's bytes as %s", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body, ctype)
		}
	}
	rec := f.do(http.MethodGet, g.Base+"img.png", "", "Range", "bytes=0-3")
	if rec.Code != 206 || rec.Body.String() != "\x89PNG" {
		t.Errorf("Range img.png = %d %q, want 206 and four bytes", rec.Code, rec.Body)
	}
	head := f.do(http.MethodHead, g.Base+"img.png", "")
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "14" {
		t.Errorf("HEAD img.png = %d bytes, Content-Length %q, want 0 and 14", head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

func TestServe_Refusals(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	f.h.signer.now = func() time.Time { return f.now }
	expired, err := f.h.Grant(filepath.Join(f.demo, "index.html"), f.now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tampered := g.Base[:len(PathPrefix)+2] + "Z" + g.Base[len(PathPrefix)+3:]
	if tampered == g.Base {
		tampered = g.Base[:len(PathPrefix)+2] + "Y" + g.Base[len(PathPrefix)+3:]
	}
	cases := []struct {
		name, target string
		status       int
	}{
		{"bad token", PathPrefix + "nope/index.html", 403},
		{"tampered token", tampered + "index.html", 403},
		{"expired token", expired.URL, 403},
		{"empty rel", g.Base, 404},
		{"trailing slash", g.Base + "sub/", 404},
		{"dotfile", g.Base + ".env", 404},
		{"nested dot", g.Base + "sub/.git/config", 404},
		{"symlinked file", g.Base + "link.txt", 404},
		{"symlinked dir", g.Base + "subalias/inner.html", 404},
		{"symlink to parent", g.Base + "up/root.html", 404},
		{"symlink escaping", g.Base + "escape/secret.txt", 404},
		{"dir", g.Base + "sub", 404},
		{"dotdot", g.Base + "..", 400},
		{"dot", g.Base + ".", 400},
		{"encoded dotdot", g.Base + "%2e%2e/x", 400},
		{"encoded slash", g.Base + "a%2Fb", 400},
		{"encoded backslash", g.Base + "a%5cb", 400},
		{"NUL", g.Base + "a%00b", 400},
		{"empty segment", g.Base + "sub//inner.html", 400},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			u, err := url.Parse(tc.target)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.target, err)
			}
			req.URL, req.Host = u, testHost
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Errorf("GET %s = %d %s, want %d", tc.target, rec.Code, rec.Body, tc.status)
			}
			if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox ") {
				t.Errorf("GET %s refusal lacks the preview header set", tc.target)
			}
		})
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := f.do(m, g.URL, "")
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s preview = %d Allow %q, want 405 GET, HEAD", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	req := httptest.NewRequest(http.MethodGet, g.URL, http.NoBody)
	req.Host = "bad host"
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 400 || strings.Contains(rec.Header().Get("Content-Security-Policy"), "http://") {
		t.Errorf("bad Host = %d CSP %q, want 400 with no source in the policy", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
}

func TestServe_RefusesAFIFOAndOversizeHTML(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	if err := syscall.Mkfifo(filepath.Join(f.demo, "pipe.txt"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if rec := f.do(http.MethodGet, g.Base+"pipe.txt", ""); rec.Code != 404 {
		t.Errorf("GET FIFO = %d, want 404", rec.Code)
	}
	big := filepath.Join(f.demo, "big.html")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxHTMLBytes+1); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, g.Base+"big.html", ""); rec.Code != 404 {
		t.Errorf("GET 8 MiB+ page = %d, want 404", rec.Code)
	}
}

func TestServe_RefusesAnAssetOverTheFileCeiling(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	big := filepath.Join(f.demo, "big.bin")
	write(t, big, "")
	if err := os.Truncate(big, maxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, g.Base+"big.bin", ""); rec.Code != 404 || rec.Body.Len() > 1024 {
		t.Errorf("GET 64 MiB+ asset = %d with %d body bytes, want a short 404", rec.Code, rec.Body.Len())
	}
}

func TestServe_AnOverlongTokenIsRefusedWithNoSourceInThePolicy(t *testing.T) {
	f := newFixture(t)
	rec := f.do(http.MethodGet, PathPrefix+strings.Repeat("a", tokenMaxLen+1)+"/index.html", "")
	if rec.Code != 403 {
		t.Errorf("GET with a %d-byte token = %d, want 403", tokenMaxLen+1, rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "http://") {
		t.Errorf("overlong-token CSP = %q, want no granted source", csp)
	}
}

func TestServe_AnOverlongHostIsRefusedWithNoSourceInThePolicy(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	label := strings.Repeat("a", 63) + "."
	atBound := strings.Repeat(label, 3) + strings.Repeat("b", maxHostBytes-3*len(label)-len(":9847")) + ":9847"
	for _, tc := range []struct {
		name   string
		host   string
		status int
	}{
		{"at the bound", atBound, 200},
		{"one past the bound", "x" + atBound, 400},
		{"near a MiB", strings.Repeat("a", 1<<20-64), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, g.URL, http.NoBody)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			csp := rec.Header().Get("Content-Security-Policy")
			if rec.Code != tc.status {
				t.Errorf("GET with a %d-byte Host = %d, want %d", len(tc.host), rec.Code, tc.status)
			}
			if tc.status == 200 {
				if !strings.Contains(csp, "http://"+tc.host+PathPrefix) {
					t.Errorf("CSP for a %d-byte Host lacks the granted source", len(tc.host))
				}
				return
			}
			if strings.Contains(csp, "http://") || len(csp) > 1024 {
				t.Errorf("CSP for a %d-byte Host is %d bytes and carries a source, want a source-free policy", len(tc.host), len(csp))
			}
			if strings.Contains(rec.Body.String(), tc.host[:64]) {
				t.Errorf("refusal body echoes the Host: %.120s", rec.Body)
			}
		})
	}
}

func TestServe_ASwappedComponentIsRefused(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "sub", "inner.html"))
	sub := filepath.Join(f.demo, "sub")
	if err := os.Rename(sub, filepath.Join(f.demo, "sub-real")); err != nil {
		t.Fatal(err)
	}
	symlink(t, f.ws, sub)
	if rec := f.do(http.MethodGet, g.Base+"inner.html", ""); rec.Code != 404 {
		t.Errorf("GET after the granted folder became a symlink = %d, want 404", rec.Code)
	}
	if rec := f.do(http.MethodGet, g.Base+"root.html", ""); rec.Code != 404 {
		t.Errorf("GET a workspace file through the swapped folder = %d, want 404", rec.Code)
	}
}

func TestServe_AFileGrowingAfterTheSizeCheckIsServedAtTheCheckedSize(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t, filepath.Join(f.demo, "index.html"))
	grow := filepath.Join(f.demo, "grow.bin")
	write(t, grow, "0123456789")
	t.Cleanup(func() { sizeAccepted = func() {} })
	sizeAccepted = func() {
		fh, err := os.OpenFile(grow, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = fh.Close() }()
		if _, err := fh.WriteString("appended-after-the-check"); err != nil {
			t.Error(err)
		}
	}
	rec := f.do(http.MethodGet, g.Base+"grow.bin", "")
	if rec.Code != 200 || rec.Body.String() != "0123456789" || rec.Header().Get("Content-Length") != "10" {
		t.Errorf("GET grown file = %d %q Content-Length %q, want 200 \"0123456789\" 10", rec.Code, rec.Body, rec.Header().Get("Content-Length"))
	}
}

func TestHandler_UnopenableWorkspaceAnswers503(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "missing"), filebrowse.Sensitive{}, newTestSigner(t, time.Now()), slog.New(slog.DiscardHandler))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, PathPrefix + "t/x.html", ""},
		{http.MethodPost, "/api/preview/grant", `{"path":"/w/d/x.html"}`},
		{http.MethodGet, "/api/preview/stamp?path=/w/d/x.html", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		req.Host = testHost
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.target, rec.Code)
		}
	}
}

func TestHandler_AWorkspaceThatOpensButCannotBeResolvedAnswers503(t *testing.T) {
	evalSymlinks = func(string) (string, error) { return "", errors.New("resolve failed") }
	t.Cleanup(func() { evalSymlinks = filepath.EvalSymlinks })
	var logs bytes.Buffer
	h := New(t.TempDir(), filebrowse.Sensitive{}, newTestSigner(t, time.Now()), slog.New(slog.NewTextHandler(&logs, nil)))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/preview/grant", strings.NewReader(`{"path":"/w/d/x.html"}`))
	req.Host = testHost
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("grant over a workspace root that opened but did not resolve = %d, want 503", rec.Code)
	}
	if !strings.Contains(logs.String(), "workspace root could not be resolved") {
		t.Errorf("the resolve failure logged:\n%s\nwant the could-not-be-resolved warning", logs.String())
	}
}

func TestGrant_ReExtractsTheHint(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.demo, "index.html")
	if g := f.grant(t, p); g.Hint == nil || g.Hint.Preset != "phone" {
		t.Fatalf("first hint = %+v, want phone", g.Hint)
	}
	write(t, p, `<meta name="marotte-preview" content="desktop">`)
	if g := f.grant(t, p); g.Hint == nil || g.Hint.Preset != "desktop" {
		t.Errorf("second hint = %+v, want desktop", g.Hint)
	}
}
