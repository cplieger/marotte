package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// acme is a complete organization provider as the card saves it.
func acme() *IdentityProvider {
	return &IdentityProvider{
		Issuer: "https://login.acme.example", ClientID: "kiro-client", Name: "Acme",
		AuthorizationEndpoint: "https://login.acme.example/authorize", TokenEndpoint: "https://login.acme.example/token",
	}
}

// kasIDP is the identityProvider member of the KAS file at path, decoded; nil when the file has none.
func kasIDP(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, ok := readKAS(t, path)[kasIdentityProviderKey]
	if !ok {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode identityProvider %s: %v", raw, err)
	}
	return out
}

func TestWriteKASConfig_RendersIdentityProvider(t *testing.T) {
	t.Run("every_member", func(t *testing.T) {
		s, kas := newIsolatedStore(t)
		if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
			t.Fatalf("putIdentityProvider: %v", err)
		}
		want := map[string]any{
			"issuer": "https://login.acme.example", "clientId": "kiro-client", "name": "Acme",
			"authorizationEndpoint": "https://login.acme.example/authorize", "tokenEndpoint": "https://login.acme.example/token",
		}
		got := kasIDP(t, kas)
		if len(got) != len(want) {
			t.Errorf("identityProvider = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("identityProvider[%q] = %v, want %v", k, got[k], v)
			}
		}
	})
	t.Run("only_the_required_members", func(t *testing.T) {
		s, kas := newIsolatedStore(t)
		if _, err := s.putIdentityProvider(t.Context(), &IdentityProvider{Issuer: "https://idp.example", ClientID: "c"}); err != nil {
			t.Fatalf("putIdentityProvider: %v", err)
		}
		// KAS's schema refuses an empty name, and an empty endpoint is not a URL.
		got := kasIDP(t, kas)
		if len(got) != 2 || got["issuer"] != "https://idp.example" || got["clientId"] != "c" {
			t.Errorf("identityProvider = %v, want only issuer and clientId", got)
		}
	})
}

func TestWriteKASConfig_RemovedIdentityProviderLeavesNoKey(t *testing.T) {
	s, kas := newIsolatedStore(t)
	if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
		t.Fatalf("putIdentityProvider: %v", err)
	}
	if err := s.deleteIdentityProvider(t.Context()); err != nil {
		t.Fatalf("deleteIdentityProvider: %v", err)
	}
	if got := kasIDP(t, kas); got != nil {
		t.Errorf("identityProvider = %v after the removal, want no member", got)
	}
	if _, ok := readKAS(t, kas)[kasServerKey]; !ok {
		t.Error("the removal dropped mcpServers too")
	}
}

// A provider written outside marotte (a hand edit, `kiro-cli mcp idp setup`) is marotte's key: the first render
// with none configured removes it.
func TestWriteKASConfig_RemovesAProviderSetOutsideMarotte(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas.json")
	writeKASFile(t, kas, `{"identityProvider":{"issuer":"https://elsewhere.example","clientId":"x"}}`)
	if _, err := New(t.Context(), dir, nil, withKASConfigPath(kas)); err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := kasIDP(t, kas); got != nil {
		t.Errorf("identityProvider = %v after boot, want it removed", got)
	}
}

func TestHealKASConfig_RevertsAHandEditedIdentityProvider(t *testing.T) {
	s, kas := newIsolatedStore(t)
	if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
		t.Fatalf("putIdentityProvider: %v", err)
	}
	doc := readKAS(t, kas)
	doc[kasIdentityProviderKey] = json.RawMessage(`{"issuer":"https://evil.example","clientId":"kiro-client","name":"Acme"}`)
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeKASFile(t, kas, string(data))
	logs := captureSlog(t)

	wrote, err := s.HealKASConfig(t.Context(), "poll")
	if err != nil || !wrote {
		t.Fatalf("HealKASConfig = (%v, %v), want a write", wrote, err)
	}
	if got := kasIDP(t, kas); got["issuer"] != "https://login.acme.example" || got["tokenEndpoint"] != "https://login.acme.example/token" {
		t.Errorf("identityProvider after the heal = %v, want the stored provider back", got)
	}
	line := logs.String()
	if !strings.Contains(line, driftWarn) || !strings.Contains(line, "identity_provider_changed=") ||
		!strings.Contains(line, "issuer") {
		t.Errorf("drift WARN = %q, want it to name the changed identityProvider members", line)
	}
	if strings.Contains(line, "evil.example") {
		t.Errorf("drift WARN = %q, want member names only, never values", line)
	}
	if wrote, err := s.HealKASConfig(t.Context(), "poll"); err != nil || wrote {
		t.Errorf("second HealKASConfig = (%v, %v), want nothing to heal", wrote, err)
	}
}

