package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/signin"
)

const gateBackendID signin.BackendID = "test"

const gateCallback = "_test/auth/token"

// gateBackend is a host-callback backend whose device flow is approved at once and whose renewal policy the test
// sets: fresh and servable judge every record but a renewed one, which is always fresh unless short, and renew
// decides every renewal.
type gateBackend struct {
	renew     func() error
	refreshes int
	mu        sync.Mutex
	fresh     bool
	servable  bool
	// short makes a renewal return a lease too short to use.
	short bool
	// hang makes a renewal block until its context ends.
	hang bool
}

func (b *gateBackend) setShort(short bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.short = short
}

func (b *gateBackend) setHang(hang bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hang = hang
}

var _ signin.Backend = (*gateBackend)(nil)

func newGateBackend() *gateBackend { return &gateBackend{fresh: true, servable: true} }

// set changes the renewal policy for every later ask.
func (b *gateBackend) set(fresh, servable bool, renew func() error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fresh, b.servable, b.renew = fresh, servable, renew
}

func (b *gateBackend) refreshCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refreshes
}

func (*gateBackend) Declaration() signin.Declaration {
	return signin.Declaration{Backend: gateBackendID, HostCallback: gateCallback, Flows: []signin.FlowKind{signin.FlowDevice}}
}

func (*gateBackend) Begin(context.Context, signin.FlowParams) (signin.DeviceStart, error) {
	return signin.DeviceStart{}, nil
}

func (*gateBackend) Poll(context.Context, signin.DeviceStart) (signin.Credential, error) {
	return signin.Credential{Data: json.RawMessage(`{}`)}, nil
}

func (b *gateBackend) Refresh(ctx context.Context, _ signin.Credential) (signin.Credential, error) {
	b.mu.Lock()
	b.refreshes++
	renew, short, hang := b.renew, b.short, b.hang
	b.mu.Unlock()
	if hang {
		<-ctx.Done()
		return signin.Credential{}, ctx.Err()
	}
	if renew != nil {
		if err := renew(); err != nil {
			return signin.Credential{}, err
		}
	}
	if short {
		return signin.Credential{Data: json.RawMessage(shortData)}, nil
	}
	return signin.Credential{Data: json.RawMessage(renewedData)}, nil
}

const (
	renewedData = `{"renewed":true}`
	shortData   = `{"short":true}`
)

func (*gateBackend) Validate(signin.Credential) error           { return nil }
func (*gateBackend) Label(signin.Credential) signin.Identity    { return signin.Identity{} }
func (*gateBackend) LiveSwitchable(_, _ signin.Credential) bool { return true }

func (b *gateBackend) Fresh(c signin.Credential, _ time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fresh || string(c.Data) == renewedData
}

func (b *gateBackend) Servable(signin.Credential, time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.servable
}

func (*gateBackend) Result(signin.Credential, time.Time) (json.RawMessage, error) {
	return json.RawMessage(`{"accessToken":"t"}`), nil
}

func (*gateBackend) Refusal(r signin.Refusal) *marotte.RPCError {
	return &marotte.RPCError{Code: -32000, Message: r.String()}
}

// gateRun is a restart-paused run of parkChat's session chain on a runtime whose sign-in vault opened on dir.
type gateRun struct {
	h     *Runtime
	br    *fakeBridge
	vault *signin.Vault
	b     *gateBackend
}

const gateRunID = "wf_1"

// openVault opens a vault on dir and joins its goroutines before the cleanups registered ahead of it, dir's removal
// among them: the vault saves dir as its lifetime ends, so on t.Context() that save races t.TempDir's RemoveAll.
func openVault(t *testing.T, dir string, backends ...signin.Backend) *signin.Vault {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	vault := signin.Open(ctx, dir, backends...)
	t.Cleanup(func() {
		cancel()
		vault.Wait()
	})
	return vault
}

