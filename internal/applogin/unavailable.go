package applogin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cplieger/access"
)

var errStoreUnavailable = errors.New("applogin: the sign-in store is unavailable")

// UnavailableStore is the Authenticator's store while the access store could not be opened: every
// call fails, so an unknown Bearer value answers the library's backend error while the agent key,
// which reads no store, still verifies.
type UnavailableStore struct {
	Cause error
}

var _ access.AuthenticatorStore = UnavailableStore{}

func (u UnavailableStore) err() error { return fmt.Errorf("%w: %w", errStoreUnavailable, u.Cause) }

// SessionByHash implements [access.AuthenticatorStore].
func (u UnavailableStore) SessionByHash(context.Context, access.Hash) (*access.Session, bool, error) {
	return nil, false, u.err()
}

// UpdateSessionActivity implements [access.AuthenticatorStore].
func (u UnavailableStore) UpdateSessionActivity(context.Context, access.Hash, access.SessionActivity) error {
	return u.err()
}

// UserByID implements [access.AuthenticatorStore].
func (u UnavailableStore) UserByID(context.Context, int64) (*access.User, bool, error) {
	return nil, false, u.err()
}

// APIKeyByHash implements [access.AuthenticatorStore].
func (u UnavailableStore) APIKeyByHash(context.Context, access.Hash) (*access.Key, bool, error) {
	return nil, false, u.err()
}

// TouchAPIKey implements [access.AuthenticatorStore].
func (u UnavailableStore) TouchAPIKey(context.Context, int64, time.Time) error { return u.err() }
