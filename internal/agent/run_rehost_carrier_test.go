package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func waitForRunCarrier(t *testing.T, h *Runtime, workflowID string) {
	t.Helper()
	stop := time.Now().Add(3 * time.Second)
	for h.bridge.mgr.get(runChatID(workflowID)) == nil {
		if time.Now().After(stop) {
			t.Fatal("Setup: the loading carrier was never registered under the run's chat id")
		}
		time.Sleep(time.Millisecond)
	}
}

func unhostedRunInBubble(t *testing.T) (h *Runtime, utility, carrier *fakeBridge, gate chan struct{}) {
	t.Helper()
	h, _, utility = newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	utility.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    parentlessRunList("wf_1"),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusPaused, ""),
		methodKiroWorkflowRetry:   json.RawMessage(`{"workflowId":"wf_1","status":"running","retriedNodeIds":[]}`),
	}
	if _, err := h.runs.listRaw(t.Context()); err != nil {
		t.Fatalf("Setup: warming the utility session: %s", err)
	}
	carrier = newFakeBridge()
	gate = make(chan struct{})
	carrier.startGate = gate
	h.bridge.mgr.factory = func() ACPBridge { return carrier }
	return h, utility, carrier, gate
}

func TestRehost_ConcurrentVerbsOnAnUnhostedRunLoadItOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, carrier, gate := unhostedRunInBubble(t)

		errs := make(chan error, 2)
		go func() { errs <- h.runs.Pause(t.Context(), "wf_1") }()
		go func() { errs <- h.runs.Pause(t.Context(), "wf_1") }()
		// Both verbs are parked: one in the load, one on the host lock.
		synctest.Wait()
		if got := carrier.startCount(); got != 0 {
			t.Fatalf("Setup: %d loads finished before the gate opened", got)
		}
		close(gate)

		for range 2 {
			if err := <-errs; err != nil {
				t.Errorf("Pause on a run another verb was loading = %v, want nil", err)
			}
		}
		if got := carrier.startCount(); got != 1 {
			t.Errorf("%d loads of the launching session, want 1", got)
		}
		if got := countCalls(carrier, methodKiroWorkflowPause); got != 2 {
			t.Errorf("%d pauses reached the carrier, want 2; calls were %v", got, carrier.callLog())
		}
	})
}

func TestRehost_AVerbThatGivesUpWaitingForTheHostDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, carrier, gate := unhostedRunInBubble(t)

		first := make(chan error, 1)
		go func() { first <- h.runs.Pause(t.Context(), "wf_1") }()
		synctest.Wait()
		waiterCtx, giveUp := context.WithCancel(t.Context())
		waiter := make(chan error, 1)
		go func() { waiter <- h.runs.Pause(waiterCtx, "wf_1") }()
		synctest.Wait()
		giveUp()
		if err := <-waiter; !errors.Is(err, context.Canceled) {
			t.Fatalf("the waiting Pause = %v, want its own cancellation", err)
		}
		close(gate)

		if err := <-first; err != nil {
			t.Fatalf("the loading Pause = %v, want nil", err)
		}
		synctest.Wait()
		if got := countCalls(carrier, methodKiroWorkflowPause); got != 1 {
			t.Errorf("%d pauses reached the carrier, want only the loading verb's; calls were %v",
				got, carrier.callLog())
		}
		if got := carrier.startCount(); got != 1 {
			t.Errorf("%d loads, want 1", got)
		}
		sb := h.bridge.mgr.get(runChatID("wf_1"))
		if sb == nil {
			t.Fatal("the loading verb's carrier is gone")
		}
		if h.runs.carriers.busy(sb) {
			t.Error("the carrier is still held after both verbs returned")
		}
	})
}

func TestRehost_AnAbandonedLoadClosesItsCarrier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, carrier, gate := unhostedRunInBubble(t)

		ctx, giveUp := context.WithCancel(t.Context())
		abandoned := make(chan error, 1)
		go func() { abandoned <- h.runs.Pause(ctx, "wf_1") }()
		synctest.Wait()
		giveUp()
		if err := <-abandoned; !errors.Is(err, context.Canceled) {
			t.Fatalf("the abandoned Pause = %v, want its own cancellation", err)
		}
		close(gate)
		synctest.Wait()
		if carrier.startCount() != 1 {
			t.Fatalf("Setup: %d loads finished, want the abandoned one", carrier.startCount())
		}
		if h.bridge.mgr.get(runChatID("wf_1")) != nil || !carrier.isStopped() {
			t.Fatal("the carrier a verb abandoned before sending anything was kept, " +
				"though KAS took nothing on it")
		}

		second := newFakeBridge()
		h.bridge.mgr.factory = func() ACPBridge { return second }
		if _, err := h.runs.Retry(t.Context(), "wf_1", h.runs.affordance(t.Context(), "wf_1", "aborted")); err != nil {
			t.Fatalf("Retry after the abandoned carrier closed = %v, want nil", err)
		}
		if second.startCount() != 1 {
			t.Errorf("%d loads for the later verb, want a fresh one", second.startCount())
		}
		if countCalls(second, methodKiroWorkflowRetry) != 1 {
			t.Errorf("calls on the fresh carrier = %v, want the retry", second.callLog())
		}
		h.coord.CloseBridge(t.Context(), runChatID("wf_1"), marotte.TurnOutcomeInterrupted)
	})
}

