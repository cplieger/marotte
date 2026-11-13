package applogin

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/cplieger/access"
	"github.com/cplieger/access/webauthn"
	"github.com/cplieger/go/httpserver"
)

// OriginEnv is the deployment-time variable holding the canonical HTTPS origin.
const OriginEnv = "MAROTTE_PUBLIC_URL"

// The degraded wordings: what each message says while no valid canonical
// origin exists, so none prints an empty or placeholder origin.
const (
	DegradedMethodReason   = "Passkeys and single sign-on are off until marotte has an HTTPS address. See docs/configuration.md."
	DegradedPlainNoticeEnd = "Set up HTTPS for marotte when you can."
	DegradedSwitchOffPage  = "The admin turned off sign-in over plain HTTP, and marotte has no HTTPS address yet. Run `marotte access policy plain-http on` in the container."
	DegradedSwitchOffAsk   = "You are signed in over plain HTTP, and marotte has no HTTPS address yet. Turning this off signs out every plain-HTTP session, this one included. To get back in, use an HTTPS address for marotte or the shell: `marotte access policy plain-http on`."
	DegradedRepairNote     = "passkeys and single sign-on are off: no HTTPS address is configured"
)

// tunnelOrigin is the documented SSH tunnel's address: `ssh -L 9847:localhost:9847` to marotte's
// fixed listen port.
const tunnelOrigin = "http://localhost:9847"

// browserPeer stands in for a browser's peer in the allowed-host predicate: on Docker's bridge
// network a browser never reaches marotte from loopback.
const browserPeer = "192.0.2.1:9"

// Origin is the parsed canonical origin. The zero check means valid; a degraded Origin answers no
// RP, no redirect and no HTTPS link.
type Origin struct {
	rp     *webauthn.RelyingParty
	origin string
	host   string
	failed string
}

// allowedHost is the one host predicate: what a non-loopback browser request with Host h gets.
func allowedHost(policy *httpserver.HostPolicy, h string) bool {
	return policy.Allows(&http.Request{Host: h, RemoteAddr: browserPeer})
}

// ParseCanonicalOrigin validates raw and logs one ERROR naming the variable
// and the failed check when it is absent or invalid.
func ParseCanonicalOrigin(raw string, policy *httpserver.HostPolicy) *Origin {
	o := parseOrigin(raw, policy)
	if o.failed != "" {
		slog.Error("access: the canonical origin is unusable; passkeys and single sign-on are off",
			"variable", OriginEnv, "check", o.failed)
	}
	return o
}

const (
	schemeHTTPS = "https"
	failRPID    = "rp_id"
)

func parseOrigin(raw string, policy *httpserver.HostPolicy) *Origin {
	u, failedCheck := parseOriginURL(raw)
	if failedCheck != "" {
		return &Origin{failed: failedCheck}
	}
	origin, ok := httpsOrigin(u)
	if !ok {
		return &Origin{failed: "parse"}
	}
	host := canonicalHost(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil || webauthn.ValidateRPID(host) != nil {
		return &Origin{failed: failRPID}
	}
	if !allowedHost(policy, strings.TrimPrefix(origin, schemeHTTPS+"://")) {
		return &Origin{failed: "allowed_host"}
	}
	rp, err := webauthn.New(webauthn.RPConfig{ID: host, DisplayName: "marotte", Origins: []string{origin}})
	if err != nil {
		return &Origin{failed: failRPID}
	}
	return &Origin{rp: rp, origin: origin, host: host}
}

// canonicalHost is the one host form every origin, route key, pin key and dial-seam key uses: a
// host is case-insensitive (RFC 3986 section 3.2.2), so it is lowercased.
func canonicalHost(host string) string { return strings.ToLower(host) }

// httpsOrigin is the one origin canonicalizer: u's origin serialized as RFC 6454 section 6.2
// does, the form a browser sends. Every origin compare and route key goes through it: host per
// canonicalHost, port 443 omitted, IPv6 brackets kept. ok is false when u is not https, has no
// host or has a port outside 1 to 65535.
func httpsOrigin(u *url.URL) (origin string, ok bool) {
	host := canonicalHost(u.Hostname())
	if !strings.EqualFold(u.Scheme, schemeHTTPS) || host == "" {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.ParseUint(ps, 10, 16)
		if err != nil || n == 0 {
			return "", false
		}
		if n != 443 {
			host += ":" + strconv.FormatUint(n, 10)
		}
	}
	return schemeHTTPS + "://" + host, true
}

// canonicalOrigin is httpsOrigin of the URL raw, "" when raw is not one.
func canonicalOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	o, _ := httpsOrigin(u)
	return o
}

// parseOriginURL parses raw as a bare https origin and names the check it fails, "" when none.
func parseOriginURL(raw string) (u *url.URL, failedCheck string) {
	if strings.TrimSpace(raw) == "" {
		return nil, "absent"
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, "parse"
	case u.Scheme != schemeHTTPS:
		return nil, "scheme"
	case u.User != nil:
		return nil, "userinfo"
	case u.Path != "" && u.Path != "/", u.RawPath != "":
		return nil, "path"
	case u.RawQuery != "" || u.ForceQuery:
		return nil, "query"
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return nil, "fragment"
	case u.Hostname() == "":
		return nil, "host"
	}
	return u, ""
}

// Valid reports whether the canonical origin is usable.
func (o *Origin) Valid() bool { return o.failed == "" }

// Failed names the check an invalid origin failed; "" when valid.
func (o *Origin) Failed() string { return o.failed }

// String is the lowercased origin; "" while degraded.
func (o *Origin) String() string { return o.origin }

// RPID is the WebAuthn relying-party ID; "" while degraded.
func (o *Origin) RPID() string { return o.host }

// RP is account.Config.RP: nil while degraded.
func (o *Origin) RP() *webauthn.RelyingParty { return o.rp }

// OIDCRedirect is the OIDC callback URL; "" while degraded.
func (o *Origin) OIDCRedirect() string {
	if !o.Valid() {
		return ""
	}
	return o.origin + "/access/oidc/callback"
}

// SetupLinks returns the setup token's link forms in their order: the HTTPS
// link when the origin is valid, the tunnel link when localhost is an allowed host and the switch
// is on, and always the origin-free sentence. The token is revealed only into these lines.
func (o *Origin) SetupLinks(token access.Secret, switchOn bool, policy *httpserver.HostPolicy) []string {
	frag := "/access/setup#token=" + token.Reveal()
	var lines []string
	if o.Valid() {
		lines = append(lines, o.origin+frag)
	}
	if switchOn && allowedHost(policy, "localhost") {
		lines = append(lines, tunnelOrigin+frag+" through the SSH tunnel (docs/hardening.md)")
	}
	return append(lines, fmt.Sprintf("Or open marotte at the address you use and add %s to the end.", frag))
}

// RecoveryLink returns the HTTPS recovery link; ok is false while degraded.
func (o *Origin) RecoveryLink(token access.Secret) (link string, ok bool) {
	if !o.Valid() {
		return "", false
	}
	return o.origin + "/access/recover#token=" + token.Reveal(), true
}
