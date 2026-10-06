package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// registerPattern matches a route registration whose pattern is a string literal:
// mux.Handle("/api/tools", h) and mux.HandleFunc("GET /api/knowledge", h) alike.
var registerPattern = regexp.MustCompile(`\.Handle(?:Func)?\("([^"]+)"`)

// wildcardSegment and wildcardTail rewrite a pattern's wildcards into a concrete
// path so a synthesized request can address the route.
var (
	wildcardTail    = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\.\.\.\}`)
	wildcardSegment = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]*\}`)
)

// registeredAPIPatterns scans internal/ for every route pattern registered with a string
// literal and returns the API ones. Read off the SOURCE so a new route is covered unasked.
func registeredAPIPatterns(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range registerPattern.FindAllStringSubmatch(string(src), -1) {
			pat := m[1]
			if !strings.Contains(pat, apiPathPrefix) || seen[pat] {
				continue
			}
			seen[pat] = true
			out = append(out, pat)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal/ for route registrations: %v", err)
	}
	// kiroRescanPath is registered through its constant, which the literal scan cannot see.
	if !seen[kiroRescanPath] {
		out = append(out, kiroRescanPath)
	}
	// A floor: a regexp that stopped matching would make every assertion vacuous.
	if len(out) < 60 {
		t.Fatalf("scan found %d API routes, want at least 60 — the pattern no longer matches", len(out))
	}
	return out
}

// requestFor turns a registered pattern into a method and a concrete path that
// addresses it.
func requestFor(pattern string) (method, path string) {
	method = http.MethodGet
	path = pattern
	if m, rest, ok := strings.Cut(pattern, " "); ok {
		method, path = m, rest
	}
	path = wildcardTail.ReplaceAllString(path, "probe/probe")
	path = wildcardSegment.ReplaceAllString(path, "probe")
	path = strings.TrimSuffix(path, "{$}")
	if strings.HasSuffix(path, "/") {
		path += "probe"
	}
	return method, path
}

// TestAPIFallback_ShadowsNoRegisteredRoute pins, per route against the live source, that
// the /api/ subtree fallback out-ranks no registered route.
func TestAPIFallback_ShadowsNoRegisteredRoute(t *testing.T) {
	patterns := registeredAPIPatterns(t)

	mux := http.NewServeMux()
	mux.Handle("/", http.NotFoundHandler())
	registerAPIFallback(mux)
	for _, pat := range patterns {
		mux.Handle(pat, http.NotFoundHandler())
	}

	for _, pat := range patterns {
		method, path := requestFor(pat)
		req := httptest.NewRequest(method, "http://127.0.0.1:9847"+path, http.NoBody)
		_, matched := mux.Handler(req)
		if matched != pat {
			t.Errorf("%s %s routed to %q, want %q — the /api/ fallback shadows a real route",
				method, path, matched, pat)
		}
	}
}

// TestAPIFallback_AnswersAnUnmatchedAPIPath pins that an unclaimed /api/ path is not
// answered 200 with index.html by the "/" SPA mount, which a machine sender reads as success.
func TestAPIFallback_AnswersAnUnmatchedAPIPath(t *testing.T) {
	const spaStatus = 299 // a status no marotte handler produces

	mux := http.NewServeMux()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(spaStatus)
	}))
	registerAPIFallback(mux)
	mux.HandleFunc("GET /api/knowledge", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := map[string]struct {
		method string
		path   string
	}{
		"a path no route claims":                   {http.MethodGet, "/api/nope"},
		"a method a method-scoped route refuses":   {http.MethodPut, "/api/knowledge"},
		"a subtree of a path no route claims":      {http.MethodDelete, "/api/knowledge/x/y/z"},
		"the surface's own root, with no redirect": {http.MethodGet, "/api"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, "http://127.0.0.1:9847"+tc.path, http.NoBody))

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want 404 (the SPA mount used to answer %d with index.html)",
					tc.method, tc.path, rec.Code, spaStatus)
			}
			// A 3xx is the other silent success for a non-following caller, hence /api beside /api/.
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("Location = %q, want unset", got)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unknown endpoint"}` {
				t.Errorf("body = %q, want marotte's bare error envelope", got)
			}
		})
	}

	// The control: a fallback over the whole subtree would pass every case above.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9847/api/knowledge", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/knowledge = %d, want 200 from its own handler", rec.Code)
	}
}
