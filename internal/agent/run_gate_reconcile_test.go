package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/signin"
)

// A held run has one release however the gate moves during its retries: a closing and an opening edge while it waits
// to retry a lost resume write start no second release, so the run's heal budget is charged once.
func TestSignInGate_AGateFlapDuringAReleasesRetryStartsNoSecondRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := releaseRetryBase
		releaseRetryBase = time.Hour
		t.Cleanup(func() { releaseRetryBase = base })
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "signin.json"), []byte(signedInAtBoot), 0o600); err != nil {
			t.Fatalf("Setup: write signin.json: %v", err)
		}
		g := newGateRun(t, dir)
		g.br.setCallResult(methodKiroWorkflowInspect, heldInspect(t, signInHoldReason))
		g.br.setCallErr(methodKiroWorkflowResume, errors.New("engine busy"))
		g.h.runs.BootSweep(t.Context())
		synctest.Wait()
		if n, spent := countCalls(g.br, methodKiroWorkflowResume), g.h.runs.healsSpent(gateRunID); n != 1 || spent != 1 {
			t.Fatalf("Setup: the boot's release wrote %d resumes and spent %d heals, want 1 and 1", n, spent)
		}

		g.b.set(false, false, func() error { return signin.ErrRejected })
		if rerr := g.askVault(t.Context(), t); rerr == nil {
			t.Fatal("Setup: the rejected ask was answered with a credential")
		}
		waitUntil(t, "the gate to close", func() bool { return !g.h.runs.gateOpen() })
		g.b.set(true, true, nil)
		g.signIn(t)
		synctest.Wait()

		if n := countCalls(g.br, methodKiroWorkflowResume); n != 1 {
			t.Errorf("resume writes = %d before the release's next try, want 1: a second release started", n)
		}
		if spent := g.h.runs.healsSpent(gateRunID); spent != 1 {
			t.Errorf("heals spent = %d, want the one release's 1", spent)
		}
	})
}

// A run's release serves one opening edge of the gate, and while it runs it serves every claim: one that stopped on
// the closed gate serves the later edge it finds open, whose claim it refused while live.
func TestRunGate_OneReleasePerRunAndOpeningEdge(t *testing.T) {
	// at moves g to its read after opening edges, open or closed.
	at := func(g *runGate, opening uint64, open bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.read = func() signin.Answerability { return signin.ProviderUnreachable }
		g.state, g.opens = signin.ProviderUnreachable, opening
		if open {
			g.state = signin.Answerable
		}
	}
	t.Run("a second claim of one edge", func(t *testing.T) {
		var g runGate
		at(&g, 1, true)
		if !g.claimRelease("wf", 1) {
			t.Fatal("the edge's first claim was refused")
		}
		if g.claimRelease("wf", 1) {
			t.Error("a second claim of the edge started while the first release runs")
		}
		if g.endRelease("wf", false) {
			t.Error("a release that ended on its own edge serves it again")
		}
		if g.claimRelease("wf", 1) {
			t.Error("a claim of an edge an ended release served started another")
		}
		if !g.claimRelease("wf", 2) {
			t.Error("a later edge's claim was refused once the release ended")
		}
	})
	t.Run("a later edge while the release runs", func(t *testing.T) {
		var g runGate
		at(&g, 1, true)
		g.claimRelease("wf", 1)
		at(&g, 2, true)
		if g.claimRelease("wf", 2) {
			t.Fatal("a later edge's claim started a second release while one runs")
		}
		if !g.endRelease("wf", true) {
			t.Fatal("a release that stopped on the closed gate does not serve the later edge it refused")
		}
		if g.claimRelease("wf", 2) {
			t.Error("the later edge's claim started a release beside the one serving it")
		}
		if g.endRelease("wf", true) {
			t.Error("a release serves its own edge twice")
		}
	})
	t.Run("the gate closed again at the end", func(t *testing.T) {
		var g runGate
		at(&g, 1, true)
		g.claimRelease("wf", 1)
		at(&g, 2, false)
		if g.endRelease("wf", true) {
			t.Error("a release that stopped on the closed gate serves an edge the gate closed after")
		}
		if !g.claimRelease("wf", 3) {
			t.Error("the next edge's claim was refused")
		}
	})
	t.Run("a release that did not stop on the closed gate", func(t *testing.T) {
		var g runGate
		at(&g, 1, true)
		g.claimRelease("wf", 1)
		at(&g, 2, true)
		if g.endRelease("wf", false) {
			t.Error("a release that resumed the run or spent its tries serves a later edge")
		}
	})
	t.Run("an opening edge forgets the ended releases", func(t *testing.T) {
		var g runGate
		answer := signin.ProviderUnreachable
		g.wire(func() signin.Answerability { return answer })
		g.claimRelease("wf_ended", 0)
		g.endRelease("wf_ended", false)
		g.claimRelease("wf_live", 0)
		answer = signin.Answerable
		if m := g.refresh(); !m.opened() {
			t.Fatal("Setup: the gate did not open")
		}
		if !g.claimRelease("wf_ended", 0) {
			t.Error("an ended release still refuses a claim after an opening edge")
		}
		if g.claimRelease("wf_live", 1) {
			t.Error("an opening edge forgot a live release")
		}
	})
}

