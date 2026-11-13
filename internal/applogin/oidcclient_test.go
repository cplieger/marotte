package applogin

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/go/ssrf"
)

const (
	idpURL  = "https://idp.example.com"
	idp2URL = "https://idp2.example.com"
)

// TestOIDCClient_SeamKeepsTheChecks builds through marotte's own client and seam, never
// IdP.Client(), and shows the client's own checks still refuse.
func TestOIDCClient_SeamKeepsTheChecks(t *testing.T) {
	origin := parseOrigin(testOrigin, nil)
	t.Run("saved_origins_build", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		p := builtProvider(t, providerOf(idp), origin)
		if p.Issuer() != idp.Issuer() {
			t.Errorf("built provider issuer %q, want %q", p.Issuer(), idp.Issuer())
		}
	})
	t.Run("unsaved_origin_refused", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		rec := providerOf(idp)
		const elsewhere = "https://elsewhere.example.com"
		rec.Origins = ProviderOrigins{Issuer: elsewhere, Authorization: elsewhere, Token: elsewhere, JWKS: elsewhere}
		_, err := oidc.NewProvider(t.Context(), ProviderConfig(rec, origin, access.NewEphemeralSealer()))
		if !errors.Is(err, errOIDCOrigin) {
			t.Errorf("discovery through an unsaved origin = %v, want errOIDCOrigin", err)
		}
	})
	t.Run("ip_literal_endpoint_refused", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{TokenEndpoint: "https://192.0.2.1/token"})
		_, err := oidc.NewProvider(t.Context(), ProviderConfig(providerOf(idp), origin, access.NewEphemeralSealer()))
		if !errors.Is(err, errEndpointIPLiteral) {
			t.Errorf("an IP-literal token endpoint = %v, want errEndpointIPLiteral", err)
		}
	})
	t.Run("non_https_endpoint_refused_by_the_library", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{JWKSURI: "http://idp.example.com/jwks"})
		_, err := oidc.NewProvider(t.Context(), ProviderConfig(providerOf(idp), origin, access.NewEphemeralSealer()))
		if !errors.Is(err, oidc.ErrDiscovery) {
			t.Errorf("an http jwks_uri = %v, want oidc.ErrDiscovery", err)
		}
	})
	t.Run("redirect_refused", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		q := url.Values{
			"client_id": {idp.ClientID()}, "redirect_uri": {origin.OIDCRedirect()}, "response_type": {"code"},
			"scope": {"openid"}, "state": {"s"}, "nonce": {"n"},
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, idpURL+"/authorize?"+q.Encode(), nil)
		resp, err := OIDCClient(providerOf(idp)).Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, errOIDCRedirect) {
			t.Errorf("a redirecting endpoint = %v, want errOIDCRedirect", err)
		}
	})
	t.Run("second_issuer", func(t *testing.T) {
		newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		idp2 := newSeamIdP(t, idp2URL, accesstest.IdPOptions{})
		if p := builtProvider(t, providerOf(idp2), origin); p.Issuer() != idp2.Issuer() {
			t.Errorf("second issuer built %q", p.Issuer())
		}
	})
}

// recordingTransport records every request the library sends through marotte's client.
type recordingTransport struct {
	rt   http.RoundTripper
	reqs []recordedRequest
	mu   sync.Mutex
}

type recordedRequest struct {
	url, auth, body string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, recordedRequest{
		url: req.URL.Scheme + "://" + req.URL.Host + req.URL.Path, auth: req.Header.Get("Authorization"), body: string(body),
	})
	r.mu.Unlock()
	return r.rt.RoundTrip(req)
}

func (r *recordingTransport) urls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.reqs {
		out = append(out, q.url)
	}
	return out
}

// recordedBuild builds rec's provider through marotte's client, wrapped in a recorder.
func recordedBuild(t *testing.T, rec storeProvider, origin *Origin) (*oidc.Provider, *recordingTransport, error) {
	t.Helper()
	cfg := ProviderConfig(rec, origin, access.NewEphemeralSealer())
	r := &recordingTransport{rt: cfg.HTTPClient.Transport}
	cfg.HTTPClient.Transport = r
	p, err := oidc.NewProvider(t.Context(), cfg)
	return p, r, err
}

// newRecorderHost routes the OIDC client's dials for host to a TLS server that records each
// request's path, so a test can assert none reached it.
func newRecorderHost(t *testing.T, host string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	routeSeam(t, host, &oidcDialSeam{addr: srv.Listener.Addr().String(), roots: roots})
	return &hits
}

