package applogin

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/oidc"
)

// The builder's retry backoff: providerBackoff doubling to providerBackoffMax; variables so tests
// can shorten them.
var (
	providerBackoff    = 5 * time.Second
	providerBackoffMax = 5 * time.Minute
)

// providerBuiltHook, tests only, runs after a build succeeds and before its attach is submitted.
var providerBuiltHook func()

// ProviderConfig turns a saved provider into oidc.NewProvider's input. Scopes is
// always the one-element openid list: nil would request profile and email too.
func ProviderConfig(p storeProvider, origin *Origin, flowSealer *access.Sealer) oidc.Config { //nolint:gocritic // hugeParam: the record is read whole
	return oidc.Config{
		IssuerURL:    p.Issuer,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		RedirectURL:  origin.OIDCRedirect(),
		Scopes:       []string{"openid"},
		HTTPClient:   OIDCClient(p),
		FlowSealer:   flowSealer,
	}
}

type buildFailure struct {
	err error
	gen uint64
}

// ProviderBuildFailure is the last build failure of the saved provider still waiting for one, for
// status and Settings; nil once a provider is attached or the record changed.
func (s *Store) ProviderBuildFailure() error {
	f := s.buildFailure.Load()
	snap := s.cur.Load()
	if f == nil || f.gen != snap.providerGen || snap.oidc != nil || snap.policy.Provider.isZero() {
		return nil
	}
	return f.err
}

func (s *Store) recordBuildFailure(gen uint64, err error) {
	prev := s.buildFailure.Swap(&buildFailure{gen: gen, err: err})
	if prev != nil && prev.gen == gen {
		slog.Debug("access: the OIDC provider could not be built", "err", err)
		return
	}
	slog.Warn("access: the OIDC provider could not be built; retrying", "err", err)
}

// StartProviderBuilder builds the provider the record read at open names, the one state no save
// built, and attaches it by generation. It returns once nothing is left to build or ctx ends; the
// returned channel closes then.
func StartProviderBuilder(ctx context.Context, store *Store, origin *Origin, flowSealer *access.Sealer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buildProvider(ctx, store, origin, flowSealer)
	}()
	return done
}

func buildProvider(ctx context.Context, store *Store, origin *Origin, flowSealer *access.Sealer) {
	delay := providerBackoff
	for {
		s := store.cur.Load()
		if s.policy.Provider.isZero() || s.oidc != nil || ctx.Err() != nil {
			return
		}
		err := buildOnce(ctx, store, s, origin, flowSealer)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		store.recordBuildFailure(s.providerGen, err)
		if !sleepCtx(ctx, delay) {
			return
		}
		delay = min(2*delay, providerBackoffMax)
	}
}

// buildOnce builds s's provider and submits its attach for s's generation: a no-op unless the
// generation still names the record the provider was built from, so the next pass sees the
// attach, or the save that beat it.
func buildOnce(ctx context.Context, store *Store, s *snapshot, origin *Origin, flowSealer *access.Sealer) error {
	p, err := oidc.NewProvider(ctx, ProviderConfig(s.policy.Provider, origin, flowSealer))
	if err != nil {
		return err
	}
	if providerBuiltHook != nil {
		providerBuiltHook()
	}
	if err := store.attachProvider(ctx, s.providerGen, p); err != nil {
		return err
	}
	if store.cur.Load().oidc == p {
		slog.Info("access: OIDC provider is available")
	}
	return nil
}

// sleepCtx waits d and reports false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
