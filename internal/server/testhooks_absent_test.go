//go:build !marotte_test

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A binary built without -tags marotte_test mounts nothing under /api/test/: the
// stub registers no route, so the bare mux answers 404 for both hooks. (Inside
// the whole server the same path lands on the SPA shell, like every unknown
// /api/* path; what this pins is that nothing is mounted.)
func TestTestHooks_AbsentWithoutTheBuildTag(t *testing.T) {
	s := &Server{agent: &fakeEngine{}}
	mux := http.NewServeMux()
	s.registerTestHooks(mux)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/test/sse"},
		{http.MethodPost, "/api/test/sse/close-after"},
		{http.MethodPost, "/api/test/preview-token"},
		{http.MethodPost, "/api/test/preview-resolver"},
		{http.MethodPost, "/api/test/preview-grow"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, http.NoBody))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d without the tag, want 404", tc.method, tc.path, rec.Code)
		}
	}
}
