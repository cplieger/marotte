package agent

// Every run stays reachable: a verb on a non-resident run makes it resident first.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/workflow"
)

// One `run_complete` frame reports terminal and paused alike; only the status differs.
func runCompleteFrame(t *testing.T, workflowID, status string) *marotte.RPCResponse {
	t.Helper()
	params, err := json.Marshal(map[string]any{"workflowId": workflowID, "status": status})
	if err != nil {
		t.Fatalf("marshal run_complete: %v", err)
	}
	return &marotte.RPCResponse{Method: methodWFRunComplete, Params: params}
}

const testLaunchSession = "sess_launch"

func parentlessRunList(workflowID string) json.RawMessage {
	return json.RawMessage(`{"runs":[{"workflowId":"` + workflowID +
		`","status":"paused","parentSessionId":"` + testLaunchSession + `"}]}`)
}

// leaseParentless leases a parentless run launched from testLaunchSession with presets recorded.
func leaseParentless(t *testing.T, h *Runtime, workflowID string, presets ...string) {
	t.Helper()
	if presets == nil {
		presets = []string{}
	}
	leaseRun(t, h, runlease.Lease{
		WorkflowID: workflowID, ParentSessionID: testLaunchSession,
		Host: &runlease.HostSecurity{Presets: presets},
	})
}

// A terminal run's host session closes; a pause keeps it resident, since one engine holds it at no process cost.
func TestRunComplete_ATerminalRunReleasesItsHostSessionAndAPauseKeepsIt(t *testing.T) {
	cases := map[string]struct {
		status    string
		wantOpen  bool
		wantLease bool
	}{
		"a pause keeps both":          {string(marotte.RunStatusPaused), true, true},
		"a completed run drops both":  {"completed", false, false},
		"a failed run drops both":     {"failed", false, false},
		"a cancelled run drops both":  {"cancelled", false, false},
		"an executing run keeps both": {"running", true, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
			h.runs.grantLease(t.Context(), "wf_1", "nightly", manualLaunch(), hostSec())

			h.dispatch(t.Context(), runChatID("wf_1"), h.originOf(runChatID("wf_1")), runCompleteFrame(t, "wf_1", tc.status))

			deadline := time.Now().Add(3 * time.Second)
			for br.Open() != tc.wantOpen && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if br.Open() != tc.wantOpen {
				t.Errorf("host session open = %v for status %q, want %v", br.Open(), tc.status, tc.wantOpen)
			}
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v for status %q, want %v", held, tc.status, tc.wantLease)
			}
		})
	}
}

// A paused lease must keep its fields, minus the deadline, which the pause parks.
func TestRunStopped_APausedRunKeepsTheFieldsItsLeaseIsFor(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	// A scheduled launch carries the unattended mark.
	h.runs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch("sched_1", time.Now().Add(time.Hour)), hostSec())

	h.dispatch(t.Context(), runChatID("wf_1"), h.originOf(runChatID("wf_1")), runCompleteFrame(t, "wf_1", string(marotte.RunStatusPaused)))

	l, held := h.runs.lease("wf_1")
	if !held {
		t.Fatal("the parked run lost its lease, so nothing knows it may not start twice")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly: admission explains a blocking row from it", l.Recipe)
	}
	if l.ScheduleID != "sched_1" {
		t.Errorf("schedule_id = %q, want sched_1: the outcome has nowhere to be recorded "+
			"without it", l.ScheduleID)
	}
	if !l.Unattended {
		t.Error("the unattended mark went with the pause, so the permission floor would " +
			"leave the next ask waiting for a human who will never arrive")
	}
}

// A resident host session is handed back with no second load.
func TestEnsureResident_AResidentRunIsHandedBackWithNoLoad(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	incumbent := newFakeBridge()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: incumbent, state: bridgeIdle})
	leaseParentless(t, h, "wf_1")
	second := newFakeBridge()
	fakeSourceOf(t, h).setFactory(func() engineSession { return second })

	host, err := h.runs.ensureResident(t.Context(), "wf_1")
	if err != nil {
		t.Fatalf("ensureResident on a resident run = %v, want the incumbent", err)
	}
	if host.sb.current() != incumbent {
		t.Error("the caller was handed a session other than the resident one")
	}
	if second.startCount() != 0 {
		t.Error("a second load was written for a run whose host session was resident")
	}
}