// TestOIDCClient_OnlySavedServerOrigins: the client sends only to the saved
// issuer, token and JWKS origins, on their own ports; the authorization origin adds nothing.
func TestOIDCClient_OnlySavedServerOrigins(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	login := newRecorderHost(t, "login.example.com")
	rec := providerOf(idp)
	rec.Origins.Authorization = "https://login.example.com"
	client := OIDCClient(rec)
	for _, target := range []string{"https://login.example.com/authorize", "https://idp.example.com:8443/token", "http://idp.example.com/token"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, errOIDCOrigin) {
			t.Errorf("GET %s = %v, want errOIDCOrigin", target, err)
		}
	}
	if n := login.Load(); n != 0 {
		t.Errorf("the authorization origin received %d requests", n)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, idpURL+"/jwks", nil)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the JWKS on the saved origin: (%v, %v)", resp, err)
	}
	_ = resp.Body.Close()
}

// TestOIDCClient_AddressPolicy: public addresses, private
// ones only when pinned, and never loopback, link-local, metadata, unspecified, multicast or
// reserved, pinned or not.
func TestOIDCClient_AddressPolicy(t *testing.T) {
	never := []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "240.0.0.1", "100.64.0.1"}
	var pins []netip.Addr
	for _, a := range append(slices.Clone(never), "10.0.0.5", "fd00::5") {
		pins = append(pins, netip.MustParseAddr(a))
	}
	pinnedPolicy, unpinned := originAddressPolicy(pins), originAddressPolicy(nil)
	cases := []struct {
		addr          string
		pinned, plain bool
	}{
		{"93.184.216.34", true, true},
		{"2606:2800:220:1::1", true, true},
		{"10.0.0.5", true, false},
		{"fd00::5", true, false},
		{"::ffff:10.0.0.5", true, false},
		{"10.0.0.6", false, false},
	}
	for _, a := range never {
		cases = append(cases, struct {
			addr          string
			pinned, plain bool
		}{a, false, false})
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.addr)
		if got := pinnedPolicy(a); got != c.pinned {
			t.Errorf("%s with the pins: admitted %t, want %t", c.addr, got, c.pinned)
		}
		if got := unpinned(a); got != c.plain {
			t.Errorf("%s with no pins: admitted %t, want %t", c.addr, got, c.plain)
		}
	}
}

// routeAdmits dials ip on origin's port through the client's route for origin with a cancelled
// context: the address policy runs before the connect, so an admitted address fails on the
// context and a refused one with the policy's refusal. ok is false when origin has no route.
func routeAdmits(t *testing.T, client *http.Client, origin, ip string) (admitted, ok bool) {
	t.Helper()
	rt, ok := client.Transport.(*oidcRouter).routes[origin]
	if !ok {
		return false, false
	}
	port := "443"
	if u, err := url.Parse(origin); err == nil && u.Port() != "" {
		port = u.Port()
	}
	addr := net.JoinHostPort(ip, port)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := rt.(*http.Transport).DialContext(ctx, "tcp", addr)
	var refusal *ssrf.Error
	switch {
	case errors.As(err, &refusal) && refusal.Kind == ssrf.KindPolicyDenied:
		return false, true
	case errors.Is(err, context.Canceled):
		return true, true
	}
	t.Fatalf("dial %s through %s = %v, want the context's error or the ssrf refusal", addr, origin, err)
	return false, false
}

