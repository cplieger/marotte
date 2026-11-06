// Package notify serves POST /api/sessions/{session}/notify, the door an outside service uses to
// wake a chat: a steer while the chat's turn is live, a prompt while it is idle.
package notify

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/ids"
)

// TokenEnv names the bearer token the route admits; envx also reads it from TokenEnv+"_FILE".
const TokenEnv = "MAROTTE_NOTIFY_TOKEN"

// A configured token is a generated secret sent in an Authorization header, so it must be an
// RFC 6750 b64token, long enough not to be guessed and short enough for one header line.
const (
	minTokenBytes = 32
	maxTokenBytes = 512
)

const (
	routePrefix = "/api/sessions/"
	routeSuffix = "/notify"
)

// Gate decides who may call the notify route. Its zero value is the door off: every request
// answers 401 naming TokenEnv.
type Gate struct {
	token httpserver.StaticTokenVerifier
	on    bool
}

// ParseToken builds the Gate for a configured token, keeping only its digest. A malformed token
// is an error naming TokenEnv, never echoing the value.
func ParseToken(value string) (Gate, error) {
	if len(value) < minTokenBytes || len(value) > maxTokenBytes || !isB64Token(value) {
		return Gate{}, errors.New(TokenEnv + " is malformed: want 32 to 512 characters of A-Z a-z 0-9 - . _ ~ + / with optional trailing =")
	}
	return Gate{token: httpserver.NewStaticTokenVerifier(value), on: true}, nil
}

// On reports whether a token is configured.
func (g Gate) On() bool { return g.on }

// Authorize is the route's one credential check: it reports whether r may call the notify verb,
// and on false has written the 401. Every other credential is refused here, so swapping this
// method is the whole change when the route moves to a scoped key.
func (g Gate) Authorize(w http.ResponseWriter, r *http.Request) bool {
	if !g.on {
		httpserver.WriteJSONStatus(w, http.StatusUnauthorized, httpserver.ErrorResponse{
			Error: "notify is off: set " + TokenEnv + " (or " + TokenEnv + "_FILE) to enable it",
		})
		return false
	}
	if !g.Carries(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpserver.WriteJSONStatus(w, http.StatusUnauthorized, httpserver.ErrorResponse{
			Error: "missing or wrong bearer token",
		})
		return false
	}
	return true
}

// Carries reports whether r presents the configured token. The server refuses such a request on
// every route but this one, so the token opens nothing else.
func (g Gate) Carries(r *http.Request) bool {
	if !g.on {
		return false
	}
	scheme, presented, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || presented == "" {
		return false
	}
	return g.token.Verify(presented)
}

// IsRoute reports whether path is the notify route's shape with a valid session segment. The
// server lifts the Host allowlist on this shape, so a dot segment or separator never matches.
func IsRoute(path string) bool {
	rest, ok := strings.CutPrefix(path, routePrefix)
	if !ok {
		return false
	}
	session, ok := strings.CutSuffix(rest, routeSuffix)
	return ok && ids.ValidSessionID(session)
}

// isB64Token reports whether s matches RFC 6750 section 2.1's b64token.
func isB64Token(s string) bool {
	body := strings.TrimRight(s, "=")
	if body == "" {
		return false
	}
	for i := range len(body) {
		c := body[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~', c == '+', c == '/':
		default:
			return false
		}
	}
	return true
}
