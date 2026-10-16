package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/webhttp/v3"
)

// Empty passes for the default Builder ID flow. An attacker-controlled start URL would hand away
// the user's SSO session.
func validateProvider(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxProviderLen {
		return errors.New("provider too long")
	}
	u, perr := url.Parse(v)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("provider must be an https URL")
	}
	// Some HTTP clients use embedded userinfo before a redirect (CWE-601).
	if u.User != nil {
		return errors.New("provider must not contain credentials")
	}
	return nil
}

// validateRegion rejects anything that is not a canonical AWS region id, in any partition. Empty passes so the
// backend picks its default.
func validateRegion(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxRegionLen {
		return errors.New("region too long")
	}
	if !awsRegionRe.MatchString(v) {
		return errors.New("invalid region")
	}
	return nil
}

// parseLoginRequest decodes and validates the POST body, writing the error response itself and returning
// ok=false. An empty body (the default Builder ID flow) yields zero values and ok=true.
func parseLoginRequest(w http.ResponseWriter, r *http.Request) (provider, region string, ok bool) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Provider string `json:"provider"`
		Region   string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("login: body exceeds limit", "limit_bytes", webhttp.MaxJSONBody)
			webhttp.WriteJSONStatus(w, http.StatusRequestEntityTooLarge, httpreply.ErrorJSON("request too large"))
			return "", "", false
		}
		slog.Warn("login: decode body", "error", err)
		httpreply.BadRequest(w, "invalid JSON body")
		return "", "", false
	}
	if err := validateProvider(body.Provider); err != nil {
		httpreply.BadRequest(w, err.Error())
		return "", "", false
	}
	if err := validateRegion(body.Region); err != nil {
		httpreply.BadRequest(w, err.Error())
		return "", "", false
	}
	return body.Provider, body.Region, true
}

// LoginResponse is POST /api/login's answer: what the user opens and enters, and the attempt whoami reports on, so a
// switch whose old account stays signed in is told apart from the new sign-in's outcome.
type LoginResponse struct {
	URL     string `json:"url"`
	Code    string `json:"code"`
	Attempt uint64 `json:"attempt"`
}

// textAPIKeyActive refuses login and logout while KIRO_API_KEY signs the engine in.
const textAPIKeyActive = "marotte signs in with the KIRO_API_KEY environment variable. Remove it from marotte's environment to sign in here."

// handleLogin starts a device sign-in and answers its URL and code; the vault polls under its own lifetime, so the
// client may leave. One sign-in at a time; a concurrent POST gets 409.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	// Audit trail; client_ip is webhttp.ClientIP's spoof-safe host.
	slog.Info("login: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", logsafe.Field(r.Header.Get("User-Agent")))
	if h.apiKey {
		httpreply.Conflict(w, textAPIKeyActive)
		return
	}
	provider, region, ok := parseLoginRequest(w, r)
	if !ok {
		return
	}
	if msg := h.loginRefusal(provider); msg != "" {
		webhttp.WriteJSONStatus(w, http.StatusForbidden, httpreply.ErrorJSON(msg))
		return
	}
	pending, err := h.vault.StartSignIn(r.Context(), h.backend, signin.FlowParams{
		Flow: signin.FlowDevice, StartURL: provider, Region: region,
	})
	switch {
	case err == nil:
		webhttp.WriteJSON(w, LoginResponse{URL: pending.VerificationURL, Code: pending.UserCode, Attempt: pending.Attempt})
	case errors.Is(err, signin.ErrSignInInProgress):
		httpreply.Conflict(w, "login in progress")
	case errors.Is(err, signin.ErrInvalidParams):
		httpreply.BadRequest(w, signInErrorText(err))
	case errors.Is(err, signin.ErrSignInCancelled):
		httpreply.Conflict(w, "the sign-in was cancelled")
	default:
		slog.Warn("login: the sign-in could not start", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON(signInErrorText(err)))
	}
}

// signInErrorText is the user text of a failed start: the backend's own wording when it gave one.
func signInErrorText(err error) string {
	if fe, ok := errors.AsType[*signin.FailureError](err); ok {
		return identityText(fe.Text)
	}
	return "The sign-in could not start. The server log has the details."
}