// TestOIDCClient_IssuerRouteAdmitsItsPinsInAnySpelling: a host is case-insensitive (RFC 3986
// section 3.2.2) and https's default port is no port (RFC 6454 section 6.2), so the issuer origin's
// route admits the pinned private address whatever the spelling of the saved issuer origin or
// the pin key; another port, host or scheme is another origin and stays public-only.
func TestOIDCClient_IssuerRouteAdmitsItsPinsInAnySpelling(t *testing.T) {
	const pin = "10.0.0.7"
	for _, c := range []struct {
		name, issuer, other, key string
		issuerPinned             bool
	}{
		{"both_lower", "https://idp.example.com", "", "idp.example.com", true},
		{"issuer_mixed_case", "https://IDP.Example.com", "", "idp.example.com", true},
		{"key_mixed_case", "https://idp.example.com", "", "IDP.Example.com", true},
		{"both_mixed_case", "https://Idp.Example.com", "", "IDP.example.COM", true},
		{"issuer_default_port", "https://idp.example.com:443", "", "idp.example.com", true},
		{"other_port_public_only", "https://idp.example.com", "https://idp.example.com:8443", "idp.example.com", true},
		{"other_host_public_only", "https://idp.example.com", "https://jwks.example.com", "jwks.example.com", false},
		{"key_for_another_host", "https://idp.example.com", "", "idp2.example.com", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := storeProvider{
				Origins: ProviderOrigins{Issuer: c.issuer, Token: c.issuer, JWKS: c.issuer},
				Pinned:  map[string][]netip.Addr{c.key: {netip.MustParseAddr("10.0.0.7")}},
			}
			if c.other != "" {
				p.Origins.JWKS = c.other
			}
			client := OIDCClient(p)
			if admitted, ok := routeAdmits(t, client, idpURL, pin); !ok || admitted != c.issuerPinned {
				t.Errorf("issuer route: (admitted %t, route %t), want (%t, true)", admitted, ok, c.issuerPinned)
			}
			if public, ok := routeAdmits(t, client, idpURL, "93.184.216.34"); !ok || !public {
				t.Errorf("issuer route refused a public address (route %t)", ok)
			}
			if c.other == "" {
				return
			}
			if admitted, ok := routeAdmits(t, client, c.other, pin); !ok || admitted {
				t.Errorf("%s route: (admitted %t, route %t), want (false, true)", c.other, admitted, ok)
			}
		})
	}
	t.Run("other_scheme_has_no_route", func(t *testing.T) {
		p := storeProvider{
			Origins: ProviderOrigins{Issuer: idpURL, Token: "http://idp.example.com", JWKS: idpURL},
			Pinned:  map[string][]netip.Addr{"idp.example.com": {netip.MustParseAddr("10.0.0.7")}},
		}
		client := OIDCClient(p)
		if routes := len(client.Transport.(*oidcRouter).routes); routes != 1 {
			t.Errorf("%d routes, want the issuer's alone", routes)
		}
		if _, ok := routeAdmits(t, client, "http://idp.example.com", pin); ok {
			t.Error("an http origin has a route")
		}
	})
}

// discoveryFields are the three endpoints a discovery document names, with the IdPOptions field
// that overrides each.
var discoveryFields = []struct {
	set  func(o *accesstest.IdPOptions, v string)
	name string
	path string
}{
	{name: "authorization_endpoint", path: "/authorize", set: func(o *accesstest.IdPOptions, v string) { o.AuthorizationEndpoint = v }},
	{name: "token_endpoint", path: "/token", set: func(o *accesstest.IdPOptions, v string) { o.TokenEndpoint = v }},
	{name: "jwks_uri", path: "/jwks", set: func(o *accesstest.IdPOptions, v string) { o.JWKSURI = v }},
}

// rebuildFailure saves idp's provider, reopens, swaps in a second IdP on the same issuer whose
// discovery opts shape, and returns the builder's recorded failure; OIDC stays off.
func rebuildFailure(t *testing.T, opts accesstest.IdPOptions) error {
	t.Helper()
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	newSeamIdP(t, idpURL, opts)
	h = h.reopen()
	ctx, cancel := context.WithCancel(t.Context())
	done := StartProviderBuilder(ctx, h.store, h.origin, access.NewEphemeralSealer())
	waitUntil(t, "the build failure", func() bool { return h.store.ProviderBuildFailure() != nil })
	cancel()
	<-done
	if h.store.OIDCProvider() != nil {
		t.Error("a provider is attached")
	}
	if _, err := h.svc.AdmitOIDC(testClient); !errors.Is(err, account.ErrOIDCUnavailable) {
		t.Errorf("AdmitOIDC = %v, want ErrOIDCUnavailable", err)
	}
	return h.store.ProviderBuildFailure()
}

// TestOIDC_DiscoveryEndpointRules: an http endpoint is the library's
// ErrDiscovery naming the field, an IP-literal one marotte's refusal naming it, at a build and at
// a rebuild, with no request sent to it.
func TestOIDC_DiscoveryEndpointRules(t *testing.T) {
	origin := parseOrigin(testOrigin, nil)
	for _, f := range discoveryFields {
		for _, c := range []struct {
			endpoint, kind string
			want           error
		}{
			{"http://idp.example.com" + f.path, "http", oidc.ErrDiscovery},
			{"https://192.0.2.1" + f.path, "ip_literal", errEndpointIPLiteral},
		} {
			t.Run(f.name+"_"+c.kind, func(t *testing.T) {
				var opts accesstest.IdPOptions
				f.set(&opts, c.endpoint)
				idp := newSeamIdP(t, idpURL, opts)
				_, rec, err := recordedBuild(t, providerOf(idp), origin)
				if !errors.Is(err, c.want) || !strings.Contains(err.Error(), f.name) {
					t.Errorf("build = %v, want %v naming %s", err, c.want, f.name)
				}
				if slices.Contains(rec.urls(), c.endpoint) {
					t.Errorf("a request reached %s", c.endpoint)
				}
				if err := rebuildFailure(t, opts); !errors.Is(err, c.want) {
					t.Errorf("rebuild failure = %v, want %v", err, c.want)
				}
			})
		}
	}
}

