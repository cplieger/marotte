package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
)

type deleteRig struct {
	results map[string]json.RawMessage
	rpcErrs map[string]*marotte.RPCError
	// onCall runs inside the fake bridge's Call, before it answers, outside mu.
	onCall func(method string, params map[string]any)
	calls  []rigCall
	opened []*fakeBridge
	mu     sync.Mutex
}

type rigCall struct {
	bridge  *fakeBridge
	params  map[string]any
	method  string
	stopped bool
}

func (r *deleteRig) factory() engineSession {
	br := newFakeBridge()
	br.callResults = r.results
	br.callRPCErrs = r.rpcErrs
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		stopped := br.isClosed()
		r.mu.Lock()
		r.calls = append(r.calls, rigCall{bridge: br, method: method, params: params, stopped: stopped})
		r.mu.Unlock()
		if r.onCall != nil {
			r.onCall(method, params)
		}
		return nil, nil, false
	}
	r.mu.Lock()
	r.opened = append(r.opened, br)
	r.mu.Unlock()
	return br
}

func (r *deleteRig) of(method string) []rigCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []rigCall
	for _, c := range r.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (r *deleteRig) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.method)
	}
	return out
}

func newDeleteRig(t *testing.T, runs json.RawMessage) (*Runtime, *testChatStore, *deleteRig) {
	t.Helper()
	rig := &deleteRig{results: map[string]json.RawMessage{
		methodKiroWorkflowList:      runs,
		methodKiroWorkflowDelete:    json.RawMessage(`{}`),
		methodKiroWorkflowCancel:    json.RawMessage(`{}`),
		marotte.MethodSessionDelete: json.RawMessage(`{}`),
		// Named for no run here, so a landed cancel's reconcile releases nothing.
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}}
	cs := newTestChatStore()
	h := newRuntimeOn(t.Context(), testReaperWorkDir, rig.engine(), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	return h, cs, rig
}

// engine is a fake engine whose sessions and connection calls both come from the rig, so every call is recorded.
func (r *deleteRig) engine() *fakeEngineSource {
	lane, _ := r.factory().(*fakeBridge)
	return fakeEngineOf(r.factory, lane)
}

// insertChatSession inserts rather than opens, so openBridge's rehydrate hook does not read the inventory.
func insertChatSession(t *testing.T, h *Runtime, rig *deleteRig, chatID marotte.ChatID, sessionID string) *fakeBridge {
	t.Helper()
	br, ok := rig.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the rig handed back something other than a fake")
	}
	br.sessionID = sessionID
	h.bridge.mgr.insert(chatID, &sharedBridge{bridge: br, state: bridgeIdle})
	return br
}

func deletedSessionIDs(calls []rigCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		id, _ := c.params[marotte.KeySessionID].(string)
		out = append(out, id)
	}
	return out
}

