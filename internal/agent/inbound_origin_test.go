package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// Every inbound family answers on the bridge the request arrived on, even once a successor is
// registered under the chat: KAS settles a response by its per-connection id, so an answer on
// the successor would resolve whatever that connection has pending under the same id.
func TestInboundRequest_AnswersOnTheBridgeItArrivedOn(t *testing.T) {
	cases := []struct {
		msg  func(t *testing.T, id int64) *marotte.RPCResponse
		name string
		chat marotte.ChatID
	}{
		{name: "fs_read", chat: "c1", msg: func(t *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: marotte.MethodFSRead, Params: mustJSON(t, map[string]any{"path": "f.txt"})}
		}},
		{name: "kiro_fs_stat", chat: "c1", msg: func(t *testing.T, id int64) *marotte.RPCResponse {
			return kiroFSMsg(t, id, methodKiroFSStat, "f.txt")
		}},
		{name: "client_shell_type", chat: "c1", msg: func(_ *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: methodKiroShellType}
		}},
		{name: "secret_get", chat: "c1", msg: func(t *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: methodKiroSecretGet, Params: mustJSON(t, map[string]any{"key": "k"})}
		}},
		{name: "terminal_output_unknown", chat: "c1", msg: func(t *testing.T, id int64) *marotte.RPCResponse {
			return termIDMsg(t, id, methodTermOutput, "no-such-terminal")
		}},
		{name: "unexpected_request_refusal", chat: "c1", msg: func(_ *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: "_kiro/not/a/method"}
		}},
		{name: "run_bridge_fs_read", chat: runChatID("wf_1"), msg: func(t *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: marotte.MethodFSRead, Params: mustJSON(t, map[string]any{"path": "f.txt"})}
		}},
		{name: "run_bridge_refusal", chat: runChatID("wf_1"), msg: func(_ *testing.T, id int64) *marotte.RPCResponse {
			return &marotte.RPCResponse{ID: &id, Method: "_kiro/not/a/method"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			successor := newRecordingTermBridge()
			h := hubWithBridge(t, work, successor)
			if isRunChat(tc.chat) {
				h.bridge.mgr.insert(tc.chat, &sharedBridge{bridge: successor, state: bridgeIdle})
			}
			origin := newRecordingTermBridge()

			h.translateACPEvent(tc.chat, origin, tc.msg(t, 7))

			origin.awaitResponseTo(t, 7)
			if r, ok := successor.responseTo(7); ok {
				t.Errorf("%s: the bridge registered under %q answered request 7 (%v, %v); it never sent it",
					tc.name, tc.chat, r.result, r.err)
			}
		})
	}
}

// A process-lifetime terminal/wait_for_exit is answered on the bridge it arrived on, not on a
// successor registered under the chat while the process ran.
func TestTerminalWaitForExit_AnswersOnTheBridgeItArrivedOn(t *testing.T) {
	first := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), first)
	termID, term := spawnSleeper(t, h, "c1", first, 1)
	h.translateACPEvent("c1", first, termIDMsg(t, 7, methodTermWaitForExit, termID))

	// The tab reopened: a new bridge holds the chat while the process still runs.
	successor := newRecordingTermBridge()
	h.bridge.mgr.get("c1").swapBridge(successor)
	_ = syscall.Kill(term.cmd.Process.Pid, syscall.SIGKILL)
	waitClosed(t, term.done, "terminal")

	got := first.awaitResponseTo(t, 7)
	if st, _ := got.result.(map[string]any); got.err != nil || st[keySignal] == nil {
		t.Errorf("wait_for_exit on the first bridge = (%v, %v), want a signal exit status", got.result, got.err)
	}
	if r, ok := successor.responseTo(7); ok {
		t.Errorf("the successor bridge answered wait_for_exit 7 (%v, %v); it never sent it", r.result, r.err)
	}
}

// A bridge's end releases the terminals it created and only those: KAS's own dispose cannot
// reach a closed connection, and no later bridge can see, read or stop them.
func TestBridgeEnd_ReleasesTheTerminalsItCreated(t *testing.T) {
	ending := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), ending)
	other := newRecordingTermBridge()
	otherID, otherTerm := spawnSleeper(t, h, "c1", other, 2)

	// Through the forward loop itself, so the terminal's origin is the one forwardAt observes ending.
	ending.deliver(termCreateMsg(t, 1, "sleep", []string{"30"}, nil))
	ending.awaitResponseTo(t, 1)
	endingID, endingTerm := onlyTermOf(t, h, ending)
	t.Cleanup(func() { _ = syscall.Kill(endingTerm.cmd.Process.Pid, syscall.SIGKILL) })

	ending.endStream()

	awaitDead(t, endingTerm.cmd.Process.Pid, "the ended bridge's terminal")
	deadline := time.Now().Add(3 * time.Second)
	for registered(h, endingID) {
		if time.Now().After(deadline) {
			t.Fatal("the ended bridge's terminal is still registered 3s after its end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !processAlive(otherTerm.cmd.Process.Pid) || !registered(h, otherID) {
		t.Error("another bridge's terminal was released by this bridge's end")
	}
}

func onlyTermOf(t *testing.T, h *Runtime, origin acpResponder) (string, *agentTerminal) {
	t.Helper()
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	for id, term := range h.agentTerms.terms {
		if term.origin == origin {
			return id, term
		}
	}
	t.Fatal("Setup: the bridge created no terminal")
	return "", nil
}

func registered(h *Runtime, termID string) bool {
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	_, ok := h.agentTerms.terms[termID]
	return ok
}
