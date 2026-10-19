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
func TestForward_BridgeExitedNamesSessionAndCause(t *testing.T) {
	cases := []struct {
		name       string
		cause      string
		registered bool
	}{
		{name: "ended while registered", registered: true, cause: "ended"},
		{name: "closed by marotte before its stream ended", cause: "closed"},
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
			if got := exited["cause"]; got != tc.cause {
				t.Errorf("bridge exited cause = %v, want %q", got, tc.cause)
			}
		})
	}
}

// A session's end retires the asks it carried, each announced as ended so a card on screen says why,
// and nothing else: a successor's asks on the same chat stay answerable however the session ended. After an engine
// exit the next generation's connection mints ACP ids from zero again, so the successor reuses the old ids; a
// session closed within one generation leaves a successor on the same connection, which never repeats an id. A stale
// entry would also hold a crashed run's idle window open.
func TestBridgeEnd_RetiresTheAsksItCarriedAsEnded(t *testing.T) {
	for name, engineExited := range map[string]bool{"engineExited": true, "replacedBySuccessor": false} {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			successor := newFakeBridge()
			oldPerm := requestIDOf(t, h.bus.PendingPermsAdd(7, marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
				marotte.PermissionNeededPayload{RunID: "wf_1", NodeID: "a"}), br))
			oldQuestion := requestIDOf(t, h.bus.PendingPermsAdd(8, marotte.NewEvent(marotte.EventUserInputNeeded, "c1",
				marotte.UserInputNeededPayload{}), br))
			succIDs := []int64{9, 10}
			if engineExited {
				// A new generation: 7 again for the same kind, 8 for another.
				succIDs = []int64{7, 8}
			}
			succPerm := requestIDOf(t, h.bus.PendingPermsAdd(succIDs[0], marotte.NewEvent(marotte.EventPermissionNeeded, "c1",
				marotte.PermissionNeededPayload{Options: []marotte.PermissionOption{{OptionID: "allow"}}}), successor))
			succElicit := requestIDOf(t, h.bus.PendingPermsAdd(succIDs[1], marotte.NewEvent(marotte.EventElicitationNeeded, "c1",
				marotte.ElicitationNeededPayload{}), successor))
			registered := engineSession(successor)
			if engineExited {
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
			if got := successor.answeredIDs(); !slices.Equal(got, succIDs) {
				t.Errorf("successor answered ACP ids %v, want its own %v", got, succIDs)
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
				marotte.PermissionNeededPayload{}), guardSession(br)))
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

func poolBody(t *testing.T, h *Runtime, query string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mcpRegistry.handlePool(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/pool"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp/pool%s status = %d, want 200", query, rec.Code)
	}
	return strings.TrimSpace(rec.Body.String())
}
