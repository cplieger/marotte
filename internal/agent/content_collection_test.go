package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func newContentCollectionHub(t *testing.T, factory ACPBridgeFactory, opts ...Option) (*Runtime, *testChatStore) {
	t.Helper()
	dir := writeSettings(t, `{"content_collection_enabled":true}`)
	cs := newTestChatStore()
	h := New(context.Background(), "/tmp/work", factory, cs, append([]Option{WithConfigDir(dir)}, opts...)...)
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, cs
}

type spawnSite struct {
	spawn func(t *testing.T, h *Runtime, cs *testChatStore, br *fakeBridge)
	name  string
}

// spawnSites is kept by hand and nothing checks it is complete: a new production spawn site is
// covered by the per-spawn resolver tests only once it is added here.
func spawnSites() []spawnSite {
	workflowReplies := map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}
	return []spawnSite{
		{name: "chat_new", spawn: func(t *testing.T, h *Runtime, cs *testChatStore, _ *fakeBridge) {
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
			if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
		}},
		{name: "chat_load", spawn: func(t *testing.T, h *Runtime, cs *testChatStore, _ *fakeBridge) {
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = "sess_resume_probe"
				return true
			})
			if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
		}},
		{name: "run_launch", spawn: func(t *testing.T, h *Runtime, _ *testChatStore, br *fakeBridge) {
			br.callResults = workflowReplies
			br.callResults[methodKiroWorkflowList] = json.RawMessage(`{"runs":[]}`)
			if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
				t.Fatalf("Launch: %v", err)
			}
		}},
		{name: "run_rehost", spawn: func(t *testing.T, h *Runtime, _ *testChatStore, br *fakeBridge) {
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": "paused",
					"parentSessionId": "sess_old_run_bridge",
				}),
			}
			host, err := h.runs.hostRun(t.Context(), "wf_1")
			if err != nil {
				t.Fatalf("hostRun: %v", err)
			}
			host.release(nil)
		}},
		{name: "utility", spawn: func(t *testing.T, h *Runtime, _ *testChatStore, _ *fakeBridge) {
			if _, err := h.utility.get().session.acquire(t.Context()); err != nil {
				t.Fatalf("acquire utility session: %v", err)
			}
		}},
	}
}

func TestSpawnSites_CarryContentCollection(t *testing.T) {
	for _, site := range spawnSites() {
		t.Run(site.name, func(t *testing.T) {
			br := newFakeBridge()
			h, cs := newContentCollectionHub(t, func() ACPBridge { return br })
			site.spawn(t, h, cs, br)

			opts := br.lastStartOpts()
			if opts == nil || opts.ContentCollection == nil {
				t.Fatalf("%s spawn: StartOpts.ContentCollection = nil, want the resolver", site.name)
			}
			if on, _ := opts.ContentCollection(t.Context()); !on {
				t.Errorf("%s resolver with the switch stored on = false, want true", site.name)
			}
			h.config.SetGovernance(t.Context(), *enterpriseProfile())
			if on, _ := opts.ContentCollection(t.Context()); on {
				t.Errorf("%s resolver under an enterprise lock = true, want false (the lock is read at each assert)", site.name)
			}
		})
	}
}

func contentCollectionWrites(b *fakeBridge) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.contentCollectionWrites...)
}

// A lock change reaches every running process with no restart, config.json readable or not: the lock is the answer.
func TestGovernanceLockChange_PushesContentCollectionToEveryLiveProcess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
	}{
		{name: "readable_document"},
		{name: "unreadable_document", document: "{not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			utility := newFakeBridge()
			pushed := make(chan struct{}, 1)
			h, _ := newContentCollectionHub(t, func() ACPBridge { return utility },
				WithGovernanceLocksHook(func(context.Context) { pushed <- struct{}{} }))
			if _, err := h.utility.get().session.acquire(t.Context()); err != nil {
				t.Fatalf("acquire utility session: %v", err)
			}

			resolver := contentCollectionResolver(h.lifecycle.configDir, h.config.GovernanceLocks)
			live := map[marotte.ChatID]*fakeBridge{"c1": newFakeBridge(), "c2": newFakeBridge(), runChatID("wf_1"): newFakeBridge()}
			for id, br := range live {
				if err := br.Start(t.Context(), &marotte.StartOpts{Lifetime: t.Context(), ContentCollection: resolver}); err != nil {
					t.Fatalf("Start %s: %v", id, err)
				}
				h.bridge.mgr.insert(id, &sharedBridge{bridge: br, state: bridgeIdle})
			}
			live["utility"] = utility
			if tc.document != "" {
				rewriteConfigByHand(t, h.lifecycle.configDir, tc.document)
			}

			h.config.SetGovernance(t.Context(), *enterpriseProfile())
			select {
			case <-pushed:
			case <-time.After(10 * time.Second):
				t.Fatal("the lock change never finished its push")
			}
			for id, br := range live {
				writes := contentCollectionWrites(br)
				if len(writes) == 0 || writes[len(writes)-1] != marotte.ConfigValueContentCollectionDisabled {
					t.Errorf("%s contentCollection writes = %q, want the organization's %q last", id, writes, marotte.ConfigValueContentCollectionDisabled)
				}
			}
		})
	}
}
