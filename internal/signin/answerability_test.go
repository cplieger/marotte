package signin

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// answerEdges records every OnAnswerability delivery in order.
type answerEdges struct {
	got []Answerability
	mu  sync.Mutex
}

func (e *answerEdges) list() []Answerability {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.got)
}

func (h *harness) watchAnswers() *answerEdges {
	e := &answerEdges{}
	h.v.OnAnswerability(func(_ BackendID, a Answerability) {
		e.mu.Lock()
		e.got = append(e.got, a)
		e.mu.Unlock()
	})
	return e
}

func (e *answerEdges) waitFor(t *testing.T, want ...Answerability) {
	t.Helper()
	eventually(t, "the answerability edges", func() bool { return len(e.list()) >= len(want) })
	if got := e.list(); !slices.Equal(got, want) {
		t.Errorf("answerability edges = %v, want %v", got, want)
	}
}

var errNetworkDown = errors.New("network down")

func TestAnswerability_SignInSignOutAndLapseMoveIt(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	if a := h.v.Answerability("cb"); a != NeedsSignIn {
		t.Fatalf("Answerability with no record = %v, want %v", a, NeedsSignIn)
	}
	edges := h.watchAnswers()
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, ErrRejected })
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Fatal("a rejected renewal was answered")
	}
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	edges.waitFor(t, Answerable, NeedsSignIn, Answerable, NeedsSignIn)
}

// A transient renewal failure on a record that cannot be served is retried within the window, and the window
// passing marks the record unreachable.
func TestAnswer_ATransientFailureIsRetriedThenMarksTheProviderUnreachable(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	edges := h.watchAnswers()
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, errNetworkDown })

	start := time.Now()
	_, rerr := h.answer("cb")
	if rerr == nil || rerr.Message != "cb: unreachable" {
		t.Fatalf("answer = %v, want the unreachable refusal", rerr)
	}
	if took := time.Since(start); took < h.answerWindow {
		t.Errorf("refused after %v, want the whole %v window spent", took, h.answerWindow)
	}
	if n := fb.refreshCount(); n < 3 {
		t.Errorf("refreshes = %d, want the renewal retried within the window", n)
	}
	if a := h.v.Answerability("cb"); a != ProviderUnreachable {
		t.Errorf("Answerability = %v, want %v", a, ProviderUnreachable)
	}
	edges.waitFor(t, ProviderUnreachable)
}

func TestAnswer_ARetryThatSucceedsInsideTheWindowIsServed(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	fails := 2
	fb.setRefresh(func(_ context.Context, c fakeCred) (fakeCred, error) {
		if fails > 0 {
			fails--
			return fakeCred{}, errNetworkDown
		}
		c.Token += "+r"
		c.Expiry = h.clk.now().Add(time.Hour)
		return c, nil
	})
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answer = %q, want the renewal the third attempt made", a.Token)
	}
	if a := h.v.Answerability("cb"); a != Answerable {
		t.Errorf("Answerability = %v, want %v", a, Answerable)
	}
}

// An asker that gives up (a cancel, not its deadline) says nothing about the provider.
func TestAnswer_ACancelledAskerMarksNothing(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, errNetworkDown })
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, rerr := h.v.Answer(ctx, "cb"); rerr == nil {
		t.Fatal("answered with every renewal failing")
	}
	if a := h.v.Answerability("cb"); a != Answerable {
		t.Errorf("Answerability after a cancelled ask = %v, want %v", a, Answerable)
	}
}

// An asker whose own deadline ends before the vault's window says nothing about the provider either.
func TestAnswer_AnAskerDeadlineShorterThanTheWindowMarksNothing(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	edges := h.watchAnswers()
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, errNetworkDown })
	ctx, cancel := context.WithTimeout(t.Context(), h.answerWindow/10)
	defer cancel()
	if _, rerr := h.v.Answer(ctx, "cb"); rerr == nil {
		t.Fatal("answered with every renewal failing")
	}
	if a := h.v.Answerability("cb"); a != Answerable {
		t.Errorf("Answerability after an asker's deadline = %v, want %v", a, Answerable)
	}
	if got := edges.list(); len(got) != 0 {
		t.Errorf("answerability edges after an asker's deadline = %v, want none", got)
	}
}