func TestRehost_AChatOpenAVerbAbandonedStillSerializesTheNextVerb(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, utility := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		utility.callResults = map[string]json.RawMessage{methodKiroWorkflowList: chatRunList("sess_owned")(t)}
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("Setup: seeding the chat: %s", err)
		}
		if _, err := h.runs.listRaw(t.Context()); err != nil {
			t.Fatalf("Setup: warming the utility session: %s", err)
		}
		chatBridge := newFakeBridge()
		gate := make(chan struct{})
		chatBridge.startGate = gate
		h.bridge.mgr.factory = func() ACPBridge { return chatBridge }

		ctx, giveUp := context.WithCancel(t.Context())
		abandoned := make(chan error, 1)
		go func() { abandoned <- h.runs.Pause(ctx, "wf_1") }()
		synctest.Wait()
		next := make(chan error, 1)
		go func() { next <- h.runs.Pause(t.Context(), "wf_1") }()
		synctest.Wait()
		giveUp()
		if err := <-abandoned; !errors.Is(err, context.Canceled) {
			t.Fatalf("the abandoned Pause = %v, want its own cancellation", err)
		}
		synctest.Wait()
		select {
		case err := <-next:
			close(gate)
			t.Fatalf("the next Pause = %v while the chat was still opening, want it to wait", err)
		default:
		}
		close(gate)

		if err := <-next; err != nil {
			t.Fatalf("the next Pause = %v, want nil once the chat opened", err)
		}
		if got := countCalls(chatBridge, methodKiroWorkflowPause); got != 1 {
			t.Errorf("%d pauses reached the chat's bridge, want the next verb's only; calls were %v",
				got, chatBridge.callLog())
		}
		if h.bridge.mgr.get("c1") == nil {
			t.Error("the chat's bridge was closed by a run verb")
		}
	})
}

func TestCloseKeptCarrier_AVerbWaitsOutTheBoundsDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, utility := newTestHub()
		t.Cleanup(func() { shutdownHub(t, h) })
		utility.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList:    parentlessRunList("wf_1"),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusPaused, ""),
		}
		if _, err := h.runs.listRaw(t.Context()); err != nil {
			t.Fatalf("Setup: warming the utility session: %s", err)
		}
		inspecting := make(chan struct{})
		utility.blockOn = map[string]chan struct{}{methodKiroWorkflowInspect: inspecting}
		keptFake := newFakeBridge()
		kept := &sharedBridge{bridge: keptFake, state: bridgeIdle}
		h.bridge.mgr.insert(runChatID("wf_1"), kept)
		fresh := newFakeBridge()
		h.bridge.mgr.factory = func() ACPBridge { return fresh }

		verdict := make(chan carrierVerdict, 1)
		go func() { verdict <- h.runs.closeKeptCarrier(runChatID("wf_1"), "wf_1", kept) }()
		synctest.Wait()
		resumed := make(chan error, 1)
		go func() { resumed <- h.runs.Resume(t.Context(), "wf_1") }()
		synctest.Wait()
		close(inspecting)

		if got := <-verdict; got != carrierClosed {
			t.Fatalf("closeKeptCarrier on a parked run = %v, want carrierClosed", got)
		}
		if err := <-resumed; err != nil {
			t.Fatalf("Resume = %v, want nil on a fresh carrier", err)
		}
		if got := countCalls(keptFake, methodKiroWorkflowResume); got != 0 {
			t.Errorf("%d resumes reached the carrier the bound was closing", got)
		}
		if got := countCalls(fresh, methodKiroWorkflowResume); got != 1 {
			t.Errorf("%d resumes reached the fresh carrier, want 1; calls were %v", got, fresh.callLog())
		}
	})
}

