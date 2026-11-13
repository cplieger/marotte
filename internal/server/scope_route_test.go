package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/go/sse/v2/ssetest"
	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/route/routetest"
)

// scopeStack serves the agent's real event routes behind the production middleware stack, so the scope POST a
// tab's stream sends meets the host, CSRF and Fetch Metadata gates every other API write meets.
func scopeStack(t *testing.T) *httptest.Server {
	t.Helper()
	chats, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	rt := agent.New(t.Context(), t.TempDir(), nil, chats)
	mux := http.NewServeMux()
	rt.RegisterRoutes(routetest.On(mux))
	idem := newIdempotencyCache(idempotencyTTL)
	s := &Server{}
	srv := httptest.NewServer(httpserver.Chain(mux, s.middlewareStack(baseCSPPolicy, idem)...))
	t.Cleanup(func() {
		srv.Close()
		idem.stop()
		if err := rt.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	return srv
}

// connOf opens /api/events as a same-origin browser fetch and returns the conn its hello names.
func connOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("SSE-Wire", "1")
	req.Header.Set("SSE-Topics", `[]`)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	frames, err := ssetest.NewFrameReader(resp.Body).Read(2)
	if err != nil || frames[1].Event != "sse:hello" {
		t.Fatalf("GET /api/events = %d, frames %+v (%v), want the retry field then the hello", resp.StatusCode, frames, err)
	}
	var hello sse.Hello
	if err := json.Unmarshal([]byte(frames[1].Data), &hello); err != nil || hello.Conn == "" {
		t.Fatalf("hello %q names no conn: %v", frames[1].Data, err)
	}
	return hello.Conn
}

func postScopeAs(t *testing.T, srv *httptest.Server, fetchSite, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/events/scope", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", fetchSite)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/events/scope: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		t.Fatalf("read the scope answer: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func TestScopeRoute_ThroughTheMiddlewareStack(t *testing.T) {
	srv := scopeStack(t)
	conn := connOf(t, srv)
	body := fmt.Sprintf(`{"conn":%q,"seq":1,"topics":["run:wf_a"]}`, conn)

	t.Run("a same-origin request is served", func(t *testing.T) {
		if code, got := postScopeAs(t, srv, "same-origin", body); code != http.StatusOK || !strings.Contains(got, `"offset"`) {
			t.Errorf("= %d %s, want 200 {epoch, offset}", code, got)
		}
	})
	t.Run("a cross-site request is refused before the handler", func(t *testing.T) {
		code, got := postScopeAs(t, srv, "cross-site", fmt.Sprintf(`{"conn":%q,"seq":2,"topics":[]}`, conn))
		if code != http.StatusForbidden || strings.Contains(got, "scope_forbidden") {
			t.Errorf("= %d %s, want the stack's 403, not the handler's", code, got)
		}
	})
}
