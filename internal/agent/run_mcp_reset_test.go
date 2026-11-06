package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
)

// resetHub is a launchable hub whose MCP store enables names, with the engine ready.
func resetHub(t *testing.T, names ...string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub(WithMCPConfig(enabledConfig(names...)))
	t.Cleanup(func() { shutdownHub(t, h) })
	if !awaitEngine(h) {
		t.Fatal("the engine never became ready")
	}
	launchableRecipe(br, "wf_1")
	return h, br
}

var launchSeq atomic.Int64

// launchScheduledOK runs one scheduled launch under a fresh run id and fails the test if it errs.
func launchScheduledOK(t *testing.T, h *Runtime, br *fakeBridge) string {
	t.Helper()
	wf := fmt.Sprintf("wf_s%d", launchSeq.Add(1))
	br.setCallResult(methodKiroWorkflowNew, json.RawMessage(`{"workflowId":"`+wf+`"}`))
	id, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"}, nil, marotte.RunDefaults{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("LaunchScheduled: %v", err)
	}
	return id
}

func shortenLaunchResetBound(t *testing.T, d time.Duration) {
	t.Helper()
	prev := launchResetBound
	launchResetBound = d
	t.Cleanup(func() { launchResetBound = prev })
}

// The reset precedes the host session's door: KAS's reset after the host's acquire would connect twice.
func TestLaunchScheduled_ResetsAFailedMCPServerBeforeTheHostDoor(t *testing.T) {
	h, br := resetHub(t, "everything")
	reportMCP(t, h, "sess_chat", failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT"))
	resetsAtDoor := atomic.Int32{}
	resetsAtDoor.Store(-1)
	fakeSourceOf(t, h).setFactory(func() engineSession {
		resetsAtDoor.Store(int32(countCalls(br, methodV3MCPResetServer)))
		return br
	})

	launchScheduledOK(t, h, br)

	if n := countCalls(br, methodV3MCPResetServer); n != 1 {
		t.Fatalf("resets sent = %d, want 1 (calls %v)", n, br.callLog())
	}
	if got := br.paramsFor(methodV3MCPResetServer); got[keyServerName] != "everything" || got[marotte.KeySessionID] != marotte.SessionID("sess_chat") {
		t.Errorf("reset params = %v, want {sessionId: sess_chat, serverName: everything}", got)
	}
	if n := resetsAtDoor.Load(); n != 1 {
		t.Errorf("resets sent when the host door opened = %d, want 1 (-1: no door)", n)
	}
	log := br.callLog()
	reset := slices.Index(log, methodV3MCPResetServer)
	for _, later := range []string{methodKiroWorkflowNew, methodKiroWorkflowInvoke} {
		if i := slices.Index(log, later); i < reset {
			t.Errorf("%s at %d, want after the reset at %d (calls %v)", later, i, reset, log)
		}
	}
}

// A server one session has connected and another failed is reset in the failed session, which the run's host session
// would otherwise inherit failed; the connected session keeps its connection.
func TestLaunchScheduled_ResetsTheSessionWhereTheServerFailed(t *testing.T) {
	h, br := resetHub(t, "everything")
	reportMCP(t, h, "sess_a", connectedMCP("everything", marotte.MCPSource{}))
	reportMCP(t, h, "sess_b", failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT"))

	launchScheduledOK(t, h, br)

	if n := countCalls(br, methodV3MCPResetServer); n != 1 {
		t.Fatalf("resets sent = %d, want 1 (calls %v)", n, br.callLog())
	}
	if got := br.paramsFor(methodV3MCPResetServer); got[marotte.KeySessionID] != marotte.SessionID("sess_b") {
		t.Errorf("reset session = %v, want sess_b, where the server failed", got[marotte.KeySessionID])
	}
}

// A manual run's user is present and has the Reconnect button.
func TestLaunch_AManualRunResetsNoMCPServer(t *testing.T) {
	h, br := resetHub(t, "everything")
	reportMCP(t, h, "sess_chat", failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT"))

	if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if n := countCalls(br, methodV3MCPResetServer); n != 0 {
		t.Errorf("resets sent = %d, want none", n)
	}
}

// A reset re-mints a pending sign-in's URL under a paste in progress, and a disabled server is not live.
func TestLaunchScheduled_ResetsNoServerAwaitingSignInOrDisabled(t *testing.T) {
	h, br := resetHub(t, "remote")
	reportMCP(t, h, "sess_chat",
		oauthMCP("remote", "https://auth.example/a"),
		disabledMCP("theirs", marotte.MCPSource{Origin: "workspace", Root: "/w"}))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "remote"); err != nil {
		t.Fatalf("claimSignIn: %v", err)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), "https://auth.example/consent?state=s", false); shown.how != consentOnRow {
		t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
	}
	if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "s"); err != nil {
		t.Fatalf("beginOAuthRelay: %v", err)
	}

	launchScheduledOK(t, h, br)

	if n := countCalls(br, methodV3MCPResetServer); n != 0 {
		t.Errorf("resets sent = %d, want none", n)
	}
	if row := rowOf(t, h, "remote"); row.State != mcpStateSigningIn || !row.Relayed {
		t.Errorf("row = %+v, want it still signing_in and relayed", row)
	}
}

