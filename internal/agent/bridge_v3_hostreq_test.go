package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
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

func TestHandleOpenExternalURL(t *testing.T) {
	t.Run("SafeURLBroadcasts", func(t *testing.T) {
		h, _, _ := newTestHub()
		before := h.bus.fanout.Position().Head
		h.translateACPEvent("c1", h.originOf("c1"), openExternalURLMsg(t, 1, "https://auth.example.com/oauth"))
		types := extractTypes(t, bufferedSince(h, before))
		if missing := missingEvents(types, string(marotte.EventOpenExternalURL)); len(missing) > 0 {
			t.Errorf("missing events %v; got %v", missing, types)
		}
	})

	t.Run("UnsafeURLDoesNotBroadcast", func(t *testing.T) {
		h, _, _ := newTestHub()
		before := h.bus.fanout.Position().Head
		h.translateACPEvent("c1", h.originOf("c1"), openExternalURLMsg(t, 2, "javascript:alert(1)"))
		types := extractTypes(t, bufferedSince(h, before))
		for _, ty := range types {
			if ty == string(marotte.EventOpenExternalURL) {
				t.Fatalf("unsafe URL must not broadcast open_external_url; got %v", types)
			}
		}
	})
}

// Every KAS _kiro/openExternalUrl names no session, so it reaches the engine lane with no chat to show it in.
func TestEngineLane_OpenExternalURLWithNoChatIsRefused(t *testing.T) {
	h, _, lane := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := h.bus.fanout.Position().Head

	h.engineFrame(fakeSourceOf(t, h).current(), openExternalURLMsg(t, 11, "https://auth.example.com/authorize?state=s1"))

	deadline := time.Now().Add(5 * time.Second)
	answer, answered := lane.responseTo(11)
	for !answered && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		answer, answered = lane.responseTo(11)
	}
	if !answered {
		t.Fatal("the sessionless URL went unanswered; KAS would wait on a request nothing answers")
	}
	// The answer runs on inflight and may broadcast after it responds, so the buffer is read only once it returns.
	joinInflight(t, h)
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](answer.err)
	if !isRPC || rpcErr.Code != -32000 {
		t.Errorf("answer = (result %v, error %v), want RPC error -32000 so KAS ends the attempt at once", answer.result, answer.err)
	}
	for _, ty := range extractTypes(t, bufferedSince(h, before)) {
		if ty == string(marotte.EventOpenExternalURL) {
			t.Errorf("a URL with no chat to show it in was broadcast as open_external_url")
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
		"open_external_url": {
			msg:  openExternalURLMsg(t, id, "https://x.example"),
			want: true,
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

func TestHandleOpenExternalURL_UnsafeSchemeIsRefusedWithInvalidParams(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)

	h.translateACPEvent("c1", h.originOf("c1"), openExternalURLMsg(t, 9, "javascript:alert(1)"))

	resp, ok := br.lastResponse()
	if !ok {
		t.Fatal("the unsafe URL went unanswered; the agent would block on a request nothing will answer")
	}
	if resp.err == nil {
		t.Fatalf("openExternalUrl(javascript:) was answered with a success (%v), want an error", resp.result)
	}
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](resp.err)
	if !isRPC {
		t.Errorf("response error = %T, want *marotte.RPCError", resp.err)
	} else if rpcErr.Code != -32602 {
		t.Errorf("response error code = %d, want -32602", rpcErr.Code)
	}
}
