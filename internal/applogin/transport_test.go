package applogin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/go/slogx/capture"
)

func transportRequest(peer string, headers map[string][]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	for k, vs := range headers {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return r
}

func xfp(vs ...string) map[string][]string { return map[string][]string{"X-Forwarded-Proto": vs} }

// TestTransport_SecureFromForwardedHTTPS: Secure is TLS, or exactly one X-Forwarded-Proto field
// holding the one token https from a listed peer; Via is ViaProxy, ViaProxyNoClient,
// ViaUntrustedForwarding, then ViaDirect, as access's configuration guide orders a resolver's arms.
func TestTransport_SecureFromForwardedHTTPS(t *testing.T) {
	p := ParseProxies("10.0.0.0/8")
	tlsReq := transportRequest("192.0.2.9:1000", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	forwarded := transportRequest("10.1.2.3:443", xfp("https"))
	forwarded.Header.Set("X-Forwarded-For", "198.51.100.7")
	unlistedXFF := transportRequest("192.168.1.30:5000", map[string][]string{"X-Forwarded-For": {"198.51.100.8"}})
	for _, c := range []struct {
		r          *http.Request
		name       string
		ip         string
		exposePeer string
		via        access.Via
		secure     bool
	}{
		{tlsReq, "tls", "192.0.2.9", "", access.ViaDirect, true},
		{forwarded, "listed_peer_forwarding_a_client", "198.51.100.7", "", access.ViaProxy, true},
		{transportRequest("10.1.2.3:443", xfp("HTTPS")), "listed_peer_upper_case", "10.1.2.3", "", access.ViaProxyNoClient, true},
		{transportRequest("10.1.2.3:443", xfp("https, http")), "listed_peer_token_list", "10.1.2.3", "", access.ViaProxyNoClient, false},
		{transportRequest("10.1.2.3:443", xfp("https", "https")), "listed_peer_two_fields", "10.1.2.3", "", access.ViaProxyNoClient, false},
		{transportRequest("192.168.1.20:5000", xfp("https")), "unlisted_lan_peer", "192.168.1.20", "192.168.1.20", access.ViaUntrustedForwarding, false},
		{unlistedXFF, "unlisted_peer_forwarding_a_client", "192.168.1.30", "192.168.1.30", access.ViaUntrustedForwarding, false},
		{transportRequest("127.0.0.1:5000", nil), "plain_loopback_peer", "127.0.0.1", "", access.ViaDirect, false},
		{transportRequest("127.0.0.1:5000", xfp("https")), "loopback_peer_forged_header", "127.0.0.1", "127.0.0.1", access.ViaUntrustedForwarding, false},
		{transportRequest("[::ffff:10.1.2.3]:443", xfp("https")), "v4_mapped_listed_peer", "10.1.2.3", "", access.ViaProxyNoClient, true},
		{transportRequest("192.0.2.9:1000", nil), "plain", "192.0.2.9", "", access.ViaDirect, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := p.ClientAddr(c.r)
			if a.Secure != c.secure {
				t.Errorf("Secure = %t, want %t", a.Secure, c.secure)
			}
			if a.Via != c.via || a.IP().String() != c.ip {
				t.Errorf("ClientAddr = %+v (IP %s), want Via %s and IP %s", a, a.IP(), c.via, c.ip)
			}
			if c.exposePeer != "" {
				peer, ok := p.LastUntrustedPeer()
				if !ok || peer.String() != c.exposePeer {
					t.Errorf("LastUntrustedPeer = (%s, %t), want %s", peer, ok, c.exposePeer)
				}
			}
		})
	}
}

func TestParseProxies_MalformedEntrySkipped(t *testing.T) {
	logs := capture.Default(t)
	p := ParseProxies("10.0.0.0/8, not-a-cidr ,192.0.2.1")
	if got := len(p.Prefixes()); got != 2 {
		t.Errorf("%d prefixes, want the two valid entries", got)
	}
	if !logs.AttrContains("access: ignoring malformed TRUSTED_PROXIES entries", "entries", "not-a-cidr") {
		t.Error("no WARN naming the malformed entry")
	}
	if !p.ClientAddr(transportRequest("192.0.2.1:9", xfp("https"))).Secure {
		t.Error("a valid entry after the malformed one is not trusted")
	}
}

// TestCookieConfig_OnePredicate: the session cookie is named, flagged and timed from the one
// transport fact, so a forged-header LAN sign-in gets plain cookies and a plain session.
func TestCookieConfig_OnePredicate(t *testing.T) {
	cfg := ParseProxies("").CookieConfig()
	want := access.CookieConfig{Name: SessionCookieName, SameSite: http.SameSiteStrictMode, Posture: access.PosturePerRequest}
	if cfg != want {
		t.Errorf("CookieConfig = %+v, want %+v", cfg, want)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("CookieConfig.Validate: %v", err)
	}

	h := newHarness(t)
	h.setupOwner(t.Context())
	route, ok := h.accessRoutes()["authLoginPassword"]
	if !ok {
		t.Fatal("no authLoginPassword route")
	}
	body := `{"username":"` + testUsername + `","password":"` + testPassword + `"}`
	r := httptest.NewRequest(route.Method, "http://marotte.example"+route.Path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	route.Handler.ServeHTTP(w, r)
	if w.Code >= 300 {
		t.Fatalf("the forged-header LAN sign-in = %d %s, want success", w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("the sign-in set %d cookies, want the session's and the device's", len(cookies))
	}
	var session *http.Cookie
	for _, c := range cookies {
		if c.Secure || c.MaxAge != 0 || !c.Expires.IsZero() || strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("cookie %s = %+v, want a plain cookie with no Secure, Max-Age or Expires", c.Name, c)
		}
		if c.Name == SessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no %s cookie among %v", SessionCookieName, cookies)
	}
	if p := h.principal(access.NewSecret(session.Value)); p.Secure {
		t.Error("the forged-header session authenticates as secure")
	}
	rec, ok := h.store.sessionRecord(access.SessionHash(session.Value))
	if !ok {
		t.Fatal("the signed-in session is not stored")
	}
	if rec.Secure {
		t.Error("the forged-header sign-in stored a secure session")
	}
}
