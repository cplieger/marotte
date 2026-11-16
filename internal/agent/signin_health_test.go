package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/marotte/internal/testsupport"
)

// healthPush records every notification delivered, in order.
type healthPush struct {
	pushStub
	sent []marotte.NotificationPayload
	mu   sync.Mutex
}

func (p *healthPush) Raise(_ context.Context, _ marotte.ChatID, n *marotte.NotificationPayload, _ uint64) func(context.Context) {
	return func(context.Context) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.sent = append(p.sent, *n)
	}
}

// signInPushes is every delivered notification of the sign-in's kind.
func (p *healthPush) signInPushes() []marotte.NotificationPayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []marotte.NotificationPayload
	for _, n := range p.sent {
		if n.Kind == marotte.PushKindSignIn {
			out = append(out, n)
		}
	}
	return out
}

// healthHub is a runtime that answers the engine's credential asks from a vault over gateBackend, its pushes
// recorded.
type healthHub struct {
	h     *Runtime
	cs    *testChatStore
	vault *signin.Vault
	b     *gateBackend
	push  *healthPush
}

func newHealthHub(t *testing.T, opts ...Option) *healthHub {
	t.Helper()
	b := newGateBackend()
	vault := openVault(t, t.TempDir(), b)
	p := &healthPush{}
	h, cs, _ := newTestHub(append([]Option{WithSignIn(vault, gateBackendID), WithPush(p)}, opts...)...)
	t.Cleanup(func() { shutdownHub(t, h) })
	return &healthHub{h: h, cs: cs, vault: vault, b: b, push: p}
}

func (s *healthHub) signIn(t *testing.T) {
	t.Helper()
	p, err := s.vault.StartSignIn(t.Context(), gateBackendID, signin.FlowParams{Flow: signin.FlowDevice})
	if err != nil {
		t.Fatalf("StartSignIn: %v", err)
	}
	// Answerable alone is already true on a second sign-in, before this one commits.
	waitUntil(t, "the sign-in to commit", func() bool {
		a := s.vault.Status(gateBackendID).SignIn
		return (a.ID > p.Attempt || a.State != signin.AttemptPending) && s.vault.Answerability(gateBackendID) == signin.Answerable
	})
	waitUntil(t, "the sign-in's health", func() bool { return s.h.SignInHealth().State == marotte.SignInOK })
}

// ask answers one engine credential ask, as the engine's host callback does.
func (s *healthHub) ask(t *testing.T) *marotte.RPCError {
	t.Helper()
	answer, ok := s.h.signin.hostCallback(gateCallback)
	if !ok {
		t.Fatal("the backend's host callback is not answered")
	}
	_, rerr := answer(t.Context())
	return rerr
}

// head is the replay position now, so events reads only what follows.
func (s *healthHub) head() uint64 { return s.h.bus.fanout.Position().Head }

// events are the signin_health payloads broadcast after since.
func (s *healthHub) events(t *testing.T, since uint64) []marotte.SignInHealth {
	t.Helper()
	return payloadsOfType[marotte.SignInHealth](t, bufferedSince(s.h, since), marotte.EventSignInHealth)
}

func statesOf(hs []marotte.SignInHealth) []marotte.SignInState {
	out := make([]marotte.SignInState, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.State)
	}
	return out
}

var errProviderDown = errors.New("kiro: the token endpoint answered 500")

// failPastTheGrace makes every renewal fail, asks once, which spends the vault's answer window and marks the
// provider unreachable, then waits out the rest of the grace. It returns when the first renewal failed.
func (s *healthHub) failPastTheGrace(t *testing.T) time.Time {
	t.Helper()
	s.b.set(false, false, func() error { return errProviderDown })
	first := time.Now()
	if rerr := s.ask(t); rerr == nil {
		t.Fatal("an ask was answered with every renewal failing")
	}
	time.Sleep(signInGrace)
	synctest.Wait()
	return first
}

