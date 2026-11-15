package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/route"
)

// TestAPIFallback_AnswersAnUnmatchedAPIPath pins that an unclaimed /api/ path is not
// answered 200 with index.html by the "/" SPA mount, which a machine sender reads as success.
func TestAPIFallback_AnswersAnUnmatchedAPIPath(t *testing.T) {
	const spaStatus = 299 // a status no marotte handler produces

	mux := http.NewServeMux()
	g := NewRegistrar(mux, nil)
	g.Bind(route.AnyMethod("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(spaStatus)
	})))
	registerAPIFallback(g)
	g.Bind(route.JSON[struct{}, struct{}]("GET /api/knowledge", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

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
