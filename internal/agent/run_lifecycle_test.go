package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

type deleteGate struct {
	*fakeBridge
	entered chan struct{}
	release chan struct{}
	outcome func() (*marotte.RPCResponse, error)
	once    sync.Once
}

func (g *deleteGate) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, err := g.fakeBridge.Call(ctx, method, params)
	if method != methodKiroWorkflowDelete {
		return resp, err
	}
	g.once.Do(func() { close(g.entered) })
	// The real bridge's arms: the reply, or the caller's ctx ending after the write.
	select {
	case <-g.release:
		return g.outcome()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *deleteGate) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := g.Call(ctx, method, params)
	g.mu.Lock()
	seq := g.deliveredSeq
	g.mu.Unlock()
	return resp, seq, err
}

type lifecycleOp struct {
	do     func(ctx context.Context, h *Runtime) error
	setup  func(t *testing.T, h *Runtime, br *fakeBridge)
	name   string
	method string
	run    marotte.RunStatus
	node   marotte.RunNodeStatus
	state  string
}

var lifecycleOps = []lifecycleOp{
	{
		name: "prompt", method: marotte.MethodPrompt,
		run: marotte.RunStatusPaused, node: marotte.RunNodeStatusPaused, state: "paused",
		do: func(ctx context.Context, h *Runtime) error {
			_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "carry on", "m-op")
			return err
		},
	},
	{
		name: "answer", method: marotte.MethodPrompt,
		run: marotte.RunStatusPaused, node: marotte.RunNodeStatusPaused, state: "paused",
		setup: func(_ *testing.T, h *Runtime, _ *fakeBridge) {
			h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
			}})
		},
		do: func(ctx context.Context, h *Runtime) error {
			return h.runs.answerInput(ctx, "wf_1", "a1", "teal")
		},
	},
	{
		name: "steer", method: marotte.MethodSessionSteer,
		run: marotte.RunStatusRunning, node: marotte.RunNodeStatusRunning, state: "running",
		do: func(ctx context.Context, h *Runtime) error {
			_, err := h.runs.messageStep(ctx, "wf_1", reviewPath, "use main", "m-op")
			return err
		},
	},
	{
		// Edit or Delete on one of two waiting rows: KAS's buffer is cleared, then the kept row resubmitted.
		name: "clear_and_resubmit", method: marotte.MethodSessionSteerClear,
		run: marotte.RunStatusRunning, node: marotte.RunNodeStatusRunning, state: "running",
		setup: func(t *testing.T, h *Runtime, br *fakeBridge) {
			t.Helper()
			for _, id := range []string{"m-a", "m-b"} {
				if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "words "+id, id); err != nil {
					t.Fatalf("Setup: MessageStep(%s) = %v", id, err)
				}
			}
			br.setCallResult(marotte.MethodSessionSteerClear, json.RawMessage(`{"messageIds":["steer-m-a","steer-m-b"]}`))
			// A forward that has folded the clear's reply position, and exits before the shutdown waits on it.
			gen := h.coord.turns.attachForward(runChatID("wf_1"))
			t.Cleanup(h.coord.turns.exitFor(runChatID("wf_1"), gen))
			br.mu.Lock()
			br.deliveredSeq = 3
			br.mu.Unlock()
			h.coord.turns.observe(runChatID("wf_1"), gen, 3)
		},
		do: func(ctx context.Context, h *Runtime) error {
			return h.runs.removeStepSteer(ctx, "wf_1", reviewPath, "steer-m-a")
		},
	},
}

type deleteOutcome struct {
	arm       func(g *deleteGate, br *fakeBridge, s *stepScript)
	name      string
	after     string // open, closed or gone
	wantErr   bool
	unconfirm bool
}

func deleteAnswer(resp *marotte.RPCResponse, err error) func() (*marotte.RPCResponse, error) {
	return func() (*marotte.RPCResponse, error) { return resp, err }
}

var bridgeExited = &marotte.TransportError{Err: marotte.ErrBridgeExited, Retryable: true}

