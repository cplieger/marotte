package agent

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/signin"
)

// A prompt that meets a known outage is blocked only while one bounded reprobe also finds the provider down; a
// provider that came back runs the prompt at once, without waiting for the vault's next background reprobe.
func TestTurnBlocked_ReprobesAKnownOutageOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := newHealthHub(t)
		s.signIn(t)
		if s.h.TurnBlocked(t.Context()) {
			t.Error("TurnBlocked with no outage = true, want false")
		}
		if n := s.b.refreshCount(); n != 0 {
			t.Errorf("renewals with no outage = %d, want none", n)
		}

		s.failPastTheGrace(t)
		before := s.b.refreshCount()
		if !s.h.TurnBlocked(t.Context()) {
			t.Error("TurnBlocked with the provider still down = false, want true")
		}
		if n := s.b.refreshCount() - before; n != 1 {
			t.Errorf("renewals for one blocked prompt = %d, want 1", n)
		}

		s.b.setHang(true)
		start := time.Now()
		if !s.h.TurnBlocked(t.Context()) {
			t.Error("TurnBlocked on a hanging renewal = false, want true")
		}
		if took := time.Since(start); took != turnReprobeWait {
			t.Errorf("TurnBlocked waited %v on a hanging renewal, want its %v deadline", took, turnReprobeWait)
		}
		// The hung renewal ends at its own budget; the background reprobes after it still fail.
		s.b.setHang(false)
		time.Sleep(time.Minute)
		synctest.Wait()
		if a := s.vault.Answerability(gateBackendID); a != signin.ProviderUnreachable {
			t.Fatalf("Setup: Answerability = %v, want the outage still known", a)
		}

		s.b.set(false, false, nil)
		start = time.Now()
		if s.h.TurnBlocked(t.Context()) {
			t.Error("TurnBlocked with the provider back = true, want false")
		}
		if took := time.Since(start); took != 0 {
			t.Errorf("TurnBlocked with the provider back waited %v, want no wait for the background reprobe", took)
		}
		if a := s.vault.Answerability(gateBackendID); a != signin.Answerable {
			t.Errorf("Answerability after the reprobe = %v, want %v", a, signin.Answerable)
		}
	})
}
