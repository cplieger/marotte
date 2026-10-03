package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchMetadataGate(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := fetchMetadataGate(next)
	cases := []struct {
		path, site string
		want       int
	}{
		{"/api/x", "cross-site", http.StatusForbidden},
		{"/api/x", "same-site", http.StatusForbidden},
		{"/api", "cross-site", http.StatusForbidden},
		{"/api/shell/ws", "cross-site", http.StatusForbidden},
		{"/api/x", "same-origin", http.StatusTeapot},
		{"/api/x", "none", http.StatusTeapot},
		{"/api/x", "", http.StatusTeapot},
		{"/preview/t/x.html", "cross-site", http.StatusTeapot},
		{"/", "cross-site", http.StatusTeapot},
		{"/apix", "cross-site", http.StatusTeapot},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, http.NoBody)
		if c.site != "" {
			req.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.path == "/api/shell/ws" {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s with Sec-Fetch-Site %q = %d, want %d", c.path, c.site, rec.Code, c.want)
		}
		if c.want == http.StatusForbidden && !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s refusal body = %q, want the JSON error envelope", c.path, rec.Body)
		}
	}
}