// TestOIDC_ChangedEndpointOriginLeavesProviderUnbuilt: a discovery that
// moves one endpoint to another https origin leaves the provider unbuilt, at a build and at a
// rebuild, and sends nothing to the new origin.
func TestOIDC_ChangedEndpointOriginLeavesProviderUnbuilt(t *testing.T) {
	origin := parseOrigin(testOrigin, nil)
	for _, f := range discoveryFields {
		t.Run(f.name, func(t *testing.T) {
			moved := newRecorderHost(t, "idp2.example.com")
			var opts accesstest.IdPOptions
			f.set(&opts, idp2URL+f.path)
			idp := newSeamIdP(t, idpURL, opts)
			if _, _, err := recordedBuild(t, providerOf(idp), origin); !errors.Is(err, errEndpointsChanged) || !strings.Contains(err.Error(), f.name) {
				t.Errorf("build = %v, want errEndpointsChanged naming %s", err, f.name)
			}
			if err := rebuildFailure(t, opts); !errors.Is(err, errEndpointsChanged) {
				t.Errorf("rebuild failure = %v, want errEndpointsChanged", err)
			}
			if n := moved.Load(); n != 0 {
				t.Errorf("the moved origin received %d requests", n)
			}
		})
	}
}

// TestOIDC_EndpointOriginComparedInItsSerializedForm: an endpoint spelling the saved origin with
// the default port or another host case is the same origin (RFC 6454 section 6.2), on either
// side, and the router sends to it; another port or host is still a changed endpoint, unrequested.
func TestOIDC_EndpointOriginComparedInItsSerializedForm(t *testing.T) {
	origin := parseOrigin(testOrigin, nil)
	for _, c := range []struct {
		name, endpoint, saved string
		changed               bool
	}{
		{"default_port", "https://idp.example.com:443", idpURL, false},
		{"host_case", "https://IDP.Example.COM", idpURL, false},
		{"saved_with_default_port", idpURL, "https://idp.example.com:443", false},
		{"saved_in_another_case", idpURL, "https://Idp.Example.com", false},
		{"other_port", "https://idp.example.com:8443", idpURL, true},
		{"other_host", idp2URL, idpURL, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			moved := newRecorderHost(t, "idp2.example.com")
			idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{
				TokenEndpoint: c.endpoint + "/token", JWKSURI: c.endpoint + "/jwks",
			})
			rec := providerOf(idp)
			rec.Origins.Token, rec.Origins.JWKS = c.saved, c.saved
			_, recorded, err := recordedBuild(t, rec, origin)
			if c.changed {
				if !errors.Is(err, errEndpointsChanged) {
					t.Errorf("build = %v, want errEndpointsChanged", err)
				}
				for _, u := range recorded.urls() {
					if strings.HasPrefix(u, c.endpoint+"/") {
						t.Errorf("a request reached the changed endpoint %s", u)
					}
				}
				if n := moved.Load(); n != 0 {
					t.Errorf("the moved origin received %d requests", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("build = %v, want nil: the endpoint is the saved origin", err)
			}
			client := OIDCClient(rec)
			for _, target := range []string{c.endpoint + "/jwks", c.saved + "/jwks"} {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
				resp, err := client.Do(req)
				if err != nil {
					t.Errorf("GET %s through the router = %v, want the JWKS", target, err)
					continue
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("GET %s through the router = %d, want 200", target, resp.StatusCode)
				}
			}
		})
	}
}