func newGateRun(t *testing.T, dir string, opts ...Option) *gateRun {
	t.Helper()
	b := newGateBackend()
	vault := openVault(t, dir, b)
	h, br := agentRunOnChat(t, gateRunID, append([]Option{WithSignIn(vault, gateBackendID)}, opts...)...)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.grantLease(t.Context(), gateRunID, "publish", agentLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), gateRunID, parkSession)
	g := &gateRun{h: h, br: br, vault: vault, b: b}
	g.listed(marotte.RunStatusPaused)
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, gateRunID, stalePauseReason))
	return g
}

// listed sets the run's status in KAS's run list.
func (g *gateRun) listed(status marotte.RunStatus) {
	raw, err := json.Marshal(map[string]any{"runs": []map[string]any{{
		"workflowId": gateRunID, "name": "publish", "workflowName": "publish", "status": status, "parentSessionId": parkSession,
	}}})
	if err != nil {
		panic(err)
	}
	g.br.setCallResult(methodKiroWorkflowList, raw)
}

// pauses records every pause verb's params and answers it as KAS's landed pause reads, held by its reason;
// resumes counts every resume and flips the run to running.
type gateVerbs struct {
	pauses  []map[string]any
	resumes atomic.Int32
	mu      sync.Mutex
}

func (v *gateVerbs) pauseParams() []map[string]any {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.pauses)
}