// eventLog records what the fake engine and the responder saw, in order.
type eventLog struct {
	events []string
	mu     sync.Mutex
}

func (l *eventLog) note(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) snap() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// orderedResponder is the engine reading the host callback's answer.
type orderedResponder struct {
	log *eventLog
	err error
}

func (r *orderedResponder) Respond(_ context.Context, _ int64, _ any, err error) error {
	r.err = err
	r.log.note("respond")
	return nil
}

// slowPauses answers each pause after a while, as KAS's verb does, landing it as a held pause, and notes it.
func (g *gateRun) slowPauses(t *testing.T, log *eventLog) {
	t.Helper()
	g.br.mu.Lock()
	defer g.br.mu.Unlock()
	g.br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowPause {
			return nil, nil, false
		}
		time.Sleep(50 * time.Millisecond)
		reason, _ := params["reason"].(string)
		log.note("pause:" + reason)
		g.br.setCallResult(methodKiroWorkflowInspect, heldInspect(t, reason))
		g.listed(marotte.RunStatusPaused)
		return json.RawMessage(`{"paused":true}`), nil, true
	}
}

// failFirst makes method's next n calls fail once recorded, reaching nothing the earlier hook scripts, as a call
// KAS never took does; later calls reach that hook.
func failFirst(br *fakeBridge, method string, n int) {
	var calls atomic.Int32
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.failAfterCall == nil {
		br.failAfterCall = map[string]error{}
	}
	br.failAfterCall[method] = errors.New("the engine did not answer")
	prev := br.onCall
	br.onCall = func(m string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if m == method {
			if c := calls.Add(1); c <= int32(n) {
				if c == int32(n) {
					br.mu.Lock()
					delete(br.failAfterCall, method)
					br.mu.Unlock()
				}
				return nil, nil, false
			}
		}
		if prev != nil {
			return prev(m, params)
		}
		return nil, nil, false
	}
}

func (g *gateRun) runningNow(t *testing.T) {
	t.Helper()
	g.listed(marotte.RunStatusRunning)
	g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
}

func fastGateRetries(t *testing.T) {
	t.Helper()
	sweep, release := sweepRetryBase, releaseRetryBase
	sweepRetryBase, releaseRetryBase = time.Millisecond, time.Millisecond
	t.Cleanup(func() { sweepRetryBase, releaseRetryBase = sweep, release })
}

// The step whose credential ask finds the sign-in rejected is answered only once its run's pause was accepted: KAS
// parks a step on a refused ask when a pause was requested first, and fails it permanently otherwise.
func TestSignInGate_ARefusedAskIsAnsweredOnlyAfterTheRunningRunIsPaused(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.runningNow(t)
	log := &eventLog{}
	g.slowPauses(t, log)
	g.b.set(false, false, func() error { return signin.ErrRejected })

	answer, ok := g.h.signin.hostCallback(gateCallback)
	if !ok {
		t.Fatal("the backend's host callback is not answered")
	}
	r := &orderedResponder{log: log}
	g.h.answerHostCallback(r, 7, answer)

	if got, want := log.snap(), []string{"pause:" + signInHoldReason, "respond"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want the pause accepted before the refusal is answered", got)
	}
	if r.err == nil {
		t.Error("the rejected ask was answered with a credential")
	}
}

