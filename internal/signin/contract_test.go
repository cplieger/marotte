package signin

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// The backend-neutral seam: callbackFake and otherFake declare different host callbacks, refresh
// thresholds, stale floors and refusal payloads; selfAuthFake signs itself in.
func contractFakes() (callbackFake, otherFake, selfAuthFake *fakeBackend) {
	callbackFake = newFake(nil, "cb", "_test/auth/token")
	otherFake = newFake(nil, "other", "_test/other/token")
	otherFake.renewWithin, otherFake.staleFloor, otherFake.code, otherFake.prefix = time.Minute, 10*time.Second, -32099, "other refused: "
	selfAuthFake = newFake(nil, "self", "")
	return callbackFake, otherFake, selfAuthFake
}

func TestVault_DeclarationSelectsTheCallbackPerBackend(t *testing.T) {
	cb, other, self := contractFakes()
	h := newHarness(t, cb, other, self)
	for method, want := range map[string]BackendID{"_test/auth/token": "cb", "_test/other/token": "other"} {
		if got, ok := h.v.ForHostCallback(method); !ok || got != want {
			t.Errorf("ForHostCallback(%q) = %q, %v; want %q", method, got, ok, want)
		}
	}
	if got, ok := h.v.ForHostCallback(""); ok {
		t.Errorf("ForHostCallback(\"\") = %q: the self-auth backend must route no callback", got)
	}
	d, ok := h.v.Declaration("self")
	if !ok || d.HostCallback != "" || !slices.Equal(d.SpawnArgs, []string{"--self"}) {
		t.Errorf("Declaration(self) = %+v, %v", d, ok)
	}
	if _, ok := h.v.Declaration("absent"); ok {
		t.Error("Declaration(absent) reported a backend")
	}
}

func TestOpen_PanicsOnADuplicateDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backends  func() []Backend
		wantPanic bool
	}{
		{"duplicate_id", func() []Backend { return []Backend{newFake(nil, "a", "_x"), newFake(nil, "a", "_y")} }, true},
		{"duplicate_callback", func() []Backend { return []Backend{newFake(nil, "a", "_x"), newFake(nil, "b", "_x")} }, true},
		{"two_self_auth_backends", func() []Backend { return []Backend{newFake(nil, "a", ""), newFake(nil, "b", "")} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var v *Vault
			panicked := func() (p bool) {
				defer func() { p = recover() != nil }()
				v = Open(ctx, t.TempDir(), tc.backends()...)
				return false
			}()
			if panicked != tc.wantPanic {
				t.Errorf("panicked = %v, want %v", panicked, tc.wantPanic)
			}
			if v != nil {
				cancel()
				v.Wait()
			}
		})
	}
}

func TestVault_SignInAndSignOutMutateOnlyTheirBackend(t *testing.T) {
	cb, other, self := contractFakes()
	h := newHarness(t, cb, other, self)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	h.signIn(other, approval{cred: credOf(t, h.account("bob"))})
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatalf("SignOut(cb): %v", err)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("cb = %v, want signed_out", st.Kind)
	}
	if st := h.v.Status("other"); st.Kind != StatusSignedIn || st.Label.Email != "bob@example.com" {
		t.Errorf("other = %+v, want bob still signed in", st)
	}
	if st := h.v.Status("self"); st.Kind != StatusSignedOut {
		t.Errorf("self = %v, want untouched signed_out", st.Kind)
	}
	h.reopen()
	if st := h.v.Status("other"); st.Kind != StatusSignedIn {
		t.Errorf("after reopen other = %v, want signed_in", st.Kind)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen cb = %v, want signed_out", st.Kind)
	}
}

func TestVault_RefreshSingleflightIsPerBackend(t *testing.T) {
	cb, other, _ := contractFakes()
	h := newHarness(t, cb, other)
	stale := h.account("alice")
	stale.Expiry = h.clk.now().Add(time.Minute)
	h.signIn(cb, approval{cred: credOf(t, stale)})
	h.signIn(other, approval{cred: credOf(t, h.account("bob"))})
	release := make(chan struct{})
	cb.setRefresh(func(ctx context.Context, c fakeCred) (fakeCred, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return fakeCred{}, ctx.Err()
		}
		c.Expiry = h.clk.now().Add(time.Hour)
		return c, nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.v.Answer(context.Background(), "cb")
	}()
	eventually(t, "cb's refresh to start", func() bool { return cb.refreshCount() == 1 })
	if a := h.mustAnswer("other"); a.Account != "bob" {
		t.Errorf("other answered %q while cb's refresh blocked", a.Account)
	}
	close(release)
	<-done
}

