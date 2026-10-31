package auth

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/runesafe/v3"
	"github.com/cplieger/webhttp/v4"
)

// WhoamiState discriminates /api/whoami's three answers; "could not ask" is its own state so the UI offers a retry,
// not a sign-out.
type WhoamiState string

// Every response carries exactly one arm.
const (
	// WhoamiSignedIn carries the account's labels, and Email when the backend learned it.
	WhoamiSignedIn WhoamiState = "signed_in"
	// WhoamiSignedOut carries nothing but the sign-in attempt.
	WhoamiSignedOut WhoamiState = "signed_out"
	// WhoamiUnavailable is marotte not knowing, and carries Reason.
	WhoamiUnavailable WhoamiState = "unavailable"
)

// WhoamiResponse is /api/whoami's typed wire shape. State is the discriminator and each other field belongs to one
// arm. The Login* fields report the latest sign-in attempt on every arm: a poll waits for its own attempt to leave
// pending, since a switch keeps the old account signed in until the new one commits.
type WhoamiResponse struct {
	State WhoamiState `json:"state"`
	// Email and the four labels below belong to the signed_in arm.
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	// Reason belongs to the unavailable arm: a server-authored phrase.
	Reason string `json:"reason,omitempty"`
	// LoginState is the attempt's signin.AttemptState, LoginError a failed attempt's user text.
	LoginState   string `json:"loginState,omitempty"`
	LoginError   string `json:"loginError,omitempty"`
	LoginAttempt uint64 `json:"loginAttempt,omitempty"`
	// APIKey is true while KIRO_API_KEY signs the engine in: sign-in and sign-out answer 409, so the UI offers neither.
	APIKey bool `json:"apiKey,omitempty"`
}

// textAPIKeyAuth is the signed_in arm's method while KIRO_API_KEY signs the engine in.
const textAPIKeyAuth = "API key (KIRO_API_KEY)" //nolint:gosec // G101: a display label naming the variable, not a credential

// handleWhoami answers from the vault's memory; always 200, all three states are answers.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.apiKey {
		webhttp.WriteJSON(w, WhoamiResponse{State: WhoamiSignedIn, Auth: textAPIKeyAuth, APIKey: true})
		return
	}
	st := h.vault.Status(h.backend)
	webhttp.WriteJSON(w, whoamiFrom(&st))
}

// whoamiFrom is a Status as the account menu reads it, every upstream string bounded to one line.
func whoamiFrom(st *signin.Status) WhoamiResponse {
	out := WhoamiResponse{LoginAttempt: st.SignIn.ID, LoginState: string(st.SignIn.State)}
	if st.SignIn.State == signin.AttemptFailed {
		out.LoginError = identityText(st.SignIn.Error)
	}
	switch st.Kind {
	case signin.StatusSignedIn:
		out.State = WhoamiSignedIn
		out.Email = identityText(st.Label.Email)
		out.Auth = identityText(st.Label.Method)
		out.AccountType = identityText(st.Label.AccountType)
		out.StartURL = identityText(st.Label.StartURL)
		out.Region = identityText(st.Label.Region)
	case signin.StatusSignedOut:
		out.State = WhoamiSignedOut
	default:
		out.State = WhoamiUnavailable
		out.Reason = identityText(st.Reason)
	}
	return out
}

const maxIdentityFieldBytes = 256

// identityText prepares one upstream identity string for a single-line UI row, mapping C0/C1, DEL and Bidi controls
// to spaces since these are labels.
func identityText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxIdentityFieldBytes)
}
