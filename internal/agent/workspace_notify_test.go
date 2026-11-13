package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// workspaceMethods are the eleven workspace-global notifications KAS sends to every resident session or none.
var workspaceMethods = []string{
	methodKiroHooksDidChange, methodV3Powers, methodKiroWorkflowRecipesChanged, methodV3Governance,
	methodKiroConfigurationState, methodV3PolicyChanged, methodV3PolicyError, methodV3SteeringDocs, methodV3SystemNotify,
	methodKiroMemoryChanged, methodKiroMemorySyncStatus,
}

// countedSession is the session id of the frames countWorkspaceHandlers counts.
const countedSession marotte.SessionID = "sess_any"

// countedHub is newTestHub whose workspace handlers count the frames for countedSession they were handed. The
// counters go in before the engine starts: an observer pass's pump, untracked by the boot drain, reads the table.
func countedHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge, func(method string) int) {
	t.Helper()
	cs := newTestChatStore()
	br := newFakeBridge()
	src := fakeEngineOf(func() engineSession { return br }, br)
	gate := make(chan struct{})
	src.startGate = gate
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	count := countWorkspaceHandlers(h)
	close(gate)
	settleBoot(t, h)
	br.forgetCalls()
	return h, cs, br, count
}

// countWorkspaceHandlers wraps every workspace handler in a counter of the frames for countedSession it was handed;
// a frame for another session, such as the boot's own observer passes, goes to the real handler.
func countWorkspaceHandlers(h *Runtime) func(method string) int {
	var mu sync.Mutex
	counts := map[string]int{}
	for method, real := range h.workspaceHandlers {
		h.workspaceHandlers[method] = func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
			if sessionIDOfParams(msg.Params) != countedSession {
				real(ctx, chatID, msg)
				return
			}
			mu.Lock()
			counts[method]++
			mu.Unlock()
		}
	}
	return func(method string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[method]
	}
}

func TestWorkspaceNotifications_HandledOnceOnEveryLane(t *testing.T) {
	h, cs, br, count := countedHub(t)
	seedChat(t, cs, "c1")
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: OpenBridge: %v", err)
	}
	o := oneObserver(t, h)
	lanes := []struct {
		name    string
		deliver func(msg *marotte.RPCResponse)
	}{
		{name: "chat lane", deliver: func(msg *marotte.RPCResponse) { h.translateACPEvent("c1", h.originOf("c1"), msg) }},
		{name: "run lane", deliver: func(msg *marotte.RPCResponse) { h.dispatch(t.Context(), runChatID("wf_1"), br, msg) }},
		{name: "engine lane", deliver: func(msg *marotte.RPCResponse) { h.engineFrame(fakeSourceOf(t, h).current(), msg) }},
		{name: "observer lane", deliver: func(msg *marotte.RPCResponse) {
			want := count(msg.Method) + 1
			err := h.observe(t.Context(), func(context.Context, engineSession) error {
				o.deliver(msg)
				for deadline := time.Now().Add(5 * time.Second); count(msg.Method) < want; time.Sleep(time.Millisecond) {
					if time.Now().After(deadline) {
						return errors.New("the frame was not handed on within 5s")
					}
				}
				return nil
			})
			if err != nil {
				t.Errorf("observer lane: %s: %v", msg.Method, err)
			}
		}},
	}
	for i, lane := range lanes {
		for _, method := range workspaceMethods {
			params, _ := json.Marshal(map[string]any{"sessionId": countedSession})
			lane.deliver(&marotte.RPCResponse{Method: method, Params: params})
			if got := count(method); got != i+1 {
				t.Errorf("%s: %s handled %d times after %d lanes, want once per frame", lane.name, method, got, i+1)
			}
		}
	}
}

func TestWorkspaceNotifications_EngineAddressedConfigurationStateUpdatesSettings(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	before := settingsUpdatedCount(t, h)

	h.engineFrame(fakeSourceOf(t, h).current(), configurationStateMsg(t, "env_connection"))

	if got := h.config.KiroDefaults(); len(got) == 0 {
		t.Error("KiroDefaults after an engine-lane configuration state = empty, want the engine's view")
	}
	if n := settingsUpdatedCount(t, h) - before; n != 1 {
		t.Errorf("settings_updated broadcasts = %d, want 1", n)
	}
}

func TestWorkspaceNotifications_IsTheOnlyTable(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	if got, want := slices.Sorted(maps.Keys(h.workspaceHandlers)), slices.Sorted(slices.Values(workspaceMethods)); !slices.Equal(got, want) {
		t.Errorf("workspace table = %v, want %v", got, want)
	}
	for _, method := range workspaceMethods {
		if _, ok := h.chatHandlers[method]; ok {
			t.Errorf("%s is also a chat handler: a copy on a chat lane would be handled twice", method)
		}
		if _, ok := h.noopMethods[method]; ok {
			t.Errorf("%s is also dropped as a no-op: a copy on that lane would be lost", method)
		}
	}
}
