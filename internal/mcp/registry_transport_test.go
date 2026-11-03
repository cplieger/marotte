package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A registry entry names a catalog server, so KAS needs `{"type":"registry"}`
// and nothing it would take for a command or url.
func TestRenderKASServers_RegistryEntry(t *testing.T) {
	out := renderKASServers([]*Server{{Name: "github", Transport: transportRegistry, Enabled: true}}, kasRenderPolicy{})
	raw, err := json.Marshal(out["github"])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"type":"registry"}` {
		t.Errorf("rendered registry entry = %s, want {\"type\":\"registry\"}", raw)
	}
	off := renderKASServers([]*Server{{Name: "github", Transport: transportRegistry}}, kasRenderPolicy{})
	if raw, _ := json.Marshal(off["github"]); string(raw) != `{"type":"registry","disabled":true}` {
		t.Errorf("rendered disabled registry entry = %s", raw)
	}
}

func fieldSet(err error) map[string]string {
	out := map[string]string{}
	for _, fe := range fieldErrors(err) {
		out[fe.Field] = fe.Msg
	}
	return out
}

func TestValidate_RegistryTakesEnvAndHeaders(t *testing.T) {
	if err := validate(&Server{Name: "github", Transport: transportRegistry}); err != nil {
		t.Errorf("validate(bare registry entry) = %v, want nil", err)
	}
	sv := &Server{
		Name: "github", Transport: transportRegistry,
		Env:     []keyPair{{Name: "GITHUB_TOKEN", Value: "test-key"}},
		Headers: []keyPair{{Name: "Authorization", Value: "Bearer test-key"}},
	}
	if err := validate(sv); err != nil {
		t.Errorf("validate(registry with env and headers) = %v, want nil", err)
	}
}

// The overrides go through the same pair rules as stdio env and remote headers.
func TestValidate_RegistryHoldsOverridesToThePairRules(t *testing.T) {
	err := validate(&Server{
		Name: "github", Transport: transportRegistry,
		Env:     []keyPair{{Name: "1BAD", Value: "x"}},
		Headers: []keyPair{{Name: "X-Key", Value: "a"}, {Name: "x-key", Value: "b"}},
	})
	got := fieldSet(err)
	if !strings.Contains(got[fieldEnvPairs], `bad name "1BAD"`) {
		t.Errorf("env refusal = %q, want a bad-name refusal", got[fieldEnvPairs])
	}
	if !strings.Contains(got[fieldHeaderPairs], `duplicate name "x-key"`) {
		t.Errorf("headers refusal = %q, want a case-insensitive duplicate refusal", got[fieldHeaderPairs])
	}
}

func TestValidate_RegistryStillRefusesCommandURLAndOAuth(t *testing.T) {
	err := validate(&Server{
		Name: "github", Transport: transportRegistry,
		Command: "npx", Args: []string{"-y", "pkg"}, URL: "https://x.example/mcp",
		OAuthClientID: "client", OAuthClientMetadataURL: "https://x.example/c.json",
		OAuthRedirectURI: "localhost:7778", OAuthScopes: []string{"read"},
	})
	got := fieldSet(err)
	for _, f := range []string{fieldCommand, fieldArgs, fieldURL} {
		if !strings.Contains(got[f], "the organization's catalog supplies it") {
			t.Errorf("%s refusal = %q, want the catalog-supplies refusal", f, got[f])
		}
	}
	for _, f := range []string{fieldOAuthClientID, fieldOAuthClientMetadataURL, fieldOAuthRedirectURI, fieldOAuthScopes} {
		if !strings.Contains(got[f], "sign-in overrides are not offered for registry servers") {
			t.Errorf("%s refusal = %q, want the sign-in-overrides refusal", f, got[f])
		}
	}
	if len(got) != 7 {
		t.Errorf("refused fields = %v, want exactly the seven above", got)
	}
}

func TestWriteKASConfig_RegistryCarriesEnvAndHeaders(t *testing.T) {
	s, kas := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), &Server{
		Transport: transportRegistry, Name: "catalog", Enabled: true,
		Env:     []keyPair{{Name: "API_KEY", Value: "env-test-value"}},
		Headers: []keyPair{{Name: "X-Api-Key", Value: "header-test-value"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entry := readKASServers(t, kas)["catalog"]
	want := map[string]any{
		"type":    "registry",
		"env":     map[string]any{"API_KEY": "env-test-value"},
		"headers": map[string]any{"X-Api-Key": "header-test-value"},
	}
	got, _ := json.Marshal(entry)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Errorf("rendered registry entry = %s, want %s", got, wantJSON)
	}
}

// A registry row has no url, so the origin guard must not read an edit as a move.
func TestUpdate_RegistryKeepsAMaskedSecret(t *testing.T) {
	s, kas := newIsolatedStore(t)
	orig, err := s.Create(t.Context(), &Server{
		Transport: transportRegistry, Name: "catalog", Enabled: true,
		Env:     []keyPair{{Name: "API_KEY", Value: "env-test-value"}},
		Headers: []keyPair{{Name: "X-Api-Key", Value: "header-test-value"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if orig.Env[0].Value != secretMask || orig.Headers[0].Value != secretMask {
		t.Fatalf("Create returned env %v headers %v, want both masked", orig.Env, orig.Headers)
	}
	out, err := s.update(t.Context(), orig.ID, &Server{
		Transport: transportRegistry, Name: "catalog", Enabled: true,
		Env:     []keyPair{{Name: "API_KEY", Value: secretMask}, {Name: "REGION", Value: "eu"}},
		Headers: []keyPair{{Name: "X-Api-Key", Value: secretMask}},
	})
	if err != nil {
		t.Fatalf("update echoing the mask: %v", err)
	}
	if !slices.ContainsFunc(out.Env, func(kv keyPair) bool { return kv.Name == "REGION" && kv.Value == secretMask }) {
		t.Errorf("update returned env %v, want REGION masked", out.Env)
	}
	entry := readKASServers(t, kas)["catalog"]
	env, _ := entry["env"].(map[string]any)
	headers, _ := entry["headers"].(map[string]any)
	if env["API_KEY"] != "env-test-value" || env["REGION"] != "eu" || len(env) != 2 {
		t.Errorf("rendered env = %v, want the stored API_KEY kept and REGION added", entry["env"])
	}
	if headers["X-Api-Key"] != "header-test-value" || len(headers) != 1 {
		t.Errorf("rendered headers = %v, want the stored X-Api-Key kept", entry["headers"])
	}
}
