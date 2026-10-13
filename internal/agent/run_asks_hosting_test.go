package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
)

// workflowCallsOffHost is every `_kiro/workflow/*` call a built process got that is not a carrier
// loading session, the run's recorded host; with session empty, every one.
func workflowCallsOffHost(r *spawnRecorder, session string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, b := range r.spawned {
		if opts := b.lastStartOpts(); session != "" && opts != nil && opts.SessionID == session {
			continue
		}
		for _, m := range b.callLog() {
			if strings.HasPrefix(m, "_kiro/workflow/") {
				out = append(out, m)
			}
		}
	}
	return out
}

// recorderHub builds a distinct bridge per spawn, each answering inspect with reply.
func recorderHub(t *testing.T, reply json.RawMessage) (*Runtime, *spawnRecorder) {
	t.Helper()
	rec := &spawnRecorder{results: map[string]json.RawMessage{methodKiroWorkflowInspect: reply}}
	cs := newTestChatStore()
	h := New(context.Background(), "/tmp/work", rec.factory, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, rec
}

// chatParentedHub hosts an agent-launched run the way the user's own chat does: no carrier of its
// own, its launching chat's bridge on the session the lease records.
func chatParentedHub(t *testing.T, l runlease.Lease) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, _ := newTestHub()
	chat := newFakeBridge()
	chat.sessionID = "sess_parent"
	chat.setCallResult(methodKiroWorkflowInspect, recordedReply(t, "parked"))
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: chat, state: bridgeIdle})
	l.Origin = runlease.OriginAgent
	leaseRun(t, h, l)
	return h, chat
}

// The user's case: the run's questions are read through the launching chat's bridge, so the park
// raises the card there, and the parent's send_message plus the step's resumed turn take it down.
func TestRunAsks_AChatParentedRunIsReadThroughItsChatsBridge(t *testing.T) {
	h, chat := chatParentedHub(t, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1", ParentSessionID: "sess_parent"})

	h.chatHandlers[methodWFNodePaused](t.Context(), "c1", runNotif(methodWFNodePaused, map[string]any{
		"workflowId": recordedRun, "nodeId": "ask", "reason": "waiting",
	}))
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 1 })

	chat.setCallResult(methodKiroWorkflowInspect, recordedReply(t, "running-after-parent-send"))
	h.chatHandlers[methodKiroSessionNotify](t.Context(), "c1", runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": recordedSession, "callerSessionId": "sess_parent", "message": "Use main.",
		"severity": "info", "sender": "parent", "workflowId": recordedRun,
	}))
	h.runs.askTurnEdge(recordedRun, recordedSession, translate.TurnEdgeStart)
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 0 })
}

// A lease minted before its run named a parent session learns it from run_start, and the next
// trigger reads through the chat's bridge on it.
func TestRunAsks_RunStartRecordsTheSessionAChatParentedRunIsReadThrough(t *testing.T) {
	h, _ := chatParentedHub(t, runlease.Lease{WorkflowID: recordedRun, ChatID: "c1"})

	h.chatHandlers[methodWFRunStart](t.Context(), "c1", runNotif(methodWFRunStart, map[string]any{
		"workflowId": recordedRun, "workflowName": "asker", "parentSessionId": "sess_parent",
	}))

	if l, _ := h.runs.lease(recordedRun); l.ParentSessionID != "sess_parent" {
		t.Errorf("the lease's parent session = %q, want the run_start's", l.ParentSessionID)
	}
	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 1 })
}