// Three automatic resets per server between connects: a connect or the user's Reconnect re-arms them.
func TestLaunchScheduled_CapsResetsUntilTheServerConnects(t *testing.T) {
	failed := failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT")

	t.Run("a connect re-arms the cap", func(t *testing.T) {
		h, br := resetHub(t, "everything")
		reportMCP(t, h, "sess_chat", failed)
		for range 4 {
			launchScheduledOK(t, h, br)
		}
		if n := countCalls(br, methodV3MCPResetServer); n != maxLaunchResets {
			t.Fatalf("resets over four launches = %d, want %d", n, maxLaunchResets)
		}
		reportMCP(t, h, "sess_chat", connectedMCP("everything", marotte.MCPSource{}))
		reportMCP(t, h, "sess_chat", failed)
		launchScheduledOK(t, h, br)
		if n := countCalls(br, methodV3MCPResetServer); n != maxLaunchResets+1 {
			t.Errorf("resets after a connect and a re-failure = %d, want %d", n, maxLaunchResets+1)
		}
	})

	t.Run("the user's Reconnect re-arms the cap", func(t *testing.T) {
		h, br := resetHub(t, "everything")
		reportMCP(t, h, "sess_chat", failed)
		for range maxLaunchResets {
			launchScheduledOK(t, h, br)
		}
		if rec := postJSON(h.mcpRegistry.handleReconnect, "/api/mcp/reconnect", `{"server":"everything"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("reconnect = %d %s, want 204", rec.Code, rec.Body.String())
		}
		launchScheduledOK(t, h, br)
		if n := countCalls(br, methodV3MCPResetServer); n != maxLaunchResets+2 {
			t.Errorf("resets = %d, want %d (cap, the press, one more launch)", n, maxLaunchResets+2)
		}
	})

	t.Run("a reset nothing took is not spent", func(t *testing.T) {
		h, br := resetHub(t, "everything")
		reportMCP(t, h, "sess_chat", failed)
		br.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{
			Code: -32602, Message: "not declared",
			Data: json.RawMessage(`{"errorType":"McpServerNotDeclaredError","sessionId":"sess_chat","serverName":"everything"}`),
		})
		for range maxLaunchResets + 1 {
			launchScheduledOK(t, h, br)
		}
		if n := countCalls(br, methodV3MCPResetServer); n != maxLaunchResets+1 {
			t.Errorf("resets KAS refused as undeclared = %d, want one per launch (%d)", n, maxLaunchResets+1)
		}
	})
}

// A failed integration must not cost the run: an unanswered or refused reset still launches it.
func TestLaunchScheduled_AResetThatNeverAnswersDoesNotStopTheLaunch(t *testing.T) {
	t.Run("never answers", func(t *testing.T) {
		shortenLaunchResetBound(t, 50*time.Millisecond)
		h, br := resetHub(t, "everything")
		reportMCP(t, h, "sess_chat", failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT"))
		src := fakeSourceOf(t, h)
		src.mu.Lock()
		src.callGate = func(ctx context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error) {
			if method == methodV3MCPResetServer {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return resp, err
		}
		src.mu.Unlock()

		if id := launchScheduledOK(t, h, br); id == "" {
			t.Fatal("the launch answered no run id")
		}
		if n := countCalls(br, methodKiroWorkflowInvoke); n != 1 {
			t.Errorf("invokes = %d, want 1", n)
		}
	})

	t.Run("refused", func(t *testing.T) {
		h, br := resetHub(t, "everything")
		reportMCP(t, h, "sess_chat", failedMCP("everything", marotte.MCPSource{}, "spawn ENOENT"))
		br.setCallRPCErr(methodV3MCPResetServer, &marotte.RPCError{Code: -32603, Message: "boom"})

		launchScheduledOK(t, h, br)

		if n := countCalls(br, methodKiroWorkflowInvoke); n != 1 {
			t.Errorf("invokes = %d, want 1", n)
		}
	})
}

// Two failed servers' resets are in flight together, so the launch waits for the slower, not their sum.
func TestLaunchScheduled_ResetsTwoFailedServersInParallel(t *testing.T) {
	shortenLaunchResetBound(t, 2*time.Second)
	h, br := resetHub(t, "a", "b")
	reportMCP(t, h, "sess_chat",
		failedMCP("a", marotte.MCPSource{}, "spawn ENOENT"),
		failedMCP("b", marotte.MCPSource{}, "spawn ENOENT"))
	var arrived, met atomic.Int32
	both := make(chan struct{})
	src := fakeSourceOf(t, h)
	src.mu.Lock()
	src.callGate = func(ctx context.Context, method string, resp *marotte.RPCResponse, err error) (*marotte.RPCResponse, error) {
		if method != methodV3MCPResetServer {
			return resp, err
		}
		if arrived.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			met.Add(1)
		case <-ctx.Done():
		}
		return resp, err
	}
	src.mu.Unlock()

	launchScheduledOK(t, h, br)

	if met.Load() != 2 {
		t.Errorf("resets that saw the other in flight = %d, want 2 (serialized)", met.Load())
	}
}
