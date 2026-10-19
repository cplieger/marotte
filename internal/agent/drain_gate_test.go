package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5s", what)
	}
}

func refuteClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(what)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAdmitUnlessDraining_ShutdownWaitsForTheRequestAndTheWorkItStarts(t *testing.T) {
	h, _, _ := newTestHub()
	entered, proceed, workRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	letProceed, releaseWork := sync.OnceFunc(func() { close(proceed) }), sync.OnceFunc(func() { close(workRelease) })
	t.Cleanup(func() { letProceed(); releaseWork() })
	var workDone atomic.Bool
	handler := h.lifecycle.admitUnlessDraining(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-proceed
		h.lifecycle.inflight.Go(func() {
			<-workRelease
			workDone.Store(true)
		})
	}))
	served := make(chan struct{})
	go func() {
		defer close(served)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/command", nil))
	}()
	awaitClosed(t, entered, "the admitted request's start")

	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownHub(t, h)
	}()
	waitFor(t, h.lifecycle.Draining)
	refuteClosed(t, done, "Shutdown returned while an admitted request was still running")

	letProceed()
	awaitClosed(t, served, "the request's return")
	refuteClosed(t, done, "Shutdown returned while the work the request started was still running")

	releaseWork()
	awaitClosed(t, done, "Shutdown's return")
	if !workDone.Load() {
		t.Error("Shutdown returned before the request's work finished")
	}
}

func TestHoldUnlessDraining_HoldsShutdownUntilReleasedAndRefusesOnceItBegan(t *testing.T) {
	h, _, _ := newTestHub()
	release, ok := h.HoldUnlessDraining()
	if !ok {
		t.Fatal("HoldUnlessDraining refused before Shutdown began")
	}
	releaseOnce := sync.OnceFunc(release)
	t.Cleanup(releaseOnce)

	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownHub(t, h)
	}()
	waitFor(t, h.lifecycle.Draining)
	if late, ok := h.HoldUnlessDraining(); ok {
		late()
		t.Error("HoldUnlessDraining held once Shutdown began, want it refused")
	}
	refuteClosed(t, done, "Shutdown returned while a hold was still held")

	releaseOnce()
	awaitClosed(t, done, "Shutdown's return")
}

type blockingPush struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingPush) HasSubscribers() bool        { return true }
func (*blockingPush) Retract(marotte.PushSubject) {}
func (p *blockingPush) Send(context.Context, *marotte.NotificationPayload) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
}

func TestNotify_OnceShutdownBeganStillPushesWithoutJoiningInflight(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	p := &blockingPush{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(p.release) })
	h.coord.push = p
	beginDraining(h)

	h.coord.Notify(t.Context(), "c1", &marotte.NotificationPayload{ChatID: "c1", Title: "t", Body: "b"})

	awaitClosed(t, p.entered, "the push send")
	joinInflight(t, h)
}

func TestTurnClose_OnceShutdownBeganLeavesAParkedModelPickParked(t *testing.T) {
	h, cs, _ := newTestHub()
	defer shutdownHub(t, h)
	turnID, _ := h.stagePromptTurn(t, "c1")
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Model, c.PendingModel = "opus-5", "sonnet-5"
		return true
	})
	beginDraining(h)

	h.coord.finalizeTurn(t.Context(), "c1", &turnClose{
		Closer: closerWireEnd, Stop: marotte.StopReasonEndTurn, Turn: turnID,
	})
	joinInflight(t, h)

	// A pipeline started off inflight would land within this window.
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if c, _ := cs.Get(t.Context(), "c1"); c.Model != "opus-5" || c.PendingModel != "sonnet-5" {
			t.Fatalf("header = {Model %q, PendingModel %q} after a close once Shutdown began, want {opus-5, sonnet-5}: "+
				"the pick lands at the next spawn", c.Model, c.PendingModel)
		}
	}
}

// A frame pump Shutdown does not join (an observer pass's, a host relay's) answers through it, so once Shutdown began
// a request it would answer on inflight is answered at once.
func TestAnswerConnectionRequest_OnceShutdownBeganAnswersAtOnceWithoutJoiningInflight(t *testing.T) {
	h, src := declRuntime(t, &declBackend{callback: "_test/auth/token"})
	e := src.current()
	before := len(src.lane.answeredIDs())
	beginDraining(h)

	for _, msg := range []*marotte.RPCResponse{laneRequest(1, "_test/auth/token", ""), laneRequest(2, methodKiroSecretGet, "")} {
		if !h.answerConnectionRequest(e, msg) {
			t.Fatalf("%s was not taken as a connection request", msg.Method)
		}
	}

	got := src.lane.answeredIDs()[before:]
	for _, id := range []int64{1, 2} {
		n := 0
		for _, a := range got {
			if a == id {
				n++
			}
		}
		if n != 1 {
			t.Errorf("request %d answered %d times by the time it returned, want once (answers %v)", id, n, got)
		}
	}
}

