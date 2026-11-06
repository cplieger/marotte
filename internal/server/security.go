// Package server — security middleware: CSP headers, the ALLOWED_HOSTS anti-DNS-rebinding
// gate, and stdlib CSRF protection (net/http.CrossOriginProtection).
package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

// baseCSPPolicy is the CSP applied to every response. script-src is 'self' alone: the page
// carries no inline script. style-src keeps 'unsafe-inline' for editor highlighting,
// context-ring fills and terminal rendering; img-src allows data: for Seti UI icons.
const baseCSPPolicy = "default-src 'self'; " +
	"connect-src 'self'; " +
	"img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self'; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// buildCSPPolicy returns the CSP after checking that staticFS's index.html carries no inline
// script. It refuses rather than granting a hash, so a new inline script fails startup.
func buildCSPPolicy(staticFS fs.FS) (string, error) {
	if staticFS == nil {
		return "", errors.New("buildCSPPolicy: nil staticFS")
	}
	html, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		return "", fmt.Errorf("buildCSPPolicy: read index.html: %w", err)
	}
	if n := len(httpserver.InlineScriptHashes(html)); n != 0 {
		return "", fmt.Errorf("buildCSPPolicy: index.html carries %d inline script(s), which script-src 'self' blocks; move it to a file or change the CSP deliberately", n)
	}
	return baseCSPPolicy, nil
}

// securityMiddleware layers, outermost first: httpserver.SecurityHeaders -> the ALLOWED_HOSTS
// exact-Host allowlist -> http.NewCrossOriginProtection. The host gate precedes CSRF because
// a DNS-rebinding request makes Origin and Host agree (CWE-346), and sits inside
// SecurityHeaders so its 403 carries the baseline headers. CSRF exempts GET, so the shell
// WebSocket handshake's origin gate is the terminal engine's, not this. A CSRF refusal is
// logged and answered with the JSON error envelope, which csrf.Handler's text/plain 403 is not.
func securityMiddleware(cspPolicy string, hostPolicy *httpserver.HostPolicy, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := csrf.Check(r); err != nil {
			logOriginRefusal(r, "csrf", err.Error())
			httpreply.Forbidden(w, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
	return httpserver.SecurityHeaders(
		httpserver.WithCSP(cspPolicy),
		httpserver.WithReferrerPolicy("same-origin"),
	)(hostPolicy.Middleware()(checked))
}

// logOriginRefusal writes the one Warn line a CSRF or fetch-metadata refusal gets. It carries no
// path: a preview path holds a bearer token that only the access log's template hides, and the
// access line pairs with this one by request_id.
func logOriginRefusal(r *http.Request, gate, reason string) {
	slog.Warn("request refused by origin gate",
		"gate", gate,
		"reason", reason,
		"host", logsafe.Field(r.Host),
		"origin", logsafe.Field(originBase(r.Header.Get("Origin"))),
		"sec_fetch_site", logsafe.Field(r.Header.Get("Sec-Fetch-Site")),
		"request_id", httpserver.RequestIDFromContext(r.Context()))
}

// originBase reduces an Origin header to its RFC 6454 serialization, scheme://host[:port], so a
// browser-sent path, query or userinfo never reaches the log. It returns "null" for the opaque
// origin, "" when the header is absent and "malformed" for anything else.
func originBase(origin string) string {
	switch origin {
	case "", "null":
		return origin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "malformed"
	}
	return logsafe.Field(u.Scheme + "://" + u.Host)
}
