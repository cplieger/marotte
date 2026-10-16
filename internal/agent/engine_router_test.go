package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
)

// TestRouter_StartJournalKeepsOnlyRunsStillGoing: a run the read loop saw start is in its generation's snapshot
// until it completes or pauses, and a start after the pause journals it again.
func TestRouter_StartJournalKeepsOnlyRunsStillGoing(t *testing.T) {
	const gen = 1
	frame := func(method, wf string) *bridge.FrameHead {
		return &bridge.FrameHead{Method: method, WorkflowID: wf, SessionID: marotte.SessionID("sess_step_" + wf)}
	}
	r := newEngineRouter()
	for _, wf := range []string{"wf_running", "wf_paused", "wf_done"} {
		r.route(frame(methodWFRunStart, wf), gen)
		r.route(frame(methodWFNodeStart, wf), gen)
	}
	r.route(frame(methodWFPaused, "wf_paused"), gen)
	r.route(frame(methodWFRunComplete, "wf_done"), gen)
	if got := r.peekStarted(gen); !slices.Equal(got, []string{"wf_running"}) {
		t.Errorf("journal after a pause and a completion = %v, want [wf_running]", got)
	}
	r.route(frame(methodWFNodeStart, "wf_paused"), gen)
	if got := r.takeStarted(gen); !slices.Equal(got, []string{"wf_paused", "wf_running"}) {
		t.Errorf("journal after the paused run's next start = %v, want [wf_paused wf_running]", got)
	}
	if got := r.peekStarted(gen); len(got) != 0 {
		t.Errorf("journal after takeStarted = %v, want it forgotten", got)
	}
}

func TestRouter_StepFramesOfAChatParentedRunReachThatChatsLane(t *testing.T) {
	const chatSession, step marotte.SessionID = "sess_chat", "sess_step"
	r := newEngineRouter()
	if got := r.route(&bridge.FrameHead{Method: methodWFRunStart, WorkflowID: "wf_1", ParentSessionID: chatSession}, 1); got != chatSession {
		t.Errorf("run_start routed to %q, want the parent chat's lane %q", got, chatSession)
	}
	if got := r.route(&bridge.FrameHead{Method: methodWFNodeStart, WorkflowID: "wf_1", SessionID: step}, 1); got != chatSession {
		t.Errorf("node_start routed to %q, want %q", got, chatSession)
	}
	for _, h := range []*bridge.FrameHead{
		{Method: marotte.MethodSessionUpdate, SessionID: step},
		{Method: marotte.MethodRequestPermission, SessionID: step},
	} {
		if got := r.route(h, 1); got != chatSession {
			t.Errorf("the step's %s routed to %q, want the chat's lane %q", h.Method, got, chatSession)
		}
	}
}

func TestRouter_OwnerlessRunFramesUseTheEngineLaneWithEmptyChat(t *testing.T) {
	r := newEngineRouter()
	for _, h := range []*bridge.FrameHead{
		{Method: methodWFRunStart, WorkflowID: "wf_1"},
		{Method: methodWFNodeStart, WorkflowID: "wf_1", SessionID: "sess_step"},
		{Method: marotte.MethodSessionUpdate, SessionID: "sess_step"},
	} {
		if got := r.route(h, 1); got != "" {
			t.Errorf("%s of a run with no owner routed to %q, want the engine lane", h.Method, got)
		}
	}
	if wf, ok := r.StepRun("sess_step"); !ok || wf != "wf_1" {
		t.Errorf("StepRun(sess_step) = %q, %v, want wf_1: the engine lane still names the step's run", wf, ok)
	}
}

func TestRouter_AnUnknownDifferingSessionGoesToTheEngineLane(t *testing.T) {
	r := newEngineRouter()
	if got := r.route(&bridge.FrameHead{Method: marotte.MethodRequestPermission, SessionID: "sess_stranger"}, 1); got != "" {
		t.Fatalf("a request naming no known session routed to %q, want the engine lane", got)
	}
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	src := fakeSourceOf(t, h)
	id := int64(41)
	params, _ := json.Marshal(map[string]any{"sessionId": "sess_stranger", "toolCall": map[string]any{"toolCallId": "t1"}})
	before := len(src.lane.answeredIDs())
	h.engineFrame(src.current(), &marotte.RPCResponse{Method: marotte.MethodRequestPermission, ID: &id, Params: params})
	if got := src.lane.answeredIDs()[before:]; !slices.Equal(got, []int64{id}) {
		t.Errorf("answers on the engine lane = %v, want exactly one for request %d", got, id)
	}
}

// Under -race: the router's tables are written by verbs while the read loop routes.
func TestRouter_ConcurrentMutationAndRoute(t *testing.T) {
	r := newEngineRouter()
	var wg sync.WaitGroup
	const n = 200
	wg.Go(func() {
		for i := range n {
			wf := fmt.Sprintf("wf_%d", i%7)
			r.registerOwner(wf, "sess_host")
			r.seedSteps(wf, []string{"sess_step_" + wf})
			if i%3 == 0 {
				r.forgetWorkflow(wf)
			}
		}
	})
	wg.Go(func() {
		for i := range n {
			wf := fmt.Sprintf("wf_%d", i%7)
			for _, h := range []*bridge.FrameHead{
				{Method: methodWFNodeStart, WorkflowID: wf, SessionID: marotte.SessionID("sess_step_" + wf)},
				{Method: marotte.MethodSessionUpdate, SessionID: marotte.SessionID("sess_step_" + wf)},
			} {
				if got := r.route(h, 1); got != "" && got != "sess_host" {
					t.Errorf("a frame routed to %q, want the owner or the engine lane", got)
				}
			}
		}
	})
	wg.Wait()
	r.forgetWorkflow("wf_0")
	if got := r.route(&bridge.FrameHead{Method: marotte.MethodSessionUpdate, SessionID: "sess_step_wf_0"}, 1); got != "" {
		t.Errorf("a forgotten run's step frame routed to %q, want the engine lane", got)
	}
}

// launchAs makes the host session the run's owner before invoke is written, so a frame KAS emits the instant it
// reads invoke routes to the host's lane.
func TestRouter_OwnerRegisteredBeforeInvokeIsVisibleToItsFirstFrame(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	var routed atomic.Value
	br.mu.Lock()
	br.onCall = func(method string, p map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method == methodKiroWorkflowInvoke {
			wf, _ := p[keyWorkflowID].(string)
			routed.Store(h.router.route(&bridge.FrameHead{Method: methodWFNodeStart, WorkflowID: wf, SessionID: "sess_step"}, 1))
		}
		return nil, nil, false
	}
	br.mu.Unlock()
	launchableRecipe(br, "wf_9")

	wf, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	got, _ := routed.Load().(marotte.SessionID)
	if want := h.router.ownerOf(wf); got == "" || got != want {
		t.Errorf("the frame read with invoke routed to %q, want the host session %q", got, want)
	}
}

// peekStarted is generation gen's journalled starts, kept.
func (r *engineRouter) peekStarted(gen uint64) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(maps.Keys(r.started[gen]))
}

// ownerOf is the session workflowID's frames route to, "" for the engine lane.
func (r *engineRouter) ownerOf(workflowID string) marotte.SessionID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner[workflowID]
}
