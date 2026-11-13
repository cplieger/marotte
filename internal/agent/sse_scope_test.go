package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/go/sse/v2/ssetest"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route/routetest"
)

// The scope contract is sse's docs/wire.md "Scopes": a connection receives broadcasts (topic "") plus the
// topics in its SSE-Topics scope, an absent header is everything, and POST /api/events/scope replaces a live
// connection's scope. These pin marotte's wiring of it, through the routes a browser calls.

// scopeServer serves the runtime's own routes, so a test reaches /api/events and /api/events/scope as a tab does.
func scopeServer(t *testing.T) (*Runtime, *httptest.Server) {
	t.Helper()
	h, _, _ := newTestHub()
	mux := http.NewServeMux()
	h.RegisterRoutes(routetest.On(mux))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

// liveStream is one open /api/events connection, read past its connect frames.
type liveStream struct {
	frames *ssetest.FrameReader
	conn   string
}

// openStream connects with topics as the SSE-Topics header, or with none when topics is "", and returns once
// the connect hook's last frame arrived, so the connection is subscribed before the test publishes.
func openStream(t *testing.T, srv *httptest.Server, topics string) *liveStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(wireHeader, "1")
	if topics != "" {
		req.Header.Set("SSE-Topics", topics)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/events (SSE-Topics %s): %v", topics, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/events (SSE-Topics %s) = %d, want 200", topics, resp.StatusCode)
	}
	s := &liveStream{frames: ssetest.NewFrameReader(resp.Body)}
	for {
		f := s.next(t)
		if f.Event == "sse:hello" {
			var hello sse.Hello
			if err := json.Unmarshal([]byte(f.Data), &hello); err != nil {
				t.Fatalf("decode hello %q: %v", f.Data, err)
			}
			s.conn = hello.Conn
			continue
		}
		if frameType(t, f) == marotte.EventStatusSnapshot {
			return s
		}
	}
}

func (s *liveStream) next(t *testing.T) ssetest.Frame {
	t.Helper()
	got, err := s.frames.Read(1)
	if err != nil || len(got) != 1 {
		t.Fatalf("read a frame: %v (%d frames)", err, len(got))
	}
	return got[0]
}

// runsBefore returns the workflow_id of every run frame read before the first frame of type stop.
func (s *liveStream) runsBefore(t *testing.T, stop marotte.EventType) []string {
	t.Helper()
	var runs []string
	for {
		f := s.next(t)
		if f.Event != "" {
			continue // the retry field and keepalives
		}
		typ := frameType(t, f)
		if typ == stop {
			return runs
		}
		var evt struct {
			Payload struct {
				WorkflowID string `json:"workflow_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(f.Data), &evt); err != nil {
			t.Fatalf("decode %q: %v", f.Data, err)
		}
		runs = append(runs, string(typ)+" "+evt.Payload.WorkflowID)
	}
}

func frameType(t *testing.T, f ssetest.Frame) marotte.EventType {
	t.Helper()
	if f.Event != "" {
		return marotte.EventType(f.Event)
	}
	var evt struct {
		Type marotte.EventType `json:"type"`
	}
	if err := json.Unmarshal([]byte(f.Data), &evt); err != nil {
		t.Fatalf("decode %q: %v", f.Data, err)
	}
	return evt.Type
}

// publishRunsThenChat emits one entry_delta per run, on its run topic, then a chat frame on "".
func publishRunsThenChat(h *Runtime, runs ...string) {
	for _, run := range runs {
		h.bus.emit(marotte.ServerEvent{
			Type: marotte.EventEntryDelta, Topic: marotte.RunTopic(run),
			Payload: map[string]string{"workflow_id": run},
		})
	}
	h.bus.emit(marotte.ServerEvent{Type: marotte.EventChatUpdated, ChatID: "c1"})
}

func TestHandleSSE_EachScopeReceivesItsOwnRunsAndEveryBroadcast(t *testing.T) {
	h, srv := scopeServer(t)
	streams := map[string]*liveStream{
		`["run:wf_a"]`: openStream(t, srv, `["run:wf_a"]`),
		`["run:wf_b"]`: openStream(t, srv, `["run:wf_b"]`),
		`[]`:           openStream(t, srv, `[]`),
		"no header":    openStream(t, srv, ""),
	}
	publishRunsThenChat(h, "wf_a", "wf_b")

	want := map[string][]string{
		`["run:wf_a"]`: {"entry_delta wf_a"},
		`["run:wf_b"]`: {"entry_delta wf_b"},
		`[]`:           nil,
		"no header":    {"entry_delta wf_a", "entry_delta wf_b"},
	}
	for scope, s := range streams {
		if got := s.runsBefore(t, marotte.EventChatUpdated); !slices.Equal(got, want[scope]) {
			t.Errorf("scope %s received %q before the chat frame, want %q", scope, got, want[scope])
		}
	}
}

func TestHandleSSE_AScopeOutsideTheTopicGrammarIs400(t *testing.T) {
	h, _, _ := newTestHub()
	for name, topics := range map[string]string{
		"not JSON":        `["run:wf_a"`,
		"a space":         `["run wf_a"]`,
		"an empty topic":  `[""]`,
		"129-byte topic":  `["run:` + strings.Repeat("a", 125) + `"]`,
		"not an array":    `{"topics":[]}`,
		"a quote escaped": `["run:\"a"]`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(hookOnlyContext(t))
			req.Header.Set(wireHeader, "1")
			req.Header.Set("SSE-Topics", topics)
			rec := httptest.NewRecorder()
			h.handleSSE(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"sse_scope"`) {
				t.Errorf("SSE-Topics %s = %d %s, want 400 sse_scope", topics, rec.Code, rec.Body.String())
			}
			if legacy, v3 := h.SSEConnects(); legacy != 0 || v3 != 0 {
				t.Errorf("a refused connect counted as (legacy %d, v3 %d) connects, want none", legacy, v3)
			}
		})
	}
}

