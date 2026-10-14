package forges

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// connReadCacheBytes bounds each connection's forgeapi read cache at the lower
// end of the cache author's 8 to 16 MiB per connection: marotte revalidates few
// REST reads per connection (the first repository pages, the merge settings of
// opened repositories, GitLab's per-scope merge-request lists), GitHub's
// pull-request reads are GraphQL, which is never cached, and forgeapi keeps no
// entry over a quarter of the bound, far above any projected page. An eviction
// costs one full read, never a wrong answer.
const connReadCacheBytes = 8 << 20

func connectionFor(spec *connectionSpec) forgeapi.Connection {
	return forgeapi.Connection{
		WebBaseURL: spec.webBase(),
		APIBaseURL: spec.APIBaseURL,
		CABytes:    []byte(spec.CAPEM),
		ClientCert: []byte(spec.ClientCertPEM),
		ClientKey:  []byte(spec.ClientKeyPEM),
		Proxy:      spec.Proxy,
	}
}

// connOptions is the option set every forgeapi entry that opens an endpoint for
// spec takes. The credential source and the device grant read the address
// policy from their own options, so a set given to the client alone would let a
// private-range instance answer its API and refuse its refresh.
func connOptions(spec *connectionSpec) []forgeapi.Option {
	return []forgeapi.Option{
		forgeapi.WithLogger(slog.Default().With("component", "forges")),
		forgeapi.WithPrivateAddresses(spec.PrivateAddresses),
		forgeapi.WithPlaintextHTTP(spec.PlaintextHTTP),
	}
}

// The kind is the user's own statement at connect time, so nothing is spent detecting it.
func newFamilyClient(spec *connectionSpec, opts []forgeapi.Option) (forgeapi.Core, error) {
	conn := connectionFor(spec)
	switch spec.Kind.family() {
	case forgeapi.FamilyGitHub:
		return asCore(github.New(conn, opts...))
	case forgeapi.FamilyGitLab:
		return asCore(gitlab.New(conn, opts...))
	case forgeapi.FamilyGitea:
		return asCore(gitea.New(conn, opts...))
	}
	return nil, fmt.Errorf("forges: kind %q has no forgeapi family", spec.Kind)
}

// asCore keeps a failed constructor's nil pointer from becoming a non-nil Core.
func asCore[T forgeapi.Core](c T, err error) (forgeapi.Core, error) {
	if err != nil {
		return nil, err
	}
	return c, nil
}

type staticSource string

func (s staticSource) Token(context.Context) (string, error) { return string(s), nil }
func (staticSource) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (staticSource) State() forgeapi.CredState               { return forgeapi.CredValid }

// storedToken is the credential source every reader's client reads through: the
// active slot's stored token, never refreshed, so every refresh runs in check
// under the connection's id lock. State and Kind are the refreshing source's,
// which read without sending.
type storedToken struct {
	store creds.Store
	src   *creds.Source
	key   string
}

var _ forgeapi.CredentialSource = storedToken{}

func (t storedToken) Token(context.Context) (string, error) {
	switch t.src.State() {
	case forgeapi.CredReconnectRequired:
		return "", &forgeapi.Error{Kind: forgeapi.KindUnauthorized, Code: forgeapi.CodeReconnectRequired, Message: reconnectReason}
	case forgeapi.CredValid, forgeapi.CredRefreshDue, forgeapi.CredRefreshing:
		rec, ok, err := t.store.Load(t.key)
		if err != nil {
			break
		}
		if ok && (rec.Expiry.IsZero() || time.Now().Before(rec.Expiry)) {
			return rec.Token, nil
		}
		return "", &forgeapi.Error{
			Kind: forgeapi.KindTransient, Retryable: true,
			Message: "the token expired; it is renewed on the next check",
		}
	}
	return "", &forgeapi.Error{
		Kind: forgeapi.KindTransient, Retryable: true,
		Message: "the credential store could not be read",
	}
}

func (t storedToken) Kind() forgeapi.CredKind   { return t.src.Kind() }
func (t storedToken) State() forgeapi.CredState { return t.src.State() }

// openConn builds the one client a loaded sign-in reads through, with its
// refreshing source and the stored-token source it reads by. Construction
// sends nothing; either constructor's refusal leaves nothing to close.
func openConn(store creds.Store, spec *connectionSpec, newCore coreFactory, extra []forgeapi.Option,
) (core forgeapi.Core, src *creds.Source, cred storedToken, err error) {
	key := slotKey(spec.ID, spec.Slot)
	opts := slices.Concat(connOptions(spec), extra)
	src, err = creds.NewSource(store, key, connectionFor(spec), opts...)
	if err != nil {
		return nil, nil, storedToken{}, err
	}
	cred = storedToken{store: store, key: key, src: src}
	core, err = newCore(spec, slices.Concat(opts, []forgeapi.Option{
		forgeapi.WithCredentialSource(cred),
		forgeapi.WithRotationCursor(forgeapi.RotationCursor(spec.RotationCursor)),
		forgeapi.WithMutations(true),
		forgeapi.WithReadCache(connReadCacheBytes),
	}))
	if err != nil {
		return nil, nil, storedToken{}, err
	}
	return core, src, cred, nil
}

// verifier is a throwaway client for spec holding token in memory, for the
// identity read a sign-in makes before anything is stored. The caller closes it.
func verifier(m *Manager, spec *connectionSpec, token string) (forgeapi.Core, error) {
	return m.newCore(spec, slices.Concat(connOptions(spec), m.extraOpts, []forgeapi.Option{
		forgeapi.WithCredentialSource(staticSource(token)),
		forgeapi.WithReadCache(0),
	}))
}
