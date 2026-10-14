package forges

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/keyenc"
)

// dnsName253 is a DNS name of the 253 octets RFC 1035 allows.
var dnsName253 = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." +
	strings.Repeat("d", 61)

func TestPATLogin_ValidatesItsBodyAndAnswersTheServerMintedID(t *testing.T) {
	giteaUser := `{"id":1,"login":"alice"}`

	t.Run("a Gitea host with a port", func(t *testing.T) {
		h := newConnectHarness(t, &userWire{status: http.StatusOK, body: giteaUser})
		id := h.signedIn(t, KindGitea, "git.test:3000", "gitea-secret", "")
		if want := keyenc.Join("gitea", "git.test:3000"); id != want {
			t.Errorf("minted id = %q, want %q", id, want)
		}
		if rec := h.do(t, http.MethodGet, "/api/forges/"+url.PathEscape(id), ""); rec.Code != http.StatusOK {
			t.Errorf("GET the minted id = %d %s, want its row", rec.Code, rec.Body)
		}
	})

	refusals := []struct {
		name, body string
		status     int
		code       string
		message    string
	}{
		{"an unknown kind", `{"kind":"bitbucket","token":"t"}`, http.StatusBadRequest, codeKindInvalid, ""},
		{"a null body", `null`, http.StatusBadRequest, codeKindInvalid, ""},
		{"an array body", `[{"kind":"github","token":"t"}]`, http.StatusBadRequest, "", "invalid json"},
		{"a trailing value", `{"kind":"github","token":"t"}{"kind":"gitlab"}`, http.StatusBadRequest, "", "invalid json"},
		{"a host with a path", `{"kind":"gitea","host":"git.test/x","token":"t"}`, http.StatusBadRequest, "", errHostInvalid.Error()},
		{"a host with userinfo", `{"kind":"gitea","host":"u@git.test","token":"t"}`, http.StatusBadRequest, "", errHostInvalid.Error()},
		{"a host with a backslash", `{"kind":"gitea","host":"git\\.test","token":"t"}`, http.StatusBadRequest, "", errHostInvalid.Error()},
		{"an empty token", `{"kind":"github","token":""}`, http.StatusBadRequest, codeTokenRequired, ""},
		{
			"a web base on another host", `{"kind":"github","token":"t","web_base_url":"https://evil.test"}`,
			http.StatusBadRequest, codeWebBaseInvalid, "",
		},
		{"a Gitea body with no host", `{"kind":"gitea","token":"t"}`, http.StatusBadRequest, "", errHostInvalid.Error()},
		{
			"a 260-byte host", `{"kind":"gitea","host":"` + dnsName253 + `.e:65535","token":"t"}`,
			http.StatusBadRequest, "", errHostInvalid.Error(),
		},
		{
			"a 9 KiB host", `{"kind":"gitea","host":"` + strings.Repeat("a", 9<<10) + `","token":"t"}`,
			http.StatusBadRequest, "", errHostInvalid.Error(),
		},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			wire := &userWire{status: http.StatusOK, body: giteaUser}
			h := newConnectHarness(t, wire)
			rec := h.do(t, http.MethodPost, loginPath, tc.body)
			body := decodeBody(t, rec)
			if rec.Code != tc.status || (tc.code != "" && body["code"] != tc.code) ||
				(tc.message != "" && body["error"] != tc.message) {
				t.Errorf("POST %s %s = %d %v, want %d code %q error %q", loginPath, tc.name, rec.Code, body, tc.status, tc.code, tc.message)
			}
			if n := len(wire.requests()); n != 0 {
				t.Errorf("the forge saw %d requests, want none", n)
			}
			h.assertNothingStored(t, "https://git.test")
		})
	}

	t.Run("a token the forge refuses", func(t *testing.T) {
		h := newConnectHarness(t, &userWire{status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`})
		rec := h.login(t, KindGitHub, "", "ghp_revoked", "")
		if body := decodeBody(t, rec); rec.Code != http.StatusUnauthorized || body["kind"] != "unauthorized" || body["error"] == "" {
			t.Errorf("a refused token = %d %v, want 401 with the envelope's sentence and kind unauthorized", rec.Code, body)
		}
	})
	t.Run("a forge outage", func(t *testing.T) {
		h := newConnectHarness(t, &userWire{status: http.StatusServiceUnavailable, body: `{"message":"down"}`})
		rec := h.login(t, KindGitHub, "", "ghp", "")
		if body := decodeBody(t, rec); rec.Code != http.StatusBadGateway || body["kind"] != "upstream" {
			t.Errorf("a forge answering 503 = %d %v, want 502 with forgeapi's upstream envelope", rec.Code, body)
		}
	})

	t.Run("a 259-byte host", func(t *testing.T) {
		h := newConnectHarness(t, &userWire{status: http.StatusOK, body: giteaUser})
		host := dnsName253 + ":65535"
		rec := h.login(t, KindGitea, host, "gitea-secret", "")
		var got LoginResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("POST %s with a 259-byte host = %d %s, want 200", loginPath, rec.Code, rec.Body)
		}
		if _, parsed, ok := parseID(got.ForgeID); keyenc.IsHashed(got.ForgeID) || !ok || parsed != host {
			t.Errorf("minted id %q: hashed %v, parsed host %q (%v); want a readable id naming the host",
				got.ForgeID, keyenc.IsHashed(got.ForgeID), parsed, ok)
		}
		if rec := h.do(t, http.MethodDelete, "/api/forges/"+url.PathEscape(got.ForgeID), ""); rec.Code != http.StatusOK {
			t.Errorf("DELETE the minted id = %d %s, want 200", rec.Code, rec.Body)
		}
	})

	t.Run("a device start with a 9 KiB host", func(t *testing.T) {
		h := newConnectHarness(t, nil)
		rec := h.do(t, http.MethodPost, "/api/forges/oauth/gitlab/start",
			`{"host":"`+strings.Repeat("a", 9<<10)+`","client_id":"app"}`)
		if body := decodeBody(t, rec); rec.Code != http.StatusBadRequest || body["error"] != errHostInvalid.Error() {
			t.Errorf("a device start with a 9 KiB host = %d %v, want 400 naming the host", rec.Code, body)
		}
		if n := h.handler.grants.held(); n != 0 {
			t.Errorf("the registry holds %d grants, want none", n)
		}
	})
}