// session/load sends `_kiro/terminal/shell_type` first, so the host lane must be registered and forwarded before
// the load (KAS 2.27.0 otherwise stalls ~2s).
func TestEnsureResident_AHostRequestDuringTheLoadIsAnswered(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	leaseParentless(t, h, "wf_1")
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", ""))
	host := &hostRequestingBridge{fakeBridge: newFakeBridge(), answered: make(chan struct{})}
	fakeSourceOf(t, h).setFactory(func() engineSession { return host })

	if err := h.runs.resume(t.Context(), "wf_1", callerPage); err != nil {
		t.Fatalf("Resume on a non-resident parentless run = %v, want nil", err)
	}
	if !slices.Contains(host.callLog(), methodKiroWorkflowResume) {
		t.Errorf("the resume never reached the loaded host session; calls were %v", host.callLog())
	}
}

// hostRequestingBridge sends `_kiro/terminal/shell_type` from inside session/load and fails unless answered.
type hostRequestingBridge struct {
	*fakeBridge
	answered chan struct{}
	once     sync.Once
}

func (b *hostRequestingBridge) door(ctx context.Context, so *marotte.SessionOpts, eo *marotte.EngineOpts, load bool) error {
	if load {
		id := int64(41)
		b.deliver(&marotte.RPCResponse{ID: &id, Method: methodKiroShellType})
		select {
		case <-b.answered:
		case <-time.After(2 * time.Second):
			return errors.New("session/load: _kiro/terminal/shell_type was never answered")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.fakeBridge.door(ctx, so, eo, load)
}

func (b *hostRequestingBridge) Respond(ctx context.Context, id int64, result any, err error) error {
	b.once.Do(func() { close(b.answered) })
	return b.fakeBridge.Respond(ctx, id, result, err)
}

// The lease follows the unknown-outcome rule: armDeadline needs it, so dropping it on a cancellation leaves a taken
// retry unbounded.
func TestRetry_ACancelledRetryKeepsTheLeaseItMinted(t *testing.T) {
	cases := map[string]struct {
		cause     error
		wantLease bool
	}{
		// The client walked away mid-verb.
		"a cancelled retry keeps it": {context.Canceled, true},
		"a deadline keeps it":        {context.DeadlineExceeded, true},
		// A known refusal gives the lease back.
		"a refused retry gives it back": {errors.New("refused"), false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: parentlessRunList("wf_1"),
			}
			br.callErrs = map[string]error{methodKiroWorkflowRetry: tc.cause}
			if _, held := h.runs.lease("wf_1"); held {
				t.Fatal("the fixture holds a lease, so Retry mints none and this proves nothing")
			}

			aff := h.runs.affordance(t.Context(), "wf_1", "aborted")
			if _, err := h.runs.retry(t.Context(), "wf_1", aff); err == nil {
				t.Fatal("a failed retry reported success")
			}
			if !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
				t.Fatalf("Setup: the retry never reached KAS; calls were %v", br.callLog())
			}
			why := "a run KAS may be driving cannot be bounded without the lease armDeadline reads"
			if !tc.wantLease {
				why = "a lease minted for a run that provably never re-drove was stranded"
			}
			if _, held := h.runs.lease("wf_1"); held != tc.wantLease {
				t.Errorf("lease held = %v, want %v: %s", held, tc.wantLease, why)
			}
		})
	}
}

// leasedNonResident is a leased parentless run whose host session is not resident, answering inspect with reply.
func leasedNonResident(t *testing.T, reply json.RawMessage) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	leaseParentless(t, h, "wf_1")
	br.setCallResult(methodKiroWorkflowInspect, reply)
	return h, br
}