// TestOIDC_SignInFetchesExactlyThreeURLs: a whole sign-in fetches
// discovery, jwks_uri and token_endpoint, all through marotte's client, and no userinfo.
func TestOIDC_SignInFetchesExactlyThreeURLs(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	origin := parseOrigin(testOrigin, nil)
	p, rec, err := recordedBuild(t, providerOf(idp), origin)
	if err != nil {
		t.Fatal(err)
	}
	cookies := ParseProxies("").CookieConfig()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, testOrigin+"/access/oidc/start", nil)
	authURL, err := p.Begin(w, r, cookies, oidc.BeginRequest{Intent: oidc.IntentSignIn, Redirect: "/"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	browser := idp.Client()
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, authURL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize answered %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cb := httptest.NewRequest(http.MethodGet, loc.String(), nil)
	for _, c := range w.Result().Cookies() {
		cb.AddCookie(c)
	}
	if _, claims, err := p.Finish(httptest.NewRecorder(), cb, cookies, oidc.Callback{
		State: loc.Query().Get("state"), Code: loc.Query().Get("code"),
	}); err != nil || claims.Identity.Subject == "" {
		t.Fatalf("Finish = (%+v, %v)", claims, err)
	}
	got := slices.Compact(slices.Sorted(slices.Values(rec.urls())))
	want := []string{idpURL + discoveryPath, idpURL + "/jwks", idpURL + "/token"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the client fetched %v, want exactly %v", got, want)
	}
}

// TestPolicy_MarkerSecretInNoErrorOrLog, at the store: a saved marker
// secret reaches no build failure, captured log line or printed record.
func TestPolicy_MarkerSecretInNoErrorOrLog(t *testing.T) {
	logs := capture.Default(t)
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{ClientSecret: markerSecret})
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	routeSeam(t, "idp.example.com", &oidcDialSeam{addr: "127.0.0.1:1", roots: idp.RootCAs()})
	h = h.reopen()
	ctx, cancel := context.WithCancel(t.Context())
	done := StartProviderBuilder(ctx, h.store, h.origin, access.NewEphemeralSealer())
	waitUntil(t, "the build failure", func() bool { return h.store.ProviderBuildFailure() != nil })
	cancel()
	<-done
	texts := []string{h.store.ProviderBuildFailure().Error()}
	for _, r := range logs.Records() {
		texts = append(texts, fmt.Sprint(r))
		r.Attrs(func(a slog.Attr) bool { texts = append(texts, a.String()); return true })
	}
	pol := h.store.policy()
	texts = append(texts, fmt.Sprintf("%v %+v %#v", pol, pol, pol), fmt.Sprintf("%v %+v", h.store.PolicyView(), h.store.PolicyView()))
	for _, s := range texts {
		if strings.Contains(s, markerSecret) {
			t.Errorf("the marker secret is in %q", s)
		}
	}
}

// TestPolicy_IdentityChangeNeedsTheSecret, at planning: an issuer-only
// and a client-ID-only change with no replacement secret are refused before any request, and the
// stored provider is unchanged.
func TestPolicy_IdentityChangeNeedsTheSecret(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{ClientSecret: markerSecret})
	other := newRecorderHost(t, "idp2.example.com")
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	before := h.store.policy()
	issuerOnly := rec
	issuerOnly.Issuer = idp2URL
	issuerOnly.Origins = ProviderOrigins{Issuer: idp2URL, Authorization: idp2URL, Token: idp2URL, JWKS: idp2URL}
	clientOnly := rec
	clientOnly.ClientID = "another-client"
	for name, next := range map[string]storeProvider{"issuer_only": issuerOnly, "client_id_only": clientOnly} {
		t.Run(name, func(t *testing.T) {
			next.ClientSecret = access.Secret{}
			if _, err := planProviderEdit(h.store.policy().Provider, next, access.Secret{}); !errors.Is(err, ErrClientSecretRequired) {
				t.Errorf("planProviderEdit = %v, want ErrClientSecretRequired", err)
			}
		})
	}
	after := h.store.policy()
	if after.Generation != before.Generation || providerFieldsDiffer(&after.Provider, &before.Provider) ||
		after.Provider.ClientSecret.Reveal() != markerSecret {
		t.Error("the stored provider changed")
	}
	if n := other.Load(); n != 0 {
		t.Errorf("the new issuer's origin received %d requests", n)
	}
}

func TestPolicy_ClientSecretSurvivesAReopen(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	candidate := h.store.policy()
	candidate.Provider = rec
	edit := &providerEdit{record: rec, built: builtProvider(t, rec, h.origin)}
	if _, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, "", candidate, candidate.Generation, edit); err != nil {
		t.Fatalf("putPolicy: %v", err)
	}
	h = h.reopen()
	saved := h.store.policy().Provider
	if saved.ClientSecret.Reveal() != idp.ClientSecret() {
		t.Error("the client secret did not survive the reopen")
	}
	cfg := ProviderConfig(saved, h.origin, access.NewEphemeralSealer())
	if cfg.ClientSecret.Reveal() != idp.ClientSecret() {
		t.Error("the rebuilt provider's config carries another secret")
	}
	builtProvider(t, saved, h.origin)
}