// stepAsk is KAS's step reading the answer to its credential ask: a refusal parks the step when a pause of its run
// was accepted first, and fails it for good otherwise.
type stepAsk struct {
	verbs   *gateVerbs
	outcome chan string
}

func newStepAsk(verbs *gateVerbs) *stepAsk {
	return &stepAsk{verbs: verbs, outcome: make(chan string, 1)}
}

func (s *stepAsk) Respond(_ context.Context, _ int64, _ any, err error) error {
	switch {
	case err == nil:
		s.outcome <- "answered"
	case len(s.verbs.pauseParams()) > 0:
		s.outcome <- "parked"
	default:
		s.outcome <- "failed"
	}
	return nil
}

func clearCallErr(br *fakeBridge, method string) {
	br.mu.Lock()
	defer br.mu.Unlock()
	delete(br.callErrs, method)
}

// A refused credential ask is not answered while no sweep can pause the running run, however long that lasts: the
// refusal waits for the run's pause, so the step parks, or for a sign-in, which answers it with a credential.
func TestSignInGate_ARefusedAskWaitsUntilTheRunningRunIsPaused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		signIn bool
		want   string
	}{
		{name: "the run list fails, then answers", method: methodKiroWorkflowList, want: "parked"},
		{name: "the pause fails, then lands", method: methodKiroWorkflowPause, want: "parked"},
		{name: "the run list fails until a sign-in", method: methodKiroWorkflowList, signIn: true, want: "answered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastGateRetries(t)
			logs := captureLogs(t)
			g := newGateRun(t, t.TempDir())
			g.signIn(t)
			g.runningNow(t)
			step := newStepAsk(g.watchVerbs(t))
			g.br.setCallErr(tc.method, errors.New("engine busy"))
			g.b.set(false, false, func() error { return signin.ErrRejected })
			answer, ok := g.h.signin.hostCallback(gateCallback)
			if !ok {
				t.Fatal("the backend's host callback is not answered")
			}
			go g.h.answerHostCallback(step, 7, answer)

			waitUntil(t, "the held refusal's ERROR", func() bool {
				return strings.Contains(logs.String(), "a credential refusal is held until every running run is paused")
			})
			select {
			case got := <-step.outcome:
				t.Fatalf("the ask was answered (%s) while its run could not be paused", got)
			default:
			}
			if tc.signIn {
				g.b.set(true, true, nil)
				g.signIn(t)
			} else {
				clearCallErr(g.br, tc.method)
			}
			select {
			case got := <-step.outcome:
				if got != tc.want {
					t.Errorf("the asking step = %s, want %s", got, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the held ask was never answered")
			}
		})
	}
}

// A refused ask that arrives while a restart waits for its doors is not written while that engine lives: its runs
// still run, so the refusal waits out the drain rather than failing the asking step.
func TestSignInGate_ARefusedAskWaitsOutTheEnginesRestart(t *testing.T) {
	logs := captureLogs(t)
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.runningNow(t)
	log := &eventLog{}
	src := fakeSourceOf(t, g.h)
	src.mu.Lock()
	src.stopHook = func() { log.note("engine stop") }
	src.mu.Unlock()
	_, _, door, err := g.h.engine.acquire(t.Context())
	if err != nil {
		t.Fatalf("Setup: a door on the engine: %v", err)
	}
	if !g.h.engine.requestRestart(restartCause{Kind: restartConfirmed}) {
		t.Fatal("Setup: the confirmed restart was refused")
	}
	waitUntil(t, "the restart's door wait", func() bool { return g.h.engine.status().State == marotte.EngineRestarting })
	g.b.set(false, false, func() error { return signin.ErrRejected })
	answer, _ := g.h.signin.hostCallback(gateCallback)
	r := &orderedResponder{log: log}
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		g.h.answerHostCallback(r, 7, answer)
	}()

	waitUntil(t, "the refusal to wait out the restart", func() bool {
		return strings.Contains(logs.String(), "a credential refusal waits out the engine's restart")
	})
	door()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the refusal was never written once the engine ended")
	}
	if got := log.snap(); len(got) < 2 || got[0] != "engine stop" || !slices.Contains(got, "respond") {
		t.Errorf("events = %v, want the refusal written only after the draining engine stopped", got)
	}
}

