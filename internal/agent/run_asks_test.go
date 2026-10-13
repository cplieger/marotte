package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// The recorded kiro-cli 2.29.1 run the workflow package's fixtures come from.
const (
	recordedRun     = "wf_66658d5c69b1f284"
	recordedSession = "sess_c8ebb89e-a1ac-41da-aeaa-1aaa24762563"
)

func recordedReply(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "workflow", "testdata", "inspect", name+".json"))
	if err != nil {
		t.Fatalf("Setup: fixture %s: %v", name, err)
	}
	return raw
}

// askCoreHub is a hub whose run wf_1 is leased and hosted by its own carrier, with a push that
// records retractions.
func askCoreHub(t *testing.T, workflowID string, reply json.RawMessage) (*Runtime, *fakeBridge, *askPush) {
	t.Helper()
	cs := newTestChatStore()
	br := newFakeBridge()
	fp := &askPush{}
	h := New(context.Background(), "/tmp/work", func() ACPBridge { return br }, cs, WithPush(fp))
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	leaseRun(t, h, runlease.Lease{WorkflowID: workflowID, ParentSessionID: testLaunchSession})
	h.bridge.mgr.insert(runChatID(workflowID), &sharedBridge{bridge: br, state: bridgeIdle})
	if reply != nil {
		br.setCallResult(methodKiroWorkflowInspect, reply)
	}
	return h, br, fp
}

// askPush records every notification's subject and every retraction, from any goroutine.
type askPush struct {
	sent, retracted []marotte.PushSubject
	mu              sync.Mutex
}

func (*askPush) HasSubscribers() bool                     { return true }
func (*askPush) SetPreferences(map[marotte.PushKind]bool) {}
func (*askPush) Preferences() map[marotte.PushKind]bool   { return nil }
func (*askPush) Close()                                   {}
func (p *askPush) Retract(subj marotte.PushSubject) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retracted = append(p.retracted, subj)
}

func (p *askPush) Send(_ context.Context, n *marotte.NotificationPayload) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, n.PushSubject)
}

func retracted(fp *askPush) []marotte.PushSubject {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return slices.Clone(fp.retracted)
}

func TestRunAsks_ACellLivesExactlyAsLongAsItsLease(t *testing.T) {
	h, _, fp := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	if h.runs.cellFor("wf_unleased") != nil {
		t.Error("a run with no lease got a cell, so a TUI-launched run would show a card nobody can answer")
	}
	first := lastAsksOf(t, h, "wf_1")
	if first.Reachable || len(first.Asks) != 0 || first.ChatID != runChatID("wf_1") || first.Epoch != h.Epoch() {
		t.Errorf("a new cell = %+v, want no asks, unreachable, its envelope set", first)
	}
	r := readNow(t, h, "wf_1")
	if len(r.asks) != 1 {
		t.Fatalf("Setup: the read found %d asks, want 1", len(r.asks))
	}

	h.runs.releaseLease(t.Context(), "wf_1")

	frames := runAsksFrames(t, h, "wf_1")
	final := frames[len(frames)-1]
	if len(final.Asks) != 0 || !final.Reachable {
		t.Errorf("the release's frame = %+v, want no asks, reachable: the run waits on nobody", final)
	}
	if h.runs.asks.get("wf_1") != nil || len(h.runs.asks.list()) != 0 {
		t.Error("a released run is still named, so every reconnect keeps its card")
	}
	want := marotte.RunAskSubject(r.asks[0].Position)
	if !slices.Contains(retracted(fp), want) {
		t.Errorf("retracted = %v, want the wait's own subject %v", retracted(fp), want)
	}
}

// A cell rebuilt for the same run after its lease ended and came back must stamp newer snapshots
// than the old cell's last, or a client holding that one drops every new frame as stale.
func TestRunAsks_SeqGrowsAcrossARebuiltCell(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	readNow(t, h, "wf_1")
	h.runs.releaseLease(t.Context(), "wf_1")
	frames := runAsksFrames(t, h, "wf_1")
	ended := frames[len(frames)-1].Seq

	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
	if got := lastAsksOf(t, h, "wf_1").Seq; got <= ended {
		t.Errorf("the rebuilt cell's first seq = %d, want above the ended cell's %d", got, ended)
	}
	readNow(t, h, "wf_1")
	frames = runAsksFrames(t, h, "wf_1")
	if got := frames[len(frames)-1].Seq; got <= ended {
		t.Errorf("the rebuilt cell's frame seq = %d, want above %d", got, ended)
	}
}

