package server

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// Through the production stack an API read reaches its handler marked to answer at once while the engine is down, and
// a write reaches it unmarked, so it still waits for the engine.
func TestMiddleware_APIReadsDoNotWaitForTheEngine(t *testing.T) {
	cases := []struct {
		method, path string
		refused      bool
	}{
		{http.MethodGet, "/api/governance", true},
		{http.MethodGet, "/api/runs/wf-1", true},
		{http.MethodHead, "/api/config-template", true},
		{http.MethodPost, "/api/runs", false},
		{http.MethodGet, "/", false},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			var seen, reached bool
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				reached, seen = true, marotte.EngineWaitRefused(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			serveStack(apiStack(t, &Server{}, mux), c.method, c.path, "")
			if !reached {
				t.Fatalf("%s %s never reached the handler", c.method, c.path)
			}
			if seen != c.refused {
				t.Errorf("%s %s marked WithoutEngineWait = %v, want %v", c.method, c.path, seen, c.refused)
			}
		})
	}
}
