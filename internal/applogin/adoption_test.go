package applogin

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/access"
	"github.com/cplieger/access/accesshttp"
	"github.com/cplieger/access/accesstest"
	"github.com/cplieger/access/account"
	"github.com/cplieger/access/oidc"
)

// quietSweep keeps the library's sweep from running inside a test's fake-clock jumps, so every
// write a test sees is its own.
func quietSweep(t *testing.T) {
	cleanupInterval = 1000 * time.Hour
	t.Cleanup(func() { cleanupInterval = 0 })
}

// diskMark is the store file's bytes and the count of durable publishes, to show a call wrote
// nothing.
type diskMark struct {
	data      []byte
	publishes int64
}

func (h *harness) diskMark() diskMark {
	h.t.Helper()
	data, err := os.ReadFile(h.disk.path)
	if err != nil {
		h.t.Fatalf("read the store file: %v", err)
	}
	return diskMark{data: data, publishes: h.disk.publishes.Load()}
}

func (h *harness) wroteNothingSince(before diskMark, what string) {
	h.t.Helper()
	after := h.diskMark()
	if after.publishes != before.publishes || !bytes.Equal(after.data, before.data) {
		h.t.Errorf("%s wrote the store: %d publishes after %d, file changed %t",
			what, after.publishes, before.publishes, !bytes.Equal(after.data, before.data))
	}
}

// accessRoutes is accesshttp over the harness's service, by route name; each refusal's code is
// written as the body.
func (h *harness) accessRoutes() map[string]accesshttp.Route {
	h.t.Helper()
	ah, err := accesshttp.New(accesshttp.Config{
		Account: h.svc, Prefix: "/api/access", OIDCPrefix: "/access/oidc",
		Errors: func(w http.ResponseWriter, _ *http.Request, e accesshttp.Error) {
			w.WriteHeader(e.Status)
			_, _ = w.Write([]byte(e.Code))
		},
	})
	if err != nil {
		h.t.Fatalf("accesshttp.New: %v", err)
	}
	routes := map[string]accesshttp.Route{}
	for _, r := range ah.Routes() {
		routes[r.Name] = r
	}
	return routes
}

// serve sends a plain request with token's session cookie through route.
func serve(route accesshttp.Route, token access.Secret, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(route.Method, "http://marotte.example"+route.Path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token.Reveal()})
	w := httptest.NewRecorder()
	route.Handler.ServeHTTP(w, r)
	return w
}

// sessionCookie is the Set-Cookie of the session cookie in w, if any.
func sessionCookie(w *httptest.ResponseRecorder) (*http.Cookie, bool) {
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c, true
		}
	}
	return nil, false
}

// TestAdoption_KeyPastExpiresAtIsRefused: a key authenticates one nanosecond before its ExpiresAt
// and is refused from that instant, with no store write.
func TestAdoption_KeyPastExpiresAtIsRefused(t *testing.T) {
	quietSweep(t)
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		si := h.setupOwner(ctx)
		created, err := h.svc.CreateAPIKey(ctx, account.OutOfBand("admin-socket"), si.User.ID,
			account.NewKey{Label: "expiring", Scopes: []string{"tools"}, ExpiresIn: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		stored, ok := h.store.keyRecord(created.Key.ID)
		if !ok || stored.ExpiresAt.IsZero() {
			t.Fatalf("the stored key = (%+v, %t), want one with an ExpiresAt", stored.ExpiresAt, ok)
		}
		bearer := "Bearer " + created.Plaintext.Reveal()
		time.Sleep(time.Until(stored.ExpiresAt) - time.Nanosecond)
		synctest.Wait()
		if p, err := h.authn.Authenticate(bearerRequest(bearer)); err != nil || !p.IsAPIKey() {
			t.Fatalf("one nanosecond before ExpiresAt = (%v, %v), want the key's principal", p.Method, err)
		}
		synctest.Wait()
		time.Sleep(time.Nanosecond)
		if now := time.Now(); !now.Equal(stored.ExpiresAt) {
			t.Fatalf("the clock reads %v, want the ExpiresAt instant %v", now, stored.ExpiresAt)
		}
		before := h.diskMark()
		_, err = h.authn.Authenticate(bearerRequest(bearer))
		if !errors.Is(err, access.ErrInvalidAPIKey) {
			t.Errorf("at ExpiresAt = %v, want ErrInvalidAPIKey", err)
		}
		synctest.Wait()
		h.wroteNothingSince(before, "the refused key")
	})
}

