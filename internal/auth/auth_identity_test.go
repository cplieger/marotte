package auth

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/procout"
)

// countingReader is an identity read that counts its runs and answers what the test set, for the cache's rules
// without a subprocess.
type countingReader struct {
	// block, when non-nil, holds a read open so a test can observe the cache mid-refresh.
	block chan struct{}
	resp  WhoamiResponse
	mu    sync.Mutex
	calls int
}

func (r *countingReader) read(context.Context) WhoamiResponse {
	r.mu.Lock()
	r.calls++
	resp := r.resp
	block := r.block
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	return resp
}

func (r *countingReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *countingReader) setResp(resp WhoamiResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resp = resp
}

// signedIn is the arm a successful read produces, for the tests below.
func signedIn(email string) WhoamiResponse {
	return WhoamiResponse{State: WhoamiSignedIn, Email: email}
}

// waitForCalls polls until the reader has run n times, failing at the deadline; the refresh goroutine has no handle.
func waitForCalls(t *testing.T, r *countingReader, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("read ran %d times in 2s, want at least %d", r.count(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIdentityCache_ColdSnapshotIsUnavailable(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Second)

	got := c.snapshot()

	// The seed, not a sign-out: nobody has asked kiro-cli yet.
	if got.State != WhoamiUnavailable {
		t.Errorf("State = %q, want %q before the first read lands", got.State, WhoamiUnavailable)
	}
	if got.Reason != reasonNotRead {
		t.Errorf("Reason = %q, want %q", got.Reason, reasonNotRead)
	}
}

func TestIdentityCache_ColdSnapshotKicksARead(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Second)

	c.snapshot()
	waitForCalls(t, r, 1)

	if got := c.snapshot(); got.Email != "u@example.com" {
		t.Errorf("Email after the kicked read = %q, want u@example.com", got.Email)
	}
}

func TestIdentityCache_FreshSnapshotReadsNothing(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()

	for range 20 {
		if got := c.snapshot(); got.Email != "u@example.com" {
			t.Fatalf("Email = %q, want u@example.com", got.Email)
		}
	}

	if got := r.count(); got != 1 {
		t.Errorf("20 snapshots ran the read %d times, want the 1 from refresh", got)
	}
}

func TestIdentityCache_StaleSnapshotRefreshesBehindTheAnswer(t *testing.T) {
	r := &countingReader{resp: signedIn("first@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()
	r.setResp(signedIn("second@example.com"))
	c.invalidate()

	// The stale answer returns immediately; a reader never waits on a fork.
	if got := c.snapshot(); got.Email != "first@example.com" {
		t.Errorf("Email = %q, want the held first@example.com while the refresh runs", got.Email)
	}
	waitForCalls(t, r, 2)
	if got := c.snapshot(); got.Email != "second@example.com" {
		t.Errorf("Email after the background refresh = %q, want second@example.com", got.Email)
	}
}

func TestIdentityCache_InvalidateKeepsTheHeldIdentity(t *testing.T) {
	// The login window: every poll revalidates and still gets the last answer, not an unavailable banner.
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()

	c.invalidate()

	// Held value and refresh launch share a lock, so this is the pre-refresh answer.
	if got := c.snapshot(); got.State != WhoamiSignedIn || got.Email != "u@example.com" {
		t.Errorf("snapshot after invalidate = %+v, want the held signed_in identity", got)
	}
}

func TestIdentityCache_PublishOverwritesWithoutReading(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()
	// Stale first, so only the publish can satisfy freshness below; the reader would answer signed_in again.
	c.invalidate()

	signedOut := signedOutIdentity()
	c.publish(&signedOut)

	if got := c.snapshot(); got.State != WhoamiSignedOut {
		t.Fatalf("State = %q, want %q", got.State, WhoamiSignedOut)
	}
	// The publish must leave the entry fresh, or a refresh reverts the logout. Polled: the wrong answer would arrive
	// shortly after.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := c.snapshot(); got.State != WhoamiSignedOut {
			t.Fatalf("State became %q, want the published %q to stand: publish must mark the entry fresh",
				got.State, WhoamiSignedOut)
		}
		time.Sleep(time.Millisecond)
	}
	if got := r.count(); got != 1 {
		t.Errorf("read ran %d times, want the 1 from refresh", got)
	}
}

func TestIdentityCache_ConcurrentSnapshotsRunOneRead(t *testing.T) {
	// A burst of cold readers (page loads, SSE reconnects) must not fork a kiro-cli each.
	r := &countingReader{resp: signedIn("u@example.com"), block: make(chan struct{})}
	c := newIdentityCache(r.read, time.Hour)

	var wg sync.WaitGroup
	for range 25 {
		wg.Go(func() { c.snapshot() })
	}
	wg.Wait()
	close(r.block)

	if got := r.count(); got > 1 {
		t.Errorf("25 concurrent snapshots ran the read %d times, want at most 1", got)
	}
}

func TestRun_PrimesThenStops(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	h := NewHandler(fixedPath("/bin/true"))
	h.identity = newIdentityCache(r.read, time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()

	waitForCalls(t, r, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation")
	}

	if got := h.identity.snapshot().Email; got != "u@example.com" {
		t.Errorf("Email after the prime = %q, want u@example.com", got)
	}
}

func TestUnavailableIdentity_SanitizesTheReason(t *testing.T) {
	// The sanitize sits in the constructor so a future reason from upstream bytes cannot skip it.
	got := unavailableIdentity("bad\u202ereason\nwith\rcontrols")

	for _, bad := range []string{"\u202e", "\n", "\r"} {
		if strings.Contains(got.Reason, bad) {
			t.Errorf("Reason = %q, want %q folded out", got.Reason, bad)
		}
	}
	if got.State != WhoamiUnavailable {
		t.Errorf("State = %q, want %q", got.State, WhoamiUnavailable)
	}
}

// TestIdentityCache_PublishWinsAgainstAnInFlightRead pins that a page load one second before a logout can leave a fork
// running; without the generation fence its signed_in overwrites signed_out for up to a minute.
func TestIdentityCache_PublishWinsAgainstAnInFlightRead(t *testing.T) {
	block := make(chan struct{})
	r := &countingReader{resp: signedIn("u@example.com"), block: block}
	c := newIdentityCache(r.read, time.Hour)

	// The page load's refresh is held open: the window the logout lands in.
	c.snapshot()
	waitForCalls(t, r, 1)

	// marotte knows the logout outcome, so it publishes.
	signedOut := signedOutIdentity()
	c.publish(&signedOut)
	if got := c.snapshot(); got.State != WhoamiSignedOut {
		t.Fatalf("State right after publish = %q, want %q", got.State, WhoamiSignedOut)
	}

	// The pre-logout read finishes and must be discarded.
	close(block)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := c.snapshot(); got.State != WhoamiSignedOut {
			t.Fatalf("State became %q after the in-flight read landed, want the published %q to stand",
				got.State, WhoamiSignedOut)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestIdentityCache_RebuildStillClearsBusyWhenItsAnswerIsDiscarded pins that the fence drops the value, not the claim, or
// the cache never refreshes again.
func TestIdentityCache_RebuildStillClearsBusyWhenItsAnswerIsDiscarded(t *testing.T) {
	block := make(chan struct{})
	r := &countingReader{resp: signedIn("first@example.com"), block: block}
	c := newIdentityCache(r.read, time.Hour)

	c.snapshot()
	waitForCalls(t, r, 1)
	signedOut := signedOutIdentity()
	c.publish(&signedOut)
	close(block)
	waitForIdle(t, c)

	r.mu.Lock()
	r.block = nil
	r.resp = signedIn("second@example.com")
	r.mu.Unlock()
	c.invalidate()
	c.snapshot()
	waitForCalls(t, r, 2)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := c.snapshot(); got.State == WhoamiSignedIn && got.Email == "second@example.com" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want the second read's identity — busy was never released", c.snapshot())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForIdle polls until no read is in flight, failing at the deadline; the refresh goroutine has no handle.
func waitForIdle(t *testing.T, c *identityCache) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		busy := c.busy
		c.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a read was still in flight after 2s; rebuild never released busy")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestReadIdentity_SignedOutIsTheArmForAFailedExitWithAPayload pins that kiro-cli signals signed out with a null-account
// payload and a non-zero exit, so the payload must decide. The reverse is
// TestReadIdentity_CLIFailureIsUnavailable's.
func TestReadIdentity_SignedOutIsTheArmForAFailedExitWithAPayload(t *testing.T) {
	skipIfNotUnix(t)
	h := NewHandler(fixedPath(writeFakeCLI(t, `{"account":null}`, 1)))

	got := h.readIdentity(t.Context())

	if got.State != WhoamiSignedOut {
		t.Errorf("State = %q, want %q for a null-account payload on exit 1",
			got.State, WhoamiSignedOut)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty: signed_out carries no reason", got.Reason)
	}
}

// Only an ENOENT naming the CLI is the missing-CLI arm; a missing /dev/null (opened for a nil Stdin) once reported
// kiro-cli as not installed.
func TestIdentityReadFailure_ENOENTNamingAnotherFileIsNotAMissingCLI(t *testing.T) {
	h := NewHandler(fixedPath("/versions/2.21.4/kiro-cli"))
	err := &fs.PathError{Op: "open", Path: os.DevNull, Err: fs.ErrNotExist}

	got := h.identityReadFailure(t.Context(), err, procout.NewBuffer(stderrCap), 0)

	if got.State != WhoamiUnavailable {
		t.Errorf("State = %q, want %q", got.State, WhoamiUnavailable)
	}
	if want := identityText(reasonCLIFailed); got.Reason != want {
		t.Errorf("Reason = %q, want %q for an ENOENT naming %s rather than the CLI",
			got.Reason, want, os.DevNull)
	}
}

func TestIdentityReadFailure_ENOENTNamingTheCLIIsAMissingCLI(t *testing.T) {
	const cliPath = "/versions/2.21.4/kiro-cli"
	h := NewHandler(fixedPath(cliPath))
	err := &fs.PathError{Op: "fork/exec", Path: cliPath, Err: fs.ErrNotExist}

	got := h.identityReadFailure(t.Context(), err, procout.NewBuffer(stderrCap), 0)

	if want := identityText(reasonCLIMissing); got.Reason != want {
		t.Errorf("Reason = %q, want %q for an ENOENT naming the CLI itself",
			got.Reason, want)
	}
}

func TestIdentityReadFailure_LookPathFailureIsAMissingCLI(t *testing.T) {
	h := NewHandler(fixedPath("kiro-cli"))
	err := &exec.Error{Name: "kiro-cli", Err: exec.ErrNotFound}

	got := h.identityReadFailure(t.Context(), err, procout.NewBuffer(stderrCap), 0)

	if want := identityText(reasonCLIMissing); got.Reason != want {
		t.Errorf("Reason = %q, want %q for a PATH lookup failure", got.Reason, want)
	}
}