// Once Shutdown began a sign-in ask still gets the vault's credential: KAS fails the asking step for good on any
// refusal, while the engine's stop leaves it to the next boot.
func TestAnswerConnectionRequest_OnceShutdownBeganAnswersASignInAskWithTheCredential(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	asker := newFakeBridge()
	beginDraining(g.h)

	if !g.h.answerConnectionRequest(asker, laneRequest(9, gateCallback, "")) {
		t.Fatal("the sign-in ask was not taken as a connection request")
	}

	got, ok := asker.responseTo(9)
	if !ok {
		t.Fatal("the sign-in ask was not answered by the time it returned")
	}
	if got.err != nil {
		t.Errorf("the sign-in ask was answered with %v, want the vault's credential", got.err)
	}
	if raw, _ := got.result.(json.RawMessage); string(raw) != `{"accessToken":"t"}` {
		t.Errorf("the sign-in ask's answer = %v, want the vault's credential", got.result)
	}
}

func TestRunMutations_TheRuntimesCeilingInspectsNothingOnceShutdownBegan(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	m := h.runs.mutations
	var inspects atomic.Int32
	m.ceiling = 5 * time.Millisecond
	m.inspect = func(context.Context, string) error { inspects.Add(1); return nil }
	beginDraining(h)

	m.enter("wf_1", mutationDrive, 1).replied(nil, errors.New("reply lost"), ownerLane, 0)
	time.Sleep(80 * time.Millisecond) // The ceiling runs on a real timer.

	if got := inspects.Load(); got != 0 {
		t.Errorf("inspects = %d after Shutdown began, want 0", got)
	}
}

func TestShutdown_JoinsALaneFrameBeforeWaitingOnTheWorkItStarts(t *testing.T) {
	h, _, _ := newTestHub()
	entered, proceed, workRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	letProceed, releaseWork := sync.OnceFunc(func() { close(proceed) }), sync.OnceFunc(func() { close(workRelease) })
	t.Cleanup(func() { letProceed(); releaseWork() })
	var workDone atomic.Bool
	h.lifecycle.goLaneUnlessDraining(func() {
		close(entered)
		<-proceed
		h.lifecycle.inflight.Go(func() {
			<-workRelease
			workDone.Store(true)
		})
	})
	awaitClosed(t, entered, "the lane frame's start")

	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownHub(t, h)
	}()
	waitFor(t, h.lifecycle.Draining)
	refuteClosed(t, done, "Shutdown returned while a lane frame was still being handled")

	letProceed()
	refuteClosed(t, done, "Shutdown returned while the work the lane frame started was still running")

	releaseWork()
	awaitClosed(t, done, "Shutdown's return")
	if !workDone.Load() {
		t.Error("Shutdown returned before the lane frame's work finished")
	}
}

// A lane frame still running may make inflight's first Add, which must not race a Wait, so a Shutdown that could not
// join the lanes starts none. The hold keeps inflight positive, so a Wait it started would park and show in the stacks.
func TestShutdown_ALaneJoinThatTimesOutStartsNoInflightWait(t *testing.T) {
	h, _, _ := newTestHub()
	release, ok := h.HoldUnlessDraining()
	if !ok {
		t.Fatal("HoldUnlessDraining refused before Shutdown began")
	}
	entered, frameEnd := make(chan struct{}), make(chan struct{})
	endFrame := sync.OnceFunc(func() { close(frameEnd) })
	t.Cleanup(func() { endFrame(); release() })
	h.lifecycle.goLaneUnlessDraining(func() {
		close(entered)
		<-frameEnd
		h.lifecycle.inflight.Go(func() {})
	})
	awaitClosed(t, entered, "the lane frame's start")

	ctx, cancel := context.WithCancel(context.Background())
	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- h.Shutdown(ctx) }()
	waitFor(t, h.lifecycle.Draining)
	cancel()
	select {
	case err := <-shutdownErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Shutdown = %v, want its context's cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return within 5s of its context ending")
	}

	waiting := fmt.Sprintf("sync.(*WaitGroup).Wait(%p)", &h.lifecycle.inflight)
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if strings.Contains(string(buf[:runtime.Stack(buf, true)]), waiting) {
			t.Fatal("Shutdown started the in-flight wait though a lane frame was still running")
		}
	}
}

func TestLaunchScheduled_LaunchesNothingOnceShutdownBegan(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	launchableRecipe(br, "wf_sched")
	beginDraining(h)

	_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", "sched-1", marotte.RunDefaults{}, time.Now().Add(time.Hour))

	if !errors.Is(err, errRuntimeDraining) {
		t.Errorf("LaunchScheduled = %v once Shutdown began, want errRuntimeDraining", err)
	}
	if _, leased := h.runs.lease("wf_sched"); leased {
		t.Error("a scheduled launch was leased once Shutdown began")
	}
	if br.paramsFor(methodKiroWorkflowNew) != nil {
		t.Error("a scheduled launch sent workflow/new once Shutdown began")
	}
}

func TestBootSweep_SweepsNothingOnceShutdownBegan(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	beginDraining(h)

	if h.runs.BootSweep(t.Context()) {
		t.Error("BootSweep reported reaching KAS once Shutdown began, want no pass")
	}
}
