package applogin

import (
	"errors"
	"testing"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/oidc"
)

// providerAligned fails unless the reopened file's issuer, cur's policy issuer and the published
// provider's issuer are all want ("" for none).
func providerAligned(t *testing.T, h *harness, want string) {
	t.Helper()
	snap, err := decodeFile(h.disk.path)
	if err != nil {
		t.Fatal(err)
	}
	built := ""
	if p := h.store.OIDCProvider(); p != nil {
		built = p.Issuer()
	}
	if snap.policy.Provider.Issuer != want || h.store.policy().Provider.Issuer != want || built != want {
		t.Errorf("issuers: file %q, cur %q, provider %q; want all %q", snap.policy.Provider.Issuer, h.store.policy().Provider.Issuer, built, want)
	}
}

// providerSave is a Settings save of rec built on the generation base.
func providerSave(t *testing.T, h *harness, rec storeProvider, base int64) func() error {
	t.Helper()
	built := builtProvider(t, rec, h.origin)
	return func() error {
		c := h.store.policy()
		c.Provider = rec
		_, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, ownerRPID, c, base, &providerEdit{record: rec, built: built})
		return err
	}
}

// TestPolicy_ProviderPublication: the file, cur's
// policy and the published provider stay one value through every outcome of a save.
func TestPolicy_ProviderPublication(t *testing.T) {
	setup := func(t *testing.T) (*harness, storeProvider, storeProvider) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		idp2 := newSeamIdP(t, idp2URL, accesstest.IdPOptions{})
		h := newHarness(t)
		h.setupOwner(t.Context())
		return h, providerOf(idp), providerOf(idp2)
	}
	for _, order := range []string{"a_then_b", "b_then_a"} {
		t.Run("two_groups_"+order, func(t *testing.T) {
			h, a, b := setup(t)
			first, second := a, b
			if order == "b_then_a" {
				first, second = b, a
			}
			g := h.store.policy().Generation
			if err := providerSave(t, h, first, g)(); err != nil {
				t.Fatal(err)
			}
			if err := providerSave(t, h, second, g+1)(); err != nil {
				t.Fatal(err)
			}
			providerAligned(t, h, second.Issuer)
		})
		t.Run("one_group_one_generation_"+order, func(t *testing.T) {
			h, a, b := setup(t)
			first, second := a, b
			if order == "b_then_a" {
				first, second = b, a
			}
			g := h.store.policy().Generation
			saveFirst, saveSecond := providerSave(t, h, first, g), providerSave(t, h, second, g)
			var errFirst, errSecond error
			queueInOrder(t, h.store, func() { errFirst = saveFirst() }, func() { errSecond = saveSecond() })
			if errFirst != nil || !errors.Is(errSecond, ErrPolicyConflict) {
				t.Errorf("saves = (%v, %v), want (nil, ErrPolicyConflict)", errFirst, errSecond)
			}
			providerAligned(t, h, first.Issuer)
		})
	}
	t.Run("guard_refusal", func(t *testing.T) {
		h, a, _ := setup(t)
		h.savedProvider(a, builtProvider(t, a, h.origin), true)
		owner := h.owner()
		h.fixture(func(tx *txView) error {
			u, _, _ := tx.UserByID(t.Context(), owner.ID)
			u.PasswordHash = access.Hash{}
			return tx.UpdateUser(t.Context(), u)
		})
		before := h.store.OIDCProvider()
		c := h.store.policy()
		c.Provider = storeProvider{}
		if _, _, err := h.store.putPolicy(t.Context(), h.svc, &accountTestActor, ownerRPID, c, c.Generation, &providerEdit{}); !errors.Is(err, ErrNoWorkingMethod) {
			t.Fatalf("removing the sole method's provider = %v, want ErrNoWorkingMethod", err)
		}
		if h.store.OIDCProvider() != before {
			t.Error("the refused save replaced the published provider")
		}
		providerAligned(t, h, a.Issuer)
	})
	t.Run("before_publish_failure", func(t *testing.T) {
		h, a, b := setup(t)
		h.savedProvider(a, builtProvider(t, a, h.origin), false)
		before := h.store.OIDCProvider()
		h.disk.faults().BeforePublish()
		if err := providerSave(t, h, b, h.store.policy().Generation)(); err == nil {
			t.Fatal("a save whose publication failed returned nil")
		}
		if h.store.OIDCProvider() != before {
			t.Error("the failed save replaced the published provider")
		}
		providerAligned(t, h, a.Issuer)
	})
	t.Run("after_publish_not_durable", func(t *testing.T) {
		h, a, _ := setup(t)
		h.store.setFenceTick(make(chan time.Time))
		h.disk.faults().AfterPublish()
		if err := providerSave(t, h, a, h.store.policy().Generation)(); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("a non-durable save = %v, want ErrNotDurable", err)
		}
		if !h.store.Fenced() {
			t.Error("not fenced after a non-durable save")
		}
		providerAligned(t, h, a.Issuer)
	})
}

// TestPolicy_ReplacementSecretAlwaysRebuilds: a replacement secret is a
// provider change whatever its text, so the save builds and publishes a new provider; a display
// name alone is none, so the published provider stays.
func TestPolicy_ReplacementSecretAlwaysRebuilds(t *testing.T) {
	idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
	h := newHarness(t)
	h.setupOwner(t.Context())
	rec := providerOf(idp)
	h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
	save := func(next storeProvider, replacement access.Secret) (*oidc.Provider, error) {
		base := h.store.policy()
		edit, err := planProviderEdit(base.Provider, next, replacement)
		if err != nil {
			return nil, err
		}
		var built *oidc.Provider
		if edit != nil {
			built = builtProvider(t, edit.record, h.origin)
			edit.built = built
		}
		c := base.clone()
		c.Provider = next
		_, _, err = h.store.putPolicy(t.Context(), h.svc, &accountTestActor, ownerRPID, c, base.Generation, edit)
		return built, err
	}
	t.Run("same_text_replacement", func(t *testing.T) {
		gen := h.store.cur.Load().providerGen
		built, err := save(rec, access.NewSecret(rec.ClientSecret.Reveal()))
		if err != nil {
			t.Fatal(err)
		}
		if built == nil || h.store.OIDCProvider() != built || h.store.cur.Load().providerGen != gen+1 {
			t.Error("a replacement secret with the stored text did not rebuild and publish the provider")
		}
	})
	t.Run("display_name_only", func(t *testing.T) {
		before, gen := h.store.OIDCProvider(), h.store.cur.Load().providerGen
		renamed := h.store.policy().Provider
		renamed.DisplayName = "Renamed IdP"
		if built, err := save(renamed, access.Secret{}); err != nil || built != nil {
			t.Fatalf("a display-name save = (built %t, %v), want no build and nil", built != nil, err)
		}
		if h.store.OIDCProvider() != before || h.store.cur.Load().providerGen != gen {
			t.Error("a display-name save replaced the published provider")
		}
		if h.store.policy().Provider.DisplayName != "Renamed IdP" {
			t.Error("the display name was not stored")
		}
	})
}
