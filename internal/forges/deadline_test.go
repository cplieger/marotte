package forges

// A sign-in takes one deadline at its route's entry: the forge-facing half ends
// durableReserve before it and the durable half at it, so every answer reaches
// the browser before the browser stops waiting. A stage case reads the deadline a
// stage's context carried and bounds the instant it was taken by two the test
// saw (the request's start and the first stage's arrival), never by how late
// the answer came; the lock case proves only that the answer arrives before the
// durable half's deadline.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testTotal   = 1000 * time.Millisecond
	testReserve = 200 * time.Millisecond
)

var (
	// shortWindow is the pair a case past the window waits out.
	shortWindow = mutationDeadlines{total: testTotal, reserve: testReserve}
	// roomyWindow keeps an answering case's stages far inside its forge-facing half and
	// under forgeapi's 20 s operation bound, so the identity read carries the route's
	// deadline rather than the library's.
	roomyWindow = mutationDeadlines{total: 10 * time.Second, reserve: 2 * time.Second}
	// lockWindow ends the forge-facing half at testTotal-testReserve and the durable one
	// seconds later, so a lock wait on the wrong context answers late by seconds.
	lockWindow = mutationDeadlines{total: 10 * time.Second, reserve: 10*time.Second - (testTotal - testReserve)}
)

// deadlineHarness is a harness whose mutations take d, its forge API answered by
// wire (nil for the grant endpoint itself).
func deadlineHarness(t *testing.T, d mutationDeadlines, wire http.RoundTripper) (*connectHarness, *grantEndpoint) {
	t.Helper()
	e := newGrantEndpoint(t, "900", approvedAnswer)
	h := newConnectHarness(t, wire)
	h.m.deadlines = d
	return h, e
}

