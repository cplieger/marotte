package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
)

// fetchMetadataGate refuses any /api request a browser marks as coming from
// another site, the Fetch Metadata resource-isolation policy
// (https://web.dev/articles/fetch-metadata). A sandboxed preview frame has an
// opaque origin, so its requests are cross-site: this is what keeps a GET with a
// side effect, or the shell WebSocket handshake, out of a previewed page's
// reach. An absent header (curl, health probes) and same-origin pass.
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