// TestVault_CallbackPolicyIsTheBackends drives two callback fakes with different thresholds and
// payloads through one vault: each backend's own policy decides, and the vault branches on no id.
func TestVault_CallbackPolicyIsTheBackends(t *testing.T) {
	t.Run("refresh_threshold", func(t *testing.T) {
		cb, other, _ := contractFakes()
		h := newHarness(t, cb, other)
		for _, f := range []*fakeBackend{cb, other} {
			c := h.account(string(f.decl.Backend))
			c.Expiry = h.clk.now().Add(2 * time.Minute)
			h.signIn(f, approval{cred: credOf(t, c)})
		}
		if a := h.mustAnswer("other"); a.Token != "other-token" || other.refreshCount() != 0 {
			t.Errorf("other (renews within 1m) answered %q after %d refreshes; want its token, no refresh", a.Token, other.refreshCount())
		}
		if a := h.mustAnswer("cb"); a.Token != "cb-token+r" || cb.refreshCount() != 1 {
			t.Errorf("cb (renews within 5m) answered %q after %d refreshes; want the renewed token after one", a.Token, cb.refreshCount())
		}
	})
	t.Run("stale_floor_and_refusal_payload", func(t *testing.T) {
		cb, other, _ := contractFakes()
		h := newHarness(t, cb, other)
		for _, f := range []*fakeBackend{cb, other} {
			c := h.account(string(f.decl.Backend))
			c.Expiry = h.clk.now().Add(4 * time.Minute)
			h.signIn(f, approval{cred: credOf(t, c)})
			f.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, errors.New("network down") })
		}
		if a := h.mustAnswer("cb"); a.Token != "cb-token" {
			t.Errorf("cb with 240s left after a failed refresh served %q, want its old token (floor 210s)", a.Token)
		}
		h.clk.advance(210 * time.Second)
		_, cbErr := h.answer("cb")
		if cbErr == nil || cbErr.Code != -32000 || cbErr.Message != "cb: unreachable" {
			t.Errorf("cb with 30s left = %+v, want its own unreachable refusal", cbErr)
		}
		if a := h.mustAnswer("other"); a.Token != "other-token" {
			t.Errorf("other with 30s left after a failed refresh served %q, want its old token (floor 10s)", a.Token)
		}
		h.clk.advance(25 * time.Second)
		_, otherErr := h.answer("other")
		if otherErr == nil || otherErr.Code != -32099 || otherErr.Message != "other refused: unreachable" {
			t.Errorf("other with 5s left = %+v, want its own code and text", otherErr)
		}
	})
}

// TestVault_AnAnswerToAReplacedEngineSetsNoBasis: an ask the old engine made, answered after the
// new engine spawned, does not count as what the new engine holds.
func TestVault_AnAnswerToAReplacedEngineSetsNoBasis(t *testing.T) {
	cb, _, _ := contractFakes()
	notLive(cb)
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	release := blockRefresh(h, cb)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.v.Answer(context.Background(), "cb")
	}()
	eventually(t, "the old engine's refresh to start", func() bool { return cb.refreshCount() == 1 })
	h.v.EngineSpawned("cb")
	close(release)
	<-done
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	if h.v.AccountPending("cb") {
		t.Error("a sign-in before the new engine's first answer went pending: the old engine's answer set basis")
	}
}

func TestVault_FirstSignInIsLive(t *testing.T) {
	cb, _, _ := contractFakes()
	cb.live = func(fakeCred, fakeCred) bool { return false }
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	if h.v.AccountPending("cb") {
		t.Error("AccountPending after a first sign-in")
	}
	if a := h.mustAnswer("cb"); a.Account != "alice" {
		t.Errorf("answered %q, want alice", a.Account)
	}
}

func notLive(cb *fakeBackend) { cb.live = func(fakeCred, fakeCred) bool { return false } }

func TestVault_NotLiveSwitchKeepsAnsweringTheEffectiveRecord(t *testing.T) {
	cb, _, _ := contractFakes()
	notLive(cb)
	h := newHarness(t, cb)
	seen := h.watch()
	a := h.account("alice")
	a.Arn, a.Class = "arn:alice", "org"
	h.signIn(cb, approval{cred: credOf(t, a)})
	h.mustAnswer("cb")
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})

	for range 3 {
		got := h.mustAnswer("cb")
		if got.Account != "alice" || got.Token != "alice-token" || got.Arn != "arn:alice" || got.Class != "org" {
			t.Errorf("answer = %+v, want alice's token, ARN and class whole", got)
		}
	}
	if !h.v.AccountPending("cb") {
		t.Error("AccountPending = false, want true")
	}
	st := h.v.Status("cb")
	if st.Label.Email != "bob@example.com" || !h.v.AccountPending("cb") {
		t.Errorf("Status = %+v, want bob's label and AccountPending", st)
	}
	got := seen.wait(t, 2)
	if len(got) != 2 || !got[1].AccountPending || got[0].AccountPending {
		t.Errorf("changes = %+v, want the switch to carry AccountPending", got)
	}
}