// A leased run whose lease records no launching session names no host: opening it reads nothing,
// through the utility least of all, and says both its state and its asks are unknown.
func TestOpenRun_ALeaseWithNoRecordedParentIsUnreachable(t *testing.T) {
	h, rec := recorderHub(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1"})

	w := httptest.NewRecorder()
	h.runRoutes.handleRun(w, runReq("wf_1"))

	if w.Code != http.StatusBadGateway {
		t.Errorf("GET = %d, want 502: %s", w.Code, w.Body.String())
	}
	var body struct {
		AsksReachable *bool `json:"asks_reachable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.AsksReachable == nil || *body.AsksReachable {
		t.Errorf("body = %s, want asks_reachable false", w.Body.String())
	}
	if p := lastAsksOf(t, h, "wf_1"); p.Reachable {
		t.Error("the run was published reachable although nothing read it")
	}
	if calls := workflowCallsOffHost(rec, ""); len(calls) != 0 {
		t.Errorf("a process got %v, want no workflow call: the utility's run list can stamp it as the run's owner", calls)
	}
}

// A paused run whose process died keeps its question across the re-host: the replacement carrier's
// read finds the same wait under the same id, with no empty snapshot between and no utility read.
func TestRunAsks_ARehostedRunKeepsItsQuestion(t *testing.T) {
	parked := inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step"))
	h, rec := recorderHub(t, parked)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
	old := newFakeBridge()
	old.setCallResult(methodKiroWorkflowInspect, parked)
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: old, state: bridgeIdle})
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeEnd)
	joinInflight(t, h)
	askID := readNow(t, h, "wf_1").asks[0].ID
	h.bridge.mgr.remove(runChatID("wf_1"))

	got, body := getRun(t, h, "wf_1")

	if len(got.OpenAsks) != 1 || got.OpenAsks[0].AskID != askID || !got.AsksReachable {
		t.Errorf("open_asks = %+v reachable %v, want the same wait %s: %s", got.OpenAsks, got.AsksReachable, askID, body)
	}
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Asks[0].AskID != askID {
		t.Errorf("the published snapshot = %+v, want the same wait", p.Asks)
	}
	frames := runAsksFrames(t, h, "wf_1")
	first := slices.IndexFunc(frames, func(f marotte.RunAsksPayload) bool { return len(f.Asks) > 0 })
	for _, f := range frames[max(first, 0):] {
		if len(f.Asks) != 1 || f.Asks[0].AskID != askID {
			t.Errorf("seq %d = %+v: the re-host churned the card", f.Seq, f.Asks)
		}
	}
	if calls := workflowCallsOffHost(rec, testLaunchSession); len(calls) != 0 {
		t.Errorf("a process other than the run's recorded host got %v", calls)
	}
}

// A carrier started to read a run whose read fails is kept, since closing it would kill a run it may
// be driving; it is read again through itself and closed once a read shows the run stopped.
func TestRunAsks_AnUnreadableReadKeepsTheCarrierItStarted(t *testing.T) {
	old := unreadCarrierReread
	unreadCarrierReread = 20 * time.Millisecond
	t.Cleanup(func() { unreadCarrierReread = old })
	spoils := map[string]struct{ spoil, heal func(*fakeBridge) }{
		"the inspect call fails": {
			spoil: func(b *fakeBridge) { b.setCallErr(methodKiroWorkflowInspect, errors.New("broken pipe")) },
			heal: func(b *fakeBridge) {
				b.mu.Lock()
				delete(b.callErrs, methodKiroWorkflowInspect)
				b.mu.Unlock()
			},
		},
		"the reply does not decode": {
			spoil: func(b *fakeBridge) { b.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"state":"paused"}`)) },
			heal:  func(*fakeBridge) {},
		},
	}
	opens := map[string]func(*testing.T, *Runtime){
		"opened by GET": func(_ *testing.T, h *Runtime) { h.runRoutes.handleRun(httptest.NewRecorder(), runReq("wf_1")) },
		"read at boot":  func(t *testing.T, h *Runtime) { h.runs.askBootPass(t.Context()) },
	}
	for spoilName, s := range spoils {
		for openName, open := range opens {
			t.Run(spoilName+" "+openName, func(t *testing.T) {
				var (
					mu    sync.Mutex
					built []*fakeBridge
				)
				cs := newTestChatStore()
				h := New(context.Background(), "/tmp/work", func() ACPBridge {
					b := newFakeBridge()
					s.spoil(b)
					mu.Lock()
					built = append(built, b)
					mu.Unlock()
					return b
				}, cs)
				cs.wire(h)
				t.Cleanup(func() { shutdownHub(t, h) })
				leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})

				open(t, h)

				sb := h.bridge.mgr.get(runChatID("wf_1"))
				if sb == nil {
					t.Fatal("the carrier was closed on a read that showed nothing about the run")
				}
				carrier, ok := sb.current().(*fakeBridge)
				if !ok {
					t.Fatalf("Setup: the carrier is a %T", sb.current())
				}
				inspects := func() int {
					return len(slices.DeleteFunc(carrier.callLog(), func(m string) bool { return m != methodKiroWorkflowInspect }))
				}
				waitFor(t, func() bool { return inspects() >= 3 })
				if h.bridge.mgr.get(runChatID("wf_1")) != sb {
					t.Fatal("a re-read that still showed nothing closed the carrier")
				}

				s.heal(carrier)
				carrier.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
				waitFor(t, func() bool { return h.bridge.mgr.get(runChatID("wf_1")) == nil })
				if p := lastAsksOf(t, h, "wf_1"); !p.Reachable || len(p.Asks) != 1 {
					t.Errorf("after the readable re-read = %+v, want its question published", p)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(built) != 1 {
					t.Errorf("%d processes built, want the one carrier", len(built))
				}
			})
		}
	}
}

