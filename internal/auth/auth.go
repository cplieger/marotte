// Package auth serves /api/whoami, /api/login and /api/logout over marotte's own sign-in vault (internal/signin): the
// account the agent engine is answered from. It runs no kiro-cli.
package auth

import (
	"net/http"
	"net/netip"
	"regexp"

	"github.com/cplieger/marotte/internal/signin"
)

// Maximum byte lengths for login request fields; 2048 matches browser URL limits.
const (
	maxProviderLen = 2048
	maxRegionLen   = 32
)

// awsRegionRe matches AWS region ids in every partition (us-east-1, cn-north-1, us-gov-west-1, us-isob-east-1).
// Non-empty segments also reject flags, shell metacharacters, whitespace, uppercase and `us--east-1`; the backend
// refuses a region of a partition it does not serve.
var awsRegionRe = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d+$`)

// Handler is the /api/whoami, /api/login and /api/logout bundle for one backend of the vault.
type Handler struct {
	vault   *signin.Vault
	backend signin.BackendID
	// managedSettingsPath is the administrator's file the sign-in controls come from.
	managedSettingsPath string
	// trusted is the reverse-proxy set for webhttp.ClientIP in the login/logout audit logs. Nil logs the socket peer.
	trusted []netip.Prefix
	// apiKey: the engine is signed in by KIRO_API_KEY, which kiro-cli prefers to marotte's answer, so sign-in and
	// sign-out here would change nothing the engine uses.
	apiKey bool
}

// Option configures a Handler at construction time.
type Option func(*Handler)

// WithTrustedProxies sets the reverse-proxy networks trusted when resolving the audit-log client IP. Empty trusts
// nothing, so the socket peer is logged.
func WithTrustedProxies(trusted []netip.Prefix) Option {
	return func(h *Handler) { h.trusted = trusted }
}

// WithAPIKey reports that the agent engine inherits KIRO_API_KEY: whoami names it, and login and logout are refused
// while it does, keeping the vault's stored account for when the key is removed.
func WithAPIKey(active bool) Option {
	return func(h *Handler) { h.apiKey = active }
}

// NewHandler returns the routes over backend's sign-in in vault.
func NewHandler(vault *signin.Vault, backend signin.BackendID, opts ...Option) *Handler {
	h := &Handler{vault: vault, backend: backend, managedSettingsPath: managedSettingsPath}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RegisterRoutes wires GET /api/whoami, POST /api/login, GET /api/login/options and POST /api/logout onto mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/whoami", h.handleWhoami)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/login/options", h.handleLoginOptions)
	mux.HandleFunc("/api/logout", h.handleLogout)
}