// TestAdoption_AuthKeyCreateIsRecentAuthUnderEmptyScopes: with marotte's empty key-permission
// scope, key creation is a recent-auth route: a fresh session mints, an eleven-minute-old sign-in
// is refused.
func TestAdoption_AuthKeyCreateIsRecentAuthUnderEmptyScopes(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	si := h.setupOwner(ctx)
	if s := h.svc.CreateKeysScope(); s != "" {
		t.Fatalf("CreateKeysScope = %q, want empty", s)
	}
	route, ok := h.accessRoutes()["authKeyCreate"]
	if !ok {
		t.Fatal("no authKeyCreate route")
	}
	if route.Class != accesshttp.ClassRecentAuth || route.Scope != "" {
		t.Errorf("authKeyCreate is (%q, %q), want (%q, \"\")", route.Class, route.Scope, accesshttp.ClassRecentAuth)
	}
	const body = `{"label":"adoption","scopes":["tools"]}`
	keys, _ := h.store.ListAPIKeys(ctx)
	if w := serve(route, si.Token, body); w.Code >= 300 {
		t.Errorf("a fresh session's key create = %d %s, want success", w.Code, w.Body)
	}
	if after, _ := h.store.ListAPIKeys(ctx); len(after) != len(keys)+1 {
		t.Errorf("%d keys after a fresh session's create, want %d", len(after), len(keys)+1)
	}
	old := h.signIn(ctx)
	aged, ok := h.store.sessionRecord(old.Session.TokenHash)
	if !ok {
		t.Fatal("the second sign-in is not stored")
	}
	aged.AuthTime = aged.AuthTime.Add(-11 * time.Minute)
	h.fixture(func(tx *txView) error { tx.putSession(&aged); return nil })
	keys, _ = h.store.ListAPIKeys(ctx)
	w := serve(route, old.Token, body)
	if got := w.Body.String(); got != string(accesshttp.CodeRecentAuthRequired) {
		t.Errorf("an eleven-minute-old sign-in's key create = %d %s, want %s", w.Code, got, accesshttp.CodeRecentAuthRequired)
	}
	if after, _ := h.store.ListAPIKeys(ctx); len(after) != len(keys) {
		t.Errorf("%d keys after the refused create, want %d", len(after), len(keys))
	}
}

// TestAdoption_LogoutOnStoreErrorCookieOutcome measures authLogout's cookie on a store that cannot
// save: the refusal leaves the cookie and the session, so the client is still signed in. A saved
// sign-out clears the cookie.
func TestAdoption_LogoutOnStoreErrorCookieOutcome(t *testing.T) {
	t.Run("store_refuses", func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		si := h.setupOwner(ctx)
		h.store.setFenceTick(make(chan time.Time))
		h.disk.faults().AfterPublish()
		if err := h.store.CreateSession(plainCtx(ctx), newTestSession(si.User.ID, time.Now())); !errors.Is(err, ErrNotDurable) {
			t.Fatalf("the non-durable write = %v, want ErrNotDurable", err)
		}
		if !h.store.Fenced() {
			t.Fatal("the store is not fenced")
		}
		w := serve(h.accessRoutes()["authLogout"], si.Token, "")
		if got := w.Body.String(); w.Code != http.StatusServiceUnavailable || got != string(accesshttp.CodeUnavailable) {
			t.Errorf("logout on a fenced store = %d %s, want 503 %s", w.Code, got, accesshttp.CodeUnavailable)
		}
		if c, ok := sessionCookie(w); ok {
			t.Errorf("the refused logout set the session cookie %q, want it left alone", c.String())
		}
		if p := h.principal(si.Token); p.SessionID != si.Session.ID {
			t.Errorf("after the refused logout the cookie authenticates session %q, want %q", p.SessionID, si.Session.ID)
		}
	})
	t.Run("store_saves", func(t *testing.T) {
		h := newHarness(t)
		si := h.setupOwner(t.Context())
		w := serve(h.accessRoutes()["authLogout"], si.Token, "")
		if w.Code != http.StatusNoContent {
			t.Errorf("logout = %d %s, want 204", w.Code, w.Body)
		}
		if c, ok := sessionCookie(w); !ok || c.MaxAge >= 0 {
			t.Errorf("logout's session cookie = (%v, %t), want one cleared with a negative Max-Age", c, ok)
		}
	})
}