// KAS reroutes a prompt only while the addressed step waits; past that it runs as an ordinary turn,
// so an ask the fresh read no longer holds is final and nothing is sent.
func TestAnswerInput_AMovedOnStepIsGone(t *testing.T) {
	cases := map[string]json.RawMessage{
		"a different execution waits now": inspectAsk(t, "wf_1", marotte.RunStatusPaused, parkedStep("review", "sess_other")),
		"the run is over":                 inspectReply(t, "wf_1", "failed", ""),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			h, br := leasedNonResident(t, reply)

			err := h.runs.answerInput(t.Context(), "wf_1", markerlessAskID(h, "wf_1", reviewPath, "sess_step"), "the main branch")
			if !errors.Is(err, errAskGone) {
				t.Fatalf("AnswerInput for a moved-on step = %v, want errAskGone", err)
			}
			if slices.Contains(br.callLog(), marotte.MethodPrompt) {
				t.Error("the answer was sent anyway; KAS runs it as an ordinary turn on a step nobody asked to steer")
			}
		})
	}
}

// The answering request registers its own pending before its read, which holds every ask of the run;
// KAS's own refusal conditions still refuse it.
func TestAnswerInput_ARunningSiblingRefusesTheAnswer(t *testing.T) {
	h, br := leasedNonResident(t, inspectAsk(t, "wf_1", marotte.RunStatusRunning,
		parkedStep("review", "sess_step"), askStepNode{id: "build", status: "running", session: "sess_build"}))
	askID := markerlessAskID(h, "wf_1", reviewPath, "sess_step")

	if err := h.runs.answerInput(t.Context(), "wf_1", askID, "the main branch"); !errors.Is(err, errAskBranchRunning) {
		t.Fatalf("AnswerInput beside a running branch = %v, want errAskBranchRunning", err)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Error("the answer was sent while a sibling ran, where KAS declines the reroute")
	}
	if p := lastAsksOf(t, h, "wf_1"); len(p.Asks) != 1 || p.Asks[0].Blocked != marotte.RunAskBranchRunning {
		t.Errorf("after the refusal the run publishes %+v, want its ask shown, blocked by the running branch", p.Asks)
	}
}

// Each parked branch is its own ask, so the second one is answered at its own session.
func TestAnswerInput_AParkedBranchIsAnsweredAtItsOwnSession(t *testing.T) {
	h, br := leasedNonResident(t, inspectAsk(t, "wf_1", marotte.RunStatusPaused,
		parkedStep("branch_a", "sess_a"), parkedStep("branch_b", "sess_b")))
	askID := markerlessAskID(h, "wf_1", workflow.PathKey([]string{"root", "branch_b"}), "sess_b")

	if err := h.runs.answerInput(t.Context(), "wf_1", askID, "the main branch"); err != nil {
		t.Fatalf("AnswerInput for the second parked branch = %v, want nil", err)
	}
	if got := br.paramsFor(marotte.MethodPrompt)["sessionId"]; got != "sess_b" {
		t.Errorf("sessionId = %v, want sess_b", got)
	}
}

// A read that fails is not "nothing waits": nothing is sent, the refusal is retryable, and the
// answer's pending registration is gone so the card is sendable again.
func TestAnswerInput_AnUnreadableRunRefusesRetryably(t *testing.T) {
	h, br := leasedNonResident(t, nil)
	br.setCallErr(methodKiroWorkflowInspect, errors.New("engine ended"))

	err := h.runs.answerInput(t.Context(), "wf_1", markerlessAskID(h, "wf_1", reviewPath, "sess_step"), "the main branch")
	if !errors.Is(err, errAskUnreadable) {
		t.Fatalf("AnswerInput with an unreadable run = %v, want errAskUnreadable", err)
	}
	if status, _, _ := command.StatusOf(err); status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Error("an answer was sent to a run marotte could not read")
	}
	if p := h.runs.asks.get("wf_1").pendingOf(); p != nil {
		t.Error("the refused answer left its pending set, so every card stays disabled")
	}
	if lastAsksOf(t, h, "wf_1").Reachable {
		t.Error("the run reads reachable after a failed read")
	}
}

// A run marotte holds no lease for has no cell: the answer is refused before any session is made resident.
func TestAnswerInput_AnUnleasedRunMakesNoSessionResident(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}

	before := br.startCount()
	if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); !errors.Is(err, errAskGone) {
		t.Fatalf("AnswerInput for an unleased run = %v, want errAskGone", err)
	}
	if got := br.startCount() - before; got != 0 {
		t.Errorf("session doors during the call = %d, want 0", got)
	}
}