// A trigger racing the lease's release never leaves a cell behind it, in either order.
func TestRunAsks_ACreateRacingTheEndLeavesNoCell(t *testing.T) {
	for i := range 50 {
		rs := &Runs{}
		if err := rs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf_1", Origin: runlease.OriginManual}); err != nil {
			t.Fatalf("Setup: lease: %v", err)
		}
		if i%2 == 0 {
			rs.cellFor("wf_1")
		}
		// Hold the cells' lock so the release and the trigger both queue on it.
		rs.asks.mu.Lock()
		var wg sync.WaitGroup
		wg.Go(func() { rs.releaseLease(t.Context(), "wf_1") })
		waitFor(t, func() bool { _, held := rs.lease("wf_1"); return !held })
		wg.Go(func() { rs.cellFor("wf_1") })
		rs.asks.mu.Unlock()
		wg.Wait()
		if rs.asks.get("wf_1") != nil || len(rs.asks.list()) != 0 {
			t.Fatalf("iteration %d: a cell outlived its lease", i)
		}
	}
}

func TestRunAsks_ASeededCellIsUnreachableUntilRead(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.runs.asks.seed(h.runs.leaseStore().List())
	if p := lastAsksOf(t, h, "wf_1"); p.Reachable {
		t.Error("a seeded cell reads reachable, so a client closes the run's banners before marotte read it")
	}
	readNow(t, h, "wf_1")
	if p := lastAsksOf(t, h, "wf_1"); !p.Reachable || len(p.Asks) != 1 {
		t.Errorf("after the read = %+v, want the ask, reachable", p)
	}
}

// A trigger with no live host reads nothing and keeps the last snapshot, so the card stays
// answerable.
func TestRunAsks_AHostlessTriggerKeepsTheLastSnapshot(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	readNow(t, h, "wf_1")
	before := lastAsksOf(t, h, "wf_1")
	h.bridge.mgr.remove(runChatID("wf_1"))

	c := h.runs.asks.get("wf_1")
	if _, err := h.runs.refreshCell(t.Context(), c, nil); !errors.Is(err, errNoAskHost) {
		t.Fatalf("a hostless refresh = %v, want errNoAskHost", err)
	}
	if after := lastAsksOf(t, h, "wf_1"); after.Seq != before.Seq {
		t.Errorf("a hostless trigger published %+v, want the last snapshot kept", after)
	}
}

// An unreadable reply is "unknown", never "answered": the asks stay and the run reads unreachable.
func TestRunAsks_AnUndecodableReplyKeepsTheAsks(t *testing.T) {
	h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	readNow(t, h, "wf_1")
	br.setCallResult(methodKiroWorkflowInspect, json.RawMessage(`{"workflowId":"wf_1","state":{"workflowId":"wf_1","status":7}}`))

	c := h.runs.asks.get("wf_1")
	if _, err := h.runs.readAsks(t.Context(), c, h.runs.askHost("wf_1")); err == nil {
		t.Fatal("an undecodable reply read as fine")
	}
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Reachable {
		t.Errorf("after an undecodable reply = %+v, want the ask kept, unreachable", p)
	}
}

// A cell made mid-run starts its quiet clock at creation, so its first read is settling, and only
// a settle window with nothing moving turns that final.
func TestRunAsks_SettlingTurnsUnsettledOnlyAfterTheQuietWindow(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusRunning, parkedStep("review", "sess_step")))
	if got := readNow(t, h, "wf_1").asks[0].Wait; got != workflow.BlockSettling {
		t.Fatalf("a fresh cell's first read = %q, want settling", got)
	}
	c := h.runs.asks.get("wf_1")
	c.mu.Lock()
	c.lastMotion = time.Now().Add(-settleQuiet)
	c.mu.Unlock()
	if got := readNow(t, h, "wf_1").asks[0].Wait; got != workflow.BlockUnsettled {
		t.Errorf("after the quiet window = %q, want unsettled, so an agent retrying /answer gets a final code", got)
	}
}

