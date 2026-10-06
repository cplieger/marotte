package push

import (
	"fmt"
	"net/url"
	"strings"
)

func init() {
	for i, r := range pushEndpointRules {
		hasHost := r.Host != ""
		hasSuffix := r.Suffix != ""
		if hasHost == hasSuffix {
			panic(fmt.Sprintf("push: pushEndpointRules[%d]: exactly one of Host or Suffix must be set", i))
		}
		if hasSuffix && !strings.HasPrefix(r.Suffix, ".") {
			panic(fmt.Sprintf("push: pushEndpointRules[%d]: Suffix must start with '.'", i))
		}
	}
}

// pushEndpointRule declares a single vendor's match semantics for push
// endpoint validation. Exactly one of Host or Suffix is set.
type pushEndpointRule struct {
	Host   string // exact-match (empty if suffix-only)
	Suffix string // suffix-match; must start with "." (empty if exact-only)
}

// pushEndpointRules lists the browser push services a stored endpoint may target: an
// unvalidated endpoint the server later POSTs to is an SSRF primitive.
var pushEndpointRules = []pushEndpointRule{
	{Host: "fcm.googleapis.com"},                // Chrome, Edge, others on Chromium
	{Host: "updates.push.services.mozilla.com"}, // Firefox
	{Suffix: ".notify.windows.com"},             // WNS (Edge on Windows)
	{Suffix: ".push.apple.com"},                 // Safari (web.push.apple.com) + Apple push subdomains
}

// isAllowedPushEndpoint reports whether endpoint is https on a known browser push service.
// Explicit ports are rejected so the stored endpoint matches the JWT audience vapidHeader
// derives from u.Host.
func isAllowedPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if u.Port() != "" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	for _, rule := range pushEndpointRules {
		if rule.Host != "" && host == rule.Host {
			return true
		}
		if rule.Suffix != "" && strings.HasSuffix(host, rule.Suffix) {
			return true
		}
	}
	return false
}
