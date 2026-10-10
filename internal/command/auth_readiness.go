package command

import "sync/atomic"

// AuthReadiness holds the outcome of a credential the backend rejected, never
// its reason, because unauthenticated /api/health must not expose internal paths.
// Each Record replaces the previous outcome, so a successful turn clears it.
type AuthReadiness struct {
	unavailable atomic.Bool
}

// record stores whether the latest relevant turn failed authentication.
func (r *AuthReadiness) record(err error) {
	r.unavailable.Store(err != nil)
}

// Unavailable reports whether the backend rejected the latest credential.
func (r *AuthReadiness) Unavailable() bool {
	return r.unavailable.Load()
}
