package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

const msgIgnoredPublic = "a peer outside TRUSTED_PROXIES forwarded a public client address"

func clientIPRequest(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// TestClientIP_WarnsOnceForAnIgnoredPublicAddress pins the access log's client_ip and its one
// misconfiguration warning: an untrusted peer forwarding a public address logs the peer and warns
// once per process, a private forwarded address stays silent, and a trusted peer logs the client.
func TestClientIP_WarnsOnceForAnIgnoredPublicAddress(t *testing.T) {
	logs := captureLogs(t)
	s := &Server{}

	if got := s.clientIP(clientIPRequest("10.0.0.2:5000", "10.9.9.9")); got != "10.0.0.2" {
		t.Errorf("clientIP(private XFF) = %q, want the peer 10.0.0.2", got)
	}
	if strings.Contains(logs.String(), msgIgnoredPublic) {
		t.Fatalf("logs = %q, a private forwarded address must not warn", logs.String())
	}

	for range 2 {
		if got := s.clientIP(clientIPRequest("10.0.0.2:5000", "203.0.113.9")); got != "10.0.0.2" {
			t.Errorf("clientIP(untrusted peer) = %q, want the peer 10.0.0.2", got)
		}
	}
	if n := strings.Count(logs.String(), msgIgnoredPublic); n != 1 {
		t.Errorf("warnings = %d, want exactly 1 (logs %q)", n, logs.String())
	}
	if !strings.Contains(logs.String(), "peer=10.0.0.2") {
		t.Errorf("logs = %q, want the warning to name the peer", logs.String())
	}

	trusted := &Server{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	if got := trusted.clientIP(clientIPRequest("10.0.0.2:5000", "203.0.113.9")); got != "203.0.113.9" {
		t.Errorf("clientIP(trusted peer) = %q, want the forwarded client 203.0.113.9", got)
	}
}

// TestClientIP_IPv4MappedPeer pins that a dual-stack listener's IPv4-mapped peer logs as IPv4 and
// matches an IPv4 trusted prefix.
func TestClientIP_IPv4MappedPeer(t *testing.T) {
	s := &Server{}
	if got := s.clientIP(clientIPRequest("[::ffff:192.0.2.7]:5000", "")); got != "192.0.2.7" {
		t.Errorf("clientIP(mapped peer) = %q, want 192.0.2.7", got)
	}
	trusted := &Server{trustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}
	if got := trusted.clientIP(clientIPRequest("[::ffff:192.0.2.7]:5000", "203.0.113.9")); got != "203.0.113.9" {
		t.Errorf("clientIP(trusted mapped peer) = %q, want the forwarded client 203.0.113.9", got)
	}
}