// settling becomes final with no frame and no request: the quiet window's own timer re-reads.
func TestRunAsks_SettlingTurnsUnsettledOnItsOwnTimer(t *testing.T) {
	old := settleQuiet
	settleQuiet = 200 * time.Millisecond
	t.Cleanup(func() { settleQuiet = old })
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusRunning, parkedStep("review", "sess_step")))
	c := h.runs.cellFor("wf_1")
	c.mu.Lock()
	c.lastMotion = time.Now()
	c.mu.Unlock()
	if got := readNow(t, h, "wf_1").asks[0].Wait; got != workflow.BlockSettling {
		t.Fatalf("Setup: the first read = %q, want settling", got)
	}
	waitFor(t, func() bool {
		p := lastAsksOf(t, h, "wf_1")
		return len(p.Asks) == 1 && p.Asks[0].Blocked == marotte.RunAskUnsettled
	})
}

// A reply is paired with the turn record from before its inspect was sent: a turn_start landing
// while the inspect is in flight never publishes the old wait under the new turn's id.
func TestRunAsks_AReplyKeepsTheTurnMarkerFromBeforeItsInspect(t *testing.T) {
	h, br, _ := askCoreHub(t, "wf_1", nil)
	parked := inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step"))
	running := inspectAsk(t, "wf_1", marotte.RunStatusRunning, askStepNode{id: "review", status: "running", session: "sess_step"})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	br.mu.Lock()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowInspect {
			return nil, nil, false
		}
		if calls.Add(1) > 1 {
			return running, nil, true
		}
		close(entered)
		<-release
		return parked, nil, true
	}
	br.mu.Unlock()
	c := h.runs.cellFor("wf_1")
	done := make(chan error, 1)
	go func() {
		_, err := h.runs.readAsks(t.Context(), c, h.runs.askHost("wf_1"))
		done <- err
	}()
	<-entered
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("readAsks = %v", err)
	}
	waitFor(t, func() bool { p := lastAsksOf(t, h, "wf_1"); return p.Reachable && len(p.Asks) == 0 })

	old := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step")
	for _, f := range runAsksFrames(t, h, "wf_1") {
		for _, a := range f.Asks {
			if a.AskID != old {
				t.Errorf("seq %d published the wait from before the turn_start as %s, the new turn's id", f.Seq, a.AskID)
			}
		}
	}
}

// A successful pause is not a stop: every ask stays sendable and Continue-able.
func TestRunAsks_APauseLeavesEveryAskSendable(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	readNow(t, h, "wf_1")
	if err := h.runs.pause(t.Context(), "wf_1"); err != nil {
		t.Fatalf("pause = %v", err)
	}
	r := readNow(t, h, "wf_1")
	if a := r.asks[0]; a.Blocked() != "" || !a.CanContinue {
		t.Errorf("after a pause the ask = blocked %q continue %v, want sendable and Continue-able", a.Blocked(), a.CanContinue)
	}
}

func TestRunAsks_StoppingClears(t *testing.T) {
	t.Run("on a failed cancel", func(t *testing.T) {
		h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		readNow(t, h, "wf_1")
		br.setCallRPCErr(methodKiroWorkflowCancel, &marotte.RPCError{Code: -32603, Message: "Internal error"})
		if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
			t.Fatal("Setup: the refused cancel reported success")
		}
		if p := lastAsksOf(t, h, "wf_1"); p.Asks[0].Blocked == marotte.RunAskStopping {
			t.Error("a refused cancel left the run stopping, so its card never takes an answer again")
		}
	})
	t.Run("on a terminal read", func(t *testing.T) {
		h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		c := h.runs.cellFor("wf_1")
		h.runs.setStopping(t.Context(), c, true)
		br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "cancelled", ""))
		readNow(t, h, "wf_1")
		c.mu.Lock()
		stopping := c.stopping
		c.mu.Unlock()
		if stopping {
			t.Error("a terminal read left the run stopping")
		}
	})
}