// A refused ask waiting between failed sweeps wakes on the sign-in, rather than at its next retry.
func TestSignInGate_AHeldRefusalWakesOnTheSignIn(t *testing.T) {
	base := sweepRetryBase
	sweepRetryBase = time.Hour
	t.Cleanup(func() { sweepRetryBase = base })
	logs := captureLogs(t)
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.runningNow(t)
	step := newStepAsk(g.watchVerbs(t))
	g.br.setCallErr(methodKiroWorkflowList, errors.New("engine busy"))
	g.b.set(false, false, func() error { return signin.ErrRejected })
	answer, _ := g.h.signin.hostCallback(gateCallback)
	go g.h.answerHostCallback(step, 7, answer)
	waitUntil(t, "a failed refusal sweep", func() bool {
		return strings.Contains(logs.String(), "not every running run was paused before a credential refusal")
	})

	g.b.set(true, true, nil)
	g.signIn(t)
	select {
	case got := <-step.outcome:
		if got != "answered" {
			t.Errorf("the asking step = %s, want it answered from the sign-in", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held ask slept through the sign-in")
	}
}

// The ask KAS sends while its engine starts is answered at once: a starting engine runs no run, and an unanswered
// ask would stall the start.
func TestSignInGate_ARefusedAskWhileTheEngineStartsIsAnsweredAtOnce(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	src := fakeSourceOf(t, g.h)
	starting := make(chan struct{})
	hold := make(chan struct{})
	var once sync.Once
	src.mu.Lock()
	src.onStart = func() { once.Do(func() { close(starting) }) }
	src.startGate = hold
	src.mu.Unlock()
	t.Cleanup(func() { close(hold) })
	if !g.h.engine.requestRestart(restartCause{Kind: restartConfirmed}) {
		t.Fatal("Setup: the confirmed restart was refused")
	}
	<-starting
	r := &orderedResponder{log: &eventLog{}}
	answered := make(chan struct{})
	answer, _ := g.h.signin.hostCallback(gateCallback)
	go func() {
		defer close(answered)
		g.h.answerHostCallback(r, 7, answer)
	}()

	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("the ask was held while the engine starts")
	}
	if r.err == nil {
		t.Error("the signed-out ask was answered with a credential")
	}
}

// A sign-in that lands while a refused ask's sweep is pausing the running runs ends the sweep, and the ask is
// answered again from the signed-in vault rather than refused.
func TestSignInGate_ARefusedAskIsAskedAgainWhenASignInLandsMidSweep(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.br.setCallResult(methodKiroWorkflowList, kasRuns(t,
		map[string]any{"workflowId": gateRunID, "name": "x", "status": "running", "parentSessionId": parkSession},
		map[string]any{"workflowId": "wf_2", "name": "y", "status": "running", "parentSessionId": parkSession},
	))
	var pauses atomic.Int32
	g.br.mu.Lock()
	g.br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch {
		case method == methodKiroWorkflowInspect:
			id, _ := params[keyWorkflowID].(string)
			return inspectReply(t, id, marotte.RunStatusRunning, ""), nil, true
		case method == methodKiroWorkflowPause && pauses.Add(1) == 1:
			g.b.set(true, true, nil)
			g.signIn(t)
		}
		return nil, nil, false
	}
	g.br.mu.Unlock()
	g.b.set(false, false, func() error { return signin.ErrRejected })
	answer, _ := g.h.signin.hostCallback(gateCallback)
	r := &orderedResponder{log: &eventLog{}}
	g.h.answerHostCallback(r, 7, answer)

	if r.err != nil {
		t.Errorf("the ask = %v, want it answered from the sign-in that landed", r.err)
	}
	if n := pauses.Load(); n != 1 {
		t.Errorf("pauses = %d, want the second run left running on the answerable sign-in", n)
	}
}

