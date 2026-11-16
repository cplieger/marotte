package agent

import (
	"strings"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// providerDown leaves the vault holding the provider unreachable with no wait: a renewal returns a lease too short
// to use.
func (s *healthHub) providerDown(t *testing.T) {
	t.Helper()
	s.b.set(false, false, nil)
	s.b.setShort(true)
	if rerr := s.ask(t); rerr == nil {
		t.Fatal("an ask on a short lease was answered")
	}
	if a := s.vault.Answerability(gateBackendID); a != signin.ProviderUnreachable {
		t.Fatalf("Setup: answerability = %v, want provider_unreachable", a)
	}
}

// providerBack leaves the provider answering again after it refused an ask for being unreachable.
func (s *healthHub) providerBack(t *testing.T) {
	t.Helper()
	s.providerDown(t)
	s.b.setShort(false)
	if a := s.vault.Reprobe(t.Context(), gateBackendID); a != signin.Answerable {
		t.Fatalf("Setup: answerability after the reprobe = %v, want answerable", a)
	}
}

// A turn the engine failed on its credential closes with the sign-in's cause as its kind and in marotte's words,
// whichever closer wins: the outage when the newest ask the engine was answered was refused for it, the API key when
// one signs the engine in, else Kiro's refusal.
func TestCloseTurn_AnAuthClassClosesWithTheSignInCause(t *testing.T) {
	closers := map[string]func(h *Runtime, id string){
		"wire turn_end": func(h *Runtime, _ string) {
			h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)
		},
		"prompt failure": func(h *Runtime, id string) {
			h.coord.abandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "ACP error -32000: Authentication failed", "", 0)
		},
	}
	none := func(*healthHub, *testing.T) {}
	reaches := map[string]struct {
		arrange  func(s *healthHub, t *testing.T)
		want     string
		wantKind marotte.FailureKind
		opts     []Option
	}{
		"the provider is down": {
			arrange: (*healthHub).providerDown, want: marotte.SignInFailedUnreachableReason, wantKind: marotte.FailureKindSignInUnreachable,
		},
		"the provider came back after the refusal": {
			arrange: (*healthHub).providerBack, want: marotte.SignInFailedUnreachableReason, wantKind: marotte.FailureKindSignInUnreachable,
		},
		"the provider answers": {arrange: none, want: marotte.SignInFailedRefusedReason, wantKind: marotte.FailureKindSignIn},
		"an API key signs the engine in": {
			arrange: none, want: marotte.SignInFailedAPIKeyReason, wantKind: marotte.FailureKindAPIKeyRefused, opts: []Option{WithAPIKey(true)},
		},
	}
	for closerName, closeIt := range closers {
		for reachName, reach := range reaches {
			t.Run(closerName+", "+reachName, func(t *testing.T) {
				s := newHealthHub(t, reach.opts...)
				s.signIn(t)
				reach.arrange(s, t)
				id, log := streamingPromptTurn(t, s.h, "c1", "half an answer")
				log.SetEngineError(marotte.EngineError{Message: "Authentication failed. Please sign in again.", ErrorType: "TokenExpiredError"})

				closeIt(s.h, id)

				closes := closesOf(t, logOf(t, s.cs, "c1"))
				if len(closes) != 1 {
					t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
				}
				if closes[0].FailureKind != reach.wantKind || closes[0].FailureReason != reach.want {
					t.Errorf("turn_close = {%q %q}, want {%q %q}", closes[0].FailureKind, closes[0].FailureReason,
						reach.wantKind, reach.want)
				}
			})
		}
	}
}

// Another latched class keeps the engine's own account, outage or not.
func TestCloseTurn_AThrottleKeepsItsOwnAccountDuringAnOutage(t *testing.T) {
	s := newHealthHub(t)
	s.signIn(t)
	s.providerDown(t)
	_, log := streamingPromptTurn(t, s.h, "c1", "half an answer")
	log.SetEngineError(marotte.EngineError{Message: "The model is busy. Please try again in a moment.", ErrorType: "ModelThrottleError"})

	s.h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)

	closes := closesOf(t, logOf(t, s.cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].FailureKind.SignIn() || strings.HasPrefix(closes[0].FailureReason, "Kiro sign-in failed") {
		t.Errorf("turn_close = {%q %q}, want the throttle's own account", closes[0].FailureKind, closes[0].FailureReason)
	}
}

// A prompt the command layer graded a sign-in failure, with no class latched on the turn, is worded from the asks the
// engine was answered as the latched case is.
func TestCloseTurn_ASignInKindFromThePromptIsWordedFromTheStanding(t *testing.T) {
	s := newHealthHub(t)
	s.signIn(t)
	s.providerDown(t)
	id, _ := streamingPromptTurn(t, s.h, "c1", "half an answer")

	s.h.coord.abandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, marotte.SignInFailedReason,
		marotte.FailureKindSignIn, 0)

	closes := closesOf(t, logOf(t, s.cs, "c1"))
	if len(closes) != 1 {
		t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
	}
	if closes[0].FailureKind != marotte.FailureKindSignInUnreachable || closes[0].FailureReason != marotte.SignInFailedUnreachableReason {
		t.Errorf("turn_close = {%q %q}, want {%q %q}", closes[0].FailureKind, closes[0].FailureReason,
			marotte.FailureKindSignInUnreachable, marotte.SignInFailedUnreachableReason)
	}
}

// A turn's close and its grade read only the asks it could have depended on: an ask refused while it was open stays
// its cause after the provider came back and another session's ask was served, so the grade latches no sign-out,
// sends no push and the row keeps the outage's sentence.
func TestCloseTurn_AnAskServedAfterTheTurnsRefusalLeavesItTheOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t)
		s.signIn(t)
		s.failPastTheGrace(t)
		id, log := streamingPromptTurn(t, s.h, "c1", "half an answer")
		log.SetEngineError(marotte.EngineError{Message: "Authentication failed. Please sign in again.", ErrorType: "TokenExpiredError"})
		if rerr := s.ask(t); rerr == nil {
			t.Fatal("Setup: the turn's ask was answered during the outage")
		}
		s.b.set(false, false, nil)
		if a := s.vault.Reprobe(t.Context(), gateBackendID); a != signin.Answerable {
			t.Fatalf("Setup: answerability after the reprobe = %v, want answerable", a)
		}
		if rerr := s.ask(t); rerr != nil {
			t.Fatalf("Setup: another session's ask after the recovery = %v, want served", rerr)
		}
		synctest.Wait()

		s.h.coord.abandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "ACP error -32000: Authentication failed", "", 0)
		closes := closesOf(t, logOf(t, s.cs, "c1"))
		if len(closes) != 1 {
			t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
		}
		s.h.NoteEngineRefusal(closes[0].FailureKind)
		synctest.Wait()

		if closes[0].FailureKind != marotte.FailureKindSignInUnreachable || closes[0].FailureReason != marotte.SignInFailedUnreachableReason {
			t.Errorf("turn_close = {%q %q}, want {%q %q}", closes[0].FailureKind, closes[0].FailureReason,
				marotte.FailureKindSignInUnreachable, marotte.SignInFailedUnreachableReason)
		}
		if got := s.h.SignInHealth().State; got != marotte.SignInOK || s.h.SignInRefused() {
			t.Errorf("state = %q (refused %v), want ok: the turn's ask was refused for the outage", got, s.h.SignInRefused())
		}
		if n := len(s.push.signInPushes()); n != 1 {
			t.Errorf("sign-in pushes = %d, want only the failing one", n)
		}
	})
}