// Renewals failing for longer than the grace, with no usable lease left, read failing from the episode's first
// failure, and the state reaches every client once.
func TestSignInHealth_RenewalsFailingPastTheGraceTurnFailing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t)
		s.signIn(t)
		head := s.head()
		s.b.set(false, false, func() error { return errProviderDown })
		first := time.Now()
		if rerr := s.ask(t); rerr == nil {
			t.Fatal("an ask was answered with every renewal failing")
		}
		synctest.Wait()
		if got := s.h.SignInHealth().State; got != marotte.SignInOK {
			t.Errorf("state %v after the first failure, inside the grace = %q, want ok", time.Since(first), got)
		}

		time.Sleep(signInGrace)
		synctest.Wait()

		got := s.h.SignInHealth()
		if got.State != marotte.SignInFailing || got.Cause != marotte.SignInCauseProviderUnreachable {
			t.Errorf("health past the grace = %s/%s, want failing/provider_unreachable", got.State, got.Cause)
		}
		if got.Since == nil || !got.Since.Equal(first) {
			t.Errorf("Since = %v, want the first failed renewal at %v", got.Since, first)
		}
		evs := s.events(t, head)
		if len(evs) != 1 || evs[0].State != marotte.SignInFailing || evs[0].Version != got.Version {
			t.Errorf("signin_health events = %+v, want exactly one carrying %+v", evs, got)
		}
	})
}

// The next usable renewal ends the episode: the state reads ok again, every client is told, and the end is logged
// with how long it lasted.
func TestSignInHealth_ASuccessfulRenewalClearsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t)
		s.signIn(t)
		logs := captureLogs(t)
		s.failPastTheGrace(t)
		if got := s.h.SignInHealth().State; got != marotte.SignInFailing {
			t.Fatalf("Setup: state = %q, want failing", got)
		}
		head := s.head()

		s.b.set(false, false, nil)
		time.Sleep(time.Minute)
		synctest.Wait()

		if got := s.h.SignInHealth(); got.State != marotte.SignInOK || got.Cause != "" || got.Since != nil {
			t.Errorf("health after a usable renewal = %+v, want ok with no cause or since", got)
		}
		if got := statesOf(s.events(t, head)); !slices.Equal(got, []marotte.SignInState{marotte.SignInOK}) {
			t.Errorf("signin_health states after the recovery = %v, want [ok]", got)
		}
		var ends []string
		for line := range strings.Lines(logs.String()) {
			if strings.Contains(line, `"msg":"sign-in health: the Kiro sign-in works again"`) {
				ends = append(ends, line)
			}
		}
		if len(ends) != 1 || !strings.Contains(ends[0], `"duration":`) || !strings.Contains(ends[0], `"from":"failing"`) {
			t.Errorf("end lines = %q, want exactly one naming the state it ended and its duration", ends)
		}
	})
}

// A blip shorter than the grace shows nothing: neither failures the cached token still covers, nor a lease too short
// to use that a usable one replaces in time.
func TestSignInHealth_ABlipUnderTheGraceShowsNothing(t *testing.T) {
	cases := map[string]func(t *testing.T, s *healthHub){
		"failures while the token is still served": func(t *testing.T, s *healthHub) {
			s.b.set(false, true, func() error { return errProviderDown })
			if rerr := s.ask(t); rerr != nil {
				t.Fatalf("an ask the cached token covers = %v, want an answer", rerr)
			}
			s.b.set(false, true, nil)
			if rerr := s.ask(t); rerr != nil {
				t.Fatalf("an ask after the provider came back = %v, want an answer", rerr)
			}
		},
		"a short lease replaced inside the grace": func(t *testing.T, s *healthHub) {
			s.b.set(false, false, nil)
			s.b.setShort(true)
			if rerr := s.ask(t); rerr == nil {
				t.Fatal("an ask on a short lease was answered")
			}
			if a := s.vault.Answerability(gateBackendID); a != signin.ProviderUnreachable {
				t.Fatalf("Setup: answerability after a short lease = %v, want provider_unreachable", a)
			}
			s.b.setShort(false)
			if rerr := s.ask(t); rerr != nil {
				t.Fatalf("an ask after a usable lease = %v, want an answer", rerr)
			}
		},
	}
	for name, blip := range cases {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newHealthHub(t)
				s.signIn(t)
				head := s.head()

				blip(t, s)
				time.Sleep(2 * signInGrace)
				synctest.Wait()

				if got := s.h.SignInHealth().State; got != marotte.SignInOK {
					t.Errorf("state after the blip = %q, want ok", got)
				}
				if evs := s.events(t, head); len(evs) != 0 {
					t.Errorf("signin_health events = %+v, want none", evs)
				}
				if n := len(s.push.signInPushes()); n != 0 {
					t.Errorf("sign-in pushes = %d, want none", n)
				}
			})
		})
	}
}