// A renewal whose lease is too short to use closes the gate like an outage: the running run is paused, with the
// unreachable reason, before the refusal; nothing launches; the next usable renewal resumes it.
func TestSignInGate_AShortLeaseClosesTheGateUntilAUsableRenewal(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.runningNow(t)
	log := &eventLog{}
	g.slowPauses(t, log)
	g.b.set(false, false, nil)
	g.b.setShort(true)

	answer, _ := g.h.signin.hostCallback(gateCallback)
	g.h.answerHostCallback(&orderedResponder{log: log}, 7, answer)

	if got, want := log.snap(), []string{"pause:" + providerHoldReason, "respond"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want the run paused for the provider before the refusal", got)
	}
	if a := g.vault.Answerability(gateBackendID); a != signin.ProviderUnreachable {
		t.Errorf("Answerability after a short lease = %v, want ProviderUnreachable", a)
	}
	if _, err := g.h.runs.admitRunWork(refuseClosed, gateRunID, ""); !errors.Is(err, errRunsProviderDown) {
		t.Errorf("admission = %v, want the unreachable refusal", err)
	}

	resumes := countResumes(t, g.br, gateRunID)
	g.b.setShort(false)
	if rerr := g.askVault(t.Context(), t); rerr != nil {
		t.Fatalf("ask once the lease is usable = %v, want an answer", rerr)
	}
	awaitResumes(t, resumes, 1)
}

// A run verb admitted while the gate was open and landing after it closed is not left running: the gate's
// closing sweep listed the run before the verb started it, so the verb's own settle sweeps again and pauses it.
func TestSignInGate_AVerbCrossingTheClosingEdgeLeavesNoRunRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		verb string
		wf   string
		send func(g *gateRun) error
	}{
		{
			name: "a user resume", verb: methodKiroWorkflowResume, wf: gateRunID,
			send: func(g *gateRun) error { return g.h.runs.resume(context.Background(), gateRunID) },
		},
		{
			name: "an automatic resume", verb: methodKiroWorkflowResume, wf: gateRunID,
			send: func(g *gateRun) error {
				g.h.runs.resumeIfInterrupted(context.Background(), parkChat, gateRunID)
				return nil
			},
		},
		{
			name: "a launch", verb: methodKiroWorkflowNew, wf: "wf_9",
			send: func(g *gateRun) error {
				_, _, err := g.h.runs.launch(context.Background(), "agent://wf-coder",
					map[string]string{"prompt": "fix the build"}, marotte.RunDefaults{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateRun(t, t.TempDir())
			g.signIn(t)
			g.br.setCallResult(methodKiroWorkflowNew, json.RawMessage(`{"workflowId":"wf_9"}`))
			release := make(chan struct{})
			var pauses atomic.Int32
			startedVerb := func() {
				g.br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
					"workflowId": tc.wf, "name": "x", "status": "running", "parentSessionId": parkSession,
				}))
				g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, tc.wf, marotte.RunStatusRunning, ""))
			}
			g.br.mu.Lock()
			g.br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
				switch {
				case method == tc.verb:
					<-release
					if method == methodKiroWorkflowResume {
						startedVerb()
					}
					return nil, nil, false
				case method == methodKiroWorkflowInvoke:
					startedVerb()
				case method == methodKiroWorkflowPause && params[keyWorkflowID] == tc.wf &&
					params["reason"] == signInHoldReason:
					pauses.Add(1)
					// As KAS lists a landed pause, so a later sweep finds nothing left running.
					g.br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
						"workflowId": tc.wf, "name": "x", "status": "paused", "parentSessionId": parkSession,
					}))
				}
				return nil, nil, false
			}
			g.br.mu.Unlock()

			done := make(chan error, 1)
			go func() { done <- tc.send(g) }()
			waitUntil(t, "the verb to be written", func() bool { return slices.Contains(g.br.callLog(), tc.verb) })
			lists := countCalls(g.br, methodKiroWorkflowList)
			g.b.set(false, false, func() error { return signin.ErrRejected })
			if rerr := g.askVault(t.Context(), t); rerr == nil {
				t.Fatal("a rejected renewal was answered")
			}
			waitUntil(t, "the closing edge's sweep", func() bool { return countCalls(g.br, methodKiroWorkflowList) > lists })
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("the admitted verb = %v, want it written", err)
			}
			waitUntil(t, "the crossed run's pause", func() bool { return pauses.Load() == 1 })
		})
	}
}