// postScope sends the scope request @cplieger/sse sends: POST application/json {"conn","seq","topics"}.
func postScope(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/events/scope", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

func scopeBody(conn string, seq int, topics string) string {
	return fmt.Sprintf(`{"conn":%q,"seq":%d,"topics":%s}`, conn, seq, topics)
}

func TestScopeRoute_ReplacesTheScopeOfALiveConnection(t *testing.T) {
	h, srv := scopeServer(t)
	s := openStream(t, srv, `[]`)
	if s.conn == "" {
		t.Fatal("the hello of a request-scoped connection names no conn")
	}

	code, body := postScope(t, srv, scopeBody(s.conn, 1, `["run:wf_a"]`))
	if code != http.StatusOK || !strings.Contains(body, `"epoch"`) || !strings.Contains(body, `"offset"`) {
		t.Fatalf("POST /api/events/scope = %d %s, want 200 {epoch, offset}", code, body)
	}
	publishRunsThenChat(h, "wf_a", "wf_b")
	if got, want := s.runsBefore(t, marotte.EventChatUpdated), []string{"entry_delta wf_a"}; !slices.Equal(got, want) {
		t.Errorf("after the rescope the connection received %q, want %q", got, want)
	}
}

func TestScopeRoute_AnswersTheWireContractsRefusals(t *testing.T) {
	_, srv := scopeServer(t)
	s := openStream(t, srv, `[]`)

	t.Run("an unknown conn is 404 scope_conn", func(t *testing.T) {
		code, body := postScope(t, srv, scopeBody("AAAAAAAAAAAAAAAAAAAAAA", 1, `[]`))
		if code != http.StatusNotFound || !strings.Contains(body, `"code":"scope_conn"`) {
			t.Errorf("= %d %s, want 404 scope_conn", code, body)
		}
	})
	t.Run("a topic outside the grammar is 400 sse_scope", func(t *testing.T) {
		code, body := postScope(t, srv, scopeBody(s.conn, 1, `["run wf_a"]`))
		if code != http.StatusBadRequest || !strings.Contains(body, `"code":"sse_scope"`) {
			t.Errorf("= %d %s, want 400 sse_scope", code, body)
		}
	})
	t.Run("a GET is 405 Allow POST, not a missing route", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/api/events/scope")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
			t.Errorf("GET = %d Allow %q, want 405 Allow POST", resp.StatusCode, resp.Header.Get("Allow"))
		}
	})
}

// A connection whose owner is not the caller is refused 403, so the scope route can only narrow the caller's
// own connections.
func TestScopeRoute_RefusesAConnectionAnotherPrincipalOwns(t *testing.T) {
	h, srv := scopeServer(t)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.bus.fanout.Serve(w, r, sse.WithRequestScope("another principal"))
	}))
	t.Cleanup(foreign.Close)
	s := openForeignStream(t, foreign)

	code, body := postScope(t, srv, scopeBody(s, 1, `[]`))
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"scope_forbidden"`) {
		t.Errorf("POST for another principal's connection = %d %s, want 403 scope_forbidden", code, body)
	}
}

// openForeignStream connects to a bare hub server and returns its conn.
func openForeignStream(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	frames, err := ssetest.NewFrameReader(resp.Body).Read(2)
	if err != nil || frames[1].Event != "sse:hello" {
		t.Fatalf("read the hello: %v %+v", err, frames)
	}
	var hello sse.Hello
	if err := json.Unmarshal([]byte(frames[1].Data), &hello); err != nil || hello.Conn == "" {
		t.Fatalf("hello %q names no conn: %v", frames[1].Data, err)
	}
	return hello.Conn
}

// The drain gate refuses new streams, but a live one may still narrow: a late scope request changes nothing
// a shutdown cares about.
func TestScopeRoute_AnswersWhileDraining(t *testing.T) {
	h, srv := scopeServer(t)
	s := openStream(t, srv, `[]`)
	h.lifecycle.draining.Store(true)
	t.Cleanup(func() { h.lifecycle.draining.Store(false) })

	if code, body := postScope(t, srv, scopeBody(s.conn, 1, `["run:wf_a"]`)); code != http.StatusOK {
		t.Errorf("POST /api/events/scope while draining = %d %s, want 200", code, body)
	}
}