// A definite refusal reads signed_out at once, with no grace; a sign-out the person asked for reads none, and an
// engine refusal during an outage is the outage.
func TestSignInHealth_ADefiniteRefusalGivesSignedOut(t *testing.T) {
	t.Run("the identity provider rejects the renewal", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newHealthHub(t)
			s.signIn(t)
			head := s.head()
			s.b.set(false, false, func() error { return signin.ErrRejected })
			if rerr := s.ask(t); rerr == nil {
				t.Fatal("a rejected renewal was answered")
			}
			synctest.Wait()
			got := s.h.SignInHealth()
			if got.State != marotte.SignInSignedOut || got.Cause != marotte.SignInCauseRejected || got.Since == nil {
				t.Errorf("health after a rejected renewal = %+v, want signed_out/rejected with a since", got)
			}
			if got := statesOf(s.events(t, head)); !slices.Equal(got, []marotte.SignInState{marotte.SignInSignedOut}) {
				t.Errorf("signin_health states = %v, want [signed_out]", got)
			}
			s.b.set(true, true, nil)
			s.signIn(t)
		})
	})
	t.Run("Kiro refuses a credential while the provider answers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newHealthHub(t)
			s.signIn(t)
			s.h.NoteEngineRefusal()
			synctest.Wait()
			if got := s.h.SignInHealth(); got.State != marotte.SignInSignedOut || got.Cause != marotte.SignInCauseRefused {
				t.Errorf("health after an engine refusal = %s/%s, want signed_out/refused", got.State, got.Cause)
			}
			if !s.h.SignInRefused() {
				t.Error("SignInRefused = false after an engine refusal, want true")
			}
			s.h.NoteEngineAccepted()
			synctest.Wait()
			if got := s.h.SignInHealth().State; got != marotte.SignInOK || s.h.SignInRefused() {
				t.Errorf("state after an accepted turn = %q (refused %v), want ok", got, s.h.SignInRefused())
			}
		})
	})
	t.Run("a sign-out the person asked for", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newHealthHub(t)
			if got := s.h.SignInHealth().State; got != marotte.SignInNone {
				t.Errorf("state with no account = %q, want none", got)
			}
			s.signIn(t)
			if err := s.vault.SignOut(gateBackendID); err != nil {
				t.Fatalf("SignOut: %v", err)
			}
			synctest.Wait()
			if got := s.h.SignInHealth().State; got != marotte.SignInNone {
				t.Errorf("state after a sign-out = %q, want none", got)
			}
			if n := len(s.push.signInPushes()); n != 0 {
				t.Errorf("sign-in pushes = %d, want none for a sign-out the person asked for", n)
			}
		})
	})
	t.Run("an engine refusal graded after the provider recovered", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newHealthHub(t)
			s.signIn(t)
			s.failPastTheGrace(t)
			s.b.set(false, false, nil)
			if a := s.vault.Reprobe(t.Context(), gateBackendID); a != signin.Answerable {
				t.Fatalf("Setup: answerability after the reprobe = %v, want answerable", a)
			}
			synctest.Wait()
			if got := s.h.SignInHealth().State; got != marotte.SignInOK {
				t.Fatalf("Setup: state after the recovery = %q, want ok", got)
			}

			s.h.NoteEngineRefusal()
			synctest.Wait()

			if got := s.h.SignInHealth().State; got != marotte.SignInOK || s.h.SignInRefused() {
				t.Errorf("state = %q (refused %v), want ok: the engine's ask was refused for the outage", got, s.h.SignInRefused())
			}
			if n := len(s.push.signInPushes()); n != 1 {
				t.Errorf("sign-in pushes = %d, want only the failing one", n)
			}
		})
	})
	t.Run("an engine refusal while the provider is down", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newHealthHub(t)
			s.signIn(t)
			s.failPastTheGrace(t)
			s.h.NoteEngineRefusal()
			synctest.Wait()
			if got := s.h.SignInHealth(); got.State != marotte.SignInFailing || got.Cause != marotte.SignInCauseProviderUnreachable {
				t.Errorf("health = %s/%s, want failing/provider_unreachable: the refusal is the outage's", got.State, got.Cause)
			}
		})
	})
}

