package signin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// providerSwitch is a refresh that fails while down is set and renews for an hour otherwise.
type providerSwitch struct {
	h    *harness
	down atomic.Bool
}

func (p *providerSwitch) refresh(_ context.Context, c fakeCred) (fakeCred, error) {
	if p.down.Load() {
		return fakeCred{}, errNetworkDown
	}
	c.Token += "+r"
	c.Expiry = p.h.clk.now().Add(time.Hour)
	return c, nil
}

// unreachableHarness signs alice in with a token past its floor and runs one ask through a provider that is down,
// so the vault holds the record unreachable; probeEvery stays the harness's hour, so no background reprobe runs.
func unreachableHarness(t *testing.T) (*harness, *fakeBackend, *providerSwitch) {
	t.Helper()
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	p := &providerSwitch{h: h}
	p.down.Store(true)
	fb.setRefresh(p.refresh)
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Fatal("answered while the provider is down")
	}
	// A renewal outlives its asker (Answer's contract): the last retry can start as the window ends, and one still
	// running would land in a caller's before count or renew through the caller's next hook.
	awaitFlight(h.v.slots["cb"])
	if a := h.v.Answerability("cb"); a != ProviderUnreachable {
		t.Fatalf("Answerability = %v, want %v before the reprobe", a, ProviderUnreachable)
	}
	return h, fb, p
}

// awaitFlight waits out the renewal in flight on s, if any.
func awaitFlight(s *slot) {
	s.mu.Lock()
	f := s.flight
	s.mu.Unlock()
	if f != nil {
		<-f.done
	}
}

// A provider back before the background reprobe's next tick answers the reprobe a caller asks for.
func TestReprobe_AProviderBackMakesTheRecordAnswerableAtOnce(t *testing.T) {
	h, fb, p := unreachableHarness(t)
	before := fb.refreshCount()
	p.down.Store(false)

	if a := h.v.Reprobe(t.Context(), "cb"); a != Answerable {
		t.Errorf("Reprobe = %v, want %v once the provider answers", a, Answerable)
	}
	if n := fb.refreshCount() - before; n != 1 {
		t.Errorf("renewals the reprobe made = %d, want 1", n)
	}
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answer after the reprobe = %q, want its renewal", a.Token)
	}
}

// A provider still down keeps the record unreachable, and the caller's deadline bounds its wait on a renewal that
// hangs.
func TestReprobe_AProviderStillDownStaysUnreachableWithinTheCallersDeadline(t *testing.T) {
	h, fb, _ := unreachableHarness(t)
	if a := h.v.Reprobe(t.Context(), "cb"); a != ProviderUnreachable {
		t.Errorf("Reprobe with the provider down = %v, want %v", a, ProviderUnreachable)
	}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fb.setRefresh(func(ctx context.Context, _ fakeCred) (fakeCred, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return fakeCred{}, errNetworkDown
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if a := h.v.Reprobe(ctx, "cb"); a != ProviderUnreachable {
		t.Errorf("Reprobe on a hanging renewal = %v, want %v", a, ProviderUnreachable)
	}
	if took := time.Since(start); took > 10*h.answerWindow {
		t.Errorf("Reprobe waited %v on a hanging renewal, want about its 30ms deadline", took)
	}
}

// Callers reprobing together share one renewal.
func TestReprobe_ConcurrentCallersShareOneRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, fb, p := unreachableHarness(t)
		before := fb.refreshCount()
		gate := make(chan struct{})
		fb.setRefresh(func(ctx context.Context, c fakeCred) (fakeCred, error) {
			<-gate
			return p.refresh(ctx, c)
		})
		p.down.Store(false)

		const callers = 4
		got := make([]Answerability, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Go(func() { got[i] = h.v.Reprobe(t.Context(), "cb") })
		}
		// Every caller is parked on a renewal before the first one ends.
		synctest.Wait()
		close(gate)
		wg.Wait()

		for i, a := range got {
			if a != Answerable {
				t.Errorf("caller %d Reprobe = %v, want %v", i, a, Answerable)
			}
		}
		if n := fb.refreshCount() - before; n != 1 {
			t.Errorf("renewals for %d concurrent reprobes = %d, want 1", callers, n)
		}
	})
}

// A record that is not unreachable is reported as it stands, with no renewal.
func TestReprobe_ARecordThatIsNotUnreachableRenewsNothing(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	if a := h.v.Reprobe(t.Context(), "cb"); a != NeedsSignIn {
		t.Errorf("Reprobe with no record = %v, want %v", a, NeedsSignIn)
	}
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	if a := h.v.Reprobe(t.Context(), "cb"); a != Answerable {
		t.Errorf("Reprobe of an answerable record = %v, want %v", a, Answerable)
	}
	if n := fb.refreshCount(); n != 0 {
		t.Errorf("renewals = %d, want none for a record the vault does not hold unreachable", n)
	}
	if a := h.v.Reprobe(t.Context(), "nope"); a != NeedsSignIn {
		t.Errorf("Reprobe of an unknown backend = %v, want %v", a, NeedsSignIn)
	}
}