// With nothing asking, the vault renews an unreachable record until the provider answers, and says so.
func TestAnswerability_AnUnreachableRecordTurnsAnswerableOnTheNextRenewal(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.probeEvery = 10 * time.Millisecond
	h.reopen()
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	edges := h.watchAnswers()
	var mu sync.Mutex
	down := true
	fb.setRefresh(func(_ context.Context, c fakeCred) (fakeCred, error) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			return fakeCred{}, errNetworkDown
		}
		c.Token += "+r"
		c.Expiry = h.clk.now().Add(time.Hour)
		return c, nil
	})
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Fatal("answered while the provider is down")
	}
	// A renewal the ask left in flight fails too, so only a later one can bring the record back.
	asked := fb.refreshCount()
	eventually(t, "a renewal after the ask", func() bool { return fb.refreshCount() > asked })
	mu.Lock()
	down = false
	mu.Unlock()
	edges.waitFor(t, ProviderUnreachable, Answerable)
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answer after the provider came back = %q, want the reprobe's renewal", a.Token)
	}
}

// Answerability reads the record the engine is answered from: a switch the engine cannot take live keeps a lapsed
// record answered until the next spawn, and that lapse fires no Change.
func TestAnswerability_FollowsTheEffectiveRecordNotTheDesiredOne(t *testing.T) {
	cb := newFake(nil, "cb", "_test/auth/token")
	notLive(cb)
	h := newHarness(t, cb)
	h.signIn(cb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	h.mustAnswer("cb")
	// Watched before bob's sign-in, whose change is delivered after the commit: it is the one change expected.
	seen := h.watch()
	h.signIn(cb, approval{cred: credOf(t, h.account("bob"))})
	signedIn := len(seen.wait(t, 1))
	edges := h.watchAnswers()
	h.clk.advance(time.Hour)
	cb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, ErrRejected })
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Fatal("the lapsed effective record was answered")
	}
	edges.waitFor(t, NeedsSignIn)
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("Status = %v, want bob still signed in", st.Kind)
	}
	if got := seen.list(); len(got) != signedIn {
		t.Errorf("changes = %+v, want only bob's sign-in: none for a lapse of the record the engine alone holds", got)
	}
	h.v.EngineSpawned("cb")
	if a := h.v.Answerability("cb"); a != Answerable {
		t.Errorf("Answerability after the spawn = %v, want %v", a, Answerable)
	}
	if got := edges.list(); len(got) != 1 {
		t.Errorf("answerability edges = %v, want the spawn's move left to a read", got)
	}
}

// A renewal whose lease is too short to use leaves a record no ask can be answered from: it reads unreachable, and
// with nothing asking the vault renews it until a usable lease comes back.
func TestAnswerability_AShortLeaseIsUnreachableUntilAUsableRenewal(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.probeEvery = 10 * time.Millisecond
	h.reopen()
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	edges := h.watchAnswers()
	var mu sync.Mutex
	short := true
	fb.setRefresh(func(_ context.Context, c fakeCred) (fakeCred, error) {
		mu.Lock()
		defer mu.Unlock()
		c.Token += "+r"
		c.Expiry = h.clk.now().Add(time.Hour)
		if short {
			c.Expiry = h.clk.now().Add(time.Minute)
		}
		return c, nil
	})
	if _, rerr := h.answer("cb"); rerr == nil || rerr.Message != "cb: unreachable" {
		t.Fatalf("answer on a short lease = %v, want the unreachable refusal", rerr)
	}
	if a := h.v.Answerability("cb"); a != ProviderUnreachable {
		t.Errorf("Answerability after a short lease = %v, want %v", a, ProviderUnreachable)
	}
	mu.Lock()
	short = false
	mu.Unlock()
	edges.waitFor(t, ProviderUnreachable, Answerable)
}
