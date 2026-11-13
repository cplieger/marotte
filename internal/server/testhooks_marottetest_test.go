//go:build marotte_test

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/route/routetest"
)

// probeEngine is a fakeEngine that also answers the SSE probe, so the hooks mount.
type probeEngine struct {
	fakeEngine
	armed   int
	clients int
	legacy  uint64
	v3      uint64
}

var _ sseProbe = (*probeEngine)(nil)

func (p *probeEngine) SSEClientCount() int           { return p.clients }
func (p *probeEngine) SSEConnects() (uint64, uint64) { return p.legacy, p.v3 }
func (p *probeEngine) CloseNextSSEAfter(n int)       { p.armed = n }

// runLogEngine is a probeEngine that also publishes a run's log frames, recording each run asked for.
type runLogEngine struct {
	published []string
	probeEngine
}

var _ runLogProbe = (*runLogEngine)(nil)

func (e *runLogEngine) PublishRunLog(_ context.Context, runID string) {
	e.published = append(e.published, runID)
}

func TestTestHooks_CensusReadsTheProbe(t *testing.T) {
	eng := &probeEngine{clients: 2, legacy: 1, v3: 5}
	mux := http.NewServeMux()
	(&Server{agent: eng}).registerTestHooks(routetest.On(mux))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/test/sse = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got SSEProbe
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Clients != 2 || got.LegacyConnect != 1 || got.V3Connect != 5 || got.Presence == nil {
		t.Errorf("census = %+v, want clients 2, legacy 1, v3 5, presence []", got)
	}
}

func TestTestHooks_CloseAfterArmsTheProbe(t *testing.T) {
	eng := &probeEngine{}
	mux := http.NewServeMux()
	(&Server{agent: eng}).registerTestHooks(routetest.On(mux))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test/sse/close-after", strings.NewReader(`{"after":3}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || eng.armed != 3 {
		t.Errorf("close-after = %d, armed %d; want 204 and 3", rec.Code, eng.armed)
	}

	for _, body := range []string{`{"after":0}`, `{"after":-1}`, `not json`} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/sse/close-after", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("close-after %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestTestHooks_RunLogPublishesThroughTheProbe(t *testing.T) {
	eng := &runLogEngine{}
	mux := http.NewServeMux()
	(&Server{agent: eng}).registerTestHooks(routetest.On(mux))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/sse/run-log", strings.NewReader(`{"run":"wf_ab12"}`)))
	if rec.Code != http.StatusNoContent || !slices.Equal(eng.published, []string{"wf_ab12"}) {
		t.Errorf("run-log = %d, published %q; want 204 and [wf_ab12]", rec.Code, eng.published)
	}

	// sse.ValidTopic: "run:<id>" is 1 to 128 bytes of printable ASCII, no space.
	long := strings.Repeat("a", 125)
	for _, body := range []string{`{"run":""}`, `{"run":"wf 1"}`, `{"run":"` + long + `"}`, `not json`} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/sse/run-log", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("run-log %s = %d, want 400", body, rec.Code)
		}
	}
	if len(eng.published) != 1 {
		t.Errorf("published %q after refused bodies, want only the first", eng.published)
	}

	bare := http.NewServeMux()
	(&Server{agent: &probeEngine{}}).registerTestHooks(routetest.On(bare))
	rec = httptest.NewRecorder()
	bare.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/sse/run-log", strings.NewReader(`{"run":"wf_ab12"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("run-log with no run log probe = %d, want 404", rec.Code)
	}
}

// An engine that is not the runtime mounts nothing: the hooks are the runtime's
// probe, and a server without one has no census to serve.
func TestTestHooks_NarrowEngineMountsNothing(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}}).registerTestHooks(routetest.On(mux))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/test/sse with a narrow engine = %d, want 404", rec.Code)
	}
}

// The census carries the presence table and the send filter's counters when the
// push service answers the probe: one row per tag with its verdict, the two
// transitions, and one suppressed count per registered kind.
func TestTestHooks_CensusReportsPresenceAndSuppression(t *testing.T) {
	presence := push.NewPresence()
	presence.Observe(&sse.PresenceEvent{Kind: sse.PresenceConnected, Tag: "amxAEqwvwjG23476CxNmK6"})
	svc := push.New(context.Background(), t.TempDir(), "mailto:test@example.com", push.WithPresence(presence))
	t.Cleanup(svc.Close)
	mux := http.NewServeMux()
	(&Server{agent: &probeEngine{}, push: svc}).registerTestHooks(routetest.On(mux))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/test/sse", http.NoBody))
	var got SSEProbe
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Presence) != 1 || got.Presence[0].Tag != "amxAEqwvwjG23476CxNmK6" ||
		got.Presence[0].Connected != 1 || got.Presence[0].Gone || got.Presence[0].LastAliveAt == "" {
		t.Errorf("presence = %+v, want one present row for the connected tag with its acknowledgement time", got.Presence)
	}
	if got.PresenceAlive != 1 || got.PresenceExpired != 0 {
		t.Errorf("transitions = (alive %d, expired %d), want (1, 0)", got.PresenceAlive, got.PresenceExpired)
	}
	for _, kr := range push.Kinds() {
		if _, ok := got.Suppressed[kr.Kind]; !ok {
			t.Errorf("push_suppressed_total lacks kind %q", kr.Kind)
		}
	}
}

