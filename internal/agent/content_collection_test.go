package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func newContentCollectionHub(t *testing.T, br *fakeBridge, opts ...Option) (*Runtime, *testChatStore) {
	t.Helper()
	dir := writeSettings(t, `{"content_collection_enabled":true}`)
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(func() engineSession { return br }, br), cs,
		append([]Option{WithConfigDir(dir)}, opts...)...)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, cs
}

type sessionDoor struct {
	open func(t *testing.T, h *Runtime, cs *testChatStore, br *fakeBridge)
	name string
}

// sessionDoors is kept by hand and nothing checks it is complete: a new production door is covered only once it is
// added here.
func sessionDoors() []sessionDoor {
	workflowReplies := map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}
	return []sessionDoor{
		{name: "chat_new", open: func(t *testing.T, h *Runtime, cs *testChatStore, _ *fakeBridge) {
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
		}},
		{name: "chat_load", open: func(t *testing.T, h *Runtime, cs *testChatStore, _ *fakeBridge) {
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = "sess_resume_probe"
				return true
			})
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
		}},
		{name: "host_open", open: func(t *testing.T, h *Runtime, _ *testChatStore, br *fakeBridge) {
			for method, reply := range workflowReplies {
				br.setCallResult(method, reply)
			}
			br.setCallResult(methodKiroWorkflowList, json.RawMessage(`{"runs":[]}`))
			if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
				t.Fatalf("Launch: %v", err)
			}
		}},
		{name: "host_load", open: func(t *testing.T, h *Runtime, _ *testChatStore, _ *fakeBridge) {
			leaseParentless(t, h, "wf_1")
			if _, err := h.runs.ensureResident(t.Context(), "wf_1"); err != nil {
				t.Fatalf("ensureResident: %v", err)
			}
		}},
	}
}

// Every session door asserts content collection, since KAS holds the value per process and persists none of it; the
// resolver reads the lock at each assert.
func TestSessionDoors_AssertContentCollection(t *testing.T) {
	for _, door := range sessionDoors() {
		t.Run(door.name, func(t *testing.T) {
			br := newFakeBridge()
			h, cs := newContentCollectionHub(t, br)
			door.open(t, h, cs, br)

			br.mu.Lock()
			applied, known, opts := br.contentCollectionApplied, br.contentCollectionKnown, br.engineOpts
			br.mu.Unlock()
			if !applied || !known {
				t.Errorf("%s door: content collection applied = %v known = %v, want both", door.name, applied, known)
			}
			if opts == nil || opts.ContentCollection == nil {
				t.Fatalf("%s door: the engine carries no content-collection resolver", door.name)
			}
			h.config.SetGovernance(t.Context(), *enterpriseProfile())
			if on, _ := opts.ContentCollection(t.Context()); on {
				t.Errorf("%s resolver under an enterprise lock = true, want false (the lock is read at each assert)", door.name)
			}
		})
	}
}

func contentCollectionWrites(b *fakeBridge) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.contentCollectionWrites...)
}

// A lock change reaches the engine whatever config.json holds: the lock is the answer, and every session shares the
// one process KAS holds the value in. A refresh asserts it on the running engine, or a restart's next door does.
func TestGovernanceLockChange_ReachesTheEngineReadableOrNot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
	}{
		{name: "readable_document"},
		{name: "unreadable_document", document: "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane := newFakeBridge()
			pushed := make(chan struct{}, 1)
			h, cs := newContentCollectionHub(t, lane,
				WithGovernanceLocksHook(func(context.Context) { pushed <- struct{}{} }))
			if !awaitEngine(h) {
				t.Fatal("Setup: the engine never became ready")
			}
			before := len(contentCollectionWrites(lane))
			if tc.document != "" {
				rewriteConfigByHand(t, h.lifecycle.configDir, tc.document)
			}

			// Usage analytics left on, so the lock itself moves no engine-wide value.
			h.config.SetGovernance(t.Context(), orgTelemetry(true))
			select {
			case <-pushed:
			case <-time.After(10 * time.Second):
				t.Fatal("the lock change never finished its push")
			}
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			openChat(t, h, "c1")

			if writes := contentCollectionWrites(lane)[before:]; len(writes) > 1 || slices.Contains(writes, marotte.ConfigValueContentCollectionEnabled) {
				t.Errorf("engine contentCollection writes after the lock = %q, want at most one %q", writes, marotte.ConfigValueContentCollectionDisabled)
			}
			lane.mu.Lock()
			applied, known := lane.contentCollectionApplied, lane.contentCollectionKnown
			lane.mu.Unlock()
			if applied || !known {
				t.Errorf("the engine holds content collection applied = %v known = %v, want the organization's off, confirmed", applied, known)
			}
		})
	}
}