func noRuns(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"runs":[]}`)
}

// Every chain session is deleted with an engine call, before the chat's own session is closed, so KAS still holds the
// session it deletes and SessionEnd fires there.
func TestDeleteChatStateByChain_DeletesEveryChainSessionThroughTheEngine(t *testing.T) {
	h, _, rig := newDeleteRig(t, noRuns(t))
	owner := insertChatSession(t, h, rig, "c1", "sess_cur")
	var mu sync.Mutex
	var closedAtDelete []bool
	rig.onCall = func(method string, _ map[string]any) {
		if method == marotte.MethodSessionDelete {
			mu.Lock()
			closedAtDelete = append(closedAtDelete, owner.isClosed())
			mu.Unlock()
		}
	}

	h.DeleteChatStateByChain(t.Context(), "c1", []string{"sess_old", "sess_cur"}, command.RunStopTabClosed)

	deletes := rig.of(marotte.MethodSessionDelete)
	if got, want := deletedSessionIDs(deletes), []string{"sess_old", "sess_cur"}; !slices.Equal(got, want) {
		t.Fatalf("DeleteChatStateByChain sent session/delete for %v, want %v", got, want)
	}
	for _, c := range deletes {
		if c.bridge == owner {
			t.Errorf("session/delete for %v rode the chat's session, want an engine call", c.params)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(closedAtDelete, true) {
		t.Errorf("a session/delete arrived after the chat's session closed (%v), so SessionEnd cannot fire there", closedAtDelete)
	}
	joinInflight(t, h)
	if !owner.isClosed() {
		t.Error("the chat's session was not closed after its sessions were deleted")
	}
}

func TestDeleteForSessions_DeletesEveryOwnedRunWhateverItsStatus(t *testing.T) {
	h, _, rig := newDeleteRig(t, kasRuns(t,
		map[string]any{"workflowId": "wf_done", "name": "r", "status": "completed", "parentSessionId": "sess_cur"},
		map[string]any{"workflowId": "wf_live", "name": "r", "status": "running", "parentSessionId": "sess_old"},
		map[string]any{"workflowId": "wf_foreign", "name": "r", "status": "completed", "parentSessionId": "sess_x"},
	))
	insertChatSession(t, h, rig, "c1", "sess_cur")

	h.runs.deleteForSessions(t.Context(), "c1", []string{"sess_old", "sess_cur"}, runStop{})

	var got []string
	for _, c := range rig.of(methodKiroWorkflowDelete) {
		id, _ := c.params["workflowId"].(string)
		got = append(got, id)
	}
	slices.Sort(got)
	if want := []string{"wf_done", "wf_live"}; !slices.Equal(got, want) {
		t.Errorf("DeleteForSessions deleted %v, want %v: every run the chain launched, any status, and no other", got, want)
	}
	if n := len(rig.of(methodKiroWorkflowList)); n != 1 {
		t.Errorf("DeleteForSessions read the run inventory %d times, want 1", n)
	}
}

func TestDeleteChatStateByChain_DeletesRunsBeforeSessions(t *testing.T) {
	h, _, rig := newDeleteRig(t, kasRuns(t,
		map[string]any{"workflowId": "wf_1", "name": "r", "status": "paused", "parentSessionId": "sess_cur"},
	))
	insertChatSession(t, h, rig, "c1", "sess_cur")

	h.DeleteChatStateByChain(t.Context(), "c1", []string{"sess_cur"}, command.RunStopTabClosed)

	log := rig.methods()
	lastRun := slices.Index(log, methodKiroWorkflowDelete)
	firstSession := slices.Index(log, marotte.MethodSessionDelete)
	if lastRun < 0 || firstSession < 0 {
		t.Fatalf("call log %v lacks a workflow/delete or a session/delete", log)
	}
	if lastRun > firstSession {
		t.Errorf("call log %v deletes a session before its run: KAS refuses a session that owns a live run", log)
	}
}

func TestDeleteChatStateByChain_ReapsWhenTheDeleteIsRefused(t *testing.T) {
	sessionsDir := t.TempDir()
	sessDir := filepath.Join(sessionsDir, "hash01", "sess_cur")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatalf("Setup: mkdir session: %v", err)
	}
	writeSessionRecord(t, sessDir, testReaperWorkDir)
	rig := &deleteRig{
		results: map[string]json.RawMessage{methodKiroWorkflowList: noRuns(t)},
		rpcErrs: map[string]*marotte.RPCError{marotte.MethodSessionDelete: {
			Code: -32000, Message: "session owns live workflows",
		}},
	}
	cs := newTestChatStore()
	h := newRuntimeOn(t.Context(), testReaperWorkDir, fakeEngineOf(rig.factory, nil), cs,
		WithSessionReaper(kirosession.New(sessionsDir, testReaperWorkDir),
			func(context.Context) (map[string]struct{}, bool) { return map[string]struct{}{}, true }))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })

	h.DeleteChatStateByChain(t.Context(), "c1", []string{"sess_cur"}, command.RunStopTabClosed)

	if _, err := os.Stat(sessDir); err == nil {
		t.Error("a refused session/delete left the session directory on disk: Reap is the fallback")
	}
}

func TestCloseChatState_DeletesNothing(t *testing.T) {
	h, cs, rig := newDeleteRig(t, kasRuns(t,
		map[string]any{"workflowId": "wf_1", "name": "r", "status": "running", "parentSessionId": "sess_cur"},
	))
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_cur")
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the chat: %v", err)
	}
	insertChatSession(t, h, rig, "c1", "sess_cur")
	leased(t, h.runs, "wf_1")

	h.CloseChatState(t.Context(), "c1")

	if n := len(rig.of(marotte.MethodSessionDelete)); n != 0 {
		t.Errorf("CloseChatState sent %d session/delete, want 0: a close keeps the chat reopenable", n)
	}
	if n := len(rig.of(methodKiroWorkflowDelete)); n != 0 {
		t.Errorf("CloseChatState sent %d workflow/delete, want 0: a close cancels its runs", n)
	}
	if n := len(rig.of(methodKiroWorkflowCancel)); n != 1 {
		t.Errorf("CloseChatState sent %d workflow/cancel, want 1 for the live run", n)
	}
}
