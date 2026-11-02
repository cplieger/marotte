// Package apicaller tells a request from marotte's own page apart from one another program sent.
package apicaller

import "net/http"

// Is reports whether r came from a program other than a browser page. A browser sends Sec-Fetch-Site only to a
// potentially trustworthy URL (https://www.w3.org/TR/fetch-metadata/), so a plain-HTTP LAN origin gets none, but it
// sends Origin on every non-GET fetch (https://fetch.spec.whatwg.org/#origin-header), and curl sends neither. This is
// attribution, not access control: a caller that forges either header changes only how its request is worded.
func Is(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Origin") == ""
}