func TestVault_EngineSpawnedPromotesTheDesiredRecord(t *testing.T) {
	cb, _, _ := contractFakes()
	notLive(cb)
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	h.mustAnswer("cb")
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	h.v.EngineSpawned("cb")
	if h.v.AccountPending("cb") {
		t.Error("AccountPending after EngineSpawned")
	}
	if got := h.mustAnswer("cb"); got.Account != "bob" {
		t.Errorf("answered %q, want bob", got.Account)
	}
	// basis was cleared, so before anything is answered to the new engine a not-live sign-in is live.
	h.v.EngineSpawned("cb")
	h.signIn(cb, approval{cred: credOf(t, h.account("carol"))})
	if h.v.AccountPending("cb") {
		t.Error("a sign-in before the new engine's first answer went pending")
	}
}

func TestVault_LiveSwitchChangesTheAnswerAtOnce(t *testing.T) {
	cb, _, _ := contractFakes()
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	h.mustAnswer("cb")
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	if got := h.mustAnswer("cb"); got.Account != "bob" {
		t.Errorf("answered %q, want bob", got.Account)
	}
	if h.v.AccountPending("cb") {
		t.Error("AccountPending after a live switch")
	}
}

func TestVault_SwitchingBackClearsThePending(t *testing.T) {
	cb, _, _ := contractFakes()
	cb.live = func(from, to fakeCred) bool { return to.Account == from.Account }
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	h.mustAnswer("cb")
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	if !h.v.AccountPending("cb") {
		t.Fatal("bob did not go pending")
	}
	again := h.account("alice")
	again.Token = "alice-token-2"
	h.signIn(cb, approval{cred: credOf(t, again)})
	if h.v.AccountPending("cb") {
		t.Error("AccountPending after switching back")
	}
	if got := h.mustAnswer("cb"); got.Token != "alice-token-2" {
		t.Errorf("answered %q, want alice's new token", got.Token)
	}
}

func TestVault_SignOutThenANotLiveSignInStillPends(t *testing.T) {
	cb, _, _ := contractFakes()
	notLive(cb)
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
	h.mustAnswer("cb")
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatal(err)
	}
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	if !h.v.AccountPending("cb") {
		t.Error("AccountPending = false after sign-out then a not-live sign-in")
	}
	if _, rerr := h.answer("cb"); rerr == nil || rerr.Message != "cb: signed_out" {
		t.Errorf("answer before the restart = %v, want the signed-out refusal", rerr)
	}
	h.v.EngineSpawned("cb")
	if got := h.mustAnswer("cb"); got.Account != "bob" {
		t.Errorf("answered %q after EngineSpawned, want bob", got.Account)
	}
}

// TestVault_BasisIsClearedOnlyByEngineSpawned: after a sign-out, a lapse or a rejected refresh, a
// not-live sign-in is still compared with what the engine holds and pends; after EngineSpawned it
// is live.
func TestVault_BasisIsClearedOnlyByEngineSpawned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event func(h *harness, cb *fakeBackend)
	}{
		{"sign_out", func(h *harness, _ *fakeBackend) {
			if err := h.v.SignOut("cb"); err != nil {
				h.t.Fatal(err)
			}
		}},
		{"rejected_refresh", func(h *harness, cb *fakeBackend) {
			cb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, ErrRejected })
			h.clk.advance(58 * time.Minute)
			if _, rerr := h.answer("cb"); rerr == nil {
				h.t.Fatal("a rejected refresh answered a token")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb, _, _ := contractFakes()
			notLive(cb)
			h := newHarness(t, cb)
			h.signIn(cb, approval{cred: credOf(t, h.account("alice"))})
			h.mustAnswer("cb")
			tc.event(h, cb)
			h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
			if !h.v.AccountPending("cb") {
				t.Error("the not-live sign-in went live: basis was cleared")
			}
			h.v.EngineSpawned("cb")
			h.signIn(cb, approval{cred: credOf(t, h.account("carol"))})
			if h.v.AccountPending("cb") {
				t.Error("after EngineSpawned the not-live sign-in still pends: basis survived")
			}
		})
	}
}
