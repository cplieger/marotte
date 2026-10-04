package forges

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// connectionFor is the forgeapi connection a record describes.
func connectionFor(rec *connectionRecord) forgeapi.Connection {
	return forgeapi.Connection{
		WebBaseURL: rec.webBase(),
		APIBaseURL: rec.APIBaseURL,
		CABytes:    []byte(rec.CAPEM),
		ClientCert: []byte(rec.ClientCertPEM),
		ClientKey:  []byte(rec.ClientKeyPEM),
		Proxy:      rec.Proxy,
	}
}

// connOptions is the option set every forgeapi entry that opens an endpoint for
// rec takes. The credential source and the device grant read the address policy
// from their own options, so a set given to the client alone would let a
// private-range instance answer its API and refuse its refresh.
func connOptions(rec *connectionRecord) []forgeapi.Option {
	return []forgeapi.Option{
		forgeapi.WithLogger(slog.Default().With("component", "forges")),
		forgeapi.WithPrivateAddresses(rec.PrivateAddresses),
		forgeapi.WithPlaintextHTTP(rec.PlaintextHTTP),
	}
}

// newFamilyClient builds the client of the family rec's kind names. The kind is
// the user's own statement at connect time, so nothing is spent detecting it.
func newFamilyClient(rec *connectionRecord, opts []forgeapi.Option) (forgeapi.Core, error) {
	conn := connectionFor(rec)
	switch rec.Kind.family() {
	case forgeapi.FamilyGitHub:
		return asCore(github.New(conn, opts...))
	case forgeapi.FamilyGitLab:
		return asCore(gitlab.New(conn, opts...))
	case forgeapi.FamilyGitea:
		return asCore(gitea.New(conn, opts...))
	}
	return nil, fmt.Errorf("forges: kind %q has no forgeapi family", rec.Kind)
}

// asCore keeps a failed constructor's nil pointer from becoming a non-nil Core.
func asCore[T forgeapi.Core](c T, err error) (forgeapi.Core, error) {
	if err != nil {
		return nil, err
	}
	return c, nil
}

// clientFactory holds one forgeapi client per connection, rebuilt only when
// the fields that shape the connection change.
type clientFactory struct {
	clients map[string]cachedClient
	// newCore builds a client; a test stands in a fake.
	newCore func(rec *connectionRecord, opts []forgeapi.Option) (forgeapi.Core, error)
	// extra is appended to every connection's option set; a test injects a
	// wire transport through it.
	extra []forgeapi.Option
	mu    sync.Mutex
}

// cachedClient is a connection's client, the credential source it reads its
// token from, and the record shape it was built from.
type cachedClient struct {
	core  forgeapi.Core
	cred  forgeapi.CredentialSource
	shape connectionRecord
}

func newClientFactory() *clientFactory {
	return &clientFactory{clients: make(map[string]cachedClient), newCore: newFamilyClient}
}

// clientShape is rec without the fields a live client owns or never reads: the
// rotation cursor seeds a client once and then moves inside it, the helper
// value is git's, and the owner scopes are the poller's.
func clientShape(rec *connectionRecord) connectionRecord {
	shape := *rec
	shape.HelperValue, shape.RotationCursor, shape.OwnerScopes = "", "", nil
	return shape
}

func (f *clientFactory) options(rec *connectionRecord) []forgeapi.Option {
	return append(connOptions(rec), f.extra...)
}

// sourceFor is the credential source behind rec's client, refreshing through
// the record's own trust material and address policy.
func (f *clientFactory) sourceFor(store creds.Store, rec *connectionRecord) (*creds.Source, error) {
	return creds.NewSource(store, rec.ID, connectionFor(rec), f.options(rec)...)
}

// staticSource is one token held in memory.
type staticSource string

func (s staticSource) Token(context.Context) (string, error) { return string(s), nil }
func (staticSource) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (staticSource) State() forgeapi.CredState               { return forgeapi.CredValid }

// verifier is a client for rec holding token in memory, for the identity read
// a connect makes before anything is stored. It is not cached, so the caller
// closes it.
func (f *clientFactory) verifier(rec *connectionRecord, token string) (forgeapi.Core, error) {
	return f.newCore(rec, append(f.options(rec), forgeapi.WithCredentialSource(staticSource(token))))
}

// clientFor answers rec's client, building it on first use or after the record
// changed shape. Construction sends nothing.
func (f *clientFactory) clientFor(store creds.Store, rec *connectionRecord) (cachedClient, error) {
	shape := clientShape(rec)
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[rec.ID]; ok && reflect.DeepEqual(c.shape, shape) {
		return c, nil
	}
	src, err := f.sourceFor(store, rec)
	if err != nil {
		return cachedClient{}, err
	}
	opts := append(f.options(rec),
		forgeapi.WithCredentialSource(src),
		forgeapi.WithRotationCursor(forgeapi.RotationCursor(rec.RotationCursor)),
		forgeapi.WithMutations(true),
	)
	core, err := f.newCore(rec, opts)
	if err != nil {
		return cachedClient{}, err
	}
	if old, ok := f.clients[rec.ID]; ok {
		old.core.Close()
	}
	c := cachedClient{core: core, cred: src, shape: shape}
	f.clients[rec.ID] = c
	return c, nil
}

// retain drops and closes the client of every connection not in ids. A call
// still in flight on a closed client completes; Close releases its idle pool.
func (f *clientFactory) retain(ids map[string]connectionRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.clients {
		if _, ok := ids[id]; !ok {
			f.clients[id].core.Close()
			delete(f.clients, id)
		}
	}
}