var deleteOutcomes = []deleteOutcome{
	{
		name: "pre_write_failure", after: "open", wantErr: true,
		arm: func(g *deleteGate, _ *fakeBridge, _ *stepScript) {
			g.outcome = deleteAnswer(nil, &marotte.TransportError{Err: errors.New("write to ACP: broken pipe"), Retryable: true})
		},
	},
	{
		name: "refused", after: "open", wantErr: true,
		arm: func(g *deleteGate, _ *fakeBridge, _ *stepScript) {
			g.outcome = deleteAnswer(&marotte.RPCResponse{Error: &marotte.RPCError{
				Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"the run cannot be deleted now"}`),
			}}, nil)
		},
	},
	{
		name: "success", after: "gone",
		arm: func(_ *deleteGate, _ *fakeBridge, s *stepScript) { s.set("deleted") },
	},
	{
		name: "unknown_unreadable", after: "closed", wantErr: true, unconfirm: true,
		arm: func(g *deleteGate, br *fakeBridge, _ *stepScript) {
			g.outcome = deleteAnswer(nil, bridgeExited)
			br.setCallErr(methodKiroWorkflowInspect, errors.New("the utility session is down"))
		},
	},
	{
		name: "unknown_run_kept", after: "open", wantErr: true,
		arm: func(g *deleteGate, _ *fakeBridge, _ *stepScript) { g.outcome = deleteAnswer(nil, bridgeExited) },
	},
	{
		name: "unknown_run_gone", after: "gone",
		arm: func(g *deleteGate, br *fakeBridge, _ *stepScript) {
			g.outcome = deleteAnswer(nil, bridgeExited)
			br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{
				Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
			})
		},
	},
}

func gatedHub(t *testing.T, op *lifecycleOp) (*Runtime, *fakeBridge, *deleteGate, *stepScript) {
	t.Helper()
	h, br := messageHub(t, op.run, op.node)
	// First, so a setup's own cleanup runs before the shutdown.
	t.Cleanup(func() { shutdownHub(t, h) })
	g := &deleteGate{
		fakeBridge: br, entered: make(chan struct{}), release: make(chan struct{}),
		outcome: deleteAnswer(&marotte.RPCResponse{Result: json.RawMessage(`{}`)}, nil),
	}
	h.bridge.mgr.remove(runChatID("wf_1"))
	if !h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: g, state: bridgeIdle}) {
		t.Fatal("Setup: the gated carrier was not registered for wf_1")
	}
	s := newStepScript(t, br, op.state)
	if op.setup != nil {
		op.setup(t, h, br)
	}
	return h, br, g, s
}

// Every step-session mutation crossed with every Delete outcome, in both orders: a mutation admitted
// first reaches KAS before the delete does; one arriving during the delete sends nothing; and the run
// is afterwards open, closed or gone as the outcome decides.
func TestRunLifecycle_EveryStepMutationAgainstEveryDeleteOutcome(t *testing.T) {
	orders := []struct {
		run  func(*testing.T, *lifecycleOp, *deleteOutcome)
		name string
	}{{mutationFirst, "mutation_first"}, {deleteFirst, "delete_first"}}
	for i := range lifecycleOps {
		op := &lifecycleOps[i]
		t.Run(op.name, func(t *testing.T) {
			for _, order := range orders {
				t.Run(order.name, func(t *testing.T) {
					for _, out := range deleteOutcomes {
						t.Run(out.name, func(t *testing.T) {
							synctest.Test(t, func(t *testing.T) { order.run(t, op, &out) })
						})
					}
				})
			}
		})
	}
}

// A reader leaving after the delete reached KAS does not abandon it: KAS's answer is still waited
// for, and the run it deleted is torn down here.
func TestDelete_AReaderLeavingAfterTheWriteStillFinishesTheDelete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		op := &lifecycleOps[0]
		h, br, g, s := gatedHub(t, op)
		ctx, cancel := context.WithCancel(t.Context())
		deleted := make(chan error, 1)
		go func() { deleted <- h.runs.delete(ctx, "wf_1") }()
		<-g.entered
		cancel()
		synctest.Wait()
		select {
		case err := <-deleted:
			t.Fatalf("Delete returned %v once its reader left, want it to wait for KAS's answer", err)
		default:
		}
		s.set("deleted")
		close(g.release)
		if err := <-deleted; err != nil {
			t.Errorf("Delete KAS answered after the reader left = %v, want nil", err)
		}
		checkRunAfter(t, h, br, op, &deleteOutcome{name: "success", after: "gone"})
	})
}

func mutationFirst(t *testing.T, op *lifecycleOp, out *deleteOutcome) {
	h, br, g, s := gatedHub(t, op)

	held := make(chan struct{})
	br.mu.Lock()
	br.blockOn = map[string]chan struct{}{op.method: held}
	br.mu.Unlock()
	before := countCalls(br, op.method)

	done := make(chan error, 1)
	go func() { done <- op.do(t.Context(), h) }()
	synctest.Wait()
	if countCalls(br, op.method) != before+1 {
		t.Fatalf("Setup: calls = %v, want the %s with KAS", br.callLog(), op.method)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- h.runs.delete(t.Context(), "wf_1") }()
	synctest.Wait()
	if callIndex(br, methodKiroWorkflowDelete) >= 0 {
		t.Errorf("calls = %v, want no delete while an admitted %s holds the run", br.callLog(), op.name)
	}
	close(held)
	if err := <-done; err != nil {
		t.Errorf("%s admitted before Delete = %v, want nil", op.name, err)
	}
	<-g.entered
	br.mu.Lock()
	br.blockOn = nil
	br.mu.Unlock()
	out.arm(g, br, s)
	close(g.release)
	checkDeleteResult(t, <-deleted, out)
	checkRunAfter(t, h, br, op, out)
}

func deleteFirst(t *testing.T, op *lifecycleOp, out *deleteOutcome) {
	h, br, g, s := gatedHub(t, op)
	before := countCalls(br, op.method)

	deleted := make(chan error, 1)
	go func() { deleted <- h.runs.delete(t.Context(), "wf_1") }()
	<-g.entered
	if err := op.do(t.Context(), h); err == nil {
		t.Errorf("%s during Delete = nil, want a refusal", op.name)
	}
	if n := countCalls(br, op.method); n != before {
		t.Errorf("calls = %v, want no %s while the delete is with KAS", br.callLog(), op.method)
	}
	out.arm(g, br, s)
	close(g.release)
	checkDeleteResult(t, <-deleted, out)
	if out.after == "open" {
		if err := op.do(t.Context(), h); err != nil {
			t.Errorf("%s again once the run reopened = %v, want nil", op.name, err)
		}
	}
	checkRunAfter(t, h, br, op, out)
}

func checkDeleteResult(t *testing.T, err error, out *deleteOutcome) {
	t.Helper()
	if (err != nil) != out.wantErr {
		t.Errorf("Delete = %v, want error %v", err, out.wantErr)
	}
	if errors.Is(err, errDeleteUnconfirmed) != out.unconfirm {
		t.Errorf("Delete = %v, want errDeleteUnconfirmed %v", err, out.unconfirm)
	}
}

// checkRunAfter probes the run with a fresh message: an open run takes it, a closed one refuses it
// busy, and a gone one sends nothing and keeps no record of the step.
func checkRunAfter(t *testing.T, h *Runtime, br *fakeBridge, op *lifecycleOp, out *deleteOutcome) {
	t.Helper()
	sends := []string{marotte.MethodPrompt, marotte.MethodSessionSteer}
	counts := func() []int { return []int{countCalls(br, sends[0]), countCalls(br, sends[1])} }
	before := counts()
	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "and then", "m-probe")
	switch out.after {
	case "open":
		if err != nil {
			t.Errorf("message after Delete settled %s = %v, want it taken", out.name, err)
		}
	case "closed":
		if code := refusalCode(t, h, err); code != string(marotte.RunStepBusy) {
			t.Errorf("message after an unconfirmed Delete = %v, want reason %q", err, marotte.RunStepBusy)
		}
		if got := counts(); !slices.Equal(got, before) {
			t.Errorf("calls = %v, want nothing sent to a run whose delete is unconfirmed", br.callLog())
		}
	case "gone":
		if err == nil {
			t.Error("message after the run was deleted = nil, want an error")
		}
		if got := counts(); !slices.Equal(got, before) {
			t.Errorf("calls = %v, want nothing sent to a deleted run", br.callLog())
		}
		if h.runs.log.turn("wf_1", reviewPath) != nil {
			t.Errorf("%s: the deleted run's step turn is still open, want local teardown", op.name)
		}
	}
}

func deleteRoute(h *Runtime) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/runs/wf_1", http.NoBody)
	req.SetPathValue("id", "wf_1")
	h.runRoutes.handleDelete(rec, req)
	return rec
}

// The Delete route answers a second Delete as a conflict, and one whose answer was lost as a
// gateway fault the reader can refresh from.
func TestDeleteRoute_GradesADeleteTheRunCannotSettleYet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, g, s := gatedHub(t, &lifecycleOps[0])
		deleted := make(chan *httptest.ResponseRecorder, 1)
		go func() { deleted <- deleteRoute(h) }()
		<-g.entered
		if rec := deleteRoute(h); rec.Code != http.StatusConflict {
			t.Errorf("DELETE during a Delete = %d %s, want 409", rec.Code, rec.Body.String())
		}
		deleteOutcomes[3].arm(g, br, s)
		close(g.release)
		if rec := <-deleted; rec.Code != http.StatusBadGateway {
			t.Errorf("DELETE whose answer was lost = %d %s, want 502", rec.Code, rec.Body.String())
		}
	})
}

// Background work asked for while a Delete holds the run is admitted only once the Delete settles,
// so a flush scheduled during a refused delete still runs instead of being dropped.
func TestRunLifecycle_BackgroundWorkWaitsOutADelete(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l runLifecycle
		d, err := l.beginDelete(t.Context(), "wf_1")
		if err != nil {
			t.Fatalf("Setup: beginDelete = %v", err)
		}
		admitted := make(chan error, 1)
		go func() {
			lease, err := l.admitWhenSettled(t.Context(), "wf_1")
			if err == nil {
				lease.end()
			}
			admitted <- err
		}()
		synctest.Wait()
		select {
		case err := <-admitted:
			t.Fatalf("admitWhenSettled during a Delete = %v, want it to wait", err)
		default:
		}
		if _, err := l.admit("wf_1"); !errors.Is(err, errRunClosing) {
			t.Errorf("admit during a Delete = %v, want errRunClosing", err)
		}
		d.settle()
		if err := <-admitted; err != nil {
			t.Errorf("admitWhenSettled once the Delete settled = %v, want a lease", err)
		}
	})
}