func TestCloseStoppedBridge_SparesACarrierAVerbEnteredAfterTheFrame(t *testing.T) {
	h, _, br := newTestHub()
	kept := &sharedBridge{bridge: br, state: bridgeIdle}
	h.bridge.mgr.insert(runChatID("wf_1"), kept)
	unlock, err := h.runs.hosts.acquire(t.Context(), "wf_1")
	if err != nil {
		t.Fatalf("Setup: taking the run's host lock: %s", err)
	}

	h.dispatch(t.Context(), runChatID("wf_1"), runCompleteFrame(t, "wf_1", "completed"))
	h.runs.carriers.enter(kept)
	unlock()
	t.Cleanup(func() { h.runs.carriers.leave(kept) })

	// Bounded: the close this guards against is a goroutine.
	windowEnd := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(windowEnd) {
		if h.bridge.mgr.get(runChatID("wf_1")) != kept || br.isStopped() {
			t.Fatal("a stopped-run close decided before a verb entered the carrier closed it under that verb")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunLocks_AnEndedContextNeverAcquires(t *testing.T) {
	t.Run("a free lock", func(t *testing.T) {
		var locks runLocks
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if release, err := locks.acquire(ctx, "wf_1"); err == nil {
			release()
			t.Fatal("acquire on a free lock = nil for a context that had already ended")
		}
	})
	t.Run("a waiter cancelled before its predecessor releases", func(t *testing.T) {
		// The waiter sees cancellation and release together, so a lock letting either win would acquire sometime.
		for range 64 {
			synctest.Test(t, func(t *testing.T) {
				var locks runLocks
				release, err := locks.acquire(t.Context(), "wf_1")
				if err != nil {
					t.Fatalf("Setup: the first acquire = %v", err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				got := make(chan error, 1)
				go func() {
					r, err := locks.acquire(ctx, "wf_1")
					if err == nil {
						r()
					}
					got <- err
				}()
				synctest.Wait()
				cancel()
				release()
				if err := <-got; err == nil {
					t.Fatal("acquire = nil for a waiter whose context ended before the lock freed")
				}
			})
		}
	})
}

func TestRehost_AFailedLoadStopsItsCarrier(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}
	if _, err := h.runs.listRaw(t.Context()); err != nil {
		t.Fatalf("Setup: warming the utility session: %s", err)
	}
	carrier := newFakeBridge()
	carrier.loadErr = errors.New("kiro-cli is not available yet")
	h.bridge.mgr.factory = func() ACPBridge { return carrier }

	if err := h.runs.Resume(t.Context(), "wf_1"); !errors.Is(err, errRunHostStart) {
		t.Fatalf("Resume = %v, want errRunHostStart", err)
	}
	if !carrier.isStopped() {
		t.Error("the carrier whose load failed was never stopped, so its forward loop waits forever")
	}
}

func TestOpenBridge_AFailedSpawnStopsItsBridge(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", nil)
	failing := newFakeBridge()
	failing.startErr = errors.New("kiro-cli is not available yet")
	h.bridge.mgr.factory = func() ACPBridge { return failing }

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err == nil {
		t.Fatal("OpenBridge = nil, want the spawn's failure")
	}
	if !failing.isStopped() {
		t.Error("the bridge whose spawn failed was never stopped, so its forward loop waits forever")
	}
}

func TestRehost_AFailedStarterLeavesTheCarrierToAVerbStillUsingIt(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}
	if _, err := h.runs.listRaw(t.Context()); err != nil {
		t.Fatalf("Setup: warming the utility session: %s", err)
	}
	gate, resumeHeld := make(chan struct{}), make(chan struct{})
	carrier := &pauseRefusedOnRelease{fakeBridge: newFakeBridge(), release: make(chan struct{})}
	carrier.startGate = gate
	carrier.blockOn = map[string]chan struct{}{methodKiroWorkflowResume: resumeHeld}
	h.bridge.mgr.factory = func() ACPBridge { return carrier }

	paused := make(chan error, 1)
	go func() { paused <- h.runs.Pause(t.Context(), "wf_1") }()
	waitForRunCarrier(t, h, "wf_1")
	resumed := make(chan error, 1)
	go func() { resumed <- h.runs.Resume(t.Context(), "wf_1") }()
	close(gate)
	waitForCall(t, carrier.fakeBridge, methodKiroWorkflowResume)

	close(carrier.release)
	if err := <-paused; err == nil {
		t.Fatal("Setup: the scripted pause refusal did not happen")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) == nil || carrier.isStopped() {
		t.Error("the starting verb's failure closed the carrier while another verb was inside a Call on it")
	}
	close(resumeHeld)
	if err := <-resumed; err != nil {
		t.Errorf("Resume = %v, want nil on the carrier it shared", err)
	}
}

// pauseRefusedOnRelease refuses `_kiro/workflow/pause` once release closes.
type pauseRefusedOnRelease struct {
	*fakeBridge
	release chan struct{}
}

func (b *pauseRefusedOnRelease) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if method != methodKiroWorkflowPause {
		return b.fakeBridge.Call(ctx, method, params)
	}
	select {
	case <-b.release:
		return nil, errors.New("refused")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
