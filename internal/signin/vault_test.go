package signin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func staleAccount(h *harness, name string) fakeCred {
	c := h.account(name)
	c.Expiry = h.clk.now().Add(time.Minute)
	return c
}

// blockRefresh makes fb's refresh wait for release (or its context) and returns the release.
func blockRefresh(h *harness, fb *fakeBackend) chan struct{} {
	release := make(chan struct{})
	fb.setRefresh(func(ctx context.Context, c fakeCred) (fakeCred, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return fakeCred{}, ctx.Err()
		}
		c.Token += "+r"
		c.Expiry = h.clk.now().Add(time.Hour)
		return c, nil
	})
	return release
}

func TestAnswer_TwentyConcurrentAsksMakeOneRefresh(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	release := blockRefresh(h, fb)
	var wg sync.WaitGroup
	tokens := make([]string, 20)
	for i := range tokens {
		wg.Go(func() {
			if a, rerr := h.answer("cb"); rerr == nil {
				tokens[i] = a.Token
			}
		})
	}
	eventually(t, "the refresh to start", func() bool { return fb.refreshCount() == 1 })
	close(release)
	wg.Wait()
	if n := fb.refreshCount(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
	for i, tok := range tokens {
		if tok != "alice-token+r" {
			t.Errorf("asker %d got %q, want the renewed token", i, tok)
		}
	}
}

func TestAnswer_AnAskerGivingUpDoesNotCancelTheSharedRefresh(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	release := blockRefresh(h, fb)
	ctx, cancel := context.WithCancel(t.Context())
	gaveUp := make(chan struct{})
	go func() {
		defer close(gaveUp)
		if _, rerr := h.v.Answer(ctx, "cb"); rerr == nil || rerr.Message != "cb: unreachable" {
			t.Errorf("the asker that gave up got %v, want the unreachable refusal", rerr)
		}
	}()
	eventually(t, "the refresh to start", func() bool { return fb.refreshCount() == 1 })
	cancel()
	<-gaveUp
	close(release)
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answer = %q, want the renewal the first asker started", a.Token)
	}
	if n := fb.refreshCount(); n != 1 {
		t.Errorf("refreshes = %d, want 1: the renewal was cancelled and redone", n)
	}
}

// TestAnswer_AStaleAskStartsNoSecondRefresh: an asker that read the record before a renewal
// replaced it must not renew it again, which would spend a rotated-away refresh token.
func TestAnswer_AStaleAskStartsNoSecondRefresh(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	s := h.v.slots["cb"]
	s.mu.Lock()
	old := s.effective
	s.mu.Unlock()
	h.mustAnswer("cb")
	f, ok := h.v.awaitRenewal(t.Context(), s, old)
	if !ok || f.out != renewDiscarded {
		t.Errorf("a renewal of the replaced record = %+v, %v; want discarded", f, ok)
	}
	if n := fb.refreshCount(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
}

func TestSignOutDuringRefreshDiscardsTheRefresh(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	release := blockRefresh(h, fb)
	result := make(chan error, 1)
	go func() {
		_, rerr := h.v.Answer(context.Background(), "cb")
		if rerr == nil {
			result <- errors.New("answered a token")
			return
		}
		result <- nil
	}()
	eventually(t, "the refresh to start", func() bool { return fb.refreshCount() == 1 })
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Errorf("the asker during the sign-out: %v", err)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("Status = %v after the discarded refresh, want signed_out", st.Kind)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen = %v: the discarded renewal was saved", st.Kind)
	}
}

func TestStartSignIn_ThePollOutlivesTheRequest(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	ctx, cancel := context.WithCancel(t.Context())
	p, err := h.v.StartSignIn(ctx, "cb", FlowParams{Flow: FlowDevice})
	if err != nil {
		t.Fatal(err)
	}
	if p.VerificationURL != "https://device.example/cb" || p.UserCode != "CODE-1" {
		t.Errorf("Pending = %+v", p)
	}
	cancel()
	fb.approvals <- approval{cred: credOf(t, h.account("alice"))}
	if st := h.settled("cb"); st.Kind != StatusSignedIn {
		t.Errorf("Status = %+v, want signed_in after the request ended", st)
	}
}