func TestValidate_IdentityProvider(t *testing.T) {
	ok := []*IdentityProvider{
		acme(),
		{Issuer: "https://idp.example", ClientID: "c"},
		{Issuer: "http://localhost:8080/realms/dev", ClientID: "c"},
		{Issuer: "http://127.0.0.1:9000", ClientID: "c"},
		{Issuer: "http://[::1]:9000", ClientID: "c", TokenEndpoint: "http://localhost:9000/token"},
	}
	for _, p := range ok {
		if err := validateIdentityProvider(p); err != nil {
			t.Errorf("validateIdentityProvider(%+v) = %v, want accepted (KAS accepts it)", p, err)
		}
	}
	bad := []struct {
		p     *IdentityProvider
		field string
	}{
		{&IdentityProvider{Issuer: "http://idp.example", ClientID: "c"}, fieldIDPIssuer},
		{&IdentityProvider{ClientID: "c"}, fieldIDPIssuer},
		{&IdentityProvider{Issuer: "idp.example", ClientID: "c"}, fieldIDPIssuer},
		{&IdentityProvider{Issuer: "https://idp.example"}, fieldIDPClientID},
		{&IdentityProvider{Issuer: "https://idp.example", ClientID: "c\n"}, fieldIDPClientID},
		{&IdentityProvider{Issuer: "https://idp.example", ClientID: "c", Name: strings.Repeat("n", idpNameMax+1)}, fieldIDPName},
		{&IdentityProvider{Issuer: "https://idp.example", ClientID: "c", AuthorizationEndpoint: "http://idp.example/auth"}, fieldIDPAuthorizationEndpoint},
		{&IdentityProvider{Issuer: "https://idp.example", ClientID: "c", TokenEndpoint: "ftp://localhost/token"}, fieldIDPTokenEndpoint},
	}
	for _, tc := range bad {
		fields := fieldErrors(validateIdentityProvider(tc.p))
		if len(fields) != 1 || fields[0].Field != tc.field {
			t.Errorf("validateIdentityProvider(%+v) fields = %+v, want one on %s", tc.p, fields, tc.field)
		}
	}
}

// An empty name is the absent member: the card's empty field saves, and the render leaves KAS's own display name.
func TestPutIdentityProvider_TrimsAndTreatsAnEmptyNameAsAbsent(t *testing.T) {
	s, kas := newIsolatedStore(t)
	got, err := s.putIdentityProvider(t.Context(), &IdentityProvider{Issuer: " https://idp.example ", ClientID: " c ", Name: "  "})
	if err != nil {
		t.Fatalf("putIdentityProvider: %v", err)
	}
	if *got != (IdentityProvider{Issuer: "https://idp.example", ClientID: "c"}) {
		t.Errorf("stored = %+v, want trimmed with no name", got)
	}
	if _, has := kasIDP(t, kas)["name"]; has {
		t.Error("an empty name was rendered, which KAS refuses (name min 1)")
	}
}

func TestPersist_IdentityProviderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas.json")
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
		t.Fatalf("putIdentityProvider: %v", err)
	}
	reloaded, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, unreadable := reloaded.getIdentityProvider()
	if got == nil || *got != *acme() || unreadable {
		t.Errorf("reloaded provider = %+v (unreadable %v), want %+v", got, unreadable, acme())
	}
}

