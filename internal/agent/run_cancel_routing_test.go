package agent

// A run's cancel, and the deadline's fate when that cancel is refused.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// doorRecorder hands out a distinct fake per door and serves the engine lane from one too, so every call is counted.
type doorRecorder struct {
	results map[string]json.RawMessage
	errs    map[string]error
	opened  []*fakeBridge
	mu      sync.Mutex
}

func (s *doorRecorder) factory() engineSession {
	br := newFakeBridge()
	br.callResults = s.results
	br.callErrs = s.errs
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, br)
	return br
}

func (s *doorRecorder) sawCall(method string) (bridges, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, br := range s.opened {
		hits := 0
		for _, m := range br.callLog() {
			if m == method {
				hits++
			}
		}
		if hits > 0 {
			bridges++
			calls += hits
		}
	}
	return bridges, calls
}

func agentRunList(t *testing.T, status string) json.RawMessage {
	t.Helper()
	return kasRuns(t, map[string]any{
		"workflowId": "wf_1", "name": "publish", "status": status,
		"parentSessionId": "sess_owner",
	})
}

// TestCancelForSessions_ReadsTheRunInventoryOnce pins one `workflow/list` for a tab close, not N+1. The
// bridge is inserted so openBridge's rehydrate hook does not read the inventory.
func TestCancelForSessions_ReadsTheRunInventoryOnce(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t,
			map[string]any{
				"workflowId": "wf_1", "name": "publish", "status": "running",
				"parentSessionId": "sess_owner",
			},
			map[string]any{
				"workflowId": "wf_2", "name": "publish", "status": "paused",
				"parentSessionId": "sess_owner",
			},
		),
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Named for neither run, so the leases stay put.
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	leased(t, h.runs, "wf_1")
	leased(t, h.runs, "wf_2")

	h.runs.cancelForSessions(t.Context(), "c1", []string{"sess_owner"}, userStop(stopWhyTabClosed))

	if got := callsOf(br, methodKiroWorkflowCancel); got != 2 {
		t.Errorf("the cancel went out %d times for 2 live runs, want 2", got)
	}
	if got := callsOf(br, methodKiroWorkflowList); got != 1 {
		t.Errorf("`workflow/list` went out %d times to cancel 2 runs, want 1: the loop "+
			"already holds every row a per-run lookup would re-read", got)
	}
}

// TestCancelForSessions_RoutesWithTheChatRecordALREADYDELETED pins that with retention off the record is gone
// before the teardown, so the cancel's lane comes from the lease and the resident session, once.
func TestCancelForSessions_RoutesWithTheChatRecordALREADYDELETED(t *testing.T) {
	rec := &doorRecorder{results: map[string]json.RawMessage{
		methodKiroWorkflowList:   agentRunList(t, "running"),
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Named for no run here, so the lease stays put.
		methodKiroWorkflowInspect: inspectReply(t, "wf_other", "running", ""),
	}}
	cs := newTestChatStore()
	lane, _ := rec.factory().(*fakeBridge)
	h := newRuntimeOn(t.Context(), "/tmp/work", fakeEngineOf(rec.factory, lane), cs)
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	owner, ok := rec.factory().(*fakeBridge)
	if !ok {
		t.Fatal("Setup: the recorder handed back something other than a fake")
	}
	owner.sessionID = "sess_owner"
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: owner, state: bridgeIdle})
	if _, found := cs.Get(t.Context(), "c1"); found {
		t.Fatal("Setup: the chat record exists, so this is not the delete-grade state")
	}
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", ParentSessionID: "sess_owner", Origin: runlease.OriginAgent})

	h.runs.cancelForSessions(t.Context(), "c1", []string{"sess_owner"}, userStop(stopWhyTabClosed))

	if !slices.Contains(owner.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel did not ride the launching chat's resident session for a chat whose record is already " +
			"deleted, so its lifecycle frames are not positioned on the lane they reach")
	}
	if fakes, calls := rec.sawCall(methodKiroWorkflowCancel); fakes != 1 || calls != 1 {
		t.Errorf("the cancel reached %d lane(s) in %d call(s), want exactly 1 and 1", fakes, calls)
	}
}

// TestFinishTermination_ARefusedCancelKEEPSTheDeadline pins that a refused cancel's run is still bounded.
func TestFinishTermination_ARefusedCancelKEEPSTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner pid 979344 is live")}
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")
	if !h.runs.bounded("wf_1") {
		t.Fatal("the fixture is not bounded, so it cannot show a deadline surviving")
	}

	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}

	if !h.runs.bounded("wf_1") {
		t.Error("a REFUSED cancel parked the run's deadline, so the run keeps executing " +
			"with nothing bounding it and no later arm will ever restore one")
	}
}

