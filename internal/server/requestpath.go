// Package server — the canonical-request-path gate over the API surface.
//
// ServeMux answers a non-canonical path with a 307, which is SUCCESS to a non-following
// client (`curl -sf http://host//healthz` would read healthy unconsulted), so the API surface
// and the health route refuse a non-canonical spelling themselves.
package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
)

// apiPathPrefix is the subtree this guard covers: every route but the "/" SPA/static mount,
// which stays unguarded because a browser legitimately follows its cleaning redirect.
const apiPathPrefix = "/api/"

// msgNonCanonicalPath is the refusal. It names the CLASS, not the caller-controlled path.
const msgNonCanonicalPath = "non-canonical request path"

// canonicalAPIPath refuses (400; `curl -f` needs >= 400) a request whose path is not the path
// ServeMux would route it as, when it addresses or would land on the API surface.
//
// It feeds the DECODED r.URL.Path: an encoded `/api/%2e%2e/api/version` is canonical when
// escaped and falls through to the SPA with a 200. Scope tests BOTH the raw and the cleaned
// path, because cleaning can move a path into /api/ (/%2e%2e/api/version) or out of it
// (/api/version/.. cleans to /api). Only the cleaning class is covered, not ServeMux's
// trailing-slash redirect; register both spellings of any subtree route.
func canonicalAPIPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean, canonical := httpserver.CanonicalRequestPath(r.URL.Path)
		if !canonical && onAPISurface(r.URL.Path, clean) {
			httpreply.BadRequest(w, msgNonCanonicalPath)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// onAPISurface reports whether a request either addresses the API subtree or the health
// route, or would land on one once canonicalized. canonicalAPIPath says what each leg catches.
func onAPISurface(raw, clean string) bool {
	return guardedPath(raw) || guardedPath(clean)
}

func guardedPath(p string) bool {
	return strings.HasPrefix(p, apiPathPrefix) || p == healthzPath || strings.HasPrefix(p, healthzPath+"/")
}