func TestTestHooks_PreviewTokenMintsAnExpiredGrant(t *testing.T) {
	h, ph, page := previewStack(t)
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}, preview: ph}).registerTestHooks(routetest.On(mux))
	body := `{"path":"` + page + `","expires_at":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/preview-token", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/test/preview-token = %d %s, want 200", rec.Code, rec.Body)
	}
	var g marotte.PreviewGrant
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if got := serveStack(h, http.MethodGet, g.URL, ""); got.Code != http.StatusForbidden {
		t.Errorf("GET an expired grant's URL = %d, want 403", got.Code)
	}

	bare := http.NewServeMux()
	(&Server{agent: &fakeEngine{}}).registerTestHooks(routetest.On(bare))
	rec = httptest.NewRecorder()
	bare.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/preview-token", strings.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/test/preview-token with no preview wired = %d, want 404", rec.Code)
	}
}

func TestTestHooks_PreviewResolverMakesEveryRouteAnswer503(t *testing.T) {
	h, ph, page := previewStack(t)
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}, preview: ph}).registerTestHooks(routetest.On(mux))
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	grant := `{"path":"` + page + `"}`
	routes := []struct{ name, method, target, body string }{
		{"grant", http.MethodPost, "/api/preview/grant", grant},
		{"page", http.MethodGet, g.URL, ""},
		{"stamp", http.MethodGet, "/api/preview/stamp?path=" + url.QueryEscape(page), ""},
	}
	toggle := func(on string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/preview-resolver", strings.NewReader(`{"unavailable":`+on+`}`)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST /api/test/preview-resolver %s = %d, want 204", on, rec.Code)
		}
	}
	toggle("true")
	t.Cleanup(func() { toggle("false") })
	for _, r := range routes {
		if rec := serveStack(h, r.method, r.target, r.body, "Content-Type", "application/json"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with the resolver off = %d %s, want 503", r.name, rec.Code, rec.Body)
		}
	}
	toggle("false")
	for _, r := range routes {
		if rec := serveStack(h, r.method, r.target, r.body, "Content-Type", "application/json"); rec.Code != http.StatusOK {
			t.Errorf("%s with the resolver back = %d %s, want 200", r.name, rec.Code, rec.Body)
		}
	}
}

func TestTestHooks_PreviewResolverBusyExhaustsTheRetryBudget(t *testing.T) {
	h, ph, page := previewStack(t)
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}, preview: ph}).registerTestHooks(routetest.On(mux))
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	set := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/preview-resolver", strings.NewReader(body)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST /api/test/preview-resolver %s = %d, want 204", body, rec.Code)
		}
	}
	set(`{"busy":true}`)
	t.Cleanup(func() { set(`{}`) })
	for _, r := range []struct{ name, method, target, body string }{
		{"grant", http.MethodPost, "/api/preview/grant", `{"path":"` + page + `"}`},
		{"page", http.MethodGet, g.URL, ""},
	} {
		rec := serveStack(h, r.method, r.target, r.body, "Content-Type", "application/json")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "kept changing") {
			t.Errorf("%s with the resolver busy = %d %s, want 503 naming the churn", r.name, rec.Code, rec.Body)
		}
	}
	set(`{}`)
	if rec := serveStack(h, http.MethodGet, g.URL, ""); rec.Code != http.StatusOK {
		t.Errorf("page with the resolver back = %d, want 200", rec.Code)
	}
}

func TestTestHooks_PreviewGrowServesTheCheckedSize(t *testing.T) {
	h, ph, page := previewStack(t)
	mux := http.NewServeMux()
	(&Server{agent: &fakeEngine{}, preview: ph}).registerTestHooks(routetest.On(mux))
	asset := filepath.Join(filepath.Dir(page), "grow.bin")
	if err := os.WriteFile(asset, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := ph.Grant(page, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/test/preview-grow", strings.NewReader(body)))
		return rec.Code
	}
	for _, bad := range []string{`{"path":"` + asset + `","bytes":0}`, `{"path":"` + filepath.Dir(page) + `","bytes":5}`} {
		if code := post(bad); code != http.StatusBadRequest {
			t.Errorf("POST /api/test/preview-grow %s = %d, want 400", bad, code)
		}
	}
	if code := post(`{"path":"` + asset + `","bytes":5}`); code != http.StatusNoContent {
		t.Fatalf("POST /api/test/preview-grow = %d, want 204", code)
	}
	rec := serveStack(h, http.MethodGet, g.Base+"grow.bin", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Errorf("GET grow.bin = %d %q, want 200 and the 10 bytes the size check saw", rec.Code, rec.Body)
	}
	if st, err := os.Stat(asset); err != nil || st.Size() != 15 {
		t.Errorf("grow.bin on disk = %v %v, want 15 bytes (the hook grew it)", st, err)
	}
	if rec := serveStack(h, http.MethodGet, g.Base+"grow.bin", ""); rec.Body.Len() != 15 {
		t.Errorf("second GET grow.bin = %d bytes, want 15 (the growth is one-shot)", rec.Body.Len())
	}
}
