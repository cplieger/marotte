package applogin

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
)

const (
	marker1 = "rotation-marker-one"
	marker2 = "rotation-marker-two"
)

// signInThrough runs a whole OIDC sign-in with p against idp and returns the error of Finish,
// whose token request carries the provider's client secret.
func signInThrough(t *testing.T, p *oidc.Provider, idp *accesstest.IdP) error {
	t.Helper()
	cookies := ParseProxies("").CookieConfig()
	w := httptest.NewRecorder()
	authURL, err := p.Begin(w, httptest.NewRequest(http.MethodGet, testOrigin+"/access/oidc/start", nil), cookies,
		oidc.BeginRequest{Intent: oidc.IntentSignIn, Redirect: "/"})
	if err != nil {
		return err
	}
	browser := idp.Client()
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, authURL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return err
	}
	cb := httptest.NewRequest(http.MethodGet, loc.String(), nil)
	for _, c := range w.Result().Cookies() {
		cb.AddCookie(c)
	}
	_, _, err = p.Finish(httptest.NewRecorder(), cb, cookies, oidc.Callback{State: loc.Query().Get("state"), Code: loc.Query().Get("code")})
	return err
}

// decodeBasic is the credentials of a Basic Authorization header, "" for any other.
func decodeBasic(auth string) string {
	raw, ok := strings.CutPrefix(auth, "Basic ")
	if !ok {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	creds, _ := url.QueryUnescape(string(b))
	return creds
}

// policySave is a Settings save of candidate built on base, with no provider edit.
func (h *harness) policySave(candidate storePolicy, base int64) error {
	_, _, err := h.store.putPolicy(context.Background(), h.svc, &accountTestActor, ownerRPID, candidate, base, nil)
	return err
}

// TestPolicy_StaleSaveIsRefused: a Settings save is applied only on the
// generation its form was built from.
func TestPolicy_StaleSaveIsRefused(t *testing.T) {
	t.Run("rotation_race", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{ClientSecret: marker2})
		h := newHarness(t)
		ctx := t.Context()
		h.setupOwner(ctx)
		p1 := providerOf(idp)
		p1.ClientSecret = access.NewSecret(marker1)
		h.savedProvider(p1, builtProvider(t, p1, h.origin), false)
		form := h.store.policy()
		g := form.Generation
		z := p1
		z.ClientSecret, z.DisplayName = access.NewSecret(marker2), "Rotated"
		zBuilt := builtProvider(t, z, h.origin)
		zc := form.clone()
		zc.Provider = z
		if _, _, err := h.store.putPolicy(ctx, h.svc, &accountTestActor, ownerRPID, zc, g, &providerEdit{record: z, built: zBuilt}); err != nil {
			t.Fatalf("Z: %v", err)
		}
		x := form.clone()
		x.Provider.DisplayName = "Renamed"
		publishes := h.disk.publishes.Load()
		if err := h.policySave(x, g); !errors.Is(err, ErrPolicyConflict) {
			t.Fatalf("X after Z = %v, want ErrPolicyConflict", err)
		}
		if h.disk.publishes.Load() != publishes {
			t.Error("the refused X published")
		}
		snap, err := decodeFile(h.disk.path)
		if err != nil {
			t.Fatal(err)
		}
		for name, prov := range map[string]storeProvider{"file": snap.policy.Provider, "cur": h.store.policy().Provider} {
			if prov.ClientSecret.Reveal() != marker2 || prov.DisplayName != "Rotated" {
				t.Errorf("%s holds secret marker2 %t and display name %q; want marker2 and Rotated", name, prov.ClientSecret.Reveal() == marker2, prov.DisplayName)
			}
		}
		if h.store.OIDCProvider() != zBuilt {
			t.Error("the published provider is not Z's candidate")
		}
		h = h.reopen()
		saved := h.store.policy().Provider
		if ProviderConfig(saved, h.origin, access.NewEphemeralSealer()).ClientSecret.Reveal() != marker2 {
			t.Error("after the reopen the builder's provider does not use marker2")
		}
		rebuilt, rec, err := recordedBuild(t, saved, h.origin)
		if err != nil {
			t.Fatal(err)
		}
		if err := signInThrough(t, rebuilt, idp); err != nil {
			t.Errorf("a sign-in through the rebuilt provider: %v", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, r := range rec.reqs {
			if strings.Contains(r.auth+r.body, marker1) || strings.Contains(decodeBasic(r.auth), marker1) {
				t.Errorf("a request to %s carries marker1", r.url)
			}
		}
	})
	t.Run("no_conflict", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		h := newHarness(t)
		h.setupOwner(t.Context())
		rec := providerOf(idp)
		h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
		form := h.store.policy()
		provider, gen := h.store.OIDCProvider(), h.store.cur.Load().providerGen
		x := form.clone()
		x.Provider.DisplayName = "Renamed"
		if err := h.policySave(x, form.Generation); err != nil {
			t.Fatal(err)
		}
		if p := h.store.policy(); p.Provider.DisplayName != "Renamed" || p.Generation != form.Generation+1 {
			t.Errorf("after the save: display name %q at generation %d, want Renamed at %d", p.Provider.DisplayName, p.Generation, form.Generation+1)
		}
		if h.store.OIDCProvider() != provider || h.store.cur.Load().providerGen != gen {
			t.Error("a display-name save moved the provider or providerGen")
		}
	})
	t.Run("built_before_the_boot_attach", func(t *testing.T) {
		idp := newSeamIdP(t, idpURL, accesstest.IdPOptions{})
		h := newHarness(t)
		h.setupOwner(t.Context())
		rec := providerOf(idp)
		h.savedProvider(rec, builtProvider(t, rec, h.origin), false)
		h = h.reopen()
		if h.store.OIDCProvider() != nil {
			t.Fatal("a provider is attached at open")
		}
		form := h.store.policy()
		if err := h.store.attachProvider(t.Context(), h.store.cur.Load().providerGen, builtProvider(t, rec, h.origin)); err != nil {
			t.Fatal(err)
		}
		x := form.clone()
		x.Breach = !x.Breach
		if err := h.policySave(x, form.Generation); err != nil {
			t.Errorf("a form loaded before the boot attach = %v, want applied", err)
		}
	})
	t.Run("socket_switch", func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		h.setupOwner(ctx)
		plain := newTestSession(h.owner().ID, epoch)
		if err := h.store.CreateSession(plainCtx(ctx), plain); err != nil {
			t.Fatal(err)
		}
		form := h.store.policy()
		if _, err := h.store.setPlainHTTP(ctx, h.svc, false); err != nil {
			t.Fatal(err)
		}
		x := form.clone()
		x.Breach = !x.Breach
		if err := h.policySave(x, form.Generation); !errors.Is(err, ErrPolicyConflict) {
			t.Errorf("a form loaded before the switch-off = %v, want ErrPolicyConflict", err)
		}
		if h.store.policy().PlainHTTP {
			t.Error("the switch is back on")
		}
		if _, ok := h.store.sessionRecord(plain.TokenHash); ok {
			t.Error("the deleted Plain session is back")
		}
	})
	t.Run("restore_marker", func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		h.setupOwner(ctx)
		mustCreatePasskey(t, h.store, h.owner().ID, "marker-pk")
		h.setPolicy(func(p *storePolicy) { p.Methods.Password = false })
		form := h.store.policy()
		if err := h.svc.RecoverOutOfBand(WithRestoreAllowance(plainCtx(ctx), access.MethodPassword), account.OutOfBand("admin-socket"),
			h.owner().ID, access.NewSecret(recoveredPassword), account.RecoverOptions{KeepSessions: true}); err != nil {
			t.Fatal(err)
		}
		if err := h.policySave(form.clone(), form.Generation); !errors.Is(err, ErrPolicyConflict) {
			t.Errorf("a form loaded before the recovery = %v, want ErrPolicyConflict", err)
		}
		if !h.store.policy().Methods.Password {
			t.Error("the password allowance is off again")
		}
	})
	t.Run("across_a_reopen", func(t *testing.T) {
		h := newHarness(t)
		h.setupOwner(t.Context())
		old := h.store.policy()
		for range 2 {
			c := h.store.policy()
			c.Breach = !c.Breach
			if err := h.policySave(c, c.Generation); err != nil {
				t.Fatal(err)
			}
		}
		current := h.store.policy()
		h = h.reopen()
		stale := old.clone()
		stale.Idle = minIdle
		if err := h.policySave(stale, old.Generation); !errors.Is(err, ErrPolicyConflict) {
			t.Errorf("a form built before the two saves = %v, want ErrPolicyConflict", err)
		}
		fresh := current.clone()
		fresh.Idle = minIdle
		if err := h.policySave(fresh, current.Generation); err != nil {
			t.Errorf("a form built on the generation before the reopen = %v, want applied", err)
		}
	})
	t.Run("in_one_group", func(t *testing.T) {
		for _, firstIdle := range []bool{true, false} {
			h := newHarness(t)
			h.setupOwner(t.Context())
			form := h.store.policy()
			a, b := form.clone(), form.clone()
			a.Idle, b.Absolute = minIdle, minAbsolute
			if !firstIdle {
				a, b = b, a
			}
			var errA, errB error
			queueInOrder(t, h.store, func() { errA = h.policySave(a, form.Generation) }, func() { errB = h.policySave(b, form.Generation) })
			if errA != nil || !errors.Is(errB, ErrPolicyConflict) {
				t.Errorf("first idle %t: saves = (%v, %v), want (nil, ErrPolicyConflict)", firstIdle, errA, errB)
			}
			if p := h.store.policy(); p.Idle != a.Idle || p.Absolute != a.Absolute {
				t.Errorf("first idle %t: stored (%s, %s), want the first save's", firstIdle, p.Idle, p.Absolute)
			}
		}
	})
}