// KAS answering it has no such run settles what a carrier started for the read is for: it is
// closed at once, and nothing reads the run again.
func TestRunAsks_ACarrierStartedForARunKASDoesNotKnowIsClosed(t *testing.T) {
	old := unreadCarrierReread
	unreadCarrierReread = 10 * time.Millisecond
	t.Cleanup(func() { unreadCarrierReread = old })
	for name, open := range map[string]func(*testing.T, *Runtime){
		"opened by GET": func(_ *testing.T, h *Runtime) { h.runRoutes.handleRun(httptest.NewRecorder(), runReq("wf_1")) },
		"read at boot":  func(t *testing.T, h *Runtime) { h.runs.askBootPass(t.Context()) },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &spawnRecorder{errs: map[string]error{methodKiroWorkflowInspect: &marotte.RPCError{
				Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
			}}}
			cs := newTestChatStore()
			h := New(context.Background(), "/tmp/work", rec.factory, cs)
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})

			open(t, h)

			waitFor(t, func() bool { return h.bridge.mgr.get(runChatID("wf_1")) == nil })
			_, before := rec.sawCall(methodKiroWorkflowInspect)
			if before == 0 {
				t.Fatal("Setup: no read reached a carrier")
			}
			time.Sleep(20 * unreadCarrierReread)
			if _, after := rec.sawCall(methodKiroWorkflowInspect); after != before {
				t.Errorf("%d inspects after KAS said the run does not exist, want none", after-before)
			}
			if p := lastAsksOf(t, h, "wf_1"); !p.Reachable || len(p.Asks) != 0 {
				t.Errorf("snapshot = %+v, want no asks, reachable", p)
			}
		})
	}
}

