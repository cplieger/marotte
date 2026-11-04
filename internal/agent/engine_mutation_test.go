package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// answerBudgetOf shortens clientAnswerBudget for one test, which must not run in parallel.
func answerBudgetOf(t *testing.T, d time.Duration) {
	t.Helper()
	prev := clientAnswerBudget
	clientAnswerBudget = d
	t.Cleanup(func() { clientAnswerBudget = prev })
}

// holdCall parks every Call of method until the returned release runs; the test's cleanup releases it too.
func holdCall(t *testing.T, b *fakeBridge, method string) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	b.mu.Lock()
	if b.blockOn == nil {
		b.blockOn = map[string]chan struct{}{}
	}
	b.blockOn[method] = gate
	b.mu.Unlock()
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	return release
}

// answeredWithin serves on its own goroutine and returns the recorder once serve returns, failing the test
// when it has not answered within 5 s, far past the shortened answer budget.
func answeredWithin(t *testing.T, serve func(w http.ResponseWriter)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		serve(rec)
		close(done)
	}()
	select {
	case <-done:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is still waiting on the engine 5s in, want a still_applying answer at the answer budget")
		return nil
	}
}

func assertStillApplying(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusGatewayTimeout || body.Code != stillApplyingCode {
		t.Errorf("answer = %d %s, want 504 with code %q: the engine may still apply the change",
			rec.Code, rec.Body.String(), stillApplyingCode)
	}
}

// awaitEventCount waits up to 5 s for the buffer to hold want events of typ.
func awaitEventCount(t *testing.T, h *Runtime, typ marotte.EventType, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(eventsOfType(h, typ)) < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s events = %d, want %d once the engine answered", typ, len(eventsOfType(h, typ)), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAnswerEngineMutation_SettlesTheListOnlyForAChangeOrALateAnswer pins when the refetch signal goes out: after a
// change that landed, and after any answer the client was told is still applying, a failure included, since the
// client's list shows "still" until it re-reads; an on-time refusal changed nothing and the client already knows.
func TestAnswerEngineMutation_SettlesTheListOnlyForAChangeOrALateAnswer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		late        bool
		applied     bool
		wantChanged bool
	}{
		{"an on-time change", false, true, true},
		{"an on-time refusal", false, false, false},
		{"a late change", true, true, true},
		{"a late failure", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answerBudgetOf(t, 50*time.Millisecond)
			h, _, _ := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			gate := make(chan struct{})
			if !tc.late {
				close(gate)
			}
			changed := make(chan struct{}, 1)
			m := engineMutation{
				verb:    "test change",
				changed: func() { changed <- struct{}{} },
				call: func(context.Context) engineReply {
					<-gate
					rep := engineReply{write: func(w http.ResponseWriter) { w.WriteHeader(http.StatusTeapot) }, applied: tc.applied}
					if !tc.applied {
						rep.err = errors.New("refused")
					}
					return rep
				},
			}
			rec := answeredWithin(t, func(w http.ResponseWriter) {
				h.lifecycle.answerEngineMutation(w, httptest.NewRequest(http.MethodPost, "/", nil), m)
			})
			if tc.late {
				assertStillApplying(t, rec)
				close(gate)
			} else if rec.Code != http.StatusTeapot {
				t.Errorf("answer = %d, want the call's own reply", rec.Code)
			}
			joinInflight(t, h)
			if got := len(changed) == 1; got != tc.wantChanged {
				t.Errorf("changed signal sent = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}
