package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// TestTranslateACPEvent_RefusesUnknownRequests is the red check for the wedge class: KAS's extMethod calls have
// no timeout and Bridge.Call no client deadline (turns can run for hours), so an unanswered A→C request
// leaves the prompt Call blocked and every later prompt 409ing. `_kiro/workspace/currently_open_files` is
// the live case: ungated in KAS and deliberately unimplemented here.
func TestTranslateACPEvent_RefusesUnknownRequests(t *testing.T) {
	cases := map[string]string{
		// The live case.
		"ungated workspace pull": "_kiro/workspace/currently_open_files",
		// A `_kiro/*` method with no handler and no noop entry.
		"unknown kiro extension": "_kiro/some/future/verb",
		// Accepted by the prefix router but not implemented.
		"unimplemented terminal verb": "terminal/resize",
		// A core ACP method marotte does not implement.
		"unknown core method": "session/somethingNew",
		// A noop-table method arriving with an id: the table is checked before the refusal, so without its id test
		// the request returns unanswered and unlogged.
		"a noop method arriving as a request": methodV3ToolsDidChange,
	}

	for name, method := range cases {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			id := int64(4242)

			h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{
				Method: method,
				ID:     &id,
			})

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatalf("no response to %s: an unanswered request wedges the turn", method)
			}

			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			if got.id != id {
				t.Errorf("responded to id %d, want %d", got.id, id)
			}
			if got.err == nil {
				t.Fatalf("responded to %s with a success result (%v), want an error: "+
					"marotte does not implement it and must say so", method, got.result)
			}
			// -32601 is method-not-found; -32603 would blame us.
			var rpcErr *marotte.RPCError
			if !errors.As(got.err, &rpcErr) {
				t.Fatalf("refusal for %s is not an *marotte.RPCError (%T); the code is not on the wire",
					method, got.err)
			}
			if rpcErr.Code != marotte.RPCCodeMethodNotFound {
				t.Errorf("refusal for %s used code %d, want %d (method not found)",
					method, rpcErr.Code, marotte.RPCCodeMethodNotFound)
			}
			if !strings.Contains(rpcErr.Message, method) {
				t.Errorf("refusal message %q does not name the method; a log line would not say what was refused",
					rpcErr.Message)
			}
		})
	}
}

// TestTranslateACPEvent_IgnoresUnknownNotifications pins that answering a notification is a protocol error; `msg.ID != nil` is the whole distinction.
func TestTranslateACPEvent_IgnoresUnknownNotifications(t *testing.T) {
	// An unknown method and a noop-table member: both stay silent.
	for name, method := range map[string]string{
		"unknown extension": "_kiro/some/future/notification",
		"noop table member": methodV3ToolsDidChange,
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())

			h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{
				Method: method,
				ID:     nil,
			})

			select {
			case <-br.done:
				br.respMu.Lock()
				got := br.response
				br.respMu.Unlock()
				t.Fatalf("responded to a notification (id=%d, err=%v): notifications owe no reply",
					got.id, got.err)
			case <-time.After(200 * time.Millisecond):
				// Nothing sent.
			}
		})
	}
}

// TestHubContextIsLiveOnAFreshHub guards the tests above: a shutting-down runtime would refuse to send and pass them wrongly.
func TestHubContextIsLiveOnAFreshHub(t *testing.T) {
	h, _ := hubForFSTest(t, t.TempDir())
	ctx, cancel := h.lifecycle.derivedContext()
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Fatalf("a fresh runtime's context is already done (%v); the refusal tests would be vacuous", err)
	}
}

// TestTranslateACPEvent_ReportsARefusalItCouldNotDeliver pins that an unwritten refusal is the same wedge, and the line is
// its only diagnosis, so it must not fire on success.
func TestTranslateACPEvent_ReportsARefusalItCouldNotDeliver(t *testing.T) {
	const wantLine = "chat bridge: refusal could not be delivered; the turn may be wedged"

	t.Run("a refusal that went nowhere is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, br := hubForFSTest(t, t.TempDir())
		br.respMu.Lock()
		br.respondErr = errors.New("bridge gone")
		br.respMu.Unlock()
		id := int64(4242)

		h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{Method: "_kiro/some/future/verb", ID: &id})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal was never attempted")
		}
		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a refusal that could not be written said nothing, so a wedged turn has no "+
				"diagnosis; want a line reading %q. Got: %s", wantLine, out)
		}
	})

	t.Run("a delivered refusal is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h, br := hubForFSTest(t, t.TempDir())
		id := int64(4242)

		h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{Method: "_kiro/some/future/verb", ID: &id})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the refusal was never attempted")
		}
		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a refusal that landed was reported as undelivered: %s", out)
		}
	})
}