// The closing sweep's pause waits for a resume already being written, so the pause is the run's last verb. The run
// has no ask cell, whose own ordering (controlSerialized) would hide a missing one.
func TestSignInGate_TheClosingPauseLandsAfterAnInFlightResume(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.h.runs.asks.mu.Lock()
	delete(g.h.runs.asks.cells, gateRunID)
	g.h.runs.asks.mu.Unlock()
	log := &eventLog{}
	release := make(chan struct{})
	g.br.mu.Lock()
	g.br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case methodKiroWorkflowResume:
			// KAS lists the run running from the resume's claim, before its reply.
			g.runningNow(t)
			log.note("resume claimed")
			<-release
			log.note("resume answered")
		case methodKiroWorkflowPause:
			log.note("pause")
			g.listed(marotte.RunStatusPaused)
		}
		return nil, nil, false
	}
	g.br.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- g.h.runs.resume(context.Background(), gateRunID) }()
	waitUntil(t, "the resume to be claimed", func() bool { return slices.Contains(log.snap(), "resume claimed") })
	lists := countCalls(g.br, methodKiroWorkflowList)
	g.b.set(false, false, func() error { return signin.ErrRejected })
	if rerr := g.askVault(t.Context(), t); rerr == nil {
		t.Fatal("a rejected renewal was answered")
	}
	waitUntil(t, "the closing edge's sweep", func() bool { return countCalls(g.br, methodKiroWorkflowList) > lists })
	time.Sleep(quietWindow)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the user's resume = %v", err)
	}
	waitUntil(t, "the pause", func() bool { return slices.Contains(log.snap(), "pause") })
	if got, want := log.snap(), []string{"resume claimed", "resume answered", "pause"}; !slices.Equal(got, want) {
		t.Errorf("verbs = %v, want the pause after the resume's answer", got)
	}
}

// A closing sweep that cannot read the run list or pause a run tries again until the run is paused, and says so
// at ERROR when it gives up.
func TestSignInGate_TheClosingSweepRetriesUntilTheRunIsPaused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
	}{
		{name: "the run list fails", method: methodKiroWorkflowList},
		{name: "the pause fails", method: methodKiroWorkflowPause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastGateRetries(t)
			g := newGateRun(t, t.TempDir())
			g.signIn(t)
			g.awaitEdgeFinder(t)
			before := countCalls(g.br, tc.method)
			g.runningNow(t)
			verbs := g.watchVerbs(t)
			failFirst(g.br, tc.method, 2)
			g.b.set(false, false, func() error { return signin.ErrRejected })
			if rerr := g.askVault(t.Context(), t); rerr == nil {
				t.Fatal("a rejected renewal was answered")
			}
			waitUntil(t, "the run's pause", func() bool { return len(verbs.pauseParams()) == 1 })
			if n := countCalls(g.br, tc.method) - before; n != 3 {
				t.Errorf("%s called %d times, want the two failures and the one that paused", tc.method, n)
			}
			g.b.set(true, true, nil)
			g.signIn(t)
			awaitResumes(t, &verbs.resumes, 1)
		})
	}

	t.Run("it gives up loudly", func(t *testing.T) {
		fastGateRetries(t)
		logs := captureLogs(t)
		g := newGateRun(t, t.TempDir())
		g.signIn(t)
		g.awaitEdgeFinder(t)
		before := countCalls(g.br, methodKiroWorkflowList)
		g.runningNow(t)
		g.br.setCallErr(methodKiroWorkflowList, errors.New("engine busy"))
		g.b.set(false, false, func() error { return signin.ErrRejected })
		if rerr := g.askVault(t.Context(), t); rerr == nil {
			t.Fatal("a rejected renewal was answered")
		}
		waitUntil(t, "the sweep to give up", func() bool {
			return strings.Contains(logs.String(), "a running run could not be paused")
		})
		if n := countCalls(g.br, methodKiroWorkflowList) - before; n != maxSweepAttempts {
			t.Errorf("run list read %d times, want %d", n, maxSweepAttempts)
		}
	})
}

