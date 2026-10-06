package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// A bridge whose stream ends while registered died on its own, and Forward must reap it:
// an unreaped acp-server holds KAS's workflow ownership lease and refuses later resumes.
func TestForward_ReapsARegisteredBridgeThatDied(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	// endStream, not Stop, or the fixture satisfies the assertion itself.
	br.endStream()
	h.coord.Forward("c1", br)

	if !br.isStopped() {
		t.Error("bridge.isStopped() = false, want true (a registered bridge that died must be reaped)")
	}
}

// A bridge removed from the map first has its own closer; Forward must not reap it.
func TestForward_DoesNotReapAnUnregisteredBridge(t *testing.T) {
	h, _, br := newTestHub()

	br.endStream()
	h.coord.Forward("c1", br)

	if br.isStopped() {
		t.Error("bridge.isStopped() = true, want false (an unregistered exit is a deliberate teardown)")
	}
}

// A waiting_on_user claim is owed to the agent; when it dies the claim is false.
func TestForward_DischargesWaitingOnUserWhenTheAgentDies(t *testing.T) {
	h, _, br := newTestHub()
	h.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
		Status:      marotte.ChatStatusWaitingOnUser,
		Description: "needs a decision",
	})
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	br.endStream()
	h.coord.Forward("c1", br)

	if got := h.bus.chatStatus.Get("c1").Status; got != "" {
		t.Errorf("chat status = %q, want %q (a dead agent cannot still be awaiting an answer)", got, "")
	}
}

// Only the retained waiting_on_user claim is Forward's to discharge; ClearAtTurnEnd owns the rest.
func TestForward_LeavesANonWaitingStatusAlone(t *testing.T) {
	h, _, br := newTestHub()
	h.bus.chatStatus.Merge("c1", marotte.ChatStatusPayload{
		Status:      "in_progress",
		Description: "working",
	})
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	br.endStream()
	h.coord.Forward("c1", br)

	if got := h.bus.chatStatus.Get("c1").Status; got != "in_progress" {
		t.Errorf("chat status = %q, want %q", got, "in_progress")
	}
}

// The exit line says which kind of exit it was and names the session. Not parallel: swaps the slog default.
func TestForward_BridgeExitedNamesSessionAndWhetherItEndedItself(t *testing.T) {
	cases := []struct {
		name       string
		registered bool
	}{
		{name: "died while registered", registered: true},
		{name: "removed before its stream ended", registered: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			if tc.registered {
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
			}
			logs := captureLogs(t)

			br.endStream()
			h.coord.Forward("c1", br)

			var exited map[string]any
			for line := range strings.SplitSeq(logs.String(), "\n") {
				var rec map[string]any
				if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "bridge exited" {
					exited = rec
				}
			}
			if exited == nil {
				t.Fatalf("Forward logged no bridge exited line:\n%s", logs.String())
			}
			if got, want := exited["session_id"], string(br.SessionID()); got != want {
				t.Errorf("bridge exited session_id = %v, want %q", got, want)
			}
			if got := exited["ended_itself"]; got != tc.registered {
				t.Errorf("bridge exited ended_itself = %v, want %t", got, tc.registered)
			}
		})
	}
}

// A dead bridge's asks are unanswerable, and a stale entry would hold a crashed run's idle window open.
func TestForward_ClearsTheDeadBridgesPendingDecisions(t *testing.T) {
	h, _, br := newTestHub()
	h.bus.PendingPermsAdd(7, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
		marotte.PermissionNeededPayload{RequestID: 7, RunID: "wf_1", NodeID: "a"}))
	h.bus.PendingPermsAdd(8, marotte.NewEvent(marotte.EventPermissionNeeded, "c2",
		marotte.PermissionNeededPayload{RequestID: 8}))
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})

	br.endStream()
	h.coord.Forward("c1", br)

	if got := h.bus.pendingPerms.List("c1"); len(got) != 0 {
		t.Errorf("the dead bridge's chat still holds %d decisions, want none", len(got))
	}
	if got := h.bus.pendingPerms.List("c2"); len(got) != 1 {
		t.Errorf("another chat holds %d decisions after c1's bridge died, want its own 1", len(got))
	}
}

// mcpStatusFrame is a _kiro/mcp/status frame whose connected servers each offer the one resource in uris.
func mcpStatusFrame(t *testing.T, uris map[string]string) *marotte.RPCResponse {
	t.Helper()
	servers := []map[string]any{}
	for name, uri := range uris {
		servers = append(servers, map[string]any{
			"name": name, "status": "connected",
			"resources": []map[string]any{{"name": uri, "uri": uri}},
		})
	}
	params, err := json.Marshal(map[string]any{"servers": servers})
	if err != nil {
		t.Fatalf("marshal status frame: %v", err)
	}
	return &marotte.RPCResponse{Method: methodV3MCPStatus, Params: params}
}

// poolBody is GET /api/mcp/pool?chat_id=<chatID>'s body.
func poolBody(t *testing.T, h *Runtime, chatID string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mcpRegistry.handlePool(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/pool?chat_id="+chatID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp/pool?chat_id=%s status = %d, want 200", chatID, rec.Code)
	}
	return strings.TrimSpace(rec.Body.String())
}

// A replaced bridge still draining must not overwrite or clear the serving bridge's pool;
// one dying with no replacement takes its pool.
func TestForward_MCPPoolBelongsToTheBridgeThatReportedIt(t *testing.T) {
	cases := []struct {
		name     string
		replaced bool
		want     string
	}{
		{name: "bridge died", replaced: false, want: `{"servers":[]}`},
		{
			name: "old bridge reports and exits after its replacement", replaced: true,
			want: `{"servers":[{"name":"docs","resources":[{"name":"new://r","uri":"new://r"}],"resource_templates":[]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, old := newTestHub()
			if tc.replaced {
				replacement := newFakeBridge()
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: replacement, state: bridgeIdle})
				h.coord.recordPool(replacement, mcpStatusFrame(t, map[string]string{"docs": "new://r"}))
			} else {
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: old, state: bridgeIdle})
			}

			old.deliver(mcpStatusFrame(t, map[string]string{"docs": "old://r"}))
			old.endStream()
			h.coord.Forward("c1", old)

			if got := poolBody(t, h, "c1"); got != tc.want {
				t.Errorf("c1 pool after the old bridge exited = %s, want %s", got, tc.want)
			}
			h.mcpRegistry.mu.RLock()
			_, oldKept := h.mcpRegistry.pools[old]
			h.mcpRegistry.mu.RUnlock()
			if oldKept {
				t.Error("the exited bridge's pool is still held, want it dropped")
			}
		})
	}
}
