package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/webhttp/v3"
)

// WhoamiState discriminates /api/whoami's three answers; "could not ask" is its own state so the UI offers a retry,
// not a sign-out.
type WhoamiState string

// Every response carries exactly one arm.
const (
	// WhoamiSignedIn carries Email, plus Auth/AccountType/StartURL/Region when kiro-cli reported them.
	WhoamiSignedIn WhoamiState = "signed_in"
	// WhoamiSignedOut is a working kiro-cli reporting nobody signed in; it carries nothing else.
	WhoamiSignedOut WhoamiState = "signed_out"
	// WhoamiUnavailable is marotte not knowing, and carries Reason.
	WhoamiUnavailable WhoamiState = "unavailable"
)

// WhoamiResponse is /api/whoami's typed wire shape. State is the discriminator and each other field belongs to one
// arm. Unlisted kiro-cli fields are dropped, so a compromised or upgraded CLI cannot leak attributes to the browser.
type WhoamiResponse struct {
	State WhoamiState `json:"state"`
	// Email and the four labels below belong to the signed_in arm.
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	// Reason belongs to the unavailable arm: a server-authored phrase, never CLI output.
	Reason string `json:"reason,omitempty"`
}

// handleWhoami answers from the cached identity, never a subprocess: it fires on every page load and SSE reconnect.
// Always 200; all three states are answers.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, h.identity.snapshot())
}

// whoamiInfo normalises kiro-cli's JSON whoami output into a WhoamiResponse, accepting snake_case and camelCase.
// The email decides the arm: without one (null included) it is signed_out. kiro-cli 2.0.1+ appends a non-JSON
// footer, so json.Decoder reads exactly one value.
func whoamiInfo(out []byte) (WhoamiResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return WhoamiResponse{}, err
	}
	if raw == nil {
		return signedOutIdentity(), nil
	}
	email := firstNonEmptyString(raw, "email", "Email")
	if email == "" {
		return signedOutIdentity(), nil
	}
	resp := WhoamiResponse{State: WhoamiSignedIn, Email: email}
	resp.AccountType = firstNonEmptyString(raw, "account_type", "accountType")
	if resp.AccountType != "" {
		resp.Auth = identityText(humanizeAccountType(resp.AccountType))
	}
	resp.StartURL = firstNonEmptyString(raw, "startUrl", "start_url")
	resp.Region = firstNonEmptyString(raw, "region")
	return resp, nil
}

// maxIdentityFieldBytes bounds one identity string on its way to the sidebar and the log.
const maxIdentityFieldBytes = 256

// identityText prepares one upstream identity string for a single-line UI row, mapping C0/C1, DEL and Bidi controls
// to spaces since these are labels.
func identityText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxIdentityFieldBytes)
}

// firstNonEmptyString returns the first non-empty string value among keys in raw, or "". It sanitizes the winner so
// no new field can skip it.
func firstNonEmptyString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return identityText(v)
		}
	}
	return ""
}

// The phrasing of kiro-cli's own plaintext output.
const (
	authBuilderID      = "Logged in with Builder ID"
	authIdentityCenter = "Logged in with IAM Identity Center"
	authSocialLogin    = "Logged in with social login"
	authPrefixGeneric  = "Logged in with "
)

var accountTypeLabels = map[string]string{
	"builderid":         authBuilderID,
	"identitycenter":    authIdentityCenter,
	"iamidentitycenter": authIdentityCenter,
	"social":            authSocialLogin,
}

func humanizeAccountType(t string) string {
	if label, ok := accountTypeLabels[strings.ToLower(t)]; ok {
		return label
	}
	return authPrefixGeneric + t
}
