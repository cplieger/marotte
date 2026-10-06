package auth

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"slices"

	"github.com/cplieger/webhttp/v3"
)

// managedSettingsPath is the administrator's file kiro-cli's login reads its sign-in controls from on Linux.
const managedSettingsPath = "/etc/kiro/managed-settings.json"

// managedSettingsCap bounds the read; a real file is a few hundred bytes.
const managedSettingsCap = 64 * 1024

// The sign-in methods kiro-cli's login controls name (fig_auth login_controls).
const (
	signinGoogle      = "google"
	signinGitHub      = "github"
	signinBuilderID   = "builder_id"
	signinIDC         = "idc"
	signinExternalIDP = "external_idp"
)

var signinMethods = []string{signinGoogle, signinGitHub, signinBuilderID, signinIDC, signinExternalIDP}

// LoginOptions is GET /api/login/options: which of marotte's two sign-in doors the administrator permits and the
// start URL to pre-fill.
type LoginOptions struct {
	IDCStartURL string `json:"idc_start_url,omitempty"`
	IDCRegion   string `json:"idc_region,omitempty"`
	BuilderID   bool   `json:"builder_id"`
	IDC         bool   `json:"idc"`
}

// managedSettings is the part of the administrator's file kiro-cli's login reads.
type managedSettings struct {
	Settings struct {
		IDCStartURL string `json:"idc_start_url"`
		IDCRegion   string `json:"idc_region"`
	} `json:"settings"`
	Rules []struct {
		Capability string   `json:"capability"`
		Effect     string   `json:"effect"`
		Match      []string `json:"match"`
		Exclude    []string `json:"exclude"`
	} `json:"rules"`
}

// readLoginOptions mirrors kiro-cli's login controls closely enough to hide what it would refuse. Every malformed
// restriction kiro-cli ignores is ignored here too (a non-deny rule, a match naming nothing known, an unknown
// exclude, a rule set denying all or none), so marotte never hides a permitted method.
func readLoginOptions(path string) LoginOptions {
	opts := LoginOptions{BuilderID: true, IDC: true}
	raw, err := readManagedSettings(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("login controls: the managed settings file could not be read; every sign-in method is offered", "path", path, "error", err)
		}
		return opts
	}
	var ms managedSettings
	if err := json.Unmarshal(raw, &ms); err != nil {
		slog.Warn("login controls: the managed settings file is not valid JSON; every sign-in method is offered", "path", path, "error", err)
		return opts
	}
	if validateProvider(ms.Settings.IDCStartURL) == nil {
		opts.IDCStartURL = ms.Settings.IDCStartURL
	}
	if validateRegion(ms.Settings.IDCRegion) == nil {
		opts.IDCRegion = ms.Settings.IDCRegion
	}
	denied, ok := deniedSigninMethods(ms)
	if !ok {
		return opts
	}
	opts.BuilderID = !denied[signinBuilderID]
	opts.IDC = !denied[signinIDC]
	return opts
}

func deniedSigninMethods(ms managedSettings) (map[string]bool, bool) {
	denied := map[string]bool{}
	for _, r := range ms.Rules {
		if r.Capability != "signin_method" {
			continue
		}
		if r.Effect != "deny" || slices.ContainsFunc(r.Exclude, unknownSigninMethod) {
			return nil, false
		}
		if denyMatched(r.Match, r.Exclude, denied) == 0 {
			return nil, false
		}
	}
	if len(denied) == 0 || len(denied) == len(signinMethods) {
		return nil, false
	}
	return denied, true
}

// denyMatched adds each known method that match names and exclude does not to denied, returning how many.
func denyMatched(match, exclude []string, denied map[string]bool) int {
	named := 0
	for _, m := range match {
		if unknownSigninMethod(m) || slices.Contains(exclude, m) {
			continue
		}
		denied[m] = true
		named++
	}
	return named
}

func unknownSigninMethod(m string) bool { return !slices.Contains(signinMethods, m) }

func readManagedSettings(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(io.LimitReader(f, managedSettingsCap))
}

// handleLoginOptions serves GET /api/login/options.
func (h *Handler) handleLoginOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	webhttp.WriteJSON(w, readLoginOptions(h.managedSettingsPath))
}

// loginRefusal names why the administrator forbids a login request, empty when permitted, before a device code is
// minted.
func (h *Handler) loginRefusal(provider string) string {
	opts := readLoginOptions(h.managedSettingsPath)
	if provider == "" && !opts.BuilderID {
		return "AWS Builder ID sign-in is not permitted by your administrator."
	}
	if provider != "" && !opts.IDC {
		return "Sign-in with your organization is not permitted by your administrator."
	}
	return ""
}