// The kept-carrier bound decides a leased run from a read through the carrier itself, never the
// utility, whose read can stamp it as the run's owner. The utility here would call every run
// running, so a bound reading it would spare each carrier.
func TestCloseKeptCarrier_ReadsALeasedRunThroughTheCarrier(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*fakeBridge)
		want carrierVerdict
	}{
		"a paused run's carrier is closed": {
			set: func(b *fakeBridge) {
				b.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
			},
			want: carrierClosed,
		},
		"a run KAS does not know is closed": {
			set: func(b *fakeBridge) {
				b.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{
					Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
				})
			},
			want: carrierClosed,
		},
		"an unreadable run's carrier is spared": {
			set:  func(b *fakeBridge) { b.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"state":"paused"}`)) },
			want: carrierSpared,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, rec := recorderHub(t, inspectReply(t, "wf_1", "running", ""))
			leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
			carrier := newFakeBridge()
			tc.set(carrier)
			kept := &sharedBridge{bridge: carrier, state: bridgeIdle}
			h.bridge.mgr.insert(runChatID("wf_1"), kept)

			if got := h.runs.closeKeptCarrier(runChatID("wf_1"), "wf_1", kept); got != tc.want {
				t.Errorf("closeKeptCarrier = %v, want %v", got, tc.want)
			}
			if calls := workflowCallsOffHost(rec, ""); len(calls) != 0 {
				t.Errorf("a process other than the kept carrier got %v", calls)
			}
		})
	}
}

// The re-reads of a kept carrier end with the carrier or the lease, not only with a readable read.
func TestRunAsks_AKeptCarrierIsNoLongerReadOnceGone(t *testing.T) {
	old := unreadCarrierReread
	unreadCarrierReread = 10 * time.Millisecond
	t.Cleanup(func() { unreadCarrierReread = old })
	for name, end := range map[string]func(*Runtime){
		"the carrier closed elsewhere": func(h *Runtime) {
			h.coord.closeBridge(context.Background(), runChatID("wf_1"), marotte.TurnOutcomeInterrupted)
		},
		"the lease ended": func(h *Runtime) { h.runs.releaseLease(context.Background(), "wf_1") },
	} {
		t.Run(name, func(t *testing.T) {
			h, rec := recorderHub(t, json.RawMessage(`{"state":"paused"}`))
			leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
			h.runRoutes.handleRun(httptest.NewRecorder(), runReq("wf_1"))
			waitFor(t, func() bool { _, n := rec.sawCall(methodKiroWorkflowInspect); return n >= 2 })

			end(h)
			_, before := rec.sawCall(methodKiroWorkflowInspect)
			time.Sleep(20 * unreadCarrierReread)

			// One read may have been in flight when the end landed.
			if _, after := rec.sawCall(methodKiroWorkflowInspect); after > before+1 {
				t.Errorf("%d inspects after the end, want none: the re-read outlived what it reads for", after-before)
			}
		})
	}
}

// One run's wedged inspect blocks nothing on another run: its read, publish and verbs go through.
func TestRunAsks_AWedgedRunBlocksNoOtherRun(t *testing.T) {
	h, wedged, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	other := newFakeBridge()
	other.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_2", marotte.RunStatusPaused, parkedStep("review", "sess_two")))
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_2", ParentSessionID: testLaunchSession})
	h.bridge.mgr.insert(runChatID("wf_2"), &sharedBridge{bridge: other, state: bridgeIdle})
	askID := readNow(t, h, "wf_1").asks[0].ID
	stuck := make(chan struct{})
	wedged.mu.Lock()
	wedged.blockOn = map[string]chan struct{}{methodKiroWorkflowInspect: stuck}
	wedged.mu.Unlock()
	answered := make(chan error, 1)
	go func() { answered <- h.runs.answerInput(context.Background(), "wf_1", askID, "teal") }()
	t.Cleanup(func() { close(stuck); <-answered })
	waitFor(t, func() bool {
		return len(slices.DeleteFunc(wedged.callLog(), func(m string) bool { return m != methodKiroWorkflowInspect })) == 2
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	r, err := h.runs.readAsks(ctx, h.runs.cellFor("wf_2"), h.runs.askHost("wf_2"))
	if err != nil || len(r.asks) != 1 {
		t.Fatalf("the other run's read = %d asks, %v; want its question", len(r.asks), err)
	}
	if p := lastAsksOf(t, h, "wf_2"); !p.Reachable || len(p.Asks) != 1 {
		t.Errorf("the other run's snapshot = %+v, want its question published", p)
	}
	if err := h.runs.pause(ctx, "wf_2"); err != nil {
		t.Errorf("a pause of the other run = %v, want it sent", err)
	}
	if !slices.Contains(other.callLog(), methodKiroWorkflowPause) {
		t.Error("the other run's pause never reached its process")
	}
}