func (g *gateRun) watchVerbs(t *testing.T) *gateVerbs {
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
			reason, _ := params["reason"].(string)
			g.br.setCallResult(methodKiroWorkflowInspect, heldInspect(t, reason))
			g.listed(marotte.RunStatusPaused)
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

// heldInspect is a run KAS parked on a reasoned pause: attributed to the user, the reason kept as stopReason.
func heldInspect(t *testing.T, reason string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": gateRunID,
		"state": map[string]any{
			"status": marotte.RunStatusPaused, "pauseReason": "Workflow paused during node 'review'.",
			"stopInitiator": initiatorUser, "stopReason": reason,
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func (g *gateRun) signIn(t *testing.T) {
	t.Helper()
	if _, err := g.vault.StartSignIn(t.Context(), gateBackendID, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
		t.Fatalf("StartSignIn: %v", err)
	}
	waitUntil(t, "the sign-in to commit", func() bool { return g.vault.Answerability(gateBackendID) == signin.Answerable })
	// The vault's listener reaches the gate asynchronously, and a lapse before the gate read the sign-in would leave
	// no closing edge to observe: read it now, as any admission does.
	if !g.h.runs.gateOpen() {
		t.Error("the gate reads closed on an answerable sign-in")
	}
}

// awaitEdgeFinder waits for the opening edge's finder of gate pauses to read the one run newGateRun lists, so a later
// count of list or inspect calls starts after it.
func (g *gateRun) awaitEdgeFinder(t *testing.T) {
	t.Helper()
	waitUntil(t, "the opening edge's finder", func() bool {
		return countCalls(g.br, methodKiroWorkflowList) >= 1 && countCalls(g.br, methodKiroWorkflowInspect) >= 1
	})
}

// askVault answers the engine's credential ask from the vault alone, within ctx, without answerHostCallback's pause
// before a refusal: it moves the vault's answerability, and the gate's sweeps follow that edge on their own.
func (g *gateRun) askVault(ctx context.Context, t *testing.T) *marotte.RPCError {
	t.Helper()
	answer, ok := g.h.signin.hostCallback(gateCallback)
	if !ok {
		t.Fatal("the backend's host callback is not answered")
	}
	_, rerr := answer(ctx)
	return rerr
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// writeLapsed leaves a signin.json whose record the identity provider rejected before the restart.
func writeLapsed(t *testing.T, dir string) {
	t.Helper()
	raw := `{"backends":{"test":{"data":{},"lapsed":true}}}`
	if err := os.WriteFile(filepath.Join(dir, "signin.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("write signin.json: %v", err)
	}
}

// A boot with no sign-in resumes nothing it would fail: the rehydrate sweep holds the restart-paused run, and the
// sign-in that commits resumes it exactly once.
func TestSignInGate_ABootWithNoSignInHoldsTheRunUntilTheSignIn(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	resumes := countResumes(t, g.br, gateRunID)

	g.h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	awaitResumes(t, resumes, 0)

	g.signIn(t)
	awaitResumes(t, resumes, 1)
}

func TestSignInGate_ALapsedSignInAtBootHoldsTheRun(t *testing.T) {
	dir := t.TempDir()
	writeLapsed(t, dir)
	g := newGateRun(t, dir)
	if st := g.vault.Status(gateBackendID); st.Kind != signin.StatusSignedOut {
		t.Fatalf("Setup: Status = %v, want the lapsed record read as signed out", st.Kind)
	}
	resumes := countResumes(t, g.br, gateRunID)

	g.h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	awaitResumes(t, resumes, 0)

	g.signIn(t)
	awaitResumes(t, resumes, 1)
}

// An API key authenticates the engine, so the vault's sign-in never holds a run.
func TestSignInGate_APIKeyModePassesTheGate(t *testing.T) {
	g := newGateRun(t, t.TempDir(), WithAPIKey(true))
	resumes := countResumes(t, g.br, gateRunID)
	if _, err := g.h.runs.admitRunWork(refuseClosed, gateRunID, ""); err != nil {
		t.Errorf("admission with an API key = %v, want none", err)
	}

	g.h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	awaitResumes(t, resumes, 1)
}

// A lapse while signed in pauses the running run with marotte's reason, which no heal resumes, and the next sign-in
// resumes it.
func TestSignInGate_ALapsePausesTheRunningRunAndTheNextSignInResumesIt(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.listed(marotte.RunStatusRunning)
	g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
	verbs := g.watchVerbs(t)

	g.b.set(false, false, func() error { return signin.ErrRejected })
	if rerr := g.askVault(t.Context(), t); rerr == nil {
		t.Fatal("a rejected renewal was answered")
	}
	waitUntil(t, "the running run's pause", func() bool { return len(verbs.pauseParams()) == 1 })
	p := verbs.pauseParams()[0]
	if p["initiator"] != initiatorUser || p["reason"] != signInHoldReason {
		t.Errorf("pause params = %v, want initiator %q and reason %q", p, initiatorUser, signInHoldReason)
	}
	// The landed pause's own frame: the heal must leave it alone.
	heal := g.h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	heal(t.Context(), parkChat, runNotif(methodWFPaused, map[string]any{
		"workflowId": gateRunID, "pauseReason": "Workflow paused during node 'review'.",
		"initiator": initiatorUser, "initiatorReason": signInHoldReason,
	}))
	awaitResumes(t, &verbs.resumes, 0)

	g.b.set(true, true, nil)
	g.signIn(t)
	awaitResumes(t, &verbs.resumes, 1)
}

// A renewal the identity provider does not answer is retried within the vault's window, then the running run is
// paused with the unreachable reason, and the next renewal that succeeds resumes it. In a bubble, whose clock passes
// the whole window at once.
func TestSignInGate_AnUnreachableProviderPausesThenResumesOnTheNextRenewal(t *testing.T) {
	synctest.Test(t, unreachableProviderPausesThenResumes)
}

func unreachableProviderPausesThenResumes(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	g.signIn(t)
	g.listed(marotte.RunStatusRunning)
	g.br.setCallResult(methodKiroWorkflowInspect, inspectReply(t, gateRunID, marotte.RunStatusRunning, ""))
	verbs := g.watchVerbs(t)

	down := errors.New("dial tcp: i/o timeout")
	g.b.set(false, false, func() error { return down })
	if rerr := g.askVault(t.Context(), t); rerr == nil || rerr.Message != signin.RefusedUnreachable.String() {
		t.Fatalf("ask = %v, want the unreachable refusal", rerr)
	}
	if n := g.b.refreshCount(); n < 1 {
		t.Errorf("renewals = %d, want at least one", n)
	}
	waitUntil(t, "the running run's pause", func() bool { return len(verbs.pauseParams()) == 1 })
	if p := verbs.pauseParams()[0]; p["reason"] != providerHoldReason {
		t.Errorf("pause reason = %v, want %q", p["reason"], providerHoldReason)
	}
	if _, err := g.h.runs.admitRunWork(refuseClosed, gateRunID, ""); !errors.Is(err, errRunsProviderDown) {
		t.Errorf("admission = %v, want the unreachable refusal", err)
	}
	awaitResumes(t, &verbs.resumes, 0)

	g.b.set(false, false, nil)
	if rerr := g.askVault(t.Context(), t); rerr != nil {
		t.Fatalf("ask after the provider came back = %v, want an answer", rerr)
	}
	awaitResumes(t, &verbs.resumes, 1)
}

// The sign-in edge, the heal timer and the rehydrate sweep reaching one pause at once write one resume: each reads
// the run under its resume lock, after the resume an earlier one wrote. KAS reads the run paused until the resume's
// reply, so a read outside the lock would resume it again.
func TestResume_EveryAutomaticResumerAtOnceResumesOnce(t *testing.T) {
	prev := healBaseDelay
	healBaseDelay = time.Millisecond
	t.Cleanup(func() { healBaseDelay = prev })
	g := newGateRun(t, t.TempDir())
	var resumes atomic.Int32
	running := inspectReply(t, gateRunID, marotte.RunStatusRunning, "")
	g.br.mu.Lock()
	g.br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != methodKiroWorkflowResume {
			return nil, nil, false
		}
		resumes.Add(1)
		time.Sleep(20 * time.Millisecond)
		g.br.setCallResult(methodKiroWorkflowInspect, running)
		return json.RawMessage(`{}`), nil, true
	}
	g.br.mu.Unlock()
	g.h.runs.resumeInterruptedRuns(t.Context(), parkChat)

	heal := g.h.runs.healPaused(func(context.Context, marotte.ChatID, *marotte.RPCResponse) {})
	// Registered after the runtime's own listener, so the edge's resume has started when these do.
	g.vault.OnAnswerability(func(_ signin.BackendID, a signin.Answerability) {
		if a != signin.Answerable {
			return
		}
		go heal(t.Context(), parkChat,
			runNotif(methodWFPaused, map[string]any{"workflowId": gateRunID, "pauseReason": stalePauseReason}))
		go g.h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	})
	g.signIn(t)

	awaitResumes(t, &resumes, 1)
}

// Every driving verb a person sends while the gate is closed is refused unwritten, with the sign-in's reason.
func TestSignInGate_AUserResumeIsRefusedUnwritten(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	resumes := countResumes(t, g.br, gateRunID)

	err := g.h.runs.resume(t.Context(), gateRunID, callerPage)
	if !errors.Is(err, errRunsNeedSignIn) {
		t.Fatalf("resume signed out = %v, want the sign-in refusal", err)
	}
	rec := httptest.NewRecorder()
	g.h.runRoutes.writeControlErr(rec, verbResume, gateRunID, err)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "sign_in_required") ||
		!strings.Contains(rec.Body.String(), "sign in to Kiro first") {
		t.Errorf("the refusal's reply = %d %s, want 409 sign_in_required with its sentence", rec.Code, rec.Body.String())
	}
	awaitResumes(t, resumes, 0)
}

// pausedRunOnChat is a restart-paused run of parkChat's session chain, with no sign-in gate.
func pausedRunOnChat(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, br := agentRunOnChat(t, gateRunID)
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.grantLease(t.Context(), gateRunID, "publish", agentLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), gateRunID, parkSession)
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, map[string]any{
		"workflowId": gateRunID, "name": "publish", "status": "paused", "parentSessionId": parkSession,
	}))
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, gateRunID, stalePauseReason))
	return h, br
}

