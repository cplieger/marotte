package applogin

import (
	"errors"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/access/account"
)

// TestKeyPermissions_ScopedKeyCannotMintOrCreate, at the account layer: under marotte's empty
// scopes a scoped owner key minted out of band reaches no key or user creation.
func TestKeyPermissions_ScopedKeyCannotMintOrCreate(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	si := h.setupOwner(ctx)
	created, err := h.svc.CreateAPIKey(ctx, account.OutOfBand("admin-socket"), si.User.ID, account.NewKey{Label: "scoped", Scopes: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := h.authn.Authenticate(bearerRequest("Bearer " + created.Plaintext.Reveal()))
	if err != nil || !kp.IsAPIKey() {
		t.Fatalf("the scoped key = (%v, %v), want an API-key principal", kp.Method, err)
	}
	keys, _ := h.store.ListAPIKeys(ctx)
	minted := len(h.events.of(account.EventKeyMinted))
	if _, err := h.svc.CreateAPIKey(ctx, account.As(kp), si.User.ID, account.NewKey{Label: "child", Scopes: []string{"tools"}}); !errors.Is(err, account.ErrSessionRequired) {
		t.Errorf("CreateAPIKey by the key = %v, want ErrSessionRequired", err)
	}
	if _, err := h.svc.CreateUser(ctx, account.As(kp), account.NewUser{Username: "second", Password: access.NewSecret(testPassword)}); !errors.Is(err, account.ErrSessionRequired) {
		t.Errorf("CreateUser by the key = %v, want ErrSessionRequired", err)
	}
	if after, _ := h.store.ListAPIKeys(ctx); len(after) != len(keys) {
		t.Errorf("%d keys after the refusals, want %d", len(after), len(keys))
	}
	if n, _ := h.store.UserCount(ctx); n != 1 {
		t.Errorf("%d users after the refusals, want 1", n)
	}
	if atts, _ := h.store.ListKeyAttributions(ctx); len(atts) != 0 {
		t.Errorf("%d attribution records after the refusals, want 0", len(atts))
	}
	if n := len(h.events.of(account.EventKeyMinted)); n != minted {
		t.Errorf("%d EventKeyMinted after the refusals, want %d", n, minted)
	}
}
