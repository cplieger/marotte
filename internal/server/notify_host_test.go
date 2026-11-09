package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/notify"
)

// scriptedDeliverer answers each session with its scripted error, or a steer.
type scriptedDeliverer map[string]error

func (d scriptedDeliverer) Deliver(_ context.Context, session, _, _ string) (string, error) {
	if err := d[session]; err != nil {
		return "", err
	}
	return notify.DeliveredSteer, nil
}

// hostGatedNotifyStack is the server's own routes and middleware behind an ALLOWED_HOSTS list
// that does not name the compose-network host.
func hostGatedNotifyStack(t *testing.T, gate notify.Gate, deliv notify.Deliverer) http.Handler {
	t.Helper()
	policy, invalid := httpserver.ParseHostList([]string{"marotte.example.com"},
		httpserver.WithLoopbackExempt(true),
		httpserver.WithHostAllowlistError("", "host not allowed. Add it to ALLOWED_HOSTS to serve this hostname"))
	if len(invalid) > 0 {
		t.Fatalf("test allowlist has invalid entries: %v", invalid)
	}
	s := docServer(t.TempDir(), WithHostPolicy(policy), WithNotifyGate(gate), WithNotifyDeliverer(deliv))
	idem := newIdempotencyCache(idempotencyTTL)
	t.Cleanup(idem.stop)
	return httpserver.Chain(s.mux(), s.middlewareStack(baseCSPPolicy, idem)...)
}

func serveOne(h http.Handler, method, host, path, auth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.RemoteAddr = "172.18.0.7:40000"
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const notifyBody = `{"text":"wake","source":"grelot","dedupe_key":"m-1"}`

func TestNotifyRoute_AdmitsTheComposeHost(t *testing.T) {
	gate, err := notify.ParseToken(confineTestToken)
	if err != nil {
		t.Fatal(err)
	}
	h := hostGatedNotifyStack(t, gate, scriptedDeliverer{})
	bearer := "Bearer " + confineTestToken
	for _, tc := range []struct {
		name, method, path, auth, body string
		want                           int
		hostRefusal                    bool
	}{
		{"the notify route delivers", http.MethodPost, "/api/sessions/s1/notify", bearer, notifyBody, http.StatusOK, false},
		{"a wrong token is still 401", http.MethodPost, "/api/sessions/s1/notify", "Bearer wrong", notifyBody, http.StatusUnauthorized, false},
		{"no token is still 401", http.MethodPost, "/api/sessions/s1/notify", "", notifyBody, http.StatusUnauthorized, false},
		{"settings keeps the host gate", http.MethodGet, "/api/settings", "", "", http.StatusForbidden, true},
		{"command keeps the host gate", http.MethodPost, "/api/command", "", "{}", http.StatusForbidden, true},
		{"the page keeps the host gate", http.MethodGet, "/", "", "", http.StatusForbidden, true},
		{"a GET on the notify path keeps the host gate", http.MethodGet, "/api/sessions/s1/notify", bearer, "", http.StatusForbidden, true},
		{"a dot-segment spelling keeps the host gate", http.MethodPost, "/api/sessions/../notify", bearer, notifyBody, http.StatusForbidden, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveOne(h, tc.method, "marotte:9847", tc.path, tc.auth, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			if got := strings.Contains(w.Body.String(), "ALLOWED_HOSTS"); got != tc.hostRefusal {
				t.Errorf("host refusal = %v, want %v (%s)", got, tc.hostRefusal, w.Body.String())
			}
		})
	}
	t.Run("an allowed host still reaches the notify route", func(t *testing.T) {
		w := serveOne(h, http.MethodPost, "marotte.example.com", "/api/sessions/s1/notify", bearer, notifyBody)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
	})
}

// TestNotifyToken_NeverInLogsOrResponses drives every answer the route gives, through the
// access log and the host gate, and looks for the token in the log and in every response.
// captureLogs swaps the process-wide logger, so this test is not parallel.
func TestNotifyToken_NeverInLogsOrResponses(t *testing.T) {
	logs := captureLogs(t)
	gate, err := notify.ParseToken(confineTestToken)
	if err != nil {
		t.Fatal(err)
	}
	h := hostGatedNotifyStack(t, gate, scriptedDeliverer{
		"gone": notify.ErrUnknownSession,
		"busy": notify.ErrUnavailable,
		"boom": io.ErrUnexpectedEOF,
	})
	off := hostGatedNotifyStack(t, notify.Gate{}, scriptedDeliverer{})
	bearer := "Bearer " + confineTestToken
	for _, tc := range []struct {
		name, method, host, path, auth, body string
		h                                    http.Handler
		want                                 int
	}{
		{"delivered", http.MethodPost, "marotte:9847", "/api/sessions/s1/notify", bearer, notifyBody, h, http.StatusOK},
		{"duplicate", http.MethodPost, "marotte:9847", "/api/sessions/s1/notify", bearer, notifyBody, h, http.StatusOK},
		{"a wrong token extending the real one", http.MethodPost, "marotte:9847", "/api/sessions/s1/notify", bearer + "x", notifyBody, h, http.StatusUnauthorized},
		{"the token on another route", http.MethodGet, "marotte.example.com", "/api/settings", bearer, "", h, http.StatusForbidden},
		{"the token on a refused host", http.MethodGet, "marotte:9847", "/api/settings", bearer, "", h, http.StatusForbidden},
		{"a bad body", http.MethodPost, "marotte:9847", "/api/sessions/s1/notify", bearer, `{"text":""}`, h, http.StatusBadRequest},
		{"an unknown session", http.MethodPost, "marotte:9847", "/api/sessions/gone/notify", bearer, notifyBody, h, http.StatusNotFound},
		{"a saturated engine", http.MethodPost, "marotte:9847", "/api/sessions/busy/notify", bearer, notifyBody, h, http.StatusServiceUnavailable},
		{"another fault", http.MethodPost, "marotte:9847", "/api/sessions/boom/notify", bearer, notifyBody, h, http.StatusInternalServerError},
		{"the door off", http.MethodPost, "marotte:9847", "/api/sessions/s1/notify", bearer, notifyBody, off, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := serveOne(tc.h, tc.method, tc.host, tc.path, tc.auth, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), confineTestToken) {
				t.Errorf("response body echoes the token")
			}
			for name, values := range w.Header() {
				for _, v := range values {
					if strings.Contains(v, confineTestToken) {
						t.Errorf("response header %s echoes the token", name)
					}
				}
			}
		})
	}
	if !strings.Contains(logs.String(), "/api/sessions/s1/notify") {
		t.Fatalf("the access log did not run, so the check proves nothing: %q", logs.String())
	}
	if strings.Contains(logs.String(), confineTestToken) {
		t.Errorf("the log carries the token: %q", logs.String())
	}
}