// marotte's own pending send outlives its reply until the answered step's turn_start, in either order.
func TestRunAsks_PendingEndsWhenTheStepsTurnStarts(t *testing.T) {
	t.Run("turn_start after the reply", func(t *testing.T) {
		h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := readNow(t, h, "wf_1").asks[0].ID
		if err := h.runs.answerInput(t.Context(), "wf_1", askID, "teal"); err != nil {
			t.Fatalf("answer = %v", err)
		}
		c := h.runs.asks.get("wf_1")
		if h.runs.pendingOf(c) == nil {
			t.Fatal("the admitted answer cleared pending at its reply, so a second device can answer into the starting loop")
		}
		h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
		if h.runs.pendingOf(c) != nil {
			t.Error("the step's turn_start left pending set")
		}
	})
	t.Run("turn_start before the reply", func(t *testing.T) {
		h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := readNow(t, h, "wf_1").asks[0].ID
		br.mu.Lock()
		br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
			if method == marotte.MethodPrompt {
				h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
			}
			return nil, nil, false
		}
		br.mu.Unlock()
		if err := h.runs.answerInput(t.Context(), "wf_1", askID, "teal"); err != nil {
			t.Fatalf("answer = %v", err)
		}
		if h.runs.pendingOf(h.runs.asks.get("wf_1")) != nil {
			t.Error("a turn_start that beat the reply left pending set until the ceiling")
		}
	})
}

// An admitted answer whose turn_start never arrives stops blocking the card after the ceiling.
func TestRunAsks_PendingHasACeiling(t *testing.T) {
	old := askPendingCeiling
	askPendingCeiling = 20 * time.Millisecond
	t.Cleanup(func() { askPendingCeiling = old })
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	askID := readNow(t, h, "wf_1").asks[0].ID
	if err := h.runs.answerInput(t.Context(), "wf_1", askID, "teal"); err != nil {
		t.Fatalf("answer = %v", err)
	}
	c := h.runs.asks.get("wf_1")
	waitFor(t, func() bool { return h.runs.pendingOf(c) == nil })
	waitFor(t, func() bool {
		p := lastAsksOf(t, h, "wf_1")
		return len(p.Asks) == 1 && p.Asks[0].Blocked == ""
	})
}

// A stop waits for marotte's admitted answer, at most askStopCeiling, then stops anyway.
func TestRunAsks_AStopWaitsForAPendingAnswerWithinItsCeiling(t *testing.T) {
	old := askStopCeiling
	askStopCeiling = 50 * time.Millisecond
	t.Cleanup(func() { askStopCeiling = old })
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	c := h.runs.cellFor("wf_1")
	h.runs.registerPending(t.Context(), c)

	start := time.Now()
	sent := false
	h.runs.stopSerialized(t.Context(), "wf_1", func(context.Context) bool { sent = true; return true })
	if !sent {
		t.Fatal("the stop never went out")
	}
	if waited := time.Since(start); waited < askStopCeiling {
		t.Errorf("the stop went out after %v, before the pending answer's ceiling", waited)
	}
}

// Resume, retry and pause refuse while an answer is pending rather than queueing behind it.
func TestRunAsks_AResumeIsRefusedWhileAnAnswerIsPending(t *testing.T) {
	h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.runs.registerPending(t.Context(), h.runs.cellFor("wf_1"))
	if err := h.runs.resume(t.Context(), "wf_1"); !errors.Is(err, errRunAnswerPending) {
		t.Errorf("resume with an answer pending = %v, want errRunAnswerPending", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Error("the resume was sent behind a pending answer")
	}
}

// Every verb on a run marotte holds no lease for: an answer or Continue has nothing to address,
// and the run's controls go out unserialized.
func TestRunAsks_VerbsOnARunWithNoCell(t *testing.T) {
	cases := []struct {
		do      func(*Runtime) error
		check   func(*testing.T, *Runtime)
		name    string
		method  string
		wantErr error
	}{
		{
			name: "answer", wantErr: errAskGone,
			do: func(h *Runtime) error { return h.runs.answerInput(context.Background(), "wf_1", "a1", "teal") },
		},
		{
			name: "continue", wantErr: errStepStatusMistargeted,
			do: func(h *Runtime) error {
				return h.runs.setStepStatus(context.Background(), "wf_1", marotte.RunStepStatusRequest{NodeID: "review", Status: runStepRunning})
			},
		},
		{
			name: "cancel", method: methodKiroWorkflowCancel,
			do: func(h *Runtime) error { return h.runs.cancel(context.Background(), "wf_1") },
		},
		{
			name: "delete", method: methodKiroWorkflowDelete,
			do: func(h *Runtime) error { return h.runs.delete(context.Background(), "wf_1") },
			check: func(t *testing.T, h *Runtime) {
				if h.runs.asks.get("wf_1") != nil || len(h.runs.asks.list()) != 0 {
					t.Error("the delete of a run with no lease left an ask cell behind")
				}
				if err := h.runs.delete(context.Background(), "wf_1"); errors.Is(err, errRunDeleting) {
					t.Error("the delete never settled, so the run refuses every later delete")
				}
			},
		},
		{
			name: "pause", method: methodKiroWorkflowPause,
			do: func(h *Runtime) error { return h.runs.pause(context.Background(), "wf_1") },
		},
		{
			name: "resume", method: methodKiroWorkflowResume,
			do: func(h *Runtime) error { return h.runs.resume(context.Background(), "wf_1") },
		},
		{
			name: "retry", method: methodKiroWorkflowRetry,
			do: func(h *Runtime) error {
				_, err := h.runs.retry(context.Background(), "wf_1", &runAffordance{})
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
			br.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
			br.setCallResult(methodKiroWorkflowRetry, json.RawMessage(`{"workflowId":"wf_1","status":"running","retriedNodeIds":[]}`))
			err := tc.do(h)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("%s = %v, want %v", tc.name, err, tc.wantErr)
				}
				if slices.Contains(br.callLog(), marotte.MethodPrompt) || slices.Contains(br.callLog(), methodKiroWorkflowUpdate) {
					t.Errorf("%s reached KAS: %v", tc.name, br.callLog())
				}
				return
			}
			if err != nil {
				t.Errorf("%s on a run with no cell = %v, want it sent", tc.name, err)
			}
			if !slices.Contains(br.callLog(), tc.method) {
				t.Errorf("%s never reached KAS: %v", tc.method, br.callLog())
			}
			if tc.check != nil {
				tc.check(t, h)
			}
		})
	}
}

