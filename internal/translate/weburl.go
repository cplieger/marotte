package translate

import "net/url"

// IsWebURL reports whether raw is a link a browser may open for the user: an http or https URL
// with a host. It is an allowlist, so an app scheme, file:, a relative URL and a hostless
// "https:foo" all fail.
func IsWebURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