// TestFinishTermination_ALandedCancelRELEASESTheDeadline pins that a stopped run must not keep a deadline.
func TestFinishTermination_ALandedCancelRELEASESTheDeadline(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowCancel: json.RawMessage(`{}`),
		// Still running, so the lease stays and `bounded` is readable.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "running", ""),
	}
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.cancel(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if h.runs.bounded("wf_1") {
		t.Error("a LANDED cancel left the run bounded, so the ceiling would fire again " +
			"against a run this process has already stopped")
	}
}

// TestRecordEnd_IsNotStampedOnARunThatDidNotStop pins that the reason is recorded when the stop lands, not when attempted.
func TestRecordEnd_IsNotStampedOnARunThatDidNotStop(t *testing.T) {
	t.Run("a refused ceiling cancel records nothing", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
		leased(t, h.runs, "wf_1")

		h.runs.cancelBounded("wf_1", runEndOverran)

		if got := h.runs.endReason("wf_1"); got != "" {
			t.Errorf("endReason = %q after a REFUSED cancel, want \"\": the History row would "+
				"read as ended while KAS still reports the run running", got)
		}
	})

	t.Run("a landed ceiling cancel records overran", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowCancel:  json.RawMessage(`{}`),
			methodKiroWorkflowInspect: inspectReply(t, "wf_1", "aborted", ""),
		}
		leased(t, h.runs, "wf_1")

		h.runs.cancelBounded("wf_1", runEndOverran)

		if got := h.runs.endReason("wf_1"); got != runEndOverran {
			t.Errorf("endReason = %q after a LANDED ceiling cancel, want %q: the row would "+
				"fall back to plain aborted and lose why the run stopped", got, runEndOverran)
		}
	})
}

// setCancelRetryDelay points the retry backoff at d (production: 5s, 10s, 20s). Never restored: the
// ladder's untracked timers can outlive the test. Call before anything starts a ladder.
func setCancelRetryDelay(rs *Runs, d time.Duration) {
	rs.cancelRetryBase = d
}

// TestRetryTermination_IsBoundedAndDoesNotReFireForever pins that the retry is bounded, and once spent the run is still bounded.
func TestRetryTermination_IsBoundedAndDoesNotReFireForever(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	setCancelRetryDelay(h.runs, time.Millisecond)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	// The first attempt is the caller's.
	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}

	// One plus maxCancelRetries; the poll fails closed.
	want := 1 + maxCancelRetries
	deadline := time.Now().Add(5 * time.Second)
	for callsOf(br, methodKiroWorkflowCancel) < want {
		if time.Now().After(deadline) {
			t.Fatalf("the cancel was attempted %d times, want %d (one caller plus %d "+
				"bounded re-attempts): the ladder is not re-installing itself at all",
				callsOf(br, methodKiroWorkflowCancel), want, maxCancelRetries)
		}
		time.Sleep(time.Millisecond)
	}

	// A settling window: the last retry's timer may still be in flight.
	time.Sleep(100 * time.Millisecond)
	if got := callsOf(br, methodKiroWorkflowCancel); got != want {
		t.Errorf("the cancel was attempted %d times, want %d: an unbounded ladder "+
			"retries for the life of the container", got, want)
	}
	if !h.runs.bounded("wf_1") {
		t.Error("the spent retry budget unbounded the run; the bound belongs on OUR " +
			"attempts to end it, never on the record that we are bounding it")
	}
}

// awaitCalls polls until br has taken method want times, failing closed.
func awaitCalls(t *testing.T, br *fakeBridge, method string, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for callsOf(br, method) < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s was attempted %d times, want %d: %s",
				method, callsOf(br, method), want, why)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestHealProgress_RefillsTheCancelRetryBudget pins that cancelOn and cancelBounded share the retry budget, so
// refused button presses could starve the ceiling; a completed node refills it.
func TestHealProgress_RefillsTheCancelRetryBudget(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	setCancelRetryDelay(h.runs, time.Millisecond)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	// Spend the whole ladder on the user's button.
	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	spent := 1 + maxCancelRetries
	awaitCalls(t, br, methodKiroWorkflowCancel, spent,
		"the ladder is not re-installing itself, so the budget was never spent and "+
			"this test cannot observe a refill")
	// The fake logs the call before the claim is released; wait for the spent budget's refusal, or the
	// next press no-ops on a held claim.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), logMsgCancelUnretried) {
		if time.Now().After(deadline) {
			t.Fatalf("the spent ladder never logged %q, so it is still running", logMsgCancelUnretried)
		}
		time.Sleep(time.Millisecond)
	}

	// The run completes a node.
	h.translateACPEvent("c1", h.originOf("c1"), runNotif(methodWFNodeComplete, map[string]any{
		"workflowId": "wf_1", "nodeId": "n1", "status": "completed",
	}))

	// A later refused cancel gets a fresh ladder.
	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	awaitCalls(t, br, methodKiroWorkflowCancel, spent+1+maxCancelRetries,
		"the budget was not refilled by the completed node, so the ceiling's own "+
			"re-attempts stay spent by the user's earlier presses")
}

