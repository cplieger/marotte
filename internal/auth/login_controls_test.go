package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func managedFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "managed-settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadLoginOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want LoginOptions
	}{
		{name: "no_file", want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "deny_builder_id", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["builder_id","google","github"]}]}`, want: LoginOptions{IDC: true}},
		{name: "deny_idc", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["idc"]}]}`, want: LoginOptions{BuilderID: true}},
		{
			name: "prefill",
			body: `{"settings":{"idc_start_url":"https://d-123.awsapps.com/start","idc_region":"eu-west-1"}}`,
			want: LoginOptions{BuilderID: true, IDC: true, IDCStartURL: "https://d-123.awsapps.com/start", IDCRegion: "eu-west-1"},
		},
		{name: "http_start_url_dropped", body: `{"settings":{"idc_start_url":"http://x.example/start","idc_region":"nope"}}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "every_method_denied_is_ignored", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["google","github","builder_id","idc","external_idp"]}]}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "allow_effect_ignores_the_restriction", body: `{"rules":[{"capability":"signin_method","effect":"allow","match":["idc"]}]}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "unknown_exclude_ignores_the_restriction", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["idc"],"exclude":["sso"]}]}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "unknown_match_dropped", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["sso","idc"]}]}`, want: LoginOptions{BuilderID: true}},
		{name: "nothing_recognized_ignored", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["sso"]}]}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "exclude_spares_a_method", body: `{"rules":[{"capability":"signin_method","effect":"deny","match":["idc","builder_id"],"exclude":["idc"]}]}`, want: LoginOptions{IDC: true}},
		{name: "other_capabilities_ignored", body: `{"rules":[{"capability":"shell","effect":"deny"}]}`, want: LoginOptions{BuilderID: true, IDC: true}},
		{name: "invalid_json", body: `{`, want: LoginOptions{BuilderID: true, IDC: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "absent.json")
			if tc.body != "" {
				path = managedFile(t, tc.body)
			}
			if got := readLoginOptions(path); got != tc.want {
				t.Errorf("readLoginOptions(%s) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// A denied method is refused before kiro-cli is spawned, so no device code is minted.
func TestHandleLogin_RefusesAMethodTheAdministratorDenies(t *testing.T) {
	h := NewHandler(func() string { return "/nonexistent/kiro-cli" })
	h.managedSettingsPath = managedFile(t, `{"rules":[{"capability":"signin_method","effect":"deny","match":["builder_id"]}]}`)
	rec := httptest.NewRecorder()
	h.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/login for Builder ID under a builder_id deny = %d, want 403: %s", rec.Code, rec.Body)
	}
}
