package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/webhttp/v4"
)

// textSignOutNotSaved is the logout answer when the saved sign-in could not be removed: nothing is signed out.
const textSignOutNotSaved = "marotte could not remove the saved sign-in, so you are still signed in. The server log has the details."

// textSignOutInDoubt is the logout answer when the removal was not confirmed in time.
const textSignOutInDoubt = "marotte could not confirm the sign-out yet. Check the account menu again in a moment."

// handleLogout signs the backend out of the vault. The engine keeps a token it already holds until shortly before it
// expires, so chats stop using the account within about four minutes; an engine restart stops it at once.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	slog.Info("logout: request received",
		"client_ip", webhttp.ClientIP(r, h.trusted...),
		"user_agent", logsafe.Field(r.Header.Get("User-Agent")))
	if h.apiKey {
		httpreply.Conflict(w, textAPIKeyActive)
		return
	}
	err := h.vault.SignOut(h.backend)
	switch {
	case err == nil:
		webhttp.WriteJSON(w, map[string]string{"output": "Signed out"})
	case errors.Is(err, signin.ErrInDoubt):
		slog.Error("logout: the sign-out was not confirmed in time", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(textSignOutInDoubt))
	default:
		slog.Error("logout: the saved sign-in could not be removed", "error", err)
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(textSignOutNotSaved))
	}
}
