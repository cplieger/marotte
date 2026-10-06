package push

// Shared test infrastructure for the push package: a few small builders
// and an always-erroring RoundTripper. Log assertions ride slogx/capture
// directly (see the note above errRoundTripper).

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

const testSubject = "mailto:test@example.com"

// Log-line assertions go through slogx/capture's attr helpers (AttrValue /
// HasAttr): several push paths (Subscribe, loadSubs, Send) write only a
// local variable straight to slog with no return value or exported state,
// so the log line is the sole observable distinguishing correct from broken
// behaviour.

// errRoundTripper always fails, so push() can reach its post-encryption
// HTTP body-assembly code without any real network.
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("forced transport error")
}

// newServiceOnTestServer builds a Service whose client is httptest.NewTestServer's in-memory one
// (Go 1.27), so a Send reaches h instead of dialing through ssrf.SafeTransport.
func newServiceOnTestServer(t *testing.T, h http.Handler) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTestServer(t, h)
	client := srv.Client()
	// t.TempDir() first so its removal is registered BEFORE s.Close and
	// therefore runs after it (cleanups are LIFO): the write loop has to drain
	// before the directory it writes into disappears.
	//
	// context.Background(), not t.Context(), for the same reason
	// newRoutedService states: the service lifetime has to outlive the
	// t.Cleanup(s.Close) teardown, and t.Context() is already cancelled by the
	// time cleanup funcs run. The Cleanup is what guarantees Close.
	s := New(context.Background(), t.TempDir(), testSubject)
	t.Cleanup(s.Close)
	s.client = client
	return s, srv
}

// recordingHandler answers every push with status and records how many arrived
// and what the last one addressed, so a test can assert the OUTBOUND target
// rather than only the local side effect.
type recordingHandler struct {
	mu       sync.Mutex
	requests []recordedPush
	status   int
}

// recordedPush is the assertable part of one delivery attempt.
type recordedPush struct {
	host            string
	path            string
	contentEncoding string
	authorization   string
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, recordedPush{
		host:            r.Host,
		path:            r.URL.Path,
		contentEncoding: r.Header.Get("Content-Encoding"),
		authorization:   r.Header.Get("Authorization"),
	})
	status := h.status
	h.mu.Unlock()
	if status == 0 {
		status = http.StatusCreated
	}
	w.WriteHeader(status)
}

// snapshot returns a copy of the recorded deliveries.
func (h *recordingHandler) snapshot() []recordedPush {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.requests)
}

// pushSubscriptionWithValidKeys builds a subscription whose P256dh +
// Auth survive push()'s decode/import steps so the HTTP request
// actually fires against the test server.
func pushSubscriptionWithValidKeys(t *testing.T, endpoint string) marotte.PushSubscription {
	t.Helper()
	clientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen client key: %v", err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	sub := marotte.PushSubscription{Endpoint: endpoint}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)
	return sub
}