func callsOf(br *fakeBridge, method string) int {
	n := 0
	for _, m := range br.callLog() {
		if m == method {
			n++
		}
	}
	return n
}

// TestRetryTermination_ARunNoLongerBoundedIsLeftAlone pins that the retry re-reads before acting, so a parked or
// released run is not cancelled.
func TestRetryTermination_ARunNoLongerBoundedIsLeftAlone(t *testing.T) {
	h, _, br := newTestHub()
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("owner is live")}
	// The park must land inside the base and the settle must outlast it, or the guard is never exercised.
	const base = 200 * time.Millisecond
	setCancelRetryDelay(h.runs, base)
	leased(t, h.runs, "wf_1")
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.cancel(t.Context(), "wf_1"); err == nil {
		t.Fatal("Cancel reported success for a refused cancel")
	}
	before := callsOf(br, methodKiroWorkflowCancel)
	// The run parks before the re-attempt comes due.
	h.runs.disarmDeadline(t.Context(), "wf_1")

	time.Sleep(2 * base)

	if got := callsOf(br, methodKiroWorkflowCancel); got != before {
		t.Errorf("a run marotte had stopped bounding was cancelled anyway (%d → %d calls)",
			before, got)
	}
}

// TestResumeIfInterrupted_ArmsTheDeadline pins that this path calls `_kiro/workflow/resume` directly, so it must
// arm itself rather than rely on the `run_start` frame.
func TestResumeIfInterrupted_ArmsTheDeadline(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		// stalePauseReason is a restart-reconciled run's reason; involuntarilyPaused reads the run's own state.
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", stalePauseReason),
		methodKiroWorkflowResume:  json.RawMessage(`{}`),
	}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	// Leased and parked, so the arm is a fresh budget.
	leased(t, h.runs, "wf_1")
	if h.runs.bounded("wf_1") {
		t.Fatal("the fixture is already bounded, so an arm would be unobservable")
	}

	h.runs.resumeIfInterrupted(t.Context(), "c1", "wf_1")

	if !slices.Contains(br.callLog(), methodKiroWorkflowResume) {
		t.Fatalf("the resume never went out, so the arm is untested: %v", br.callLog())
	}
	if !h.runs.bounded("wf_1") {
		t.Error("the rehydrate resume did not arm the run's deadline: a lost `run_start` " +
			"then leaves it executing unbounded with nothing able to notice")
	}
}

// TestResumeIfInterrupted_DoesNotArmARefusedResume pins that a refused resume re-drove nothing.
func TestResumeIfInterrupted_DoesNotArmARefusedResume(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowInspect: inspectReply(t, "wf_1", "paused", stalePauseReason),
	}
	br.callErrs = map[string]error{methodKiroWorkflowResume: errors.New("registry.require threw")}
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession("sess_owner")
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	leased(t, h.runs, "wf_1")

	h.runs.resumeIfInterrupted(t.Context(), "c1", "wf_1")

	if h.runs.bounded("wf_1") {
		t.Error("a REFUSED resume armed a deadline, so a run that is still parked would " +
			"be cancelled for overrunning a budget it never started spending")
	}
}

// TestCancelUnretriedMessage_IsGreppable pins the one line an operator must act on, a constant so an
// alert can key on it. It claims nothing unverified: any non-nil cancel error reaches it.
func TestCancelUnretriedMessage_IsGreppable(t *testing.T) {
	for _, unverified := range []string{"still executing", "holds its recipe"} {
		if strings.Contains(logMsgCancelUnretried, unverified) {
			t.Errorf("logMsgCancelUnretried = %q, want no claim about the run's own state: "+
				"%q is unverified for a transport fault or an unknown workflow id, and this "+
				"is the line an alert would key on", logMsgCancelUnretried, unverified)
		}
	}
	for _, other := range []string{
		logMsgRunStalled, logMsgRunBackstop, logMsgRunOrphaned, logMsgRunYieldedToSlot,
	} {
		if logMsgCancelUnretried == other {
			t.Errorf("logMsgCancelUnretried duplicates %q, so a rule reading one would "+
				"page on the other", other)
		}
	}
}