// One push per episode: entering failing sends one, nothing more while it lasts or when it turns into a sign-out,
// and only a recovery re-arms it.
func TestSignInHealth_OnePushPerEpisode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t)
		s.signIn(t)
		s.failPastTheGrace(t)
		pushes := s.push.signInPushes()
		if len(pushes) != 1 {
			t.Fatalf("sign-in pushes on entering failing = %d, want 1", len(pushes))
		}
		if p := pushes[0]; p.PushSubject != (marotte.PushSubject{}) || p.ChatID != "" ||
			!strings.HasPrefix(p.Body, "Kiro sign-in is failing") {
			t.Errorf("push = %+v, want a workspace notice saying the sign-in is failing", p)
		}

		if rerr := s.ask(t); rerr == nil {
			t.Fatal("an ask was answered during the outage")
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if n := len(s.push.signInPushes()); n != 1 {
			t.Errorf("sign-in pushes while the outage lasts = %d, want still 1", n)
		}

		s.b.set(false, false, nil)
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := s.h.SignInHealth().State; got != marotte.SignInOK {
			t.Fatalf("Setup: state after the recovery = %q, want ok", got)
		}
		// A fresh account's record, since gateBackend counts a renewed one fresh forever.
		s.signIn(t)
		s.failPastTheGrace(t)
		if n := len(s.push.signInPushes()); n != 2 {
			t.Errorf("sign-in pushes after a second episode = %d, want 2", n)
		}

		s.b.set(false, false, func() error { return signin.ErrRejected })
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := s.h.SignInHealth().State; got != marotte.SignInSignedOut {
			t.Fatalf("Setup: state after a rejected reprobe = %q, want signed_out", got)
		}
		if n := len(s.push.signInPushes()); n != 2 {
			t.Errorf("sign-in pushes after failing turned signed_out = %d, want still 2", n)
		}
	})
}

// With an API key the vault answers nothing the engine uses, so only the engine's own refusals move the state.
func TestSignInHealth_APIKeyReadsOnlyEngineRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t, WithAPIKey(true))
		if got := s.h.SignInHealth().State; got != marotte.SignInOK {
			t.Errorf("state with an API key = %q, want ok", got)
		}
		if _, err := s.vault.StartSignIn(t.Context(), gateBackendID, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
			t.Fatalf("StartSignIn: %v", err)
		}
		waitUntil(t, "the sign-in to commit", func() bool { return s.vault.Answerability(gateBackendID) == signin.Answerable })
		s.b.set(false, false, func() error { return signin.ErrRejected })
		if _, rerr := s.vault.Answer(t.Context(), gateBackendID); rerr == nil {
			t.Fatal("a rejected renewal was answered")
		}
		synctest.Wait()
		if got := s.h.SignInHealth().State; got != marotte.SignInOK {
			t.Errorf("state after the vault's record lapsed = %q, want ok: the API key signs the engine in", got)
		}

		s.h.NoteEngineRefusal()
		synctest.Wait()
		if got := s.h.SignInHealth(); got.State != marotte.SignInSignedOut || got.Cause != marotte.SignInCauseAPIKeyRefused {
			t.Errorf("health after an engine refusal = %s/%s, want signed_out/api_key_refused", got.State, got.Cause)
		}
		if p := s.push.signInPushes(); len(p) != 1 || !strings.Contains(p[0].Body, "KIRO_API_KEY") {
			t.Errorf("sign-in pushes = %+v, want one naming the KIRO_API_KEY to replace", p)
		}
		s.h.NoteEngineAccepted()
		synctest.Wait()
		if got := s.h.SignInHealth().State; got != marotte.SignInOK {
			t.Errorf("state after an accepted turn = %q, want ok", got)
		}
	})
}

// However the moves interleave, the gauge reads the state the last move left.
func TestSignInHealth_TheGaugeFollowsTheLastMove_Concurrent(t *testing.T) {
	s := newHealthHub(t, WithAPIKey(true))
	for round := range 200 {
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				if (i+round)%2 == 0 {
					s.h.NoteEngineRefusal()
				} else {
					s.h.NoteEngineAccepted()
				}
			})
		}
		wg.Wait()
		want := s.h.SignInHealth().State
		for _, st := range []marotte.SignInState{marotte.SignInOK, marotte.SignInSignedOut} {
			v := testsupport.MetricValue(t, `marotte_signin_state{state="`+string(st)+`"}`)
			if (v == 1) != (st == want) {
				t.Fatalf("round %d: marotte_signin_state{state=%q} = %v with the state %q", round, st, v, want)
			}
		}
	}
}