// Cancel is an engine call on the run id: it opens no host session.
func TestCancel_MakesNoSessionResident(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    parentlessRunList("wf_1"),
		methodKiroWorkflowCancel:  json.RawMessage(`{}`),
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
	}
	h.runs.grantLease(t.Context(), "wf_1", "nightly", manualLaunch(), hostSec())

	before := br.startCount()
	if err := h.runs.cancel(t.Context(), "wf_1", callerPage); err != nil {
		t.Fatalf("Cancel on a non-resident run = %v, want nil", err)
	}
	if got := br.startCount() - before; got != 0 {
		t.Errorf("session doors during a cancel = %d, want 0", got)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("cancel registered a host session")
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Errorf("the cancel verb never went out; calls were %v", br.callLog())
	}
}

// A failed run stays openable with no lease and no host session: the read is an engine call and passes `state` and
// `nodePlan` through.
func TestHandleRun_AParkedRunsPageRendersWithNoLeaseAndNoHostSession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	tree, err := json.Marshal(map[string]any{
		"workflowId": "wf_1",
		"state": map[string]any{
			"status":      string(marotte.RunStatusPaused),
			"pauseReason": "Step 'review' is waiting for user input.",
			"root": map[string]any{
				"nodeId": "review", "type": "step", "status": string(marotte.RunStatusPaused),
			},
		},
		"nodePlan": map[string]any{"type": "sequence"},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowInspect: tree}

	if _, held := h.runs.lease("wf_1"); held {
		t.Fatal("the fixture holds a lease, so this exercises the wrong state")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/wf_1", nil)
	req.SetPathValue("id", "wf_1")
	h.runRoutes.handleRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/runs/wf_1 with no lease and no host session = %d, want 200: %s",
			rec.Code, rec.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the reply: %v", err)
	}
	for _, key := range []string{"state", "nodePlan"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the reply carries no %q, so the tab has no tree to render", key)
		}
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("the read made a host session resident")
	}
}

// A parentless run's host session loads its launching session with the presets its lease recorded at launch, never
// the current profile's: a fresh session would make its steps ask, and today's profile is not the launch's.
func TestEnsureResident_LoadsAParentlessRunsLaunchingSession(t *testing.T) {
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	leaseParentless(t, h, "wf_1", "preset-at-launch")
	br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", ""))

	if err := h.runs.resume(t.Context(), "wf_1", callerPage); err != nil {
		t.Fatalf("Resume on a non-resident parentless run = %v, want nil", err)
	}
	opts := br.lastSessionOpts()
	if opts == nil || opts.SessionID != testLaunchSession || opts.Role != marotte.SessionRoleHost {
		t.Fatalf("the door was %+v, want a host load of the launching session %q", opts, testLaunchSession)
	}
	if !slices.Equal(opts.Presets, []string{"preset-at-launch"}) {
		t.Errorf("the load carried presets %v, want the lease's [preset-at-launch]", opts.Presets)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Error("the loaded host session is not registered under the run's synthetic chat id")
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Errorf("the resume never reached KAS; calls were %v", br.callLog())
	}
}

// A chat-parented run is made resident through its chat's own door, never as a host lane; a refused verb leaves the
// chat's session open.
func TestEnsureResident_AChatParentedRunIsMadeResidentThroughItsChat(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_owned",
		}),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", ""),
	}
	br.setCallRPCErr(methodKiroWorkflowResume, &marotte.RPCError{Code: -32603, Message: "Workflow wf_1 is not registered."})
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}

	if err := h.runs.resume(t.Context(), "wf_1", callerPage); err == nil {
		t.Fatal("Setup: the scripted resume refusal did not happen")
	}
	if opts := br.lastSessionOpts(); opts == nil || opts.SessionID != "sess_owned" || opts.Role != marotte.SessionRoleChat {
		t.Errorf("the door was %+v, want the chat's own session loaded through its chat", opts)
	}
	if sb := h.bridge.mgr.get("c1"); sb == nil || !sb.current().Open() {
		t.Fatal("a refused verb closed the chat's session, which belongs to the conversation")
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("a host lane was registered for a chat-parented run")
	}
}

