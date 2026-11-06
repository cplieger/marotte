package agent

import (
	"net/url"
	"strings"
)

// consentRequest is _kiro/openExternalUrl's params. Server names the MCP server signing in. FlowID is set on a
// connected server's mid-session re-auth (KAS's oauthCompletion "notification"), which ends with
// _kiro/mcp/oauthCompleted; a connect carries none and ends with the server's next _kiro/mcp/status.
type consentRequest struct {
	URL    string `json:"url"`
	Server string `json:"serverName"`
	FlowID string `json:"oauthFlowId"`
}

// googleConsentHost and googleConsentPath are Google's OAuth 2.0 authorization endpoint, the one Google's MCP grants
// use. Only it is rewritten: another authorization server may reject parameters it does not know.
const (
	googleConsentHost = "accounts.google.com"
	googleConsentPath = "/o/oauth2/v2/auth"
)

// withGoogleOfflineAccess asks Google's consent page for offline access, so the grant carries a refresh token and
// outlives its one-hour access token (https://developers.google.com/identity/protocols/oauth2/web-server#offline).
// Google returns a refresh token only on a consent screen, hence prompt=consent. Any existing access_type and prompt
// are replaced and every other query segment keeps its bytes and order, so redirect_uri and state still match the
// listener; any other URL comes back unchanged.
func withGoogleOfflineAccess(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host != googleConsentHost || u.Path != googleConsentPath {
		return rawURL
	}
	var kept []string
	for seg := range strings.SplitSeq(u.RawQuery, "&") {
		key, _, _ := strings.Cut(seg, "=")
		if k, kErr := url.QueryUnescape(key); seg == "" || (kErr == nil && (k == "access_type" || k == "prompt")) {
			continue
		}
		kept = append(kept, seg)
	}
	u.RawQuery = strings.Join(append(kept, "access_type=offline", "prompt=consent"), "&")
	return u.String()
}
