package mcp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func remoteWith(metadataURL, redirect string) *Server {
	return &Server{
		Transport: TransportHTTP, Name: "s", URL: "https://mcp.example/mcp",
		OAuthClientMetadataURL: metadataURL, OAuthRedirectURI: redirect,
	}
}

func TestValidate_OAuthClientMetadataURL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "httpsWithPath", value: "https://slack.test/oauth-client.json"},
		{name: "plainHTTP", value: "http://x.test/c.json", wantErr: true},
		{name: "rootPath", value: "https://x.test/", wantErr: true},
		{name: "noPath", value: "https://x.test", wantErr: true},
		{name: "unparseable", value: "https://%zz/c.json", wantErr: true},
		{name: "controlChar", value: "https://x.test/c\x01.json", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(remoteWith(tc.value, ""))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(clientMetadataUrl=%q) err = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), fieldOAuthClientMetadataURL) {
				t.Errorf("Validate(clientMetadataUrl=%q) err = %q, want it to name %s", tc.value, err, fieldOAuthClientMetadataURL)
			}
		})
	}
}

func TestValidate_OAuthRedirectURI(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "hostPort", value: "localhost:7778"},
		{name: "portOnly", value: ":7778"},
		{name: "loopbackIP", value: "127.0.0.1:7778"},
		{name: "fullURL", value: "http://127.0.0.1:7778/oauth/callback"},
		{name: "foreignHost", value: "example.com:7778", wantErr: true},
		{name: "noPort", value: "localhost", wantErr: true},
		{name: "fullURLNoPort", value: "http://localhost/cb", wantErr: true},
		{name: "httpsScheme", value: "https://localhost:7778", wantErr: true},
		{name: "query", value: "http://localhost:7778/cb?x=1", wantErr: true},
		{name: "fragment", value: "http://localhost:7778/cb#f", wantErr: true},
		{name: "belowRelayFloor", value: ":80", wantErr: true},
		{name: "outOfRange", value: ":70000", wantErr: true},
		{name: "signedPort", value: ":+7778", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(remoteWith("", tc.value))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(redirectUri=%q) err = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), fieldOAuthRedirectURI) {
				t.Errorf("Validate(redirectUri=%q) err = %q, want it to name %s", tc.value, err, fieldOAuthRedirectURI)
			}
		})
	}
}

func TestValidate_StdioRejectsOAuthMetadataFields(t *testing.T) {
	err := validate(&Server{
		Transport: TransportStdio, Name: "s", Command: "npx",
		OAuthClientMetadataURL: "https://x.test/c.json", OAuthRedirectURI: ":7778",
	})
	if err == nil {
		t.Fatal("Validate(stdio with oauth metadata fields) = nil, want both rejected")
	}
	for _, field := range []string{fieldOAuthClientMetadataURL, fieldOAuthRedirectURI} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("Validate(stdio) err = %q, missing %s", err, field)
		}
	}
}

