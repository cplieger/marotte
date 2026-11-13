package applogin

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/oidc"
	"github.com/cplieger/go/slogx/capture"
)

// heldBuilder starts the builder over a store opened with a saved, unattached provider, and holds
// it after its build returned and before its attach is submitted.
type heldBuilder struct {
	built   chan struct{}
	release chan struct{}
	done    <-chan struct{}
	cancel  context.CancelFunc
}

func startHeldBuilder(t *testing.T, h *harness) *heldBuilder {
	t.Helper()
	b := &heldBuilder{built: make(chan struct{}), release: make(chan struct{})}
	providerBuiltHook = func() {
		close(b.built)
		<-b.release
	}
	t.Cleanup(func() { providerBuiltHook = nil })
	ctx, cancel := context.WithCancel(t.Context())
	b.cancel = cancel
	b.done = StartProviderBuilder(ctx, h.store, h.origin, access.NewEphemeralSealer())
	<-b.built
	return b
}

// reopenedWithProvider saves rec built, then reopens so the provider is saved and not attached.
func reopenedWithProvider(t *testing.T, rec storeProvider) *harness {
	t.Helper()
	h := newHarness(t)
	h.setupOwner(t.Context())
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	return h.reopen()
}

// attachedMatchesRecord fails unless the published provider is want, the save's candidate, built
// from the record the save stored.
func attachedMatchesRecord(t *testing.T, h *harness, want *oidc.Provider) {
	t.Helper()
	if got := h.store.OIDCProvider(); got != want {
		t.Errorf("the published provider is %p, want the save's candidate %p", got, want)
	}
}

// TestProvider_HeldAttachRacesASave: a build held between its
// return and its attach, while the owner saves; the save's candidate stays attached.
func TestProvider_HeldAttachRacesASave(t *testing.T) {
	cases := []struct {
		edit func(rec storeProvider, idp2 *accesstest.IdP) storeProvider
		name string
	}{
		{name: "client_id_and_secret", edit: func(rec storeProvider, _ *accesstest.IdP) storeProvider {
			rec.ClientID, rec.ClientSecret = "new-client", access.NewSecret("new-secret")
			return rec
		}},
		{name: "pins_only", edit: func(rec storeProvider, _ *accesstest.IdP) storeProvider {
			rec.Pinned = map[string][]netip.Addr{"idp.example.com": {netip.MustParseAddr("10.0.0.7")}}
			return rec
		}},
		{name: "issuer", edit: func(_ storeProvider, idp2 *accesstest.IdP) storeProvider { return providerOf(idp2) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
			idp2 := newSeamIdP(t, idp2URL, accesstest.IdPOptions{})
			h := reopenedWithProvider(t, providerOf(idp))
			b := startHeldBuilder(t, h)
			next := c.edit(h.store.policy().Provider, idp2)
			saved := builtProvider(t, next, h.origin)
			candidate := h.store.policy()
			candidate.Provider = next
			if _, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, ownerRPID, candidate, candidate.Generation,
				&providerEdit{record: next, built: saved}); err != nil {
				t.Fatal(err)
			}
			publishes := h.disk.publishes.Load()
			close(b.release)
			<-b.done
			attachedMatchesRecord(t, h, saved)
			if h.disk.publishes.Load() != publishes {
				t.Error("the held attach published")
			}
			if p := h.store.policy().Provider; providerFieldsDiffer(&p, &next) {
				t.Error("cur's record is not the save's")
			}
		})
	}
	t.Run("removal", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		h := reopenedWithProvider(t, providerOf(idp))
		b := startHeldBuilder(t, h)
		c := h.store.policy()
		c.Provider = storeProvider{}
		if _, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, ownerRPID, c, c.Generation, &providerEdit{}); err != nil {
			t.Fatal(err)
		}
		close(b.release)
		<-b.done
		if h.store.OIDCProvider() != nil {
			t.Error("the held attach attached a removed provider")
		}
	})
	t.Run("current_generation", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		h := reopenedWithProvider(t, providerOf(idp))
		before := h.store.policy()
		b := startHeldBuilder(t, h)
		close(b.release)
		<-b.done
		if h.store.OIDCProvider() == nil {
			t.Fatal("the attach for the current generation attached nothing")
		}
		if after := h.store.policy(); after.Generation != before.Generation || providerFieldsDiffer(&after.Provider, &before.Provider) {
			t.Error("the attach changed the policy record")
		}
	})
}