// A verb holding a cell that the lease's release ended completes against it, and the release lets
// go of a stop waiting on that verb's pending answer.
func TestRunAsks_AnEndMidVerb(t *testing.T) {
	t.Run("an answer in flight completes and publishes nothing after the end", func(t *testing.T) {
		h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		askID := readNow(t, h, "wf_1").asks[0].ID
		held := make(chan struct{})
		br.mu.Lock()
		br.blockOn = map[string]chan struct{}{marotte.MethodPrompt: held}
		br.mu.Unlock()
		done := make(chan error, 1)
		go func() { done <- h.runs.answerInput(t.Context(), "wf_1", askID, "teal") }()
		waitForCall(t, br, marotte.MethodPrompt)

		h.runs.releaseLease(t.Context(), "wf_1")
		ended := len(runAsksFrames(t, h, "wf_1"))
		close(held)
		if err := <-done; err != nil {
			t.Errorf("the answer the end overtook = %v, want it to complete", err)
		}
		joinInflight(t, h)
		if n := len(runAsksFrames(t, h, "wf_1")); n != ended {
			t.Errorf("%d frames after the run's final one, want none: a client would resurrect the card", n-ended)
		}
	})
	t.Run("a stop waiting on a pending answer is let go", func(t *testing.T) {
		h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		h.runs.registerPending(t.Context(), h.runs.cellFor("wf_1"))
		sent := make(chan struct{})
		go h.runs.stopSerialized(t.Context(), "wf_1", func(context.Context) bool { close(sent); return true })
		waitFor(t, func() bool {
			c := h.runs.asks.get("wf_1")
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.stopping
		})

		h.runs.releaseLease(t.Context(), "wf_1")

		select {
		case <-sent:
		case <-time.After(2 * time.Second):
			t.Fatal("the stop kept waiting on a pending answer of a run whose lease ended")
		}
	})
}

// The parking turn's warning is the ask's question: kept on the turn the notify arrives in, and fixed
// at that turn's end.
func TestRunAsks_TheParkingTurnsWarningIsTheQuestion(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
	h.runs.onNotify(t.Context(), runChatID("wf_1"), runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": "sess_parent", "callerSessionId": "sess_step", "message": "Which branch?",
		"severity": "warning", "sender": "step", "workflowId": "wf_1",
	}))
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeEnd)

	a := readNow(t, h, "wf_1").asks[0]
	if a.Text != "Which branch?" || a.TextSource != workflow.TextAsked {
		t.Errorf("ask text = %q (%s), want the step's warning as its question", a.Text, a.TextSource)
	}
	if a.ID == markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "review"}), "sess_step") {
		t.Error("the ask keyed on the boot placeholder although its turn_start was seen")
	}
}