// TestAdoption_KeyPrincipalDeadlineIsExpiresAt: a key principal's AbsoluteDeadline, from
// Authenticate and from Recheck, is the stored key's ExpiresAt, and zero for a key that never
// expires.
func TestAdoption_KeyPrincipalDeadlineIsExpiresAt(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	si := h.setupOwner(ctx)
	for _, c := range []struct {
		name      string
		expiresIn time.Duration
	}{{"expiring", 30 * 24 * time.Hour}, {"never", 0}} {
		t.Run(c.name, func(t *testing.T) {
			created, err := h.svc.CreateAPIKey(ctx, account.OutOfBand("admin-socket"), si.User.ID,
				account.NewKey{Label: c.name, Scopes: []string{"tools"}, ExpiresIn: c.expiresIn})
			if err != nil {
				t.Fatal(err)
			}
			stored, ok := h.store.keyRecord(created.Key.ID)
			if !ok {
				t.Fatal("the key is not stored")
			}
			if stored.ExpiresAt.IsZero() != (c.expiresIn == 0) {
				t.Fatalf("stored ExpiresAt %v for ExpiresIn %v", stored.ExpiresAt, c.expiresIn)
			}
			p, err := h.authn.Authenticate(bearerRequest("Bearer " + created.Plaintext.Reveal()))
			if err != nil {
				t.Fatal(err)
			}
			if !p.AbsoluteDeadline.Equal(stored.ExpiresAt) {
				t.Errorf("Authenticate's AbsoluteDeadline = %v, want ExpiresAt %v", p.AbsoluteDeadline, stored.ExpiresAt)
			}
			q, err := h.authn.Recheck(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if !q.AbsoluteDeadline.Equal(stored.ExpiresAt) {
				t.Errorf("Recheck's AbsoluteDeadline = %v, want ExpiresAt %v", q.AbsoluteDeadline, stored.ExpiresAt)
			}
		})
	}
}

// TestAdoption_SessionCapsStoredAndEnforced: a plain password sign-in stores a 30-minute idle cap
// and a 12-hour absolute cap, a step-up keeps both, and the verifier refuses the session 31
// minutes after its last activity without writing.
func TestAdoption_SessionCapsStoredAndEnforced(t *testing.T) {
	quietSweep(t)
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx := t.Context()
		h.setupOwner(ctx)
		caps := func(what string, hash access.Hash) {
			t.Helper()
			r, ok := h.store.sessionRecord(hash)
			if !ok {
				t.Fatalf("%s: no stored session", what)
			}
			if r.IdleCap != 30*time.Minute || r.AbsoluteCap != 12*time.Hour {
				t.Errorf("%s stores caps (%v, %v), want (30m, 12h)", what, r.IdleCap, r.AbsoluteCap)
			}
		}
		si := h.signIn(ctx)
		caps("the plain sign-in", si.Session.TokenHash)
		up, err := h.svc.StepUpPassword(plainCtx(ctx), testClient, h.principal(si.Token), access.NewSecret(testPassword))
		if err != nil {
			t.Fatalf("StepUpPassword: %v", err)
		}
		caps("the stepped-up session", up.Session.TokenHash)
		synctest.Wait()
		time.Sleep(29 * time.Minute)
		if p := h.principal(up.Token); p.SessionID != up.Session.ID {
			t.Fatalf("29 minutes idle authenticates %q, want %q", p.SessionID, up.Session.ID)
		}
		synctest.Wait()
		active, _ := h.store.sessionRecord(up.Session.TokenHash)
		time.Sleep(31 * time.Minute)
		before := h.diskMark()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: up.Token.Reveal()})
		if _, err := h.authn.Authenticate(r); !errors.Is(err, access.ErrSessionExpired) {
			t.Errorf("31 minutes idle = %v, want ErrSessionExpired", err)
		}
		synctest.Wait()
		h.wroteNothingSince(before, "the refused session")
		if after, _ := h.store.sessionRecord(up.Session.TokenHash); !after.LastActivity.Equal(active.LastActivity) {
			t.Errorf("LastActivity moved from %v to %v on the refusal", active.LastActivity, after.LastActivity)
		}
	})
}