// TestProvider_AttachGroupedWithFailedWriteRetries: an attach held in one group with a
// policy write whose publication fails receives the failure, and the builder's next pass
// attaches.
func TestProvider_AttachGroupedWithFailedWriteRetries(t *testing.T) {
	old := providerBackoff
	providerBackoff = 20 * time.Millisecond
	t.Cleanup(func() { providerBackoff = old })
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := reopenedWithProvider(t, providerOf(idp))
	logs := capture.Default(t)
	entered, pending, release := h.store.holdGroup()
	filler := make(chan error, 1)
	go func() { filler <- h.store.attachProvider(context.Background(), 0, nil) }()
	<-entered
	b := startHeldBuilder(t, h)
	providerBuiltHook = nil
	close(b.release)
	waitUntil(t, "the builder's attach queued", func() bool { return pending() == 2 })
	h.disk.faults().BeforePublish()
	saveErr := make(chan error, 1)
	go func() {
		c := h.store.policy()
		c.Breach = !c.Breach
		saveErr <- h.policySave(c, c.Generation)
	}()
	waitUntil(t, "the policy write queued", func() bool { return pending() == 3 })
	release()
	if err := <-filler; err != nil {
		t.Fatal(err)
	}
	if err := <-saveErr; err == nil {
		t.Fatal("the policy write's failed publication returned nil")
	}
	waitUntil(t, "the retried attach", func() bool { return h.store.OIDCProvider() != nil })
	b.cancel()
	<-b.done
	const msg = "access: the OIDC provider could not be built; retrying"
	if !logs.AttrContains(msg, "err", errInjected.Error()) {
		t.Error("the attach did not receive the group's publication failure")
	}
}

// TestProvider_CancelWhileHeldAttachesNothing: the builder's context ends while its attach is
// held; the builder returns and nothing is attached.
func TestProvider_CancelWhileHeldAttachesNothing(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := reopenedWithProvider(t, providerOf(idp))
	b := startHeldBuilder(t, h)
	b.cancel()
	close(b.release)
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the builder did not return after its context ended")
	}
	if h.store.OIDCProvider() != nil {
		t.Error("a cancelled builder attached a provider")
	}
}

// TestProvider_BootDoesNotWaitOnIdP, at the store: opening makes no
// network call, OIDC stays off while the issuer never answers, and the provider attaches once it
// does, within the in-flight attempt and one backoff.
func TestProvider_BootDoesNotWaitOnIdP(t *testing.T) {
	oldTimeout, oldBackoff := oidcClientTimeout, providerBackoff
	oidcClientTimeout, providerBackoff = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { oidcClientTimeout, providerBackoff = oldTimeout, oldBackoff })
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	hits := newRecorderHost(t, "idp.example.com")
	h = h.reopen()
	if n := hits.Load(); n != 0 {
		t.Errorf("the open sent %d requests to the issuer", n)
	}
	if h.store.OIDCProvider() != nil {
		t.Fatal("a provider is attached at open")
	}
	newSeamIdP(t, idpURL, accesstest.IdPOptions{Discovery: accesstest.IdPEndpoint{Delay: time.Hour}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := StartProviderBuilder(ctx, h.store, h.origin, access.NewEphemeralSealer())
	waitUntil(t, "the hung build's failure", func() bool { return h.store.ProviderBuildFailure() != nil })
	if h.store.OIDCProvider() != nil {
		t.Fatal("a provider attached while the issuer never answered")
	}
	newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	answered := time.Now()
	waitUntil(t, "the attach after the issuer answers", func() bool { return h.store.OIDCProvider() != nil })
	if d, bound := time.Since(answered), oidcClientTimeout+4*providerBackoff+time.Second; d > bound {
		t.Errorf("the attach took %s after the issuer answered, want under %s", d, bound)
	}
	cancel()
	<-done
}
