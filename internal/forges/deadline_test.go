package forges

// A sign-in takes one deadline at its route's entry: the forge-facing half ends
// durableReserve before it and the durable half at it, so every answer reaches
// the browser before the browser stops waiting.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testTotal   = 1000 * time.Millisecond
	testReserve = 200 * time.Millisecond
	// testSlack is how late past the deadline an answer may be written on a
	// loaded machine.
	testSlack = 150 * time.Millisecond
)

// shortDeadlines is a harness whose mutations get testTotal, the forge-facing
// half ending testReserve before it.
func shortDeadlines(t *testing.T, onUser func()) (*connectHarness, *grantEndpoint) {
	t.Helper()
	e := newGrantEndpoint(t, "900", approvedAnswer)
	e.mu.Lock()
	e.onUser = onUser
	e.mu.Unlock()
	h := newConnectHarness(t, nil)
	h.m.deadlines = mutationDeadlines{total: testTotal, reserve: testReserve}
	return h, e
}

func (h *connectHarness) loginAt(t *testing.T, ctx context.Context, e *grantEndpoint) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	body := fmt.Sprintf(`{"kind":"github","host":%q,"token":"ghp","web_base_url":%q,"plaintext_http":true,"private_addresses":true}`,
		e.host(), e.srv.URL)
	start := time.Now()
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, loginPath, strings.NewReader(body)))
	return rec, time.Since(start)
}

func sleepFor(d time.Duration) func() { return func() { time.Sleep(d) } }

func TestSignIn_OneDeadlineCoversVerifyLockAndSave(t *testing.T) {
	t.Run("a slow verify inside the window", func(t *testing.T) {
		h, e := shortDeadlines(t, sleepFor(250*time.Millisecond))
		rec, took := h.loginAt(t, t.Context(), e)
		var got LoginResult
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.ForgeID != e.id() {
			t.Errorf("sign-in = %d %s, want 200 naming %s", rec.Code, rec.Body, e.id())
		}
		if took > testTotal {
			t.Errorf("the answer took %v, want it before the %v deadline", took, testTotal)
		}
	})
	t.Run("a verify past the window", func(t *testing.T) {
		h, e := shortDeadlines(t, sleepFor(testTotal))
		rec, took := h.loginAt(t, t.Context(), e)
		if body := decodeBody(t, rec); rec.Code != http.StatusGatewayTimeout || body["code"] != codeSignInTimedOut {
			t.Errorf("sign-in = %d %v, want 504 %s", rec.Code, body, codeSignInTimedOut)
		}
		if took > testTotal+testSlack {
			t.Errorf("the answer took %v, want it by the %v deadline", took, testTotal)
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a durable half past the deadline", func(t *testing.T) {
		h, e := shortDeadlines(t, sleepFor(250*time.Millisecond))
		h.m.records.writeFile = func(ctx context.Context, _ string, _ []byte) error {
			<-ctx.Done()
			return ctx.Err()
		}
		rec, took := h.loginAt(t, t.Context(), e)
		if body := decodeBody(t, rec); rec.Code != http.StatusGatewayTimeout || body["code"] != codeSignInTimedOut {
			t.Errorf("sign-in = %d %v, want 504 %s", rec.Code, body, codeSignInTimedOut)
		}
		if took > testTotal+testSlack {
			t.Errorf("the answer took %v, want it by the %v deadline the route took at its entry", took, testTotal)
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a lock held past the window", func(t *testing.T) {
		h, e := shortDeadlines(t, nil)
		unlock, err := h.m.locks.lock(t.Context(), e.id())
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		rec, took := h.loginAt(t, t.Context(), e)
		if body := decodeBody(t, rec); rec.Code != http.StatusConflict || body["code"] != codeConnectionBusy {
			t.Errorf("sign-in behind a held lock = %d %v, want 409 %s", rec.Code, body, codeConnectionBusy)
		}
		if took > testTotal {
			t.Errorf("the answer took %v, want it before the %v deadline", took, testTotal)
		}
		h.assertNothingStored(t, e.srv.URL)
	})
	t.Run("a token sign-in the browser leaves mid-verify", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		h, e := shortDeadlines(t, cancel)
		h.loginAt(t, ctx, e)
		h.assertNothingStored(t, e.srv.URL)
	})

	t.Run("a device grant answering inside the window", func(t *testing.T) {
		h, e := shortDeadlines(t, nil)
		grant := h.startGrant(t, e)
		_, release := e.holdFirstToken(t)
		go func() {
			time.Sleep(250 * time.Millisecond)
			release()
		}()
		start := time.Now()
		body := decodeBody(t, h.pollGrant(t, t.Context(), grant))
		if body["status"] != stateComplete || body["forge_id"] != e.id() || time.Since(start) > testTotal {
			t.Errorf("poll = %v after %v, want complete naming %s before the deadline", body, time.Since(start), e.id())
		}
	})
	t.Run("a device sign-in past the window", func(t *testing.T) {
		h, e := shortDeadlines(t, sleepFor(testTotal))
		grant := h.startGrant(t, e)
		_, release := e.holdFirstToken(t)
		go func() {
			// Longer than testReserve, so a deadline taken after this answer would end its
			// forge-facing window past testTotal; still well inside the window on a loaded machine.
			time.Sleep(2 * testReserve)
			release()
		}()
		start := time.Now()
		body := decodeBody(t, h.pollGrant(t, t.Context(), grant))
		if body["status"] != statusError || body["code"] != codeSignInTimedOut || time.Since(start) > testTotal {
			t.Errorf("poll = %v after %v, want error %s by the deadline taken before the grant's answer",
				body, time.Since(start), codeSignInTimedOut)
		}
	})
	t.Run("a device sign-in the browser leaves mid-verify", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		h, e := shortDeadlines(t, nil)
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