// TestAdoption_LastMethodGuardHonoursMethodUsable: a passkey whose allowance is off counts for
// nothing, so removing the password is the last method and writes nothing; with the allowance on
// the same removal succeeds.
func TestAdoption_LastMethodGuardHonoursMethodUsable(t *testing.T) {
	for _, passkeyOn := range []bool{false, true} {
		t.Run(map[bool]string{false: "passkey_allowance_off", true: "passkey_allowance_on"}[passkeyOn], func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			h.setupOwner(ctx)
			mustCreatePasskey(t, h.store, h.owner().ID, "adoption-pk")
			h.setPolicy(func(p *storePolicy) { p.Methods.Passkey = passkeyOn })
			p := h.principal(h.signIn(ctx).Token)
			before := h.diskMark()
			_, err := h.svc.RemovePassword(ctx, testClient, p, access.NewSecret(testPassword))
			if passkeyOn {
				if err != nil {
					t.Errorf("RemovePassword with a usable passkey = %v, want success", err)
				}
				return
			}
			if !errors.Is(err, account.ErrLastMethod) {
				t.Errorf("RemovePassword with the passkey allowance off = %v, want ErrLastMethod", err)
			}
			h.wroteNothingSince(before, "the refused removal")
			if h.owner().PasswordHash.IsZero() {
				t.Error("the password was removed")
			}
		})
	}
}

// TestAdoption_DiscoveryRefusesHTTPEndpoint: a discovery document naming an http
// authorization_endpoint, token_endpoint or jwks_uri is refused with ErrDiscovery naming the
// field, and marotte's client sends no request to it.
func TestAdoption_DiscoveryRefusesHTTPEndpoint(t *testing.T) {
	origin := parseOrigin(testOrigin, nil)
	for _, c := range []struct {
		field string
		opts  accesstest.IdPOptions
		url   string
	}{
		{"authorization_endpoint", accesstest.IdPOptions{AuthorizationEndpoint: "http://idp.example.com/authorize"}, "http://idp.example.com/authorize"},
		{"token_endpoint", accesstest.IdPOptions{TokenEndpoint: "http://idp.example.com/token"}, "http://idp.example.com/token"},
		{"jwks_uri", accesstest.IdPOptions{JWKSURI: "http://idp.example.com/jwks"}, "http://idp.example.com/jwks"},
	} {
		t.Run(c.field, func(t *testing.T) {
			idp := newSeamIdP(t, idpURL, c.opts)
			_, rec, err := recordedBuild(t, providerOf(idp), origin)
			if !errors.Is(err, oidc.ErrDiscovery) || !strings.Contains(err.Error(), c.field) {
				t.Errorf("NewProvider = %v, want ErrDiscovery naming %s", err, c.field)
			}
			for _, u := range rec.urls() {
				if u == c.url || strings.HasPrefix(u, "http://") {
					t.Errorf("the client sent a request to %s", u)
				}
			}
			if got := rec.urls(); len(got) == 0 || got[0] != idpURL+discoveryPath {
				t.Errorf("the client fetched %v, want discovery first", got)
			}
		})
	}
}
