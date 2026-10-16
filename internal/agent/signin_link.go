package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// signinLink is the engine's view of the sign-in vault: the backend the engine runs, its declared spawn arguments and
// host callback, and the account the running engine answers from. A zero link (no vault) declares nothing.
type signinLink struct {
	vault   *signin.Vault
	backend signin.BackendID
	// apiKey: the engine authenticates with an allowlisted API key, which KAS prefers over the host callback, so a
	// vault change is never pending against the engine.
	apiKey bool
}

// WithSignIn wires the sign-in vault and the backend the agent engine runs.
func WithSignIn(v *signin.Vault, backend signin.BackendID) Option {
	return func(h *Runtime) { h.signin.vault, h.signin.backend = v, backend }
}

// WithAPIKey records that an API key reaches the agent engine: a sign-in or a switch then marks no restart pending
// and reloads no session, and the stored account applies again on an engine started without the key.
func WithAPIKey(active bool) Option {
	return func(h *Runtime) { h.signin.apiKey = active }
}

// spawnArgs are the backend declaration's SpawnArgs, the only source of the engine's authentication argv.
func (l signinLink) spawnArgs() []string {
	if l.vault == nil {
		return nil
	}
	d, ok := l.vault.Declaration(l.backend)
	if !ok {
		return nil
	}
	return slices.Clone(d.SpawnArgs)
}

func (l signinLink) accountPending() bool {
	return !l.apiKey && l.vault != nil && l.vault.AccountPending(l.backend)
}

// engineSpawned promotes the desired account before a spawn records its fingerprint.
func (l signinLink) engineSpawned() {
	if l.vault != nil {
		l.vault.EngineSpawned(l.backend)
	}
}

// hostCallback reports whether method is a backend's declared host callback, and answers it.
func (l signinLink) hostCallback(method string) (answer func(context.Context) (json.RawMessage, *marotte.RPCError), ok bool) {
	if l.vault == nil || method == "" {
		return nil, false
	}
	id, ok := l.vault.ForHostCallback(method)
	if !ok {
		return nil, false
	}
	return func(ctx context.Context) (json.RawMessage, *marotte.RPCError) { return l.vault.Answer(ctx, id) }, true
}

// onChange registers fn for every committed sign-in edge of the engine's backend; none reaches an engine the API key
// authenticates.
func (l signinLink) onChange(fn func(signin.Change)) {
	if l.vault == nil || l.apiKey {
		return
	}
	l.vault.OnChange(func(c signin.Change) {
		if c.Backend == l.backend {
			fn(c)
		}
	})
}