// A lease no load can hold refuses the verb as a run state: nothing is registered, the chat's session stays, the
// ask stays offered.
func TestEnsureResident_ALaunchingSessionNoLoadCanHoldRefusesTheVerb(t *testing.T) {
	cases := map[string]struct {
		lease runlease.Lease
		chain []string
		// failLoads makes every session the factory builds refuse a named load.
		failLoads bool
	}{
		"the lease records no launching session": {
			lease: runlease.Lease{WorkflowID: "wf_1", Host: hostSec()},
		},
		"the lease records no security profile": {
			lease: runlease.Lease{WorkflowID: "wf_1", ParentSessionID: testLaunchSession},
		},
		"the chat's load fell back to a fresh session": {
			lease:     runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: "sess_owned"},
			chain:     []string{"sess_owned"},
			failLoads: true,
		},
		"the run came from a segment the chat has retired": {
			lease: runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: "sess_launched_from"},
			chain: []string{"sess_launched_from", "sess_now"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, cs, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			if len(tc.chain) > 0 {
				if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
					c.Name = "Launcher"
					for _, s := range tc.chain {
						c.RecordSession(s)
					}
					return true
				}); err != nil {
					t.Fatalf("Setup: seeding the chat: %s", err)
				}
			}
			var built []*fakeBridge
			var mu sync.Mutex
			if tc.failLoads {
				fakeSourceOf(t, h).setFactory(func() engineSession {
					f := newFakeBridge()
					f.loadErr = errors.New("session/load: refused")
					mu.Lock()
					built = append(built, f)
					mu.Unlock()
					return f
				})
			}
			leaseRun(t, h, tc.lease)

			err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch")
			if !errors.Is(err, errLaunchSessionUnavailable) {
				t.Fatalf("AnswerInput = %v, want errLaunchSessionUnavailable", err)
			}
			if errors.Is(err, errRunHostStart) {
				t.Error("a state of the run was reported as a load fault on this server")
			}
			calls := slices.Clone(br.callLog())
			mu.Lock()
			for _, f := range built {
				calls = append(calls, f.callLog()...)
			}
			mu.Unlock()
			if slices.Contains(calls, marotte.MethodPrompt) {
				t.Errorf("the answer was sent although no session holds the launching session; calls were %v", calls)
			}
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Error("a host lane was left registered for a run nothing can drive")
			}
			if len(tc.chain) > 0 && h.bridge.mgr.get("c1") == nil {
				t.Error("the refused verb closed the chat's session, which belongs to the conversation")
			}
			if h.runs.asks.get("wf_1").pendingOf() != nil {
				t.Error("a verb that never reached KAS left the run's answer pending")
			}
		})
	}
}

// Retry is never sent under another session.
func TestRetry_RefusesARunWhoseLaunchingSegmentTheChatRetired(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "aborted", "parentSessionId": "sess_launched_from",
		}),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_launched_from")
		c.RecordSession("sess_now")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}

	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance(t.Context(), "wf_1", "aborted"))
	if !errors.Is(err, errLaunchSessionUnavailable) {
		t.Fatalf("Retry = %v, want errLaunchSessionUnavailable", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry was sent under a session the run was not launched from")
	}
}

// A refused load and an unreadable inventory are server faults, not run states; nothing is left registered.
func TestEnsureResident_AFailedLoadIsAFaultOnThisServer(t *testing.T) {
	cases := map[string]struct {
		loadErr error
		listErr error
		leased  bool
	}{
		"KAS refuses the load": {leased: true, loadErr: fmt.Errorf("session/load: %w",
			&marotte.RPCError{Code: -32603, Message: "Internal error"})},
		"the inventory cannot be read": {listErr: errors.New("engine ended")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList:    parentlessRunList("wf_1"),
				methodKiroWorkflowInspect: inspectPaused(t, "wf_1", ""),
			}
			br.loadErr = tc.loadErr
			if tc.listErr != nil {
				br.setCallErr(methodKiroWorkflowList, tc.listErr)
			}
			if tc.leased {
				leaseParentless(t, h, "wf_1")
			}
			starts := br.startCount()

			err := h.runs.resume(t.Context(), "wf_1", callerPage)
			if !errors.Is(err, errRunHostStart) {
				t.Fatalf("Resume = %v, want errRunHostStart", err)
			}
			if errors.Is(err, errLaunchSessionUnavailable) {
				t.Error("a fault on this server was reported as a state of the run")
			}
			if got := br.startCount() - starts; got != 0 {
				t.Errorf("%d sessions opened, want 0: no fresh session may stand in", got)
			}
			if h.bridge.mgr.get(runChatID("wf_1")) != nil {
				t.Error("a host lane was left registered for a run nothing loaded")
			}
			if slices.Contains(br.callLog(), methodKiroWorkflowResume) {
				t.Error("the resume was sent although no session holds the launching session")
			}
		})
	}
}