func TestSignOut_CancelsARunningPoll(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		name := "from_signed_out"
		if signedIn {
			name = "from_signed_in"
		}
		t.Run(name, func(t *testing.T) {
			fb := newFake(nil, "cb", "_test/auth/token")
			h := newHarness(t, fb)
			if signedIn {
				h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
				<-fb.pollEnded
			}
			p, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.v.SignOut("cb"); err != nil {
				t.Fatal(err)
			}
			want := Attempt{ID: p.Attempt, State: attemptCancelled}
			if st := h.v.Status("cb"); st.SignIn != want {
				t.Errorf("SignIn = %+v, want %+v", st.SignIn, want)
			}
			select {
			case err := <-fb.pollEnded:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("the poll ended with %v, want its context cancelled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the sign-out did not cancel the poll")
			}
			st := h.v.Status("cb")
			if st.Kind != StatusSignedOut || st.SignIn != want {
				t.Errorf("Status = %+v, want signed_out and the attempt still cancelled", st)
			}
		})
	}
}

// TestSignOut_DuringBeginCancelsTheAttempt: StartSignIn reports the cancellation whether Begin then
// succeeds or fails.
func TestSignOut_DuringBeginCancelsTheAttempt(t *testing.T) {
	for _, tc := range []struct {
		beginErr error
		name     string
	}{
		{name: "begin_succeeds"},
		{name: "begin_fails", beginErr: errors.New("fault: begin")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFake(nil, "cb", "_test/auth/token")
			fb.beginErr = tc.beginErr
			entered, release := make(chan struct{}), make(chan struct{})
			fb.beginHook = func() {
				close(entered)
				<-release
			}
			h := newHarness(t, fb)
			out := make(chan error, 1)
			go func() {
				_, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice})
				out <- err
			}()
			<-entered
			if err := h.v.SignOut("cb"); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-out; !errors.Is(err, ErrSignInCancelled) {
				t.Errorf("StartSignIn = %v, want ErrSignInCancelled", err)
			}
			if st := h.v.Status("cb"); st.SignIn != (Attempt{ID: 1, State: attemptCancelled}) {
				t.Errorf("SignIn = %+v, want attempt 1 cancelled", st.SignIn)
			}
		})
	}
}

// TestSignOut_DuringTheSignInsSaveRemovesIt: the poll already returned and its save is in flight
// when the user signs out; the sign-out waits for that commit and removes it.
func TestSignOut_DuringTheSignInsSaveRemovesIt(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	h.disk.set(func(d *faultDisk) { d.writeGate, d.entered = gate, entered })
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
		t.Fatal(err)
	}
	fb.approvals <- approval{cred: credOf(t, h.account("alice"))}
	<-entered
	out := make(chan error, 1)
	go func() { out <- h.v.SignOut("cb") }()
	select {
	case err := <-out:
		t.Fatalf("SignOut returned %v while the sign-in's save was in flight, so that save will commit after it", err)
	case <-time.After(20 * time.Millisecond): // SignOut must still be waiting for the save
	}
	release()
	if err := <-out; err != nil {
		t.Fatal(err)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("Status = %v, want signed_out", st.Kind)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen = %v, want signed_out", st.Kind)
	}
}

func TestStartSignIn_ASecondWhilePollingIsRefused(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); !errors.Is(err, ErrSignInInProgress) {
		t.Errorf("second StartSignIn = %v, want ErrSignInInProgress", err)
	}
}

func TestStartSignIn_RefusesAnUnknownBackendOrFlow(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	if _, err := h.v.StartSignIn(t.Context(), "absent", FlowParams{Flow: FlowDevice}); !errors.Is(err, errUnknownBackend) {
		t.Errorf("unknown backend = %v", err)
	}
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{}); !errors.Is(err, errFlowUnsupported) {
		t.Errorf("empty flow = %v", err)
	}
	if st := h.v.Status("cb"); st.SignIn.State == AttemptPending {
		t.Error("a refused StartSignIn left an attempt pending")
	}
}

