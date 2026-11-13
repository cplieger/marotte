package applogin

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/go/ssrf"
)

const (
	oidcBodyLimit = 1 << 20
	discoveryPath = "/.well-known/openid-configuration"
)

// oidcClientTimeout bounds each outbound OIDC request; a variable so tests can shorten it.
var oidcClientTimeout = 30 * time.Second

var (
	errOIDCOrigin   = errors.New("access: an OIDC request to an origin the saved provider does not name")
	errOIDCRedirect = errors.New("access: an OIDC endpoint answered with a redirect")
)

// oidcDialSeam, tests only, sends an origin host's dials to addr and trusts roots, so a test IdP
// on a loopback port stands in for a named https issuer; every other check stays. oidcSeams maps
// a host to its *oidcDialSeam and is empty in production; a sync.Map because a test swaps the
// issuer while a provider builder dials.
type oidcDialSeam struct {
	roots *x509.CertPool
	addr  string
}

var oidcSeams sync.Map

// OIDCClient is the outbound-trust client for one saved provider: it reaches only
// the saved issuer, token and JWKS origins, each through its own ssrf transport, the issuer's
// admitting its pinned private addresses and every other origin public addresses only.
func OIDCClient(p storeProvider) *http.Client { //nolint:gocritic // hugeParam: the record is copied into the router
	r := &oidcRouter{routes: map[string]http.RoundTripper{}, saved: p.Origins}
	for _, raw := range []string{p.Origins.Issuer, p.Origins.Token, p.Origins.JWKS} {
		o := canonicalOrigin(raw)
		if o == "" {
			continue
		}
		if _, ok := r.routes[o]; ok {
			continue
		}
		if t := originTransport(o, routePins(&p, o)); t != nil {
			r.routes[o] = t
		}
	}
	return &http.Client{
		Timeout:   oidcClientTimeout,
		Transport: r,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errOIDCRedirect
		},
	}
}

// routePins is the private addresses the route for the canonical origin admits: the pins saved
// under the issuer's host when origin is the saved issuer origin, nil for every other origin.
func routePins(p *storeProvider, origin string) []netip.Addr {
	if origin != canonicalOrigin(p.Origins.Issuer) {
		return nil
	}
	host := originHost(origin)
	for key, addrs := range p.Pinned {
		if canonicalHost(key) == host {
			return addrs
		}
	}
	return nil
}

func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// originTransport builds the ssrf transport for one https origin: its port only, loopback,
// link-local, metadata and the other non-routable ranges always refused, and only the private
// addresses in pins admitted beside public ones.
func originTransport(origin string, pins []netip.Addr) http.RoundTripper {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != schemeHTTPS {
		return nil
	}
	port := uint16(443)
	if ps := u.Port(); ps != "" {
		n, err := strconv.ParseUint(ps, 10, 16)
		if err != nil {
			return nil
		}
		port = uint16(n)
	}
	allow := originAddressPolicy(pins)
	opts := []ssrf.TransportOption{ssrf.WithAllowedPorts(port), ssrf.WithAddressPolicy(allow)}
	var roots *x509.CertPool
	seam, _ := oidcSeams.Load(canonicalHost(u.Hostname()))
	ds, _ := seam.(*oidcDialSeam)
	if ds != nil {
		host, sport, err := net.SplitHostPort(ds.addr)
		if err != nil {
			return nil
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return nil
		}
		n, err := strconv.ParseUint(sport, 10, 16)
		if err != nil {
			return nil
		}
		seamAllow := func(a netip.Addr) bool { return a == ip || allow(a) }
		opts = []ssrf.TransportOption{
			ssrf.WithAllowedPorts(uint16(n)), ssrf.WithAddressPolicy(seamAllow),
			ssrf.WithDialer(&net.Dialer{Timeout: oidcClientTimeout}),
		}
		roots = ds.roots
	}
	t := ssrf.SafeTransport(opts...)
	if ds != nil {
		base := t.DialContext
		t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return base(ctx, network, ds.addr)
		}
		t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return t
}

// originAddressPolicy admits a public address, and a private one only when pins lists it; every
// other address (loopback, link-local, metadata, unspecified, multicast, reserved) never.
func originAddressPolicy(pins []netip.Addr) func(netip.Addr) bool {
	return func(a netip.Addr) bool {
		return ssrf.IsPublicAddr(a) || (a.IsPrivate() && pinned(pins, a))
	}
}

func pinned(pins []netip.Addr, a netip.Addr) bool {
	for _, p := range pins {
		if p.Unmap() == a.Unmap() {
			return true
		}
	}
	return false
}

// oidcRouter sends each request through its origin's transport and refuses any other origin.
type oidcRouter struct {
	routes map[string]http.RoundTripper
	saved  ProviderOrigins
}

func (r *oidcRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, _ := httpsOrigin(req.URL)
	t, ok := r.routes[origin]
	if !ok {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %s://%s", errOIDCOrigin, req.URL.Scheme, req.URL.Host)
	}
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = limitedBody{Reader: io.LimitReader(resp.Body, oidcBodyLimit), Closer: resp.Body}
	if strings.HasSuffix(req.URL.Path, discoveryPath) && resp.StatusCode == http.StatusOK {
		return checkDiscovery(resp, r.saved)
	}
	return resp, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

// checkDiscovery applies marotte's two rules beside the library's scheme check: each endpoint's
// host is a DNS name, never an IP literal, and each https endpoint's origin is the one saved for
// it. A non-https endpoint is left to the library, whose ErrDiscovery names the field.
func checkDiscovery(resp *http.Response, saved ProviderOrigins) (*http.Response, error) {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var doc struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("access: OIDC discovery document: %w", err)
	}
	fields := []struct{ name, value, saved string }{
		{"authorization_endpoint", doc.Authorization, saved.Authorization},
		{"token_endpoint", doc.Token, saved.Token},
		{"jwks_uri", doc.JWKS, saved.JWKS},
	}
	for _, f := range fields {
		if _, err := netip.ParseAddr(originHost(f.value)); err == nil {
			return nil, fmt.Errorf("%w: %s", errEndpointIPLiteral, f.name)
		}
	}
	for _, f := range fields {
		u, err := url.Parse(f.value)
		if err != nil || !strings.EqualFold(u.Scheme, schemeHTTPS) {
			continue
		}
		if origin, _ := httpsOrigin(u); origin == "" || origin != canonicalOrigin(f.saved) {
			return nil, fmt.Errorf("%w: %s is %s, saved %s", errEndpointsChanged, f.name, f.value, f.saved)
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

var (
	// errEndpointIPLiteral refuses a discovery endpoint whose host is an IP literal; the error
	// names the field.
	errEndpointIPLiteral = errors.New("access: an OIDC discovery endpoint's host is an IP literal")
	// errEndpointsChanged refuses a discovery endpoint on an origin other than the one the owner
	// approved at Test; the error names the field. Settings answers it by asking for a new Test.
	errEndpointsChanged = errors.New("access: the OIDC provider's endpoints changed")
)
