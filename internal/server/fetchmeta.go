package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
)

// fetchMetadataGate refuses any /api request a browser marks as cross-site or same-site
// (https://web.dev/articles/fetch-metadata), which keeps a sandboxed preview frame's opaque
// origin away from side-effecting GETs and the shell WebSocket. An absent header passes.
func fetchMetadataGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, apiPathPrefix) {
			switch r.Header.Get("Sec-Fetch-Site") {
			case "cross-site", "same-site":
				httpreply.Forbidden(w, "cross-site requests to the API are refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
