// Package server — security middleware: CSP headers, the ALLOWED_HOSTS anti-DNS-rebinding
// gate, and stdlib CSRF protection (net/http.CrossOriginProtection).
package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/cplieger/webhttp/v3"
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
	if n := len(webhttp.InlineScriptHashes(html)); n != 0 {
		return "", fmt.Errorf("buildCSPPolicy: index.html carries %d inline script(s), which script-src 'self' blocks; move it to a file or change the CSP deliberately", n)
	}
	return baseCSPPolicy, nil
}

// securityMiddleware layers, outermost first: webhttp.SecurityHeaders -> the ALLOWED_HOSTS
// exact-Host allowlist -> http.NewCrossOriginProtection. The host gate precedes CSRF because
// a DNS-rebinding request makes Origin and Host agree (CWE-346), and sits inside
// SecurityHeaders so its 403 carries the baseline headers. CSRF exempts GET, so the shell
// WebSocket handshake's origin gate is the terminal engine's, not this.
func securityMiddleware(cspPolicy string, hostPolicy *webhttp.HostPolicy, next http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	return webhttp.SecurityHeaders(
		webhttp.WithCSP(cspPolicy),
		webhttp.WithReferrerPolicy("same-origin"),
	)(hostPolicy.Middleware()(csrf.Handler(next)))
}