func TestStartSignIn_ABeginFailureEndsTheAttempt(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	fb.beginErr = ErrInvalidParams
	h := newHarness(t, fb)
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("StartSignIn = %v, want the backend's error", err)
	}
	if st := h.v.Status("cb"); st.SignIn != (Attempt{ID: 1, State: AttemptFailed, Error: textSignInFailed}) {
		t.Errorf("SignIn = %+v, want attempt 1 failed, so a later sign-in is not refused", st.SignIn)
	}
}

// TestSignIn_AnAttemptIsPendingUntilItCommits: while A stays signed in, a switch to B reads pending
// from the moment StartSignIn returns, so a poll that sees signed_in cannot mistake A for B.
func TestSignIn_AnAttemptIsPendingUntilItCommits(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	p, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice})
	if err != nil {
		t.Fatal(err)
	}
	mid := h.v.Status("cb")
	if mid.SignIn != (Attempt{ID: p.Attempt, State: AttemptPending}) || mid.Kind != StatusSignedIn || mid.Label.Email != "alice@example.com" {
		t.Errorf("Status while B polls = %+v, want B's attempt pending with alice still in place", mid)
	}
	fb.approvals <- approval{cred: credOf(t, h.account("bob"))}
	after := h.settled("cb")
	if after.Label.Email != "bob@example.com" || after.SignIn != (Attempt{ID: p.Attempt, State: AttemptSignedIn}) {
		t.Errorf("Status after B committed = %+v, want bob and B's attempt signed in", after)
	}
}

func TestSignIn_AFailedSwitchShowsItsErrorWhileTheOldAccountStays(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	before := h.v.Status("cb")
	st := h.signIn(fb, approval{err: &FailureError{Text: "The sign-in was declined in the browser."}})
	want := Attempt{ID: before.SignIn.ID + 1, State: AttemptFailed, Error: "The sign-in was declined in the browser."}
	if st.SignIn != want {
		t.Errorf("SignIn = %+v, want %+v", st.SignIn, want)
	}
	if st.Kind != StatusSignedIn || st.Label.Email != "alice@example.com" {
		t.Errorf("Status = %+v, want alice still signed in", st)
	}
	if a := h.mustAnswer("cb"); a.Account != "alice" {
		t.Errorf("answered %q, want alice", a.Account)
	}
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
		t.Fatal(err)
	}
	if st := h.v.Status("cb"); st.SignIn.Error != "" || st.SignIn.State != AttemptPending {
		t.Errorf("SignIn = %+v after the next StartSignIn, want a pending attempt with no error", st.SignIn)
	}
}

func TestSignIn_AnUnusableCredentialFailsTheSignIn(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	bad := h.account("alice")
	bad.Bad = true
	st := h.signIn(fb, approval{cred: credOf(t, bad)})
	if st.Kind != StatusSignedOut || st.SignIn.Error != textUnusable {
		t.Errorf("Status = %+v, want signed_out with the unusable text", st)
	}
}

func TestSignIn_APollPastTheCodesExpiryReadsAsTimedOut(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	fb.expiresIn = 10 * time.Millisecond
	h := newHarness(t, fb)
	if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
		t.Fatal(err)
	}
	if st := h.settled("cb"); st.SignIn.Error != textSignInTimeout {
		t.Errorf("SignIn.Error = %q, want the timeout text", st.SignIn.Error)
	}
}

func TestSignIn_AnUntypedFailureReadsAsGeneric(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	if st := h.signIn(fb, approval{err: errors.New("boom")}); st.SignIn.Error != textSignInFailed {
		t.Errorf("SignIn.Error = %q, want the generic text", st.SignIn.Error)
	}
}

func TestOnChange_FiresOnTheSignedOutToSignedInEdge(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	seen := h.watch()
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatal(err)
	}
	got := seen.wait(t, 2)
	want := []Change{
		{Backend: "cb", From: StatusSignedOut, To: StatusSignedIn},
		{Backend: "cb", From: StatusSignedIn, To: StatusSignedOut},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("changes = %+v, want %+v", got, want)
	}
}

