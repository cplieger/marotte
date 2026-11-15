package applogin

import (
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/cplieger/access"
	"github.com/cplieger/go/httpserver"
)

// Proxies is the one TRUSTED_PROXIES rule: ClientAddr's address, Via and Secure come from one
// resolution, so the client address, the limiter key and the transport never disagree.
type Proxies struct {
	// untrusted is the last peer seen forwarding from outside the trusted set.
	untrusted atomic.Pointer[netip.Addr]
	trusted   []netip.Prefix
}

// ParseProxies parses TRUSTED_PROXIES, comma-separated CIDRs or addresses. A malformed entry is
// skipped with a WARN naming it: dropping one only narrows trust.
func ParseProxies(raw string) *Proxies {
	var entries []string
	for e := range strings.SplitSeq(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			entries = append(entries, e)
		}
	}
	prefixes, invalid := httpserver.ParseCIDRs(entries)
	if len(invalid) > 0 {
		slog.Warn("access: ignoring malformed TRUSTED_PROXIES entries (want CIDR or IP)", "entries", invalid)
	}
	return &Proxies{trusted: prefixes}
}

// Prefixes returns the trusted prefixes.
func (p *Proxies) Prefixes() []netip.Prefix { return slices.Clone(p.trusted) }

// ClientAddr is access.Config.ClientAddr's resolver. Secure is r.TLS or httpserver's
// ForwardedHTTPS from a trusted peer; a loopback peer is never secure by itself. A forwarding
// header from an untrusted peer answers ViaUntrustedForwarding and records the peer for the
// exposure warning.
func (p *Proxies) ClientAddr(r *http.Request) access.ClientAddr {
	res := httpserver.ResolveClient(r, p.trusted...)
	a := access.ClientAddr{Peer: res.Peer, Secure: r.TLS != nil || res.ForwardedHTTPS}
	switch {
	case res.Forwarded:
		a.Via, a.Forwarded = access.ViaProxy, res.IP
	case res.PeerTrusted:
		a.Via = access.ViaProxyNoClient
	// httpserver's UntrustedForwarding does not count this header; access's default resolver does.
	case res.UntrustedForwarding || len(r.Header.Values("X-Forwarded-Proto")) > 0:
		a.Via = access.ViaUntrustedForwarding
		peer := res.Peer
		p.untrusted.Store(&peer)
	default:
		a.Via = access.ViaDirect
	}
	return a
}

// LastUntrustedPeer reports the last peer seen forwarding from outside TRUSTED_PROXIES; ok is
// false when none has.
func (p *Proxies) LastUntrustedPeer() (peer netip.Addr, ok bool) {
	if a := p.untrusted.Load(); a != nil {
		return *a, true
	}
	return netip.Addr{}, false
}

// SessionCookieName is the session cookie's base name.
const SessionCookieName = "marotte_session"

// CookieConfig is the one session cookie. Under PosturePerRequest access derives its name,
// Secure flag and lifetime from ClientAddr's Secure, so a forged header never makes it persist.
func (p *Proxies) CookieConfig() access.CookieConfig {
	return access.CookieConfig{
		Name:     SessionCookieName,
		SameSite: http.SameSiteStrictMode,
		Posture:  access.PosturePerRequest,
	}
}
