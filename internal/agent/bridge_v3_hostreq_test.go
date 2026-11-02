package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/sse"
)

func TestIsSafeExternalURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://auth.example.com/oauth?code=1", true},
		{"http://localhost:8080/callback", true},
		{"HTTPS://Example.com", true},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"ftp://example.com", false},
		{"", false},
		{"not a url", false},
		{"//example.com", false},
	}
	for _, tt := range tests {
		if got := isSafeExternalURL(tt.url); got != tt.want {
			t.Errorf("isSafeExternalURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func openExternalURLMsg(t *testing.T, id int64, url string) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{
		Method: methodKiroOpenExternalURL,
		ID:     &id,
		Params: mustJSON(t, map[string]any{"url": url}),
	}
}

// connectPage holds one live SSE connection, a page that can show a URL, until the test ends.
func connectPage(t *testing.T, h *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
		req.Header.Set(wireHeader, "1")
		h.handleSSE(httptest.NewRecorder(), req)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	deadline := time.Now().Add(5 * time.Second)
	for h.bus.fanout.ClientCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the page's SSE connection never subscribed")
		}
		time.Sleep(time.Millisecond)
	}
}

// An unclaimed URL is shown on every page and anchored for the relay by its state.
func TestEngineLane_OpenExternalURLWithAPageConnectedIsShownEverywhere(t *testing.T) {
	const consent = "https://auth.example.com/authorize?state=s1&redirect_uri=http%3A%2F%2F127.0.0.1%3A4000%2Foauth%2Fcallback"
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 12, consent))

	answer := awaitLaneAnswer(t, lane, 12)
	joinInflight(t, h)
	if answer.err != nil {
		t.Fatalf("answer error = %v, want success so KAS keeps its listener waiting", answer.err)
	}
	var shown []sse.ReplayEvent
	for _, e := range bufferedSince(h, before) {
		if strings.Contains(string(e.Event.Data), `"type":"`+string(marotte.EventOpenExternalURL)+`"`) {
			shown = append(shown, e)
		}
	}
	if len(shown) != 1 {
		t.Fatalf("open_external_url events = %d, want 1", len(shown))
	}
	if shown[0].Event.Topic != "" || !strings.Contains(string(shown[0].Event.Data), `"url":`) {
		t.Errorf("event = topic %q data %s, want every page's topic carrying the URL", shown[0].Event.Topic, shown[0].Event.Data)
	}
	h.mcpRegistry.mu.RLock()
	anchors := slices.Clone(h.mcpRegistry.lazySignIns)
	h.mcpRegistry.mu.RUnlock()
	if len(anchors) != 1 || anchors[0].state != "s1" || anchors[0].authURL != consent {
		t.Fatalf("anchors = %+v, want one on the URL keyed by its state s1", anchors)
	}
	var evt struct {
		Payload marotte.OpenExternalURLPayload `json:"payload"`
	}
	if err := json.Unmarshal(shown[0].Event.Data, &evt); err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	// A page replaying the event later must see when KAS stopped waiting, not a fresh 300 s.
	if !evt.Payload.ExpiresAt.Equal(anchors[0].deadline) {
		t.Errorf("expires_at = %v, want the anchor's deadline %v", evt.Payload.ExpiresAt, anchors[0].deadline)
	}
}

// With no page connected nobody can open the URL, so KAS is told at once rather than waiting 300 s.
func TestEngineLane_OpenExternalURLWithNoPageConnectedIsRefused(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 11, "https://auth.example.com/authorize?state=s1"))

	answer := awaitLaneAnswer(t, lane, 11)
	// The answer runs on inflight and may broadcast after it responds, so the buffer is read only once it returns.
	joinInflight(t, h)
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](answer.err)
	if !isRPC || rpcErr.Code != -32000 {
		t.Errorf("answer = (result %v, error %v), want RPC error -32000 so KAS ends the attempt at once", answer.result, answer.err)
	}
	for _, ty := range extractTypes(t, bufferedSince(h, before)) {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("a URL no page can show was broadcast as open_external_url")
		}
	}
	h.mcpRegistry.mu.RLock()
	n := len(h.mcpRegistry.lazySignIns)
	h.mcpRegistry.mu.RUnlock()
	if n != 0 {
		t.Errorf("anchors = %d, want none for a refused URL", n)
	}
}

func TestEngineLane_OpenExternalURLUnsafeSchemeIsRefusedWithInvalidParams(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	connectPage(t, h)
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 9, "javascript:alert(1)"))

	answer := awaitLaneAnswer(t, lane, 9)
	joinInflight(t, h)
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](answer.err)
	if !isRPC || rpcErr.Code != -32602 {
		t.Errorf("answer = (result %v, error %v), want RPC error -32602", answer.result, answer.err)
	}
	for _, ty := range extractTypes(t, bufferedSince(h, before)) {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("an unsafe URL was broadcast as open_external_url")
		}
	}
}

func TestHandleKiroClientRequest_DoesNotClaimGetAccessToken(t *testing.T) {
	h, _, _ := newTestHub()
	id := int64(3)
	tests := map[string]struct {
		msg  *marotte.RPCResponse
		want bool
	}{
		"relay_owned_auth": {
			msg:  &marotte.RPCResponse{ID: &id, Method: "_kiro/auth/get" + "AccessToken"},
			want: false,
		},
		"shell_type": {
			msg:  &marotte.RPCResponse{ID: &id, Method: methodKiroShellType},
			want: true,
		},
		"open_external_url_is_the_engine_lanes": {
			msg:  openExternalURLMsg(t, id, "https://x.example"),
			want: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := h.inbound.handleKiroClientRequest(t.Context(), "c1", h.originOf("c1"), tc.msg); got != tc.want {
				t.Errorf("handleKiroClientRequest(%q) = %v, want %v", tc.msg.Method, got, tc.want)
			}
		})
	}
}