// heldByTheGate reports whether the gate holds workflowID for the next opening edge.
func heldByTheGate(rs *Runs, workflowID string) bool {
	rs.gate.mu.Lock()
	defer rs.gate.mu.Unlock()
	_, ok := rs.gate.heldHeals[workflowID]
	return ok
}

// kasPauses lands each pause as KAS lands one on a run with no step in flight: paused, attributed to its initiator,
// with its reason (none for a person's) as stopReason, a person's after personDelay. It records each pause as it
// arrives and counts resumes. The run list keeps reading running: the snapshot a sweep read before a pause landed.
func (g *gateRun) kasPauses(t *testing.T, personDelay time.Duration) *gateVerbs {
	t.Helper()
	v := &gateVerbs{}
	running := inspectReply(t, gateRunID, marotte.RunStatusRunning, "")
	g.br.mu.Lock()
	defer g.br.mu.Unlock()
	g.br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case methodKiroWorkflowPause:
			v.mu.Lock()
			v.pauses = append(v.pauses, params)
			v.mu.Unlock()
			reason, reasoned := params["reason"].(string)
			if !reasoned {
				time.Sleep(personDelay)
			}
			g.br.setCallResult(methodKiroWorkflowInspect, heldInspect(t, reason))
			return json.RawMessage(`{"paused":true}`), nil, true
		case methodKiroWorkflowResume:
			v.resumes.Add(1)
			g.br.setCallResult(methodKiroWorkflowInspect, running)
			return json.RawMessage(`{}`), nil, true
		}
		return nil, nil, false
	}
	return v
}

// lapseAndAsk rejects the next renewal and answers a credential ask as the engine lane does, returning once the
// refusal was written, which is after the closing sweep reached every running run.
func (g *gateRun) lapseAndAsk(t *testing.T) {
	t.Helper()
	g.b.set(false, false, func() error { return signin.ErrRejected })
	answer, ok := g.h.signin.hostCallback(gateCallback)
	if !ok {
		t.Fatal("the backend's host callback is not answered")
	}
	r := &orderedResponder{log: &eventLog{}}
	g.h.answerHostCallback(r, 7, answer)
	if r.err == nil {
		t.Fatal("the rejected ask was answered with a credential")
	}
}