// TestTranslateACPEvent_HandlerTableIsIDAware pins the id gate on the handler-map lookup: a method KAS promotes
// to a request would otherwise be swallowed by a notification handler. The three request-shaped ones go
// through routeInboundRequest, disjoint by construction.
func TestTranslateACPEvent_HandlerTableIsIDAware(t *testing.T) {
	const method = "_kiro/mcp/status" // a notification handler in the table
	params := mustJSON(t, map[string]any{
		"servers": []map[string]any{{"name": "github", "status": "connected"}},
	})

	t.Run("a notification still reaches its handler", func(t *testing.T) {
		h, _ := hubForFSTest(t, t.TempDir())

		h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{Method: method, Params: params})

		if snap := h.mcpRegistry.snapshot(); len(snap) != 1 {
			t.Fatalf("registry snapshot = %+v, want the one server the notification carried: "+
				"gating the lookup must not stop notification handling", snap)
		}
	})

	t.Run("the same method arriving as a request is refused", func(t *testing.T) {
		h, br := hubForFSTest(t, t.TempDir())
		id := int64(31337)

		h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{Method: method, ID: &id, Params: params})

		select {
		case <-br.done:
		case <-time.After(2 * time.Second):
			t.Fatal("a request-shaped table member got no response; a notification handler " +
				"swallowed it and the fence was never reached")
		}
		br.respMu.Lock()
		got := br.response
		br.respMu.Unlock()

		var rpcErr *marotte.RPCError
		if !errors.As(got.err, &rpcErr) {
			t.Fatalf("refusal is not an *marotte.RPCError (%T, result %v)", got.err, got.result)
		}
		if rpcErr.Code != marotte.RPCCodeMethodNotFound {
			t.Errorf("refusal code = %d, want %d", rpcErr.Code, marotte.RPCCodeMethodNotFound)
		}
		// The handler must not have run: handled and refused would be two answers to one id.
		if snap := h.mcpRegistry.snapshot(); len(snap) != 0 {
			t.Errorf("the notification handler also ran (snapshot %+v); the frame was handled "+
				"and refused, which is two answers on one id", snap)
		}
	})
}

// TestTranslateACPEvent_AskMethodsDispatchOnce pins that the three request-shaped members dispatch from the request side
// exactly once. It catches zero (a missing whitelist sends approvals to -32601); two is unreachable by construction.
func TestTranslateACPEvent_AskMethodsDispatchOnce(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	before := h.bus.fanout.Position().Head
	id := int64(31338)

	h.translateACPEvent("c1", h.originOf("c1"), &marotte.RPCResponse{
		Method: marotte.MethodRequestPermission,
		ID:     &id,
		Params: mustJSON(t, map[string]any{
			"sessionId": "sess_1",
			"toolCall":  map[string]any{"toolCallId": "tc1", "title": "write file", "kind": "edit"},
			"options":   []map[string]any{{"optionId": "accept", "name": "Allow", "kind": "allow_once"}},
		}),
	})

	asks := 0
	for _, e := range bufferedSince(h, before) {
		var msg marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if msg.Type == marotte.EventPermissionNeeded {
			asks++
		}
	}
	if asks != 1 {
		t.Errorf("permission_needed fired %d times, want 1: 0 means the ask never reached its "+
			"handler, 2 means both halves dispatched it", asks)
	}

	select {
	case <-br.done:
		br.respMu.Lock()
		got := br.response
		br.respMu.Unlock()
		t.Errorf("the ask was answered on the wire (id=%d, err=%v) instead of raised to the user; "+
			"it fell through to the refusal", got.id, got.err)
	case <-time.After(200 * time.Millisecond):
	}
}