// A launch session no load can hold is a 409; a refused load is a 500.
func TestControlHandler_SplitsAnUnavailableLaunchSessionFromAFailedLoad(t *testing.T) {
	cases := map[string]struct {
		list    json.RawMessage
		loadErr error
		leased  bool
		want    int
	}{
		"no launching session is a 409": {
			list: json.RawMessage(`{"runs":[{"workflowId":"wf_1","status":"paused"}]}`),
			want: http.StatusConflict,
		},
		"a run the inventory does not list is a 404": {list: json.RawMessage(`{"runs":[]}`), want: http.StatusNotFound},
		"a refused load is a 500": {
			list:    parentlessRunList("wf_1"),
			leased:  true,
			loadErr: fmt.Errorf("session/load: %w", &marotte.RPCError{Code: -32603, Message: "Internal error"}),
			want:    http.StatusInternalServerError,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			t.Cleanup(func() { shutdownHub(t, h) })
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList:    tc.list,
				methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", ""),
			}
			br.loadErr = tc.loadErr
			if tc.leased {
				leaseParentless(t, h, "wf_1")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/runs/wf_1/resume", nil)
			req.SetPathValue("id", "wf_1")
			rec := httptest.NewRecorder()
			h.runRoutes.handleResume(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("resume = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusConflict && !strings.Contains(rec.Body.String(), "Cancel and delete still work") {
				t.Errorf("the body = %s, want the sentence naming what still works", rec.Body)
			}
		})
	}
}

// A run from a retired segment is refused even with its chat open: that session is another one.
func TestRetry_RefusesARetiredSegmentRunWhileItsChatIsOpen(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "aborted", "parentSessionId": "sess_launched_from",
		}),
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess_launched_from")
		c.RecordSession("sess_now")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}
	open, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil || open.SessionID() != "sess_now" {
		t.Fatalf("Setup: opening the chat = (%v, %v), want it live on sess_now", open, err)
	}

	_, err = h.runs.retry(t.Context(), "wf_1", h.runs.affordance(t.Context(), "wf_1", "aborted"))
	if !errors.Is(err, errLaunchSessionUnavailable) {
		t.Fatalf("Retry = %v, want errLaunchSessionUnavailable", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Error("the retry was sent on a chat open on a session the run was not launched from")
	}
}

// An incomplete chat scan proves nothing: the unreadable chat may own the session.
func TestEnsureResident_AnIncompleteChatScanRefusesToLoadAsAHost(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:    parentlessRunList("wf_1"),
		methodKiroWorkflowInspect: inspectPaused(t, "wf_1", ""),
	}
	unreadable := filepath.Join(cs.Dir(), "c9")
	if err := os.MkdirAll(unreadable, 0o700); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	if err := os.WriteFile(filepath.Join(unreadable, "chat.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("Setup: %s", err)
	}
	starts := br.startCount()

	if err := h.runs.resume(t.Context(), "wf_1", callerPage); !errors.Is(err, errRunHostStart) {
		t.Fatalf("Resume = %v, want errRunHostStart", err)
	}
	if got := br.startCount() - starts; got != 0 {
		t.Errorf("%d sessions opened, want 0: the session may belong to the unreadable chat", got)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) != nil {
		t.Error("a host lane was registered on a session no complete scan proved parentless")
	}
}