// A hand-corrupted provider stays in the record and in force, KAS's key as the file has it, until a save replaces it.
func TestLoad_AnUnreadableIdentityProviderIsHeld(t *testing.T) {
	dir := t.TempDir()
	kas := filepath.Join(dir, "kas.json")
	const kept = `{"issuer":"https://login.acme.example","clientId":"kiro-client"}`
	writeKASFile(t, kas, `{"identityProvider":`+kept+`}`)
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"),
		[]byte(`{"version":1,"servers":[],"identity_provider":{"issuer":"http://not-https.example","client_id":"kiro-client"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), dir, nil, withKASConfigPath(kas))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, unreadable := s.getIdentityProvider(); got != nil || !unreadable {
		t.Errorf("getIdentityProvider = (%+v, %v), want none readable and held", got, unreadable)
	}
	if got := kasIDP(t, kas); got["issuer"] != "https://login.acme.example" {
		t.Errorf("KAS identityProvider = %v, want the file's own kept while the record's is held", got)
	}
	if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
		t.Fatalf("putIdentityProvider: %v", err)
	}
	rec, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rec), "not-https") {
		t.Errorf("record = %s, want the held provider replaced by the save", rec)
	}
}

// An administrator's documented block installs in one paste; a different configured provider is never replaced.
func TestImport_IdentityProviderBlockInstallsWhenNoneIsSet(t *testing.T) {
	const block = `{
	  "identityProvider": {"issuer": "https://login.acme.example", "clientId": "kiro-client", "name": "Acme"},
	  "mcpServers": {"docs": {"url": "https://mcp.acme.example/docs", "oauth": {"mode": "enterprise"}}}
	}`
	t.Run("none_set", func(t *testing.T) {
		s, mux := newRoutedStore(t)
		decodeImport(t, postImport(t, mux, block))
		got, _ := s.getIdentityProvider()
		if got == nil || got.Issuer != "https://login.acme.example" || got.ClientID != "kiro-client" || got.Name != "Acme" {
			t.Errorf("provider after the paste = %+v, want the pasted one", got)
		}
		if len(s.list(t.Context())) != 1 {
			t.Error("the servers did not land with the provider")
		}
	})
	t.Run("a_different_one_set", func(t *testing.T) {
		s, mux := newRoutedStore(t)
		other := &IdentityProvider{Issuer: "https://other.example", ClientID: "o"}
		if _, err := s.putIdentityProvider(t.Context(), other); err != nil {
			t.Fatal(err)
		}
		got := decodeImport(t, postImport(t, mux, block))
		if p, _ := s.getIdentityProvider(); p == nil || *p != *other {
			t.Errorf("provider after the paste = %+v, want the configured one kept", p)
		}
		if !strings.Contains(strings.Join(got.Notes, "\n"), "Organization sign-in") {
			t.Errorf("notes = %v, want one naming the card", got.Notes)
		}
	})
	t.Run("the_same_one_set", func(t *testing.T) {
		s, mux := newRoutedStore(t)
		if _, err := s.putIdentityProvider(t.Context(), &IdentityProvider{
			Issuer: "https://login.acme.example", ClientID: "kiro-client", Name: "Acme",
		}); err != nil {
			t.Fatal(err)
		}
		got := decodeImport(t, postImport(t, mux, block))
		if len(got.Notes) != 0 {
			t.Errorf("notes = %v, want none for the provider already set", got.Notes)
		}
	})
	t.Run("only_a_provider", func(t *testing.T) {
		s, mux := newRoutedStore(t)
		decodeImport(t, postImport(t, mux, `{"identityProvider":{"issuer":"https://idp.example","clientId":"c"}}`))
		if p, _ := s.getIdentityProvider(); p == nil || p.Issuer != "https://idp.example" {
			t.Errorf("provider = %+v, want the pasted one", p)
		}
	})
}

func TestImport_IdentityProviderKeysAreClassified(t *testing.T) {
	cases := map[string]string{
		"typo":     `{"identityProvider":{"issuer":"https://idp.example","clientID":"c"},"mcpServers":{"a":{"command":"x"}}}`,
		"insecure": `{"identityProvider":{"issuer":"http://idp.example","clientId":"c"},"mcpServers":{"a":{"command":"x"}}}`,
		"shape":    `{"identityProvider":"https://idp.example","mcpServers":{"a":{"command":"x"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, mux := newRoutedStore(t)
			if rec := postImport(t, mux, body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d %s, want 400", rec.Code, rec.Body.String())
			}
			if p, _ := s.getIdentityProvider(); p != nil || len(s.list(t.Context())) != 0 {
				t.Error("a refused paste stored something")
			}
		})
	}
}

// recordingReset counts the session resets the store runs.
type recordingReset struct{ n int }

func (r *recordingReset) reset(context.Context) error {
	r.n++
	return nil
}

func TestUpdateIdentityProvider_IssuerChangeForgetsSessions(t *testing.T) {
	steps := []struct {
		name  string
		apply func(*Store) error
		want  int
	}{
		{"first_set", func(s *Store) error { _, err := s.putIdentityProvider(context.Background(), acme()); return err }, 0},
		{"name_only", func(s *Store) error {
			p := acme()
			p.Name = "Acme Corp"
			_, err := s.putIdentityProvider(context.Background(), p)
			return err
		}, 0},
		{"endpoint_only", func(s *Store) error {
			p := acme()
			p.Name, p.TokenEndpoint = "Acme Corp", ""
			_, err := s.putIdentityProvider(context.Background(), p)
			return err
		}, 0},
		{"issuer", func(s *Store) error {
			_, err := s.putIdentityProvider(context.Background(), &IdentityProvider{Issuer: "https://new.example", ClientID: "kiro-client"})
			return err
		}, 1},
		{"client_id", func(s *Store) error {
			_, err := s.putIdentityProvider(context.Background(), &IdentityProvider{Issuer: "https://new.example", ClientID: "other"})
			return err
		}, 2},
		{"removal", func(s *Store) error { return s.deleteIdentityProvider(context.Background()) }, 3},
		{"removal_of_none", func(s *Store) error { return s.deleteIdentityProvider(context.Background()) }, 3},
	}
	s := newTestStore(t)
	r := &recordingReset{}
	s.SetIdentityProviderSessionReset(r.reset)
	for _, step := range steps {
		if err := step.apply(s); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if r.n != step.want {
			t.Errorf("after %s: resets = %d, want %d", step.name, r.n, step.want)
		}
	}
}

// The reset runs once KAS's file names the new provider, so a renewal under the old one cannot store it again.
func TestUpdateIdentityProvider_ForgetsSessionsAfterTheKASWrite(t *testing.T) {
	s, kas := newIsolatedStore(t)
	if _, err := s.putIdentityProvider(t.Context(), acme()); err != nil {
		t.Fatal(err)
	}
	var seen string
	s.SetIdentityProviderSessionReset(func(context.Context) error {
		seen, _ = kasIDP(t, kas)["issuer"].(string)
		return errors.New("disk full")
	})
	if _, err := s.putIdentityProvider(t.Context(), &IdentityProvider{Issuer: "https://new.example", ClientID: "c"}); err != nil {
		t.Fatalf("putIdentityProvider = %v, want the save to stand when the reset fails", err)
	}
	if seen != "https://new.example" {
		t.Errorf("KAS issuer at the reset = %q, want the new provider already written", seen)
	}
}

func TestIdentityProviderRoute(t *testing.T) {
	s, mux := newRoutedStore(t)
	s.SetIdentityProviderStatus(func(_ context.Context, cfg *IdentityProvider) (marotte.MCPIdentityProviderPayload, *marotte.SubjectStamp) {
		p := marotte.MCPIdentityProviderPayload{Known: true}
		if cfg != nil {
			p.Status = &marotte.MCPIdentityProviderStatus{State: marotte.MCPIdentityProviderSignedOut, Issuer: cfg.Issuer}
		}
		return p, &marotte.SubjectStamp{Kind: "mcp_identity_provider", Version: "3"}
	})
	if rec := doJSON(t, mux, http.MethodPut, "/api/mcp/identity-provider", map[string]string{"issuer": "http://idp.example", "client_id": "c"}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), fieldIDPIssuer) {
		t.Errorf("PUT insecure = %d %s, want 400 naming %s", rec.Code, rec.Body.String(), fieldIDPIssuer)
	}
	if rec := doJSON(t, mux, http.MethodPut, "/api/mcp/identity-provider", map[string]string{"issuer": "https://idp.example", "client_id": "c"}); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s, want 200", rec.Code, rec.Body.String())
	}
	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/identity-provider", nil)
	var got IdentityProviderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET %s: %v", rec.Body.String(), err)
	}
	if got.Config == nil || got.Config.Issuer != "https://idp.example" || got.Status.Status == nil ||
		got.Status.Status.Issuer != "https://idp.example" || got.Subject == nil || got.Subject.Version != "3" {
		t.Errorf("GET = %s, want the config with the status read for it and its stamp", rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodDelete, "/api/mcp/identity-provider", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s, want 200", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, mux, http.MethodGet, "/api/mcp/identity-provider", nil)
	if !strings.Contains(rec.Body.String(), `"config":null`) {
		t.Errorf("GET after DELETE = %s, want a null config", rec.Body.String())
	}
}
