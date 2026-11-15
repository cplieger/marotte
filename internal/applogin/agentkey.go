package applogin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/access"
	"github.com/cplieger/marotte/internal/route"
)

// MethodAgent is the agent key's Method; the library reserves no value for a custom verifier.
const MethodAgent access.Method = "agent"

// AgentKeyEnv is the variable the per-boot agent key is exported in; children inherit it.
const AgentKeyEnv = "MAROTTE_API_KEY"

const agentKeyPrefix = "mtagent_"

// agentKeyVerifier holds only the agent key's hash: the process environment keeps the one
// plaintext copy.
type agentKeyVerifier struct {
	hash   access.Hash
	user   *access.User
	scopes []string
}

var _ access.CredentialVerifier = (*agentKeyVerifier)(nil)

// NewAgentKeyVerifier mints the per-boot agent key, exports its plaintext through setenv and
// returns a verifier built from its hash alone. An error is fatal at boot.
func NewAgentKeyVerifier(setenv func(key, value string) error) (access.CredentialVerifier, error) {
	g, err := access.GenerateAPIKey(agentKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("access: mint the agent key: %w", err)
	}
	if err := setenv(AgentKeyEnv, g.Plaintext.Reveal()); err != nil {
		return nil, fmt.Errorf("access: export the agent key: %w", err)
	}
	return &agentKeyVerifier{
		hash: g.Hash,
		// ID -1: no store assigns a non-positive ID (rule 17), and the user is never stored.
		user: &access.User{ID: -1, Username: "agent", Role: access.RoleUser, Enabled: true},
		scopes: []string{
			route.ScopeChatsRead, route.ScopeRunsRead, route.ScopeRunsAnswer, route.ScopeTools,
		},
	}, nil
}

// Verify answers ok false with a nil error for every request that is not the agent key's, so an
// owner key continues to the API-key verifier; it never returns an error.
func (v *agentKeyVerifier) Verify(_ context.Context, r *http.Request) (access.Principal, bool, error) {
	tok, ok := bearer(r)
	if !ok {
		return access.Principal{}, false, nil
	}
	if !access.APIKeyHash(tok).Equal(v.hash) {
		return access.Principal{}, false, nil
	}
	return access.Principal{User: v.user.Clone(), Method: MethodAgent, Scopes: slices.Clone(v.scopes)}, true, nil
}

// bearer returns the token of an Authorization header whose scheme is Bearer, compared
// case-insensitively (RFC 6750 §2.1, RFC 9110 §11.1).
func bearer(r *http.Request) (string, bool) {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}