// nonResidentRunInBubble is a leased parentless run whose host load waits on gate.
func nonResidentRunInBubble(t *testing.T) (h *Runtime, host *fakeBridge, gate chan struct{}) {
	t.Helper()
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", marotte.RunStatusPaused, ""),
	}
	leaseParentless(t, h, "wf_1")
	host = newFakeBridge()
	gate = make(chan struct{})
	host.startGate = gate
	fakeSourceOf(t, h).setFactory(func() engineSession { return host })
	return h, host, gate
}

func TestEnsureResident_ConcurrentVerbsLoadOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, host, gate := nonResidentRunInBubble(t)

		errs := make(chan error, 2)
		go func() { errs <- h.runs.resume(t.Context(), "wf_1", callerPage) }()
		go func() { errs <- h.runs.resume(t.Context(), "wf_1", callerPage) }()
		// Both verbs are parked: one in the load, one on the run's lock.
		synctest.Wait()
		if got := host.startCount(); got != 0 {
			t.Fatalf("Setup: %d loads finished before the gate opened", got)
		}
		close(gate)

		for range 2 {
			if err := <-errs; err != nil {
				t.Errorf("Resume on a run another verb was loading = %v, want nil", err)
			}
		}
		if got := host.startCount(); got != 1 {
			t.Errorf("%d loads of the launching session, want 1", got)
		}
		if got := countCalls(host, methodKiroWorkflowResume); got != 2 {
			t.Errorf("%d resumes reached the host session, want 2; calls were %v", got, host.callLog())
		}
	})
}

func TestEnsureResident_AVerbThatGivesUpWaitingDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, host, gate := nonResidentRunInBubble(t)

		first := make(chan error, 1)
		go func() { first <- h.runs.resume(t.Context(), "wf_1", callerPage) }()
		synctest.Wait()
		waiterCtx, giveUp := context.WithCancel(t.Context())
		waiter := make(chan error, 1)
		go func() { waiter <- h.runs.resume(waiterCtx, "wf_1", callerPage) }()
		synctest.Wait()
		giveUp()
		if err := <-waiter; !errors.Is(err, context.Canceled) {
			t.Fatalf("the waiting Resume = %v, want its own cancellation", err)
		}
		close(gate)

		if err := <-first; err != nil {
			t.Fatalf("the loading Resume = %v, want nil", err)
		}
		synctest.Wait()
		if got := countCalls(host, methodKiroWorkflowResume); got != 1 {
			t.Errorf("%d resumes reached the host session, want only the loading verb's; calls were %v",
				got, host.callLog())
		}
		if got := host.startCount(); got != 1 {
			t.Errorf("%d loads, want 1", got)
		}
	})
}

func TestStepGate_AnEndedWaitHoldsNothing(t *testing.T) {
	t.Run("a free lock and an ended context", func(t *testing.T) {
		gate := newStepSteers(nil, nil)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if release, err := gate.lock(ctx, "wf_1"); !errors.Is(err, context.Canceled) {
			if err == nil {
				release()
			}
			t.Fatalf("lock on a free gate with an ended context = %v, want context.Canceled", err)
		}
	})
	t.Run("a waiter cancelled before its predecessor releases", func(t *testing.T) {
		// The waiter sees cancellation and release together, so a lock letting either win would acquire sometime.
		for range 64 {
			synctest.Test(t, func(t *testing.T) {
				gate := newStepSteers(nil, nil)
				release, err := gate.lock(t.Context(), "wf_1")
				if err != nil {
					t.Fatalf("Setup: the first lock = %v", err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				got := make(chan error, 1)
				go func() {
					r, err := gate.lock(ctx, "wf_1")
					if err == nil {
						r()
					}
					got <- err
				}()
				synctest.Wait()
				cancel()
				release()
				if err := <-got; !errors.Is(err, context.Canceled) {
					t.Fatalf("lock = %v for a waiter whose context ended before the gate freed, want context.Canceled", err)
				}
				next, cancelNext := context.WithTimeout(t.Context(), time.Second)
				defer cancelNext()
				again, err := gate.lock(next, "wf_1")
				if err != nil {
					t.Fatalf("lock after the cancelled waiter left = %v, want the gate free", err)
				}
				again()
			})
		}
	})
}