func TestAnswer_ARejectionLapsesTheRecordAndFiresTheEdge(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	seen := h.watch()
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, ErrRejected })
	if _, rerr := h.answer("cb"); rerr == nil || rerr.Message != "cb: rejected" {
		t.Errorf("answer = %v, want the rejected refusal", rerr)
	}
	if _, rerr := h.answer("cb"); rerr == nil || rerr.Message != "cb: signed_out" {
		t.Errorf("next answer = %v, want the signed-out refusal", rerr)
	}
	if n := fb.refreshCount(); n != 1 {
		t.Errorf("refreshes = %d, want 1: a lapsed record is not renewed again", n)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("Status = %v, want signed_out", st.Kind)
	}
	if got := seen.wait(t, 2); len(got) != 2 || got[1].From != StatusSignedIn || got[1].To != StatusSignedOut {
		t.Errorf("changes = %+v, want the sign-in then one signed_in to signed_out edge", got)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen = %v, want the lapse saved", st.Kind)
	}
}

func TestAnswer_UnknownBackendIsRefused(t *testing.T) {
	h := newHarness(t, newFake(nil, "cb", "_test/auth/token"))
	if _, rerr := h.v.Answer(t.Context(), "absent"); rerr == nil {
		t.Error("Answer(absent) answered")
	}
	if st := h.v.Status("absent"); st.Kind != statusUnavailable {
		t.Errorf("Status(absent) = %v", st.Kind)
	}
	if err := h.v.SignOut("absent"); !errors.Is(err, errUnknownBackend) {
		t.Errorf("SignOut(absent) = %v", err)
	}
}

// TestRefresh_AShortRenewalIsNeverAnswered: a renewal whose credential Servable refuses is never an
// answer. The old record is answered while it is still Servable, the asker is refused otherwise,
// and the short record is installed and saved, since it may carry a rotated refresh token.
func TestRefresh_AShortRenewalIsNeverAnswered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		want      string
		oldLeft   time.Duration
		renewLeft time.Duration
	}{
		{name: "renewal_at_the_kas_floor_old_servable", oldLeft: 250 * time.Second, renewLeft: 180 * time.Second, want: "alice-token"},
		{name: "renewal_at_the_stale_floor_old_servable", oldLeft: 250 * time.Second, renewLeft: 210 * time.Second, want: "alice-token"},
		{name: "renewal_short_old_short", oldLeft: 200 * time.Second, renewLeft: 120 * time.Second},
		{name: "renewal_past_the_stale_floor", oldLeft: 200 * time.Second, renewLeft: 211 * time.Second, want: "renewed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFake(nil, "cb", "_test/auth/token")
			h := newHarness(t, fb)
			old := h.account("alice")
			old.Expiry = h.clk.now().Add(tc.oldLeft)
			h.signIn(fb, approval{cred: credOf(t, old)})
			fb.setRefresh(func(_ context.Context, c fakeCred) (fakeCred, error) {
				c.Token = "renewed"
				c.Expiry = h.clk.now().Add(tc.renewLeft)
				return c, nil
			})
			a, rerr := h.answer("cb")
			switch {
			case tc.want == "" && rerr == nil:
				t.Errorf("answered %+v, want the unreachable refusal", a)
			case tc.want == "" && rerr.Message != "cb: unreachable":
				t.Errorf("refusal = %q, want unreachable", rerr.Message)
			case tc.want != "" && (rerr != nil || a.Token != tc.want):
				t.Errorf("answer = %+v, %v; want token %q", a, rerr, tc.want)
			}
			eventually(t, "the renewal to be saved", func() bool { return fileHolds(t, h.path(), `"token":"renewed"`) })
		})
	}
}

// TestRefresh_AFreshRenewalIsAnsweredWhenServableCoversOnlyTheStaleBand: Servable judges only a
// record that is not Fresh, so a backend whose Servable is false for a fresh record still has its
// normal renewal answered.
func TestRefresh_AFreshRenewalIsAnsweredWhenServableCoversOnlyTheStaleBand(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	fb.staleBandOnly = true
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answered %q, want the renewal", a.Token)
	}
}
