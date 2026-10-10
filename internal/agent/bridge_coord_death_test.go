package agent

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/command"
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

// A bridge's end retires the asks it carried, each announced as ended so a card on screen says why,
// and nothing else: a successor's asks on the same chat stay answerable however the bridge ended,
// even under the ACP ids the old bridge used, since every bridge mints them from zero. A stale entry
// would also hold a crashed run's idle window open.
func TestBridgeEnd_RetiresTheAsksItCarriedAsEnded(t *testing.T) {
	for name, endedItself := range map[string]bool{"endedItself": true, "replacedBySuccessor": false} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			successor := newFakeBridge()
			oldPerm := requestIDOf(t, h.bus.PendingPermsAdd(7, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
				marotte.PermissionNeededPayload{RunID: "wf_1", NodeID: "a"}), br))
			oldQuestion := requestIDOf(t, h.bus.PendingPermsAdd(8, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
				marotte.UserInputNeededPayload{}), br))
			// The successor reuses both ids: 7 for the same kind, 8 for another.
			succPerm := requestIDOf(t, h.bus.PendingPermsAdd(7, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
				marotte.PermissionNeededPayload{Options: []marotte.PermissionOption{{OptionID: "allow"}}}), successor))
			succElicit := requestIDOf(t, h.bus.PendingPermsAdd(8, marotte.NewEvent(marotte.EventElicitationNeeded, "c1",
				marotte.ElicitationNeededPayload{}), successor))
			registered := ACPBridge(successor)
			if endedItself {
				registered = br
			}
			h.bridge.mgr.insert("c1", &sharedBridge{bridge: registered, state: bridgeIdle})
			head := h.bus.fanout.Position().Head

			br.endStream()
			h.coord.Forward("c1", br)

			var left []int64
			for _, e := range h.bus.pendingPerms.list("c1") {
				left = append(left, requestIDOf(t, e))
			}
			if want := []int64{succPerm, succElicit}; !slices.Equal(left, want) {
				t.Errorf("asks pending after the bridge ended = %v, want only the successor's %v", left, want)
			}
			got := settledEvents(t, bufferedSince(h, head))
			slices.SortFunc(got, func(a, b marotte.DecisionSettledPayload) int { return cmp.Compare(a.RequestID, b.RequestID) })
			want := []marotte.DecisionSettledPayload{
				{RequestID: oldPerm, Kind: marotte.DecisionKindPermission, SettledBy: marotte.SettledByEnded},
				{RequestID: oldQuestion, Kind: marotte.DecisionKindUserInput, SettledBy: marotte.SettledByEnded},
			}
			if !slices.Equal(got, want) {
				t.Errorf("decision_settled after the bridge ended = %+v, want %+v", got, want)
			}

			for _, cmd := range []marotte.ClientCommand{
				answerCmd(marotte.CmdPermissionResponse, `{"request_id":`+strconv.FormatInt(succPerm, 10)+`,"option_id":"allow"}`),
				answerCmd(marotte.CmdElicitationResponse, `{"request_id":`+strconv.FormatInt(succElicit, 10)+`,"action":"decline"}`),
			} {
				if rec := postCmd(t, h, cmd); rec.Code != http.StatusOK {
					t.Fatalf("%s to the successor = %d (%s), want 200", cmd.Type, rec.Code, rec.Body.String())
				}
			}
			if got := successor.answeredIDs(); !slices.Equal(got, []int64{7, 8}) {
				t.Errorf("successor answered ACP ids %v, want its own [7 8]", got)
			}
		})
	}
}

// Closing or deleting a tab ends its bridge, and that end is what retires the bridge's asks: each
// card is told it went because its session ended, exactly once.
func TestChatTeardown_RetiresTheBridgesAsksAsEnded(t *testing.T) {
	teardowns := map[string]func(h *Runtime){
		"close":  func(h *Runtime) { h.CloseChatState(t.Context(), "c1") },
		"delete": func(h *Runtime) { h.DeleteChatStateByChain(t.Context(), "c1", nil, command.RunStopTabClosed) },
	}
	for name, teardown := range teardowns {
		t.Run(name, func(t *testing.T) {
			h, cs, br := newTestHub()
			if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
				t.Fatalf("seed the chat: %v", err)
			}
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			askID := requestIDOf(t, h.bus.PendingPermsAdd(7, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
				marotte.PermissionNeededPayload{}), br))
			head := h.bus.fanout.Position().Head

			teardown(h)
			joinInflight(t, h)

			if left := h.bus.pendingPerms.list("c1"); len(left) != 0 {
				t.Errorf("asks pending after the %s = %v, want none", name, left)
			}
			got := settledEvents(t, bufferedSince(h, head))
			want := []marotte.DecisionSettledPayload{{RequestID: askID, Kind: marotte.DecisionKindPermission, SettledBy: marotte.SettledByEnded}}
			if !slices.Equal(got, want) {
				t.Errorf("decision_settled after the %s = %+v, want %+v", name, got, want)
			}
		})
	}
}

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