// A person's pause is never replaced by the gate's, whichever lands first, so the next sign-in resumes nothing the
// person paused.
func TestSignInGate_APersonsPauseOutlivesTheGatesSweep(t *testing.T) {
	gatePauses := func(v *gateVerbs) int {
		n := 0
		for _, p := range v.pauseParams() {
			if _, reasoned := p["reason"]; reasoned {
				n++
			}
		}
		return n
	}
	t.Run("the person's pause landed before the sweep read the run", func(t *testing.T) {
		g := newGateRun(t, t.TempDir())
		g.signIn(t)
		g.runningNow(t)
		verbs := g.kasPauses(t, 0)
		if err := g.h.runs.pause(t.Context(), gateRunID); err != nil {
			t.Fatalf("Setup: the person's pause = %v", err)
		}

		g.lapseAndAsk(t)
		if n := gatePauses(verbs); n != 0 {
			t.Errorf("gate pauses sent = %d, want none over the person's pause", n)
		}
		g.b.set(true, true, nil)
		g.signIn(t)
		awaitResumes(t, &verbs.resumes, 0)
	})
	t.Run("the person's pause in flight when the sweep reached the run", func(t *testing.T) {
		g := newGateRun(t, t.TempDir())
		g.signIn(t)
		g.runningNow(t)
		verbs := g.kasPauses(t, 100*time.Millisecond)
		personDone := make(chan error, 1)
		go func() { personDone <- g.h.runs.pause(t.Context(), gateRunID) }()
		waitUntil(t, "the person's pause to be sent", func() bool { return len(verbs.pauseParams()) == 1 })

		g.lapseAndAsk(t)
		if err := <-personDone; err != nil {
			t.Fatalf("the person's pause = %v", err)
		}
		if n := gatePauses(verbs); n != 0 {
			t.Errorf("gate pauses sent = %d, want none over the person's pause", n)
		}
		g.b.set(true, true, nil)
		g.signIn(t)
		awaitResumes(t, &verbs.resumes, 0)
	})
	t.Run("the person's pause pending on a step still running", func(t *testing.T) {
		g := newGateRun(t, t.TempDir())
		g.signIn(t)
		g.runningNow(t)
		verbs := g.kasPauses(t, 0)
		if err := g.h.runs.pause(t.Context(), gateRunID); err != nil {
			t.Fatalf("Setup: the person's pause = %v", err)
		}
		g.br.setCallResult(methodKiroWorkflowInspect, pendingInspect(t))

		g.lapseAndAsk(t)
		if n := gatePauses(verbs); n != 0 {
			t.Errorf("gate pauses sent = %d, want none over the person's pending pause", n)
		}
	})
	t.Run("the gate's pause landed first", func(t *testing.T) {
		g := newGateRun(t, t.TempDir())
		g.signIn(t)
		g.runningNow(t)
		verbs := g.kasPauses(t, 0)
		g.lapseAndAsk(t)
		if n := gatePauses(verbs); n != 1 {
			t.Fatalf("Setup: gate pauses sent = %d, want 1", n)
		}
		if err := g.h.runs.pause(t.Context(), gateRunID); err != nil {
			t.Fatalf("the person's pause = %v", err)
		}

		// KAS lists the run paused once both pauses landed, so the opening edge's finder reads it.
		g.listed(marotte.RunStatusPaused)
		reads := countCalls(g.br, methodKiroWorkflowInspect)
		g.b.set(true, true, nil)
		g.signIn(t)
		waitUntil(t, "the opening edge's finder to read the run", func() bool {
			return countCalls(g.br, methodKiroWorkflowInspect) > reads
		})
		awaitResumes(t, &verbs.resumes, 0)
	})
}

// pendingInspect is a running run whose step has not reached the boundary a person's pause waits for.
func pendingInspect(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": gateRunID,
		"state": map[string]any{
			"status": marotte.RunStatusRunning, "pausePending": map[string]any{"initiator": initiatorUser},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// The sweep pauses a listed run only on a fresh read of it: a read that fails, or answers about another run, is a
// sweep to retry, and a run KAS no longer knows is done.
func TestSignInGate_TheSweepPausesOnlyARunItReadRunning(t *testing.T) {
	for _, tc := range []struct {
		set     func(g *gateRun)
		name    string
		wantErr bool
	}{
		{name: "an unreadable run", wantErr: true, set: func(g *gateRun) {
			g.br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{Code: -32603, Message: "Internal error"})
		}},
		{name: "a reply about another run", wantErr: true, set: func(g *gateRun) {
			g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, "wf_other", marotte.RunStatusRunning, ""))
		}},
		{name: "a run KAS no longer knows", wantErr: false, set: func(g *gateRun) {
			g.br.setCallRPCErr(methodKiroWorkflowInspect, &marotte.RPCError{
				Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"details":"workflow not found"}`),
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateRun(t, t.TempDir())
			g.listed(marotte.RunStatusRunning)
			tc.set(g)
			pauses := countCalls(g.br, methodKiroWorkflowPause)

			err := g.h.runs.sweepPause(t.Context())
			if (err != nil) != tc.wantErr {
				t.Errorf("sweep = %v, want an error %v", err, tc.wantErr)
			}
			if n := countCalls(g.br, methodKiroWorkflowPause); n != pauses {
				t.Errorf("pauses sent = %d, want none", n-pauses)
			}
		})
	}
}