// chainRuns answers inspect per run, each paused for a restart until resumed, and records each resume by run.
type chainRuns struct {
	resumed map[string]int
	mu      sync.Mutex
}

func (c *chainRuns) count(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumed[id]
}

func watchChain(t *testing.T, br *fakeBridge, ids ...string) *chainRuns {
	t.Helper()
	c := &chainRuns{resumed: map[string]int{}}
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, map[string]any{"workflowId": id, "name": id, "status": "paused", "parentSessionId": parkSession})
	}
	br.setCallResult(methodKiroWorkflowList, kasRuns(t, rows...))
	br.mu.Lock()
	defer br.mu.Unlock()
	br.onCall = func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		id, _ := params[keyWorkflowID].(string)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch method {
		case methodKiroWorkflowInspect:
			if c.resumed[id] > 0 {
				return inspectReply(t, id, marotte.RunStatusRunning, ""), nil, true
			}
			return inspectPaused(t, id, stalePauseReason), nil, true
		case methodKiroWorkflowResume:
			c.resumed[id]++
			return json.RawMessage(`{}`), nil, true
		}
		return nil, nil, false
	}
	return c
}

// A run whose heal budget is spent stays paused for the user however its chat comes back: a sibling run's reopen
// sweeps the chain and resumes only the sibling. The user's own resume needs no budget.
func TestHealBudget_ASpentRunIsNotResumedByItsChatsSweep(t *testing.T) {
	h, br := agentRunOnChat(t, "wf_spent")
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, id := range []string{"wf_spent", "wf_sibling"} {
		h.runs.grantLease(t.Context(), id, "publish", agentLaunch(), hostSec())
		h.runs.recordParentSession(t.Context(), id, parkSession)
	}
	chain := watchChain(t, br, "wf_spent", "wf_sibling")
	for range maxAutoHeals {
		h.runs.claimHeal("wf_spent")
	}

	h.runs.resumeInterruptedRuns(t.Context(), parkChat)
	waitUntil(t, "the sibling's resume", func() bool { return chain.count("wf_sibling") == 1 })
	time.Sleep(quietWindow)
	if n := chain.count("wf_spent"); n != 0 {
		t.Errorf("the spent run was resumed %d times by its chat's sweep, want 0", n)
	}

	if err := h.runs.resume(t.Context(), "wf_spent", callerPage); err != nil {
		t.Fatalf("the user's resume = %v, want it written", err)
	}
	if n := chain.count("wf_spent"); n != 1 {
		t.Errorf("the user's resume wrote %d resumes, want 1 with the budget spent", n)
	}
}