func TestUpdate_ReplacesOAuthMetadataFields(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(t.Context(), remoteWith("", ""))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.update(t.Context(), orig.ID, remoteWith("https://x.test/c.json", ":7778")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := s.get(t.Context(), orig.ID)
	if got == nil || got.OAuthClientMetadataURL != "https://x.test/c.json" || got.OAuthRedirectURI != ":7778" {
		t.Errorf("after Update, record = %+v, want both oauth metadata fields set", got)
	}
}

func TestSameSpec_OAuthMetadataFieldsDistinguish(t *testing.T) {
	base := remoteWith("https://x.test/a.json", ":7778")
	if !sameSpec(base, remoteWith("https://x.test/a.json", ":7778")) {
		t.Fatal("sameSpec(identical) = false")
	}
	if sameSpec(base, remoteWith("https://x.test/b.json", ":7778")) {
		t.Error("sameSpec ignored a changed oauth_client_metadata_url")
	}
	if sameSpec(base, remoteWith("https://x.test/a.json", ":7779")) {
		t.Error("sameSpec ignored a changed oauth_redirect_uri")
	}
}

func TestPersist_OAuthMetadataFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas-mcp.json")
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Create(t.Context(), remoteWith("https://x.test/c.json", "localhost:7778")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	reloaded, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	list := reloaded.list(t.Context())
	if len(list) != 1 || list[0].OAuthClientMetadataURL != "https://x.test/c.json" || list[0].OAuthRedirectURI != "localhost:7778" {
		t.Errorf("reloaded = %+v, want both oauth metadata fields", list)
	}
}

func remoteWithScopes(scopes ...string) *Server {
	sv := remoteWith("", "")
	sv.OAuthScopes = scopes
	return sv
}

// KAS joins the list with a space, so each entry must be one RFC 6749 section 3.3 scope-token.
func TestValidate_OAuthScopes(t *testing.T) {
	tooMany := make([]string, maxOAuthScopes+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("s%d", i)
	}
	cases := []struct {
		name    string
		scopes  []string
		wantErr bool
	}{
		{name: "plainTokens", scopes: []string{"repo", "offline_access", "https://graph.example/.default"}},
		{name: "absent", scopes: nil},
		{name: "atTheLengthCap", scopes: []string{strings.Repeat("a", oauthScopeMax)}},
		{name: "innerSpace", scopes: []string{"read write"}, wantErr: true},
		{name: "doubleQuote", scopes: []string{`re"ad`}, wantErr: true},
		{name: "backslash", scopes: []string{`re\ad`}, wantErr: true},
		{name: "empty", scopes: []string{""}, wantErr: true},
		{name: "controlChar", scopes: []string{"re\x01ad"}, wantErr: true},
		{name: "nonASCII", scopes: []string{"lectüre"}, wantErr: true},
		{name: "overTheLengthCap", scopes: []string{strings.Repeat("a", oauthScopeMax+1)}, wantErr: true},
		{name: "overTheCountCap", scopes: tooMany, wantErr: true},
		{name: "duplicate", scopes: []string{"read", "read"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(remoteWithScopes(tc.scopes...))
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate(oauth_scopes=%q) err = %v, wantErr %v", tc.scopes, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), fieldOAuthScopes) {
				t.Errorf("validate(oauth_scopes=%q) err = %q, want it to name %s", tc.scopes, err, fieldOAuthScopes)
			}
		})
	}
}

func TestValidate_OAuthScopesRefusedOffRemote(t *testing.T) {
	for name, sv := range map[string]*Server{
		"stdio":    {Transport: TransportStdio, Name: "s", Command: "npx", OAuthScopes: []string{"read"}},
		"registry": {Transport: TransportRegistry, Name: "s", OAuthScopes: []string{"read"}},
	} {
		t.Run(name, func(t *testing.T) {
			fields := fieldErrors(validate(sv))
			if !slices.ContainsFunc(fields, func(fe fieldError) bool { return fe.Field == fieldOAuthScopes }) {
				t.Errorf("validate(%s with oauth_scopes) fields = %+v, want one naming %s", name, fields, fieldOAuthScopes)
			}
		})
	}
}

func TestUpdate_OAuthScopesReplaceAndClear(t *testing.T) {
	s := newTestStore(t)
	orig, err := s.Create(t.Context(), remoteWithScopes("read"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.update(t.Context(), orig.ID, remoteWithScopes("read", "write")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.get(t.Context(), orig.ID); got == nil || !slices.Equal(got.OAuthScopes, []string{"read", "write"}) {
		t.Fatalf("after Update, record = %+v, want oauth_scopes [read write]", got)
	}
	if _, err := s.update(t.Context(), orig.ID, remoteWithScopes()); err != nil {
		t.Fatalf("clearing Update: %v", err)
	}
	if got := s.get(t.Context(), orig.ID); got == nil || len(got.OAuthScopes) != 0 {
		t.Errorf("after a clearing Update, record = %+v, want no oauth_scopes", got)
	}
}
