package applogin

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/cplieger/access"
	"github.com/cplieger/go/httpserver"
)

// requestFacts is the gate's request-scoped transport decision. It is immutable: the gate stores
// a new value on the context rather than change one.
type requestFacts struct {
	plain bool
}

type factsKey struct{}

func withFacts(ctx context.Context, f requestFacts) context.Context {
	return context.WithValue(ctx, factsKey{}, f)
}

// transportOf reports the transport the gate decided; ok is false outside the gate.
func transportOf(ctx context.Context) (plain, ok bool) {
	f, ok := ctx.Value(factsKey{}).(requestFacts)
	return f.plain, ok
}

// Proxies is the one TRUSTED_PROXIES rule both transport predicates read, so the client address,
// the limiter key and the HTTPS decision never disagree.
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

// RequestIsHTTPS is marotte's policy predicate: r.TLS, or a first X-Forwarded-Proto token "https"
// from a socket peer, unmapped, inside TRUSTED_PROXIES. It is never CookieConfig.HTTPS.
func (p *Proxies) RequestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !httpserver.ResolveClient(r, p.trusted...).PeerTrusted {
		return false
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(first), schemeHTTPS)
}

// ClientAddr is access.Config.ClientAddr's resolver, from the same rule. A forwarding header
// from an untrusted peer, X-Forwarded-Proto included, sets UntrustedForwarding and records the
// peer for the exposure warning.
func (p *Proxies) ClientAddr(r *http.Request) access.ClientAddr {
	res := httpserver.ResolveClient(r, p.trusted...)
	untrusted := res.UntrustedForwarding || (!res.PeerTrusted && r.Header.Get("X-Forwarded-Proto") != "")
	if untrusted {
		peer := res.Peer
		p.untrusted.Store(&peer)
	}
	return access.ClientAddr{IP: res.IP, Peer: res.Peer, Proxied: res.Forwarded, UntrustedForwarding: untrusted}
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

// CookieConfig is the one session cookie: its name and Secure flag follow the library's
// header-trusting predicate, so a forged header only makes its own client stricter, and it
// persists only on a request marotte's own rule calls HTTPS.
func (p *Proxies) CookieConfig() access.CookieConfig {
	return access.CookieConfig{
		Name:       SessionCookieName,
		SameSite:   http.SameSiteStrictMode,
		Posture:    access.PosturePerRequest,
		HTTPS:      access.RequestIsHTTPS,
		Persistent: p.RequestIsHTTPS,
	}
}