// A lifecycle frame through the run bridge's door is a trigger: the card appears with no request.
func TestRunAsks_ALifecycleFrameIsATrigger(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.dispatch(t.Context(), runChatID("wf_1"), h.originOf(runChatID("wf_1")), runNotif(methodWFNodePaused, map[string]any{
		"workflowId": "wf_1", "nodeId": "review", "nodePath": []string{"root", "review"}, "reason": "waiting",
	}))
	waitFor(t, func() bool { p := lastAsksOf(t, h, "wf_1"); return p.Reachable && len(p.Asks) == 1 })
}

// The user's chat on the recorded run: the step parks, the parent agent answers by send_message
// (the defer route goes the same way), the step's resume frames are triggers and the next read shows
// it running, so the card leaves every surface; a second answer through /answer is final ask_gone.
func TestRunAsks_TheParentsSendMessageEndsTheCard(t *testing.T) {
	h, br, _ := askCoreHub(t, recordedRun, recordedReply(t, "parked"))
	askID := readNow(t, h, recordedRun).asks[0].ID
	if p := lastAsksOf(t, h, recordedRun); len(p.Asks) != 1 || p.Asks[0].AskID != askID {
		t.Fatalf("Setup: the park published %+v, want its card on every surface", p.Asks)
	}

	br.setCallResult(methodKiroWorkflowInspect, recordedReply(t, "running-after-parent-send"))
	h.runs.onNotify(t.Context(), runChatID(recordedRun), runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId": recordedSession, "callerSessionId": testLaunchSession, "message": "Use main.",
		"severity": "info", "sender": "parent", "workflowId": recordedRun,
	}))
	h.runs.askTurnEdge(recordedRun, recordedSession, translate.TurnEdgeStart)

	waitFor(t, func() bool { p := lastAsksOf(t, h, recordedRun); return p.Reachable && len(p.Asks) == 0 })
	if err := h.runs.answerInput(t.Context(), recordedRun, askID, "use main"); !errors.Is(err, errAskGone) {
		t.Errorf("the deferred answer after the parent's = %v, want final errAskGone", err)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Error("the second route's answer reached KAS")
	}
}

// An answer typed against the step's older question, at the same position, is stale, not gone:
// the step has asked again and still waits.
func TestRunAsks_AnAnswerToAnOlderQuestionIsStale(t *testing.T) {
	h, br, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	older := readNow(t, h, "wf_1").asks[0].ID
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeStart)
	h.runs.askTurnEdge("wf_1", "sess_step", translate.TurnEdgeEnd)
	if now := readNow(t, h, "wf_1").asks[0].ID; now == older {
		t.Fatal("Setup: the re-park kept the older question's id")
	}

	if err := h.runs.answerInput(t.Context(), "wf_1", older, "teal"); !errors.Is(err, errAskStale) {
		t.Errorf("an answer to the older question = %v, want errAskStale", err)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Error("the stale answer reached KAS")
	}
}

// A push landing opens a run whose host died with a question on screen, while KAS's record on disk
// has the run over. The read goes through the process the lease records, never the utility session
// (a utility read can stamp it as the run's owner), and the reply and every surface converge on no
// question; the carrier started for the read is closed again.
func TestOpenRun_ReadsThroughTheRecordedHostNeverTheUtility(t *testing.T) {
	var (
		mu    sync.Mutex
		built []*fakeBridge
	)
	cs := newTestChatStore()
	h := New(context.Background(), "/tmp/work", func() ACPBridge {
		b := newFakeBridge()
		b.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_1", "completed", ""))
		mu.Lock()
		built = append(built, b)
		mu.Unlock()
		return b
	}, cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession})
	old := newFakeBridge()
	old.setCallResult(methodKiroWorkflowInspect, inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: old, state: bridgeIdle})
	if len(readNow(t, h, "wf_1").asks) != 1 {
		t.Fatal("Setup: the card was not up before its host died")
	}
	h.bridge.mgr.remove(runChatID("wf_1"))

	got, body := getRun(t, h, "wf_1")

	if len(got.OpenAsks) != 0 || !got.AsksReachable {
		t.Errorf("open_asks = %+v reachable %v, want none, read: %s", got.OpenAsks, got.AsksReachable, body)
	}
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 0 || !p.Reachable {
		t.Errorf("the published snapshot = %+v, want no question on any surface", p)
	}
	mu.Lock()
	defer mu.Unlock()
	carriers := 0
	for _, b := range built {
		if opts := b.lastStartOpts(); opts != nil && opts.SessionID == testLaunchSession {
			carriers++
			continue
		}
		for _, m := range b.callLog() {
			if len(m) > len("_kiro/workflow/") && m[:len("_kiro/workflow/")] == "_kiro/workflow/" {
				t.Errorf("a process other than the run's recorded host got %s", m)
			}
		}
	}
	if carriers != 1 {
		t.Errorf("%d carriers loaded the launching session, want 1", carriers)
	}
	waitFor(t, func() bool { return h.bridge.mgr.get(runChatID("wf_1")) == nil })
}

