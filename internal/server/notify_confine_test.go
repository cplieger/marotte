package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/notify"
)

const confineTestToken = "srv-notify-token-0123456789-abcdefghij" // gitleaks:allow

func notifyStack(t *testing.T, gate notify.Gate) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/api/settings", ok)
	mux.Handle("/api/command", ok)
	mux.Handle("/api/sessions/{session}/notify", ok)
	mux.Handle("/", ok)
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	s := &Server{notifyGate: gate}
	return httpserver.Chain(mux, s.middlewareStack(baseCSPPolicy, idem)...)
}

func TestNotifyToken_ConfinedToItsRoute(t *testing.T) {
	gate, err := notify.ParseToken(confineTestToken)
	if err != nil {
		t.Fatal(err)
	}
	h := notifyStack(t, gate)
	for _, tc := range []struct {
		name, method, path, auth string
		want                     int
	}{
		{"token on settings", http.MethodGet, "/api/settings", "Bearer " + confineTestToken, http.StatusForbidden},
		{"token on command", http.MethodPost, "/api/command", "Bearer " + confineTestToken, http.StatusForbidden},
		{"token on the page", http.MethodGet, "/", "Bearer " + confineTestToken, http.StatusForbidden},
		{"token on notify", http.MethodPost, "/api/sessions/s1/notify", "Bearer " + confineTestToken, http.StatusNoContent},
		{"no token on settings", http.MethodGet, "/api/settings", "", http.StatusNoContent},
		{"wrong token on settings", http.MethodGet, "/api/settings", "Bearer nope", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, http.NoBody)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), confineTestToken) {
				t.Errorf("response echoes the token")
			}
		})
	}
}

func TestNotifyToken_OffGateConfinesNothing(t *testing.T) {
	h := notifyStack(t, notify.Gate{})
	r := httptest.NewRequest(http.MethodGet, "/api/settings", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+confineTestToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the route's own answer", w.Code)
	}
}
