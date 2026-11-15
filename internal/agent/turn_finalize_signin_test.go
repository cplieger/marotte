package agent

import (
	"strings"
	"testing"

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

// A turn the engine failed on its credential closes with the sign-in's cause in marotte's words, whichever closer
// wins: the outage while the vault holds the provider unreachable, Kiro's refusal while it answers.
func TestCloseTurn_AnAuthClassClosesWithTheSignInCause(t *testing.T) {
	closers := map[string]func(h *Runtime, id string){
		"wire turn_end": func(h *Runtime, _ string) {
			h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonError)
		},
		"prompt failure": func(h *Runtime, id string) {
			h.coord.abandonInFlightTurn(t.Context(), "c1", id, marotte.StopReasonInterrupted, "ACP error -32000: Authentication failed", "", 0)
		},
	}
	reaches := map[string]struct {
		arrange func(s *healthHub, t *testing.T)
		want    string
	}{
		"the provider is down": {arrange: (*healthHub).providerDown, want: marotte.SignInFailedUnreachableReason},
		"the provider answers": {arrange: func(*healthHub, *testing.T) {}, want: marotte.SignInFailedRefusedReason},
	}
	for closerName, closeIt := range closers {
		for reachName, reach := range reaches {
			t.Run(closerName+", "+reachName, func(t *testing.T) {
				s := newHealthHub(t)
				s.signIn(t)
				reach.arrange(s, t)
				id, log := streamingPromptTurn(t, s.h, "c1", "half an answer")
				log.SetEngineError(marotte.EngineError{Message: "Authentication failed. Please sign in again.", ErrorType: "TokenExpiredError"})

				closeIt(s.h, id)

				closes := closesOf(t, logOf(t, s.cs, "c1"))
				if len(closes) != 1 {
					t.Fatalf("turn_close entries = %d, want exactly one", len(closes))
				}
				if closes[0].FailureKind != marotte.FailureKindSignIn || closes[0].FailureReason != reach.want {
					t.Errorf("turn_close = {%q %q}, want {%q %q}", closes[0].FailureKind, closes[0].FailureReason,
						marotte.FailureKindSignIn, reach.want)
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
	if closes[0].FailureKind == marotte.FailureKindSignIn || strings.HasPrefix(closes[0].FailureReason, "Kiro sign-in failed") {
		t.Errorf("turn_close = {%q %q}, want the throttle's own account", closes[0].FailureKind, closes[0].FailureReason)
	}
}
