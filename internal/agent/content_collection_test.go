package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func newContentCollectionHub(t *testing.T, br *fakeBridge, opts ...Option) (*Runtime, *testChatStore) {
	t.Helper()
	dir := writeSettings(t, `{"content_collection_enabled":true}`)
	cs := newTestChatStore()
	// Cold: these tests set the account's profile themselves, which a door's own profile would overwrite.
	src := fakeEngineOf(func() engineSession { return br }, br)
	src.noGovernance = true
	h := newRuntimeOn(context.Background(), "/tmp/work", src, cs, append([]Option{WithConfigDir(dir)}, opts...)...)
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
			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
		}},
		{name: "chat_load", open: func(t *testing.T, h *Runtime, cs *testChatStore, _ *fakeBridge) {
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = "sess_resume_probe"
				return true
			})
			if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
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
			if _, err := h.runs.residentReleased(t.Context(), "wf_1"); err != nil {
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
				WithGovernanceLocksHook(func(context.Context) {
					select {
					case pushed <- struct{}{}:
					default:
					}
				}))
			settleBoot(t, h)
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

// shareRecorder is a source that records every kiro-cli content-collection write, failing each while fail is set.
type shareRecorder struct {
	noEngine
	fail   error
	writes []bool
}

func (r *shareRecorder) ShareContent(_ context.Context, enabled bool) error {
	r.writes = append(r.writes, enabled)
	return r.fail
}

func TestKiroShare_WritesWhatMovedAndEverySpawn(t *testing.T) {
	src := &shareRecorder{}
	enabled, readable := false, true
	k := kiroShare{resolve: func(context.Context) (bool, bool) { return enabled, readable }}
	step := func(what string, force bool, want ...bool) {
		t.Helper()
		before := len(src.writes)
		k.assert(t.Context(), src, force)
		if got := src.writes[before:]; !slices.Equal(got, want) {
			t.Errorf("%s: kiro-cli writes = %v, want %v", what, got, want)
		}
	}
	step("first sync", false, false)
	step("unchanged sync", false)
	step("spawn", true, false)
	enabled = true
	step("sync after the switch moved", false, true)
	enabled, readable = false, false
	step("sync over an unreadable document", false)
	step("spawn over an unreadable document", true, false)
	enabled, readable = true, true
	src.fail = errors.New("kiro-cli down")
	step("failed write", false, true)
	src.fail = nil
	step("sync after a failed write", false, true)
	step("unchanged sync after the retry landed", false)
}

// sharingEngineSource is a fake engine that also writes kiro-cli's switch, recording how many engines existed then.
type sharingEngineSource struct {
	*fakeEngineSource
	mu       sync.Mutex
	writes   []bool
	atEngine []int
}

var _ ContentSharer = (*sharingEngineSource)(nil)

func (s *sharingEngineSource) ShareContent(_ context.Context, enabled bool) error {
	s.fakeEngineSource.mu.Lock()
	engines := len(s.engines)
	s.fakeEngineSource.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, enabled)
	s.atEngine = append(s.atEngine, engines)
	return nil
}

func (s *sharingEngineSource) recorded() (writes []bool, atEngine []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes), slices.Clone(s.atEngine)
}

// The relay copies kiro-cli's switch into the engine it spawns, so marotte's value must be there before the spawn.
func TestEngineSpawn_WritesKiroCLIsContentSwitchFirst(t *testing.T) {
	dir := t.TempDir()
	src := &sharingEngineSource{fakeEngineSource: fakeEngineOf(nil, nil)}
	cs := newTestChatStore()
	h := New(context.Background(), "/tmp/work", src, cs, WithConfigDir(dir))
	src.fakeEngineSource.mu.Lock()
	src.rt = h
	src.fakeEngineSource.mu.Unlock()
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	if bootComing(h) {
		if err := awaitBoot(h); err != nil {
			t.Fatalf("Setup: boot: %v", err)
		}
	}
	if !awaitEngine(h) {
		t.Fatal("Setup: the engine never became ready")
	}
	writes, atEngine := src.recorded()
	if !slices.Equal(writes, []bool{false}) || !slices.Equal(atEngine, []int{0}) {
		t.Fatalf("at the first spawn kiro-cli writes = %v at engine counts %v, want [false] before any engine", writes, atEngine)
	}

	rewriteConfigByHand(t, dir, `{"content_collection_enabled":true}`)
	h.ReconcileSessionSettings(t.Context())
	h.ReconcileSessionSettings(t.Context())
	if writes, _ := src.recorded(); !slices.Equal(writes, []bool{false, true}) {
		t.Errorf("after the user opted in and two reconciles kiro-cli writes = %v, want [false true]", writes)
	}
}
