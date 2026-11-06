package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/notify"
)

type steeringDeliverer struct{ sessions []string }

func (d *steeringDeliverer) Deliver(_ context.Context, session, _, _ string) (string, error) {
	d.sessions = append(d.sessions, session)
	return notify.DeliveredSteer, nil
}

// servedNotify posts a notification through the server's own route table and middleware.
func servedNotify(t *testing.T, gate notify.Gate, deliv notify.Deliverer, auth string) *httptest.ResponseRecorder {
	t.Helper()
	s := docServer(t.TempDir(), WithNotifyGate(gate), WithNotifyDeliverer(deliv))
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	h := httpserver.Chain(s.routes(), s.middlewareStack(baseCSPPolicy, idem)...)
	r := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/notify",
		strings.NewReader(`{"text":"wake","source":"grelot","dedupe_key":"m-1"}`))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNotifyRoute_IsMounted(t *testing.T) {
	gate, err := notify.ParseToken(confineTestToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("the token delivers", func(t *testing.T) {
		deliv := &steeringDeliverer{}
		w := servedNotify(t, gate, deliv, "Bearer "+confineTestToken)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"delivered":"steer"`) {
			t.Fatalf("answer = %d %s, want 200 delivered steer", w.Code, w.Body.String())
		}
		if len(deliv.sessions) != 1 || deliv.sessions[0] != "s1" {
			t.Errorf("delivered to %v, want s1", deliv.sessions)
		}
	})
	t.Run("the door off is 401, not the API fallback", func(t *testing.T) {
		w := servedNotify(t, notify.Gate{}, &steeringDeliverer{}, "Bearer "+confineTestToken)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (%s)", w.Code, w.Body.String())
		}
	})
}
