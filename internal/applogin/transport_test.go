package applogin

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/go/slogx/capture"
)

func transportRequest(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestRequestIsHTTPS_AndClientAddr(t *testing.T) {
	p := ParseProxies("10.0.0.0/8")
	tlsReq := transportRequest("192.0.2.9:1000", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	for _, c := range []struct {
		r          *http.Request
		name       string
		https      bool
		ip         string
		proxied    bool
		untrusted  bool
		exposePeer string
	}{
		{tlsReq, "tls", true, "192.0.2.9", false, false, ""},
		{transportRequest("10.1.2.3:443", map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "198.51.100.7"}), "listed_peer", true, "198.51.100.7", true, false, ""},
		{transportRequest("192.168.1.20:5000", map[string]string{"X-Forwarded-Proto": "https"}), "unlisted_lan_peer", false, "192.168.1.20", false, true, "192.168.1.20"},
		{transportRequest("[::ffff:10.1.2.3]:443", map[string]string{"X-Forwarded-Proto": "https"}), "v4_mapped_peer", true, "10.1.2.3", false, false, ""},
		{transportRequest("192.0.2.9:1000", nil), "plain", false, "192.0.2.9", false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := p.RequestIsHTTPS(c.r); got != c.https {
				t.Errorf("RequestIsHTTPS = %t, want %t", got, c.https)
			}
			a := p.ClientAddr(c.r)
			if a.IP.String() != c.ip || a.Proxied != c.proxied || a.UntrustedForwarding != c.untrusted {
				t.Errorf("ClientAddr = %+v, want IP %s proxied %t untrusted %t", a, c.ip, c.proxied, c.untrusted)
			}
			if c.exposePeer != "" {
				peer, _, ok := p.LastUntrustedPeer()
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
	if p.RequestIsHTTPS(transportRequest("192.0.2.1:9", map[string]string{"X-Forwarded-Proto": "https"})) != true {
		t.Error("a valid entry after the malformed one is not trusted")
	}
}

// TestCookieConfig_Predicates: HTTPS is the library's header predicate and
// Persistent marotte's trusted-proxy one, by identity and by behaviour.
func TestCookieConfig_Predicates(t *testing.T) {
	p := ParseProxies("10.0.0.0/8")
	cfg := p.CookieConfig()
	if reflect.ValueOf(cfg.HTTPS).Pointer() != reflect.ValueOf(access.RequestIsHTTPS).Pointer() {
		t.Error("CookieConfig.HTTPS is not access.RequestIsHTTPS")
	}
	if reflect.ValueOf(cfg.Persistent).Pointer() != reflect.ValueOf(p.RequestIsHTTPS).Pointer() {
		t.Error("CookieConfig.Persistent is not the Proxies' RequestIsHTTPS")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("CookieConfig.Validate: %v", err)
	}
	const life = 30 * 24 * time.Hour
	set := func(r *http.Request) *http.Cookie {
		w := httptest.NewRecorder()
		cfg.SetCookie(w, r, "value", int(life/time.Second))
		cs := w.Result().Cookies()
		if len(cs) != 1 {
			t.Fatalf("%d cookies set, want 1", len(cs))
		}
		return cs[0]
	}
	forged := set(transportRequest("192.168.1.20:5000", map[string]string{"X-Forwarded-Proto": "https"}))
	if forged.Name != "__Host-"+SessionCookieName || !forged.Secure || forged.MaxAge != 0 || !forged.Expires.IsZero() {
		t.Errorf("forged-header LAN cookie %+v, want __Host- name, Secure, no Max-Age or Expires", forged)
	}
	plain := set(transportRequest("192.168.1.20:5000", nil))
	if plain.Name != SessionCookieName || plain.Secure || plain.MaxAge != 0 || !plain.Expires.IsZero() {
		t.Errorf("plain cookie %+v, want %s, no Secure, Max-Age or Expires", plain, SessionCookieName)
	}
	httpsReq := transportRequest("10.1.2.3:443", map[string]string{"X-Forwarded-Proto": "https"})
	https := set(httpsReq)
	if !strings.HasPrefix(https.Name, "__Host-") || https.MaxAge != int(life/time.Second) {
		t.Errorf("HTTPS cookie %+v, want __Host- and Max-Age %d", https, int(life/time.Second))
	}
}