// A parentless run's carrier closes on `run_complete {paused}`, the very frame that announces the park:
// the read that frame triggers holds the carrier, so the card is read before the process goes.
func TestRunAsks_APausedCarrierClosesOnlyAfterItsParkIsRead(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	lastAsksOf(t, h, "wf_1")

	h.dispatch(t.Context(), runChatID("wf_1"), h.originOf(runChatID("wf_1")), runCompleteFrame(t, "wf_1", "paused"))

	waitFor(t, func() bool { return h.bridge.mgr.get(runChatID("wf_1")) == nil })
	joinInflight(t, h)
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || !p.Reachable {
		t.Errorf("after the park's frame = %+v, want its question read before the carrier closed", p)
	}
}

// A frame's carrier hold is always handed back: by the explicit read that takes the query token
// after the frame's kick, and by a kick refused at shutdown. A stranded hold keeps a parentless
// run's process up forever, since its close waits for the carrier to go idle.
func TestRunAsks_AFramesCarrierHoldIsAlwaysLeft(t *testing.T) {
	t.Run("an explicit read takes over the kick's hold", func(t *testing.T) {
		h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		c := h.runs.cellFor("wf_1")
		host := h.runs.askHost("wf_1")
		c.query <- struct{}{}
		h.runs.kickCellOn(c, host)
		done := make(chan error, 1)
		go func() {
			_, err := h.runs.readAsks(t.Context(), c, host)
			done <- err
		}()
		<-c.query
		if err := <-done; err != nil {
			t.Fatalf("readAsks = %v", err)
		}
		joinInflight(t, h)
		if h.runs.carriers.busy(host) {
			t.Error("the carrier is still held after every read ran, so its close never comes")
		}
	})
	t.Run("a kick refused at shutdown", func(t *testing.T) {
		h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
		c := h.runs.cellFor("wf_1")
		host := h.runs.askHost("wf_1")
		h.lifecycle.draining.Store(true)
		t.Cleanup(func() { h.lifecycle.draining.Store(false) })
		h.runs.kickCellOn(c, host)
		if h.runs.carriers.busy(host) {
			t.Error("a kick refused at shutdown kept the carrier held")
		}
	})
}

// After a restart marotte saw no warning, so the card's text is the parked step's last words from the
// run's own record: its send_message call's message over any text it wrote, labelled as not its question.
func TestRunAsks_AnUnseenQuestionReadsTheStepsLastWords(t *testing.T) {
	h, _, _ := askCoreHub(t, "wf_1", inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_step")))
	h.runs.log = newRunLog(t.TempDir())
	path := workflow.PathKey([]string{"root", "review"})
	turn, _, err := h.runs.log.open(t.Context(), &translate.RunStep{RunID: "wf_1", NodePath: path, SessionID: "sess_step"}, runChatID("wf_1"))
	if err != nil {
		t.Fatalf("Setup: open the step's turn: %v", err)
	}
	if _, err := turn.TextDelta(t.Context(), "", "say-1", "Thinking about branches."); err != nil {
		t.Fatalf("Setup: text: %v", err)
	}
	if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{
		ID: "call-1", Title: "Send Message", Kind: marotte.ToolKindOther,
		Input: json.RawMessage(`{"message":"Which branch should I target?","severity":"warning"}`),
	}); err != nil {
		t.Fatalf("Setup: tool call: %v", err)
	}

	a := readNow(t, h, "wf_1").asks[0]
	if a.Text != "Which branch should I target?" || a.TextSource != workflow.TextLastMessage {
		t.Errorf("ask text = %q (%s), want the step's send_message words labelled as its last message", a.Text, a.TextSource)
	}
}