// loginAt posts a token sign-in for e and answers the instant just before the
// route ran, which no deadline it took can precede.
func (h *connectHarness) loginAt(t *testing.T, ctx context.Context, e *grantEndpoint) (*httptest.ResponseRecorder, time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"kind":"github","host":%q,"token":"ghp","web_base_url":%q,"plaintext_http":true,"private_addresses":true}`,
		e.host(), e.srv.URL)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, loginPath, strings.NewReader(body)))
	return rec, start
}

// stageWire answers the identity read in memory and records when it arrived and
// the deadline its context carried. hold keeps the read until that context ends,
// a forge that never answers; a read with no deadline is refused, not held.
type stageWire struct {
	arrived  time.Time
	deadline time.Time
	mu       sync.Mutex
	hold     bool
}

func (w *stageWire) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, ok := r.Context().Deadline()
	w.mu.Lock()
	w.arrived, w.deadline = time.Now(), deadline
	w.mu.Unlock()
	switch {
	case !ok:
		return nil, errors.New("stageWire: the identity read carries no deadline")
	case w.hold:
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return aliceWire().RoundTrip(r)
}

func (w *stageWire) read() (arrived, deadline time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.arrived, w.deadline
}

// recordWrites records the deadline the first records write's context carried;
// hold keeps that write until its context ends, a disk that never answers.
func recordWrites(h *connectHarness, hold bool) (deadline func() time.Time) {
	var mu sync.Mutex
	var first time.Time
	write := h.m.records.writeFile
	h.m.records.writeFile = func(ctx context.Context, path string, data []byte) error {
		d, _ := ctx.Deadline()
		mu.Lock()
		if first.IsZero() {
			first = d
		}
		mu.Unlock()
		if hold {
			<-ctx.Done()
			return ctx.Err()
		}
		return write(ctx, path, data)
	}
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return first
	}
}

// takenBetween reports whether deadline is window after an instant in [from, to].
func takenBetween(deadline time.Time, window time.Duration, from, to time.Time) bool {
	at := deadline.Add(-window)
	return !at.Before(from) && !at.After(to)
}

// releaseHeldToken releases e's held token request once it arrives, answering the
// instant the test saw it arrive.
func releaseHeldToken(t *testing.T, e *grantEndpoint) <-chan time.Time {
	t.Helper()
	held, release := e.holdFirstToken(t)
	at := make(chan time.Time, 1)
	go func() {
		<-held
		at <- time.Now()
		release()
	}()
	return at
}

func TestSignIn_OneDeadlineCoversVerifyLockAndSave(t *testing.T) {
	t.Run("a verify inside the window saves under the entry's deadlines", func(t *testing.T) {
		d, w := roomyWindow, &stageWire{}
		h, e := deadlineHarness(t, d, w)
		saved := recordWrites(h, false)
		rec, start := h.loginAt(t, t.Context(), e)
		var got LoginResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ForgeID != e.id() {
			t.Errorf("sign-in = %d %s, want 200 naming %s", rec.Code, rec.Body, e.id())
		}
		arrived, verify := w.read()
		if !takenBetween(verify, d.total-d.reserve, start, arrived) {
			t.Errorf("verify's deadline %v, want %v after an instant from the route's entry (%v) to the read (%v)",
				verify, d.total-d.reserve, start, arrived)
		}
		if save := saved(); save.Sub(verify) != d.reserve {
			t.Errorf("the records write's deadline is %v past verify's, want %v: one deadline for both halves",
				save.Sub(verify), d.reserve)
		}
	})
	t.Run("a verify past the window", func(t *testing.T) {
		d, w := shortWindow, &stageWire{hold: true}
		h, e := deadlineHarness(t, d, w)
		rec, start := h.loginAt(t, t.Context(), e)
		if body := decodeBody(t, rec); rec.Code != http.StatusGatewayTimeout || body["code"] != codeSignInTimedOut {
			t.Errorf("sign-in = %d %v, want 504 %s", rec.Code, body, codeSignInTimedOut)
		}
		if arrived, verify := w.read(); !takenBetween(verify, d.total-d.reserve, start, arrived) {
			t.Errorf("verify ended at %v, want %v after an instant from the route's entry (%v) to the read (%v)",
				verify, d.total-d.reserve, start, arrived)
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a durable half past the deadline", func(t *testing.T) {
		d, w := shortWindow, &stageWire{}
		h, e := deadlineHarness(t, d, w)
		saved := recordWrites(h, true)
		rec, start := h.loginAt(t, t.Context(), e)
		if body := decodeBody(t, rec); rec.Code != http.StatusGatewayTimeout || body["code"] != codeSignInTimedOut {
			t.Errorf("sign-in = %d %v, want 504 %s", rec.Code, body, codeSignInTimedOut)
		}
		if arrived, _ := w.read(); !takenBetween(saved(), d.total, start, arrived) {
			t.Errorf("the records write ended at %v, want %v after an instant from the route's entry (%v) to verify's read (%v)",
				saved(), d.total, start, arrived)
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a lock held past the window", func(t *testing.T) {
		d, w := lockWindow, &stageWire{}
		h, e := deadlineHarness(t, d, w)
		unlock, err := h.m.locks.Lock(t.Context(), e.id())
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		rec, _ := h.loginAt(t, t.Context(), e)
		answered := time.Now()
		if body := decodeBody(t, rec); rec.Code != http.StatusConflict || body["code"] != codeConnectionBusy {
			t.Errorf("sign-in behind a held lock = %d %v, want 409 %s", rec.Code, body, codeConnectionBusy)
		}
		// verify's deadline is the lock's too; the durable half's comes d.reserve later.
		if _, verify := w.read(); !answered.Before(verify.Add(d.reserve)) {
			t.Errorf("the answer came at %v, want it before the durable half's deadline %v",
				answered, verify.Add(d.reserve))
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a token sign-in the browser leaves mid-verify", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		h, e := deadlineHarness(t, roomyWindow, nil)
		e.mu.Lock()
		e.onUser = cancel
		e.mu.Unlock()
		h.loginAt(t, ctx, e)
		h.assertNothingStored(t, e.srv.URL)
	})

	t.Run("a device grant answering inside the window", func(t *testing.T) {
		d, w := roomyWindow, &stageWire{}
		h, e := deadlineHarness(t, d, w)
		saved := recordWrites(h, false)
		grant := h.startGrant(t, e)
		heldAt := releaseHeldToken(t, e)
		start := time.Now()
		body := decodeBody(t, h.pollGrant(t, t.Context(), grant))
		if body["status"] != stateComplete || body["forge_id"] != e.id() {
			t.Errorf("poll = %v, want complete naming %s", body, e.id())
		}
		_, verify := w.read()
		if at := <-heldAt; !takenBetween(verify, d.total-d.reserve, start, at) {
			t.Errorf("verify's deadline %v, want %v after an instant from the poll's entry (%v) to its token request (%v)",
				verify, d.total-d.reserve, start, at)
		}
		if save := saved(); save.Sub(verify) != d.reserve {
			t.Errorf("the records write's deadline is %v past verify's, want %v", save.Sub(verify), d.reserve)
		}
	})
	t.Run("a device sign-in past the window", func(t *testing.T) {
		d, w := shortWindow, &stageWire{hold: true}
		h, e := deadlineHarness(t, d, w)
		grant := h.startGrant(t, e)
		heldAt := releaseHeldToken(t, e)
		start := time.Now()
		body := decodeBody(t, h.pollGrant(t, t.Context(), grant))
		if body["status"] != statusError || body["code"] != codeSignInTimedOut {
			t.Errorf("poll = %v, want error %s", body, codeSignInTimedOut)
		}
		_, verify := w.read()
		if at := <-heldAt; !takenBetween(verify, d.total-d.reserve, start, at) {
			t.Errorf("verify ended at %v, want %v after an instant from the poll's entry (%v) to its token request (%v): the deadline taken before the grant's answer",
				verify, d.total-d.reserve, start, at)
		}
	})
	t.Run("a device sign-in the browser leaves mid-verify", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		h, e := deadlineHarness(t, roomyWindow, nil)
		changes := &changeLog{}
		h.m.notify = changes.notify
		grant := h.startGrant(t, e)
		e.mu.Lock()
		e.onUser = cancel
		e.mu.Unlock()
		h.pollGrant(t, ctx, grant)
		if _, ok := h.m.view(e.id()); !ok {
			t.Error("the approved account is not listed, want it saved although the browser left")
		}
		if got := changes.all(); len(got) != 1 || !got[0].AccountChanged {
			t.Errorf("notified %+v, want the sign-in announced", got)
		}
	})
}