// With the gate closed no run is created: a launch is refused before workflow/new, with the sign-in's reason.
func TestSignInGate_ALaunchIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	g := newGateRun(t, t.TempDir())
	if _, err := g.h.runs.admitRunWork(refuseClosed, gateRunID, ""); !errors.Is(err, errRunsNeedSignIn) {
		t.Fatalf("admission signed out = %v, want the sign-in refusal", err)
	}
	if _, _, err := g.h.runs.LaunchScheduled(t.Context(), "bundled://demo", "s1", marotte.RunDefaults{}, time.Time{}); !errors.Is(err, errRunsNeedSignIn) || !errors.Is(err, schedule.ErrNoRunCanStart) {
		t.Errorf("LaunchScheduled signed out = %v, want the sign-in refusal the schedule runner holds", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/runs",
		strings.NewReader(`{"source":"bundled://demo"}`))
	g.h.runRoutes.handleLaunch(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "sign_in_required") {
		t.Errorf("POST /api/runs signed out = %d %s, want 409 sign_in_required", rec.Code, rec.Body.String())
	}
	if calls := g.br.callLog(); slices.Contains(calls, methodKiroWorkflowNew) {
		t.Errorf("calls = %v, want no workflow/new", calls)
	}

	g.signIn(t)
	if _, _, err := g.h.runs.LaunchScheduled(t.Context(), "bundled://no-such-recipe", "s1", marotte.RunDefaults{}, time.Time{}); err == nil || errors.Is(err, schedule.ErrNoRunCanStart) {
		t.Errorf("LaunchScheduled of a missing recipe signed in = %v, want a failure the schedule runner records", err)
	}
}
