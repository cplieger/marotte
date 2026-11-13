package applogin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/slogx/capture"
)

func hostPolicy(raw string) *httpserver.HostPolicy {
	p, _ := httpserver.ParseHostList(strings.Split(raw, ","), httpserver.WithLoopbackExempt(true))
	return p
}

// subtestName keeps a URL's subtest name free of "/", which go test reads as a level.
func subtestName(raw string) string { return strings.NewReplacer("://", "_", "/", "_").Replace(raw) }

func TestParseCanonicalOrigin_Checks(t *testing.T) {
	for _, c := range []struct {
		raw, failed, rpID string
	}{
		{"https://Marotte.Example", "", "marotte.example"},
		{"https://marotte.example:8443/", "", "marotte.example"},
		{"", "absent", ""},
		{"http://marotte.example", "scheme", ""},
		{"https://u:p@marotte.example", "userinfo", ""},
		{"https://marotte.example/app", "path", ""},
		{"https://marotte.example/?q=1", "query", ""},
		{"https://marotte.example/#f", "fragment", ""},
		{"https://192.0.2.5", "rp_id", ""},
		{"https://[2001:db8::1]", "rp_id", ""},
	} {
		t.Run(subtestName(c.raw), func(t *testing.T) {
			o := parseOrigin(c.raw, nil)
			if o.Failed() != c.failed || o.RPID() != c.rpID {
				t.Errorf("parse(%q) = failed %q rpID %q, want %q and %q", c.raw, o.Failed(), o.RPID(), c.failed, c.rpID)
			}
			if c.failed == "" && (o.RP() == nil || o.OIDCRedirect() != o.String()+"/access/oidc/callback") {
				t.Errorf("a valid origin's RP %v, redirect %q", o.RP(), o.OIDCRedirect())
			}
			if c.failed != "" && (o.RP() != nil || o.OIDCRedirect() != "" || o.String() != "") {
				t.Error("a degraded origin answers an RP, a redirect or an origin")
			}
		})
	}
}

// TestParseCanonicalOrigin_SerializedForm: the origin is the one a browser sends (RFC 6454
// section 6.2), so the default port is dropped and the host lowercased.
func TestParseCanonicalOrigin_SerializedForm(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{"https://Marotte.Example:443", "https://marotte.example"},
		{"https://marotte.example:8443/", "https://marotte.example:8443"},
		{"https://marotte.example:0443", "https://marotte.example"},
	} {
		t.Run(subtestName(c.raw), func(t *testing.T) {
			if got := parseOrigin(c.raw, nil).String(); got != c.want {
				t.Errorf("origin of %q = %q, want %q", c.raw, got, c.want)
			}
		})
	}
	if o := parseOrigin("https://marotte.example:99999", nil); o.Valid() {
		t.Errorf("a port past 65535 parsed as %q, want the parse check", o.String())
	}
}

func TestParseCanonicalOrigin_IPLiteralLogsTheRPIDCheck(t *testing.T) {
	logs := capture.Default(t)
	if o := ParseCanonicalOrigin("https://192.0.2.5", nil); o.Valid() {
		t.Fatal("an IP-literal origin is valid")
	}
	if v, ok := logs.AttrValue("access: the canonical origin is unusable", "check"); !ok || v != "rp_id" {
		t.Errorf("ERROR check = (%q, %t), want rp_id", v, ok)
	}
	if v, _ := logs.AttrValue("access: the canonical origin is unusable", "variable"); v != OriginEnv {
		t.Errorf("ERROR variable = %q, want %s", v, OriginEnv)
	}
}

// TestOrigin_AllowedHostsCases: four ALLOWED_HOSTS values.
func TestOrigin_AllowedHostsCases(t *testing.T) {
	token := access.NewSecret("tok-marker")
	browser := func(host string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host, r.RemoteAddr = host, "192.0.2.50:4000"
		return r
	}
	loopback := httptest.NewRequest(http.MethodGet, "/", nil)
	loopback.Host, loopback.RemoteAddr = "localhost", "127.0.0.1:4000"
	for _, c := range []struct {
		name, raw                string
		valid, tunnel, admitsTun bool
	}{
		{"unset", "", true, true, true},
		{"canonical_and_localhost", "marotte.example,localhost", true, true, true},
		{"canonical_only", "marotte.example", true, false, false},
		{"all_invalid", "http://bad/,a b", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			policy := hostPolicy(c.raw)
			o := parseOrigin(testOrigin, policy)
			if o.Valid() != c.valid {
				t.Errorf("origin valid = %t, want %t", o.Valid(), c.valid)
			}
			lines := o.SetupLinks(token, true, policy)
			hasTunnel := false
			for _, l := range lines {
				hasTunnel = hasTunnel || strings.HasPrefix(l, "http://localhost:9847/access/setup#token=tok-marker")
			}
			if hasTunnel != c.tunnel {
				t.Errorf("tunnel form printed %t, want %t (%q)", hasTunnel, c.tunnel, lines)
			}
			if got := policy.Allows(browser("localhost")); got != c.admitsTun {
				t.Errorf("browser request to localhost admitted %t, want %t", got, c.admitsTun)
			}
			if !policy.Allows(loopback) {
				t.Error("the loopback-peer request was refused")
			}
		})
	}
}

func TestOrigin_LinkForms(t *testing.T) {
	const marker = "tok-marker-xyz"
	token := access.NewSecret(marker)
	valid := parseOrigin(testOrigin, nil)
	lines := valid.SetupLinks(token, true, nil)
	want := []string{
		testOrigin + "/access/setup#token=" + marker,
		"http://localhost:9847/access/setup#token=" + marker + " through the SSH tunnel (docs/hardening.md)",
		"Or open marotte at the address you use and add /access/setup#token=" + marker + " to the end.",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("SetupLinks = %q, want %q", lines, want)
	}
	if link, ok := valid.RecoveryLink(token); !ok || link != testOrigin+"/access/recover#token="+marker {
		t.Errorf("RecoveryLink = (%q, %t)", link, ok)
	}
	degraded := parseOrigin("", nil)
	dl := degraded.SetupLinks(token, false, nil)
	if len(dl) != 1 || !strings.Contains(dl[0], "/access/setup#token="+marker) {
		t.Errorf("degraded SetupLinks with the switch off = %q, want the origin-free form only", dl)
	}
	if link, ok := degraded.RecoveryLink(token); ok || link != "" {
		t.Errorf("degraded RecoveryLink = (%q, %t), want none", link, ok)
	}
	if strings.Contains(fmt.Sprintf("%v", token), marker) {
		t.Error("the token printed with the v verb shows its text")
	}
}

func TestOrigin_DegradedWordingHasNoPlaceholder(t *testing.T) {
	for _, w := range []string{DegradedMethodReason, DegradedPlainNoticeEnd, DegradedSwitchOffPage, DegradedSwitchOffAsk, DegradedRepairNote} {
		if w == "" || strings.Contains(w, "<") || strings.Contains(w, "https://") || strings.Contains(w, "%") {
			t.Errorf("degraded wording %q is empty or holds a placeholder or origin", w)
		}
	}
}
