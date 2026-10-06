package agent

// Run host tests: the synthetic-id plumbing, the dispatch split and the teardown rules.

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// bufferedEvent is one decoded SSE envelope with a raw payload.
type bufferedEvent struct {
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id"`
	Payload json.RawMessage `json:"payload"`
}

// bufferedEvents decodes the SSE replay buffer, so a test asserts what a client sees.
func bufferedEvents(h *Runtime) []bufferedEvent {
	var out []bufferedEvent
	for _, e := range h.bus.fanout.Snapshot() {
		var evt bufferedEvent
		if json.Unmarshal(e.Event.Data, &evt) == nil {
			out = append(out, evt)
		}
	}
	return out
}

func marshalPayload(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Setup: decoding the payload: %s", err)
	}
	return out
}

func runNotif(method string, params map[string]any) *marotte.RPCResponse {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return &marotte.RPCResponse{Method: method, Params: raw}
}

// TestRunChatID_Namespace pins the synthetic id shape; a real chat id never reads as a run's.
func TestRunChatID_Namespace(t *testing.T) {
	if got := runChatID("wf_1"); got != "run:wf_1" {
		t.Errorf("runChatID = %q, want run:wf_1", got)
	}
	if !isRunChat("run:wf_1") {
		t.Error("run:wf_1 not recognised as a run chat")
	}
	for _, id := range []marotte.ChatID{"c-abc123", "", "wf_1", "running-jokes"} {
		if isRunChat(id) {
			t.Errorf("%q misread as a run chat", id)
		}
	}
}

// TestRunDispatch_LifecycleGoesWorkspaceGlobal pins that a parentless run's lifecycle events carry an empty chat id, never the synthetic one.
func TestRunDispatch_LifecycleGoesWorkspaceGlobal(t *testing.T) {
	h, _, _ := newTestHub()

	h.dispatch(t.Context(), "run:wf_1",
		runNotif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}))

	events := bufferedEvents(h)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].Type != string(marotte.EventRunStarted) {
		t.Errorf("type = %q, want run_started", events[0].Type)
	}
	if events[0].ChatID != "" {
		t.Errorf("chat_id = %q, want empty (workspace-global)", events[0].ChatID)
	}
}

// TestRunDispatch_StepContentIsProjected pins that a step's first chunk opens the run's turn for its node path,
// workspace-global with the workflow id, and opens no chat turn for the synthetic id.
func TestRunDispatch_StepContentIsProjected(t *testing.T) {
	logs := captureLogs(t)
	h := newBudgetRuntime(t)

	h.dispatch(t.Context(), "run:wf_1", runNotif(marotte.MethodSessionUpdate, map[string]any{
		"sessionId": "sess_step",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "step says"},
			"_meta": map[string]any{"kiro": map[string]any{"workflow": map[string]any{
				"workflowId": "wf_1",
				"nodeId":     "coder",
				"nodePath":   []any{"seq", "coder"},
				"type":       "step",
			}}},
		},
	}))

	events := bufferedEvents(h)
	if len(events) != 2 {
		t.Fatalf("a step chunk produced %d events, want 2 (turn_opened, entry_opened): %+v", len(events), events)
	}
	if events[0].Type != string(marotte.EventTurnOpened) || events[1].Type != string(marotte.EventEntryOpened) {
		t.Errorf("types = [%s %s], want [turn_opened entry_opened]", events[0].Type, events[1].Type)
	}
	for i, e := range events {
		// Workspace-global: the client routes by workflow id.
		if e.ChatID != "" {
			t.Errorf("event %d chat_id = %q, want empty (workspace-global)", i, e.ChatID)
		}
		var scoped struct {
			WorkflowID string `json:"workflow_id"`
		}
		if err := json.Unmarshal(e.Payload, &scoped); err != nil || scoped.WorkflowID != "wf_1" {
			t.Errorf("event %d workflow_id = %q (err %v), want wf_1", i, scoped.WorkflowID, err)
		}
	}
	var opened marotte.EntryOpenedPayload
	if err := json.Unmarshal(events[1].Payload, &opened); err != nil {
		t.Fatalf("decode entry_opened: %v", err)
	}
	if opened.Open.Text != "step says" || opened.Open.N != 1 {
		t.Errorf("entry_opened = %+v, want the chunk's text as delta 1", opened.Open)
	}
	// The node path keys the run turn: a repeat's iterations share a node id.
	if h.runs.log.Turn("wf_1", "seq/coder") == nil {
		t.Error("the step chunk opened no run turn for its node path")
	}
	// No chat turn.
	if h.liveTurn("run:wf_1") != nil {
		t.Error("a step chunk opened a chat turn for the synthetic chat id")
	}
	// The unhandled-notification line stays quiet, so it still flags genuinely unknown frames.
	const unhandled = "run bridge: unhandled notification"
	if out := logs.String(); strings.Contains(out, `"msg":"`+unhandled+`"`) {
		t.Errorf("a step's session/update was reported as %q: %s", unhandled, out)
	}
}

// TestRunDispatch_UnmarkedStepContentIsDropped pins that a `session/update` without `_meta.kiro.workflow` names no node.
func TestRunDispatch_UnmarkedStepContentIsDropped(t *testing.T) {
	h, _, _ := newTestHub()

	h.dispatch(t.Context(), "run:wf_1", runNotif(marotte.MethodSessionUpdate, map[string]any{
		"sessionId": "sess_step",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "unattributable"},
		},
	}))

	if events := bufferedEvents(h); len(events) != 0 {
		t.Fatalf("an unattributed chunk produced %d events, want 0: %+v", len(events), events)
	}
}

// TestRunDispatch_PermissionKeyedToRunChat pins that a step's permission broadcasts under the synthetic chat id, the run tab's dock key and reply route.
func TestRunDispatch_PermissionKeyedToRunChat(t *testing.T) {
	h, _, _ := newTestHub()

	id := int64(7)
	msg := runNotif(marotte.MethodRequestPermission, map[string]any{
		"sessionId": "sess_step",
		"toolCall":  map[string]any{"toolCallId": "tc1", "title": "write file", "kind": "edit"},
		"options":   []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
	})
	msg.ID = &id
	h.dispatch(t.Context(), "run:wf_1", msg)

	found := false
	for _, e := range bufferedEvents(h) {
		if e.Type == string(marotte.EventPermissionNeeded) {
			found = true
			if e.ChatID != "run:wf_1" {
				t.Errorf("permission chat_id = %q, want run:wf_1", e.ChatID)
			}
		}
	}
	if !found {
		t.Fatal("no permission_needed broadcast")
	}
}

// TestRunDispatch_UnknownRequestIsRefused pins that an unanswered A→C request wedges the step.
func TestRunDispatch_UnknownRequestIsRefused(t *testing.T) {
	h, _, br := newTestHub()
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

	id := int64(3)
	msg := runNotif("_kiro/spec/getTaskStatuses", map[string]any{})
	msg.ID = &id
	h.dispatch(t.Context(), "run:wf_1", msg)

	if got := br.respondCount(); got != 1 {
		t.Fatalf("unknown request got %d responses, want 1 refusal", got)
	}
}

// TestLaunchRun_SequencesNewRegisterInvoke pins created, registered, then invoked.
func TestLaunchRun_SequencesNewRegisterInvoke(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}

	id, name, err := h.runs.Launch(t.Context(), "bundled://publish", nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id != "wf_9" || name != "publish" {
		t.Errorf("Launch = (%q, %q), want (wf_9, publish)", id, name)
	}
	if h.bridge.mgr.get("run:wf_9") == nil {
		t.Error("the run bridge is not registered under its synthetic id")
	}
	// invoke after new, on the same bridge.
	calls := br.callLog()
	newIdx, invokeIdx := -1, -1
	for i, m := range calls {
		switch m {
		case methodKiroWorkflowNew:
			newIdx = i
		case methodKiroWorkflowInvoke:
			invokeIdx = i
		}
	}
	if newIdx == -1 || invokeIdx == -1 || invokeIdx < newIdx {
		t.Errorf("call order wrong: %v", calls)
	}
}

// A run step's workspace hooks autofire only when its bridge declares the v2 hook
// engine at initialize, as a chat bridge does.
func TestLaunchRun_RunBridgeDeclaresHooks(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}
	if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	opts := br.lastStartOpts()
	if opts == nil {
		t.Fatal("the run bridge was never started")
	}
	if !opts.EnableHooks {
		t.Error("Launch started the run bridge with EnableHooks=false, so workspace hooks never fire during its steps")
	}
}

// TestLaunchRun_RefusesAnUnknownSource pins that the source is re-checked against a fresh listRecipes, so no arbitrary file can be launched.
func TestLaunchRun_RefusesAnUnknownSource(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
	}
	if _, _, err := h.runs.Launch(t.Context(), "/etc/passwd", nil); err == nil {
		t.Fatal("an unlisted source launched")
	}
	if !strings.Contains(br.lastCall(), "listRecipes") {
		t.Errorf("last call = %q; the refusal happened after something other than validation", br.lastCall())
	}
}

// TestLaunchRun_AnAgentSourceGoesStraightToNew pins that agent:// skips listRecipes.
func TestLaunchRun_AnAgentSourceGoesStraightToNew(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
	_, name, err := h.runs.Launch(t.Context(), "agent://wf-coder", map[string]string{"prompt": "fix the build"})
	if err != nil {
		t.Fatalf("Launch(agent://wf-coder) = %v, want nil", err)
	}
	if name != "agent-wf-coder" {
		t.Errorf("Launch(agent://wf-coder) name = %q, want agent-wf-coder", name)
	}
	if got := br.paramsFor(methodKiroWorkflowNew)["workflowPath"]; got != "agent://wf-coder" {
		t.Errorf("new workflowPath = %v, want agent://wf-coder", got)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowListRecipes) {
		t.Errorf("calls = %v, want no listRecipes for an agent source", br.callLog())
	}
}

func TestLaunchRun_AnAgentSourceIsValidatedBeforeAnyCall(t *testing.T) {
	cases := map[string]struct {
		source string
		inputs map[string]string
	}{
		"no prompt":          {source: "agent://wf-coder"},
		"a blank prompt":     {source: "agent://wf-coder", inputs: map[string]string{"prompt": "  "}},
		"a path in the name": {source: "agent://../x", inputs: map[string]string{"prompt": "go"}},
		"an empty name":      {source: "agent://", inputs: map[string]string{"prompt": "go"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, br := newTestHub()
			if _, _, err := h.runs.Launch(t.Context(), tc.source, tc.inputs); err == nil {
				t.Errorf("Launch(%q, %v) = nil, want an error", tc.source, tc.inputs)
			}
			if calls := br.callLog(); slices.Contains(calls, methodKiroWorkflowNew) {
				t.Errorf("Launch(%q) reached new, calls %v", tc.source, calls)
			}
		})
	}
}

// TestLaunchRun_SingleRunRule pins the 409: one live run per recipe, globally.
func TestLaunchRun_SingleRunRule(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
		// Both name fields as the wire sends them; they agree only while unlabelled.
		methodKiroWorkflowList: json.RawMessage(`{"runs":[{"workflowId":"wf_1","name":"publish","workflowName":"publish","status":"running"}]}`),
	}
	_, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil)
	if err == nil || !strings.Contains(err.Error(), "live run") {
		t.Fatalf("err = %v, want the single-run refusal", err)
	}
	// A terminal run of the same recipe does not block; it carries `workflowName` so the status half is reached.
	br.callResults[methodKiroWorkflowList] = json.RawMessage(
		`{"runs":[{"workflowId":"wf_1","name":"publish","workflowName":"publish","status":"completed"}]}`,
	)
	br.callResults[methodKiroWorkflowNew] = json.RawMessage(`{"workflowId":"wf_2"}`)
	br.callResults[methodKiroWorkflowInvoke] = json.RawMessage(`{}`)
	if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
		t.Fatalf("a terminal run blocked a relaunch: %v", err)
	}
}

// TestBridgeManagerInsert_RefusesReplacement pins that inserting over a live entry would orphan its process.
func TestBridgeManagerInsert_RefusesReplacement(t *testing.T) {
	h, _, br := newTestHub()
	first := &sharedBridge{bridge: br, state: bridgeIdle}
	if !h.bridge.mgr.insert("run:wf_1", first) {
		t.Fatal("first insert refused")
	}
	if h.bridge.mgr.insert("run:wf_1", &sharedBridge{bridge: br, state: bridgeIdle}) {
		t.Error("second insert over a live entry succeeded")
	}
	if got := h.bridge.mgr.get("run:wf_1"); got != first {
		t.Error("the original entry did not survive the refused insert")
	}
}

// TestRetry_SuccessClearsTheOldTerminalReason pins that retry reuses the id, and a stale end_reason outranks live status on the client.
func TestRetry_SuccessClearsTheOldTerminalReason(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowRetry: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// The run as the bounds left it: terminated, reason recorded, claim taken.
	h.runs.claimTermination(id)
	h.runs.recordEnd(id, runEndOverran)

	// The zero affordance is a parentless run's gate result; run_retry_test.go covers the chat-parented one.
	if _, err := h.runs.Retry(t.Context(), id, &runAffordance{}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got := h.runs.endReason(id); got != "" {
		t.Errorf("the retried run still reads %q, so its row renders as aborted", got)
	}
	if !h.runs.bounded(id) {
		t.Error("the retried run holds no deadline, so nothing bounds it")
	}
	// The claim went with the reason.
	if !h.runs.claimTermination(id) {
		t.Error("the retried run kept its termination claim")
	}
}

// TestRetry_FailureKeepsTheOldTerminalReason pins that the clear follows the RPC, since a refused retry re-drove nothing.
func TestRetry_FailureKeepsTheOldTerminalReason(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callErrs = map[string]error{methodKiroWorkflowRetry: errors.New("kas refused")}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	h.runs.claimTermination(id)
	h.runs.recordEnd(id, runEndOverran)

	if _, err := h.runs.Retry(t.Context(), id, &runAffordance{}); err == nil {
		t.Fatal("a refused retry reported success")
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("the refused retry cleared the reason to %q; the run is still aborted", got)
	}
	if h.runs.bounded(id) {
		t.Error("a refused retry bounded a run that is not executing")
	}
}

// TestRetry_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable pins that a re-hosted parentless run's first frame
// can beat the retry reply; blockOn holds the call open so the frame lands inside the window.
func TestRetry_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		// The run list supplies a re-hosted run's recipe.
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","workflowName":"nightly","status":"aborted",` +
				`"parentSessionId":"` + testLaunchSession + `"}]}`,
		),
	}
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowRetry: held}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	// The real gate, which carries the recipe off KAS's run list.
	aff := h.runs.affordance(t.Context(), id, "aborted")
	if aff.origin.recipe != "nightly" {
		t.Fatalf("Setup: the gate resolved recipe %q, want nightly off KAS's run list", aff.origin.recipe)
	}

	done := make(chan error, 1)
	go func() {
		_, rErr := h.runs.Retry(t.Context(), id, aff)
		done <- rErr
	}()

	// Wait for the retry to be in flight, then deliver the frame with an empty chat id as dispatch does.
	stop := time.Now().Add(5 * time.Second)
	for !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		if time.Now().After(stop) {
			t.Fatalf("the retry never reached the bridge: %v", br.callLog())
		}
		time.Sleep(time.Millisecond)
	}
	h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
		"workflowId": id, "workflowName": "nightly",
	}))
	close(held)
	if err := <-done; err != nil {
		t.Fatalf("Retry: %v", err)
	}

	l, ok := h.runs.lease(id)
	if !ok {
		t.Fatal("the retried run holds no lease")
	}
	if l.Origin == runlease.OriginAgent {
		t.Error("a frame arriving mid-retry leased a parentless run as agent-origin, which " +
			"excludes it from the orphan sweep for good")
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual", l.Origin)
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly off the run list; a nameless lease is invisible to "+
			"the single-run rule's comparison", l.Recipe)
	}
	if !l.Bounded() {
		t.Error("the retried run took no deadline")
	}
}

// TestRetry_ReHostedRunTakesItsRecipeFromTheRunList pins that the lease needs the recipe the single-run rule compares.
func TestRetry_ReHostedRunTakesItsRecipeFromTheRunList(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","workflowName":"nightly","status":"aborted",` +
				`"parentSessionId":"` + testLaunchSession + `"}]}`,
		),
	}
	// No bridge in the manager: the re-hosting path.
	if h.bridge.mgr.get(runChatID(id)) != nil {
		t.Fatal("the fixture registered a bridge, so this exercises the wrong branch")
	}
	// The gate's answer carries the name to the lease.
	aff := h.runs.affordance(t.Context(), id, "aborted")
	if aff.origin.recipe != "nightly" {
		t.Fatalf("Setup: the gate resolved recipe %q, want nightly off KAS's run list", aff.origin.recipe)
	}

	if _, err := h.runs.Retry(t.Context(), id, aff); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	l, ok := h.runs.lease(id)
	if !ok {
		t.Fatal("the re-hosted run holds no lease, so nothing bounds it")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly off KAS's run list", l.Recipe)
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual: the user clicked Retry, so this run is the user's "+
			"own and must stay sweepable", l.Origin)
	}
	if !l.Bounded() {
		t.Error("the re-hosted run took no deadline")
	}
}

// TestRetry_CancelsNothingAndKeepsNoLeaseWhenTheRetryIsRefused pins that the lease granted before the verb goes back on refusal.
func TestRetry_CancelsNothingAndKeepsNoLeaseWhenTheRetryIsRefused(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","workflowName":"nightly","status":"aborted",` +
				`"parentSessionId":"` + testLaunchSession + `"}]}`,
		),
	}
	br.callErrs = map[string]error{methodKiroWorkflowRetry: errors.New("kas refused")}

	_, err := h.runs.Retry(t.Context(), id, h.runs.affordance(t.Context(), id, "aborted"))
	if !slices.Contains(br.callLog(), methodKiroWorkflowRetry) {
		t.Fatalf("Setup: the retry never reached KAS (err %v); calls were %v", err, br.callLog())
	}
	if err == nil {
		t.Fatal("a refused retry reported success")
	}
	if _, ok := h.runs.lease(id); ok {
		t.Error("the refused retry kept the lease it minted, so the recipe reads as busy and a " +
			"run that is not executing carries a deadline")
	}
}

// TestCancelRun_LostClaimIssuesNoSecondCancel pins that the loser sends nothing and overwrites nothing, reporting success.
func TestCancelRun_LostClaimIssuesNoSecondCancel(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	// A bound got there first.
	if !h.runs.claimTermination(id) {
		t.Fatal("the fixture could not take the claim it needs to hold")
	}
	h.runs.recordEnd(id, runEndOverran)

	if err := h.runs.Cancel(t.Context(), id); err != nil {
		t.Errorf("Cancel on an already-terminating run = %v, want nil", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Error("a second cancel went out for a run already being cancelled")
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("the row reads %q; the losing cancel overwrote the winner's reason", got)
	}
}

// TestCancelRun_WinsTheClaimAndRecordsNothing pins that a user cancel records no reason.
func TestCancelRun_WinsTheClaimAndRecordsNothing(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
	h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
	h.runs.armDeadline(t.Context(), id)

	if err := h.runs.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Error("the cancel verb never went out")
	}
	if got := h.runs.endReason(id); got != "" {
		t.Errorf("a user cancel recorded %q", got)
	}
	if h.runs.bounded(id) {
		t.Error("the cancelled run kept its wall clock running")
	}
	// The claim is held, so a later bound cannot relabel it.
	if h.runs.claimTermination(id) {
		t.Error("the cancelled run's claim was not held, so a late bound can still record over it")
	}
}

// TestCancelRun_FailedRPCHandsTheClaimBack pins that a held claim after a refusal mutes every later Cancel.
func TestCancelRun_FailedRPCHandsTheClaimBack(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callErrs = map[string]error{methodKiroWorkflowCancel: errors.New("kas refused")}
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	if err := h.runs.Cancel(t.Context(), id); err == nil {
		t.Fatal("a refused cancel reported success")
	}
	if !h.runs.claimTermination(id) {
		t.Error("the run stayed claimed after its cancel failed, so nothing can stop it")
	}
}

// TestRunDispatch_TheOtherAskKindsReachTheRunTab pins that elicitations and questions block the step too, so they
// must route through the synthetic chat id, not the refusal ladder.
func TestRunDispatch_TheOtherAskKindsReachTheRunTab(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params map[string]any
		want   marotte.EventType
	}{
		{
			name:   "a step asking the user to fill in a form",
			method: marotte.MethodElicitationCreate,
			params: map[string]any{
				"sessionId":  "sess_step",
				"toolCallId": "tc1",
				"elicitation": map[string]any{
					"message": "which environment?",
					"mode":    "form",
				},
			},
			want: marotte.EventElicitationNeeded,
		},
		{
			name:   "a step asking the user a question",
			method: marotte.MethodKiroUserInput,
			params: map[string]any{
				"sessionId":  "sess_step",
				"toolCallId": "tc1",
				"question":   "ship it?",
				"options": []map[string]any{
					{"optionId": "yes", "name": "Yes"},
				},
			},
			want: marotte.EventUserInputNeeded,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, br := newTestHub()
			h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})

			id := int64(11)
			msg := runNotif(c.method, c.params)
			msg.ID = &id
			h.dispatch(t.Context(), "run:wf_1", msg)

			var got []string
			found := false
			for _, e := range bufferedEvents(h) {
				got = append(got, e.Type)
				if e.Type != string(c.want) {
					continue
				}
				found = true
				if e.ChatID != "run:wf_1" {
					t.Errorf("%s chat_id = %q, want run:wf_1; the dock has no tab to render it in "+
						"and the answer cannot route back", c.method, e.ChatID)
				}
			}
			if !found {
				t.Errorf("%s on a run bridge emitted %v, want a %s; the step is blocked on an "+
					"answer the user was never shown", c.method, got, c.want)
			}
			if br.respondCount() != 0 {
				t.Errorf("%s was answered by the refusal ladder, which strands the step on an "+
					"\"unsupported\" reply", c.method)
			}
		})
	}
}

// TestRunDispatch_TerminalCompletionClosesTheRunsBridge pins the close on run_complete; it runs on its own
// goroutine (it closes the forward loop's channel), hence the bounded poll.
func TestRunDispatch_TerminalCompletionClosesTheRunsBridge(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})

	h.dispatch(t.Context(), runChatID(id),
		runNotif(methodWFRunComplete, map[string]any{"workflowId": id, "status": "completed"}))

	stop := time.Now().Add(5 * time.Second)
	for h.bridge.mgr.get(runChatID(id)) != nil {
		if time.Now().After(stop) {
			t.Fatal("a run that reported completion kept its bridge, so its kiro-cli subprocess " +
				"outlives the run that needed it")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLaunchRun_ReportsTheReplysOwnError pins that KAS refuses a launch in band, and the reply's error carries the reason.
func TestLaunchRun_ReportsTheReplysOwnError(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
	}
	br.callRPCErrs = map[string]*marotte.RPCError{
		methodKiroWorkflowNew: {Code: -32602, Message: "inputs.branch: Required"},
	}

	_, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil)
	if err == nil {
		t.Fatal("a launch KAS refused reported success")
	}
	if !strings.Contains(err.Error(), "inputs.branch: Required") {
		t.Errorf("Launch error = %q, want it to carry KAS's own reason; the reply's error was "+
			"dropped and the operator is told nothing actionable", err)
	}
}

// TestCancelForSessions_CancelsARunWhoseRecordIsGone pins cancels from captured chains (a retired session
// included); CancelForChat on the deleted chat is the control.
func TestCancelForSessions_CancelsARunWhoseRecordIsGone(t *testing.T) {
	cases := []struct {
		name   string
		chain  []string
		parent string
	}{
		{name: "root chat, run on a retired session", chain: []string{"sess-root-old", "sess-root-live"}, parent: "sess-root-old"},
		{name: "tangent child, single-session chain", chain: []string{"sess-tangent"}, parent: "sess-tangent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, br := newTestHub()
			const id = "wf_1"
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": id, "status": "running", "parentSessionId": tc.parent,
				}),
				methodKiroWorkflowCancel: json.RawMessage(`{}`),
			}
			h.bridge.mgr.insert(runChatID(id), &sharedBridge{bridge: br, state: bridgeIdle})
			h.runs.grantLease(t.Context(), id, "publish", manualLaunch())
			// No chat record exists.

			h.runs.CancelForChat(t.Context(), "c-doomed", userStop(stopWhyTabClosed))
			if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Fatal("the record-reading CancelForChat cancelled a run for a chat with no record; the control is broken")
			}

			h.runs.CancelForSessions(t.Context(), "c-doomed", tc.chain, userStop(stopWhyTabClosed))
			if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("the captured-chain cancel never went out; calls were %v", br.callLog())
			}
		})
	}
}

// TestCancelForChat_ReportsARunListItCouldNotRead pins that the close proceeds regardless, so the line is the only record.
func TestCancelForChat_ReportsARunListItCouldNotRead(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	seed := func(t *testing.T, cs *testChatStore) {
		t.Helper()
		if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
	}
	const wantLine = "close: run list unavailable, skipping run cancel"

	t.Run("an unreadable run list is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, br := newTestHub()
		seed(t, cs)
		br.callErrs = map[string]error{methodKiroWorkflowList: errors.New("kas gone")}

		h.runs.CancelForChat(t.Context(), chatID, userStop(stopWhyTabClosed))

		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a close that could not read the run list said nothing; want a line reading "+
				"%q. Got: %s", wantLine, out)
		}
	})

	t.Run("an ordinary close is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, br := newTestHub()
		seed(t, cs)
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
		}

		h.runs.CancelForChat(t.Context(), chatID, userStop(stopWhyTabClosed))

		if out := logs.String(); strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a close that read the run list fine reported it as unavailable: %s", out)
		}
	})
}

// TestDecodePauseFrame_KeepsWhatDecodedWhenTheDetailDrifts pins that dropping the whole frame on a type mismatch
// blinds the heal for every pause; a syntax error's leftovers are not kept.
func TestDecodePauseFrame_KeepsWhatDecodedWhenTheDetailDrifts(t *testing.T) {
	frame := func(t *testing.T, params string) pauseFrame {
		t.Helper()
		return decodePauseFrame(&marotte.RPCResponse{Params: json.RawMessage(params)})
	}

	// Every shape a `pauseDetail` change can take; workflowId and pauseReason must survive all.
	for name, params := range map[string]string{
		"the detail became a string": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedPauseReason + `","pauseDetail":"transient-error"}`,
		"the detail became a number": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedPauseReason + `","pauseDetail":7}`,
		"the detail became an array": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedPauseReason + `","pauseDetail":["transient-error"]}`,
		"class became an object": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedPauseReason + `","pauseDetail":{"class":{"kind":"transient-error"}}}`,
		// The drifted key also comes first, for a streaming decoder.
		"the drifted detail comes first": `{"pauseDetail":"transient-error","workflowId":"wf_1",` +
			`"pauseReason":"` + interruptedPauseReason + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := frame(t, params)
			if f.WorkflowID != "wf_1" {
				t.Errorf("WorkflowID = %q, want %q; a drifted detail took the run's identity "+
					"with it, so healPaused returns at its addressing gate and logs nothing",
					f.WorkflowID, "wf_1")
			}
			if f.PauseReason != interruptedPauseReason {
				t.Errorf("PauseReason = %q, want %q; the reason arms are what the heal degrades "+
					"TO, so losing the reason is what turns a degradation into blindness",
					f.PauseReason, interruptedPauseReason)
			}
			// The reason arm still decides.
			if !resumablePause(f.PauseReason, f.PauseDetail) {
				t.Error("resumablePause = false for an interrupted step whose detail drifted; " +
					"the reason arm must still answer")
			}
		})
	}

	// `occurredAt` is not declared on this type, so its wire shape cannot reach this decode: a raw
	// json.Unmarshal must return no error.
	t.Run("occurredAt's wire type cannot reach the predicate path", func(t *testing.T) {
		var f pauseFrame
		body := []byte(`{"workflowId":"wf_1","pauseReason":"` + interruptedPauseReason +
			`","pauseDetail":{"class":"transient-error","code":"EAI_AGAIN",` +
			`"occurredAt":1730000000}}`)
		if err := json.Unmarshal(body, &f); err != nil {
			t.Errorf("json.Unmarshal returned %v; a field no predicate reads must not be able "+
				"to produce a decode error at all — declaring it on pauseDetail puts KAS's "+
				"timestamp shape back on the heal's path", err)
		}
		if f.PauseDetail == nil || f.PauseDetail.Class != transientErrorClass {
			t.Fatalf("PauseDetail = %+v, want the class decoded", f.PauseDetail)
		}
	})

	// An unaccepted class is decoded cleanly and declined, not treated as drift.
	t.Run("an unknown class is decoded, not tolerated", func(t *testing.T) {
		f := frame(t, `{"workflowId":"wf_1","pauseReason":"Paused by user request",`+
			`"pauseDetail":{"class":"permanent","code":"ENOTFOUND"}}`)
		if f.PauseDetail == nil || f.PauseDetail.Class != "permanent" {
			t.Fatalf("PauseDetail = %+v, want the class decoded", f.PauseDetail)
		}
		if resumablePause(f.PauseReason, f.PauseDetail) {
			t.Error("resumablePause = true for a permanent fault nobody's arm accepts")
		}
	})

	// Asserted at the helper: a syntax error leaves the frame zero either way, and `rs.inspect` can hand it an empty document.
	for name, body := range map[string]string{
		"a truncated object": `{"workflowId":"wf_1","pauseReason":"x"`,
		"not JSON at all":    `wf_1`,
		"an empty document":  ``,
	} {
		t.Run(name+" is not a partial answer", func(t *testing.T) {
			var f pauseFrame
			if unmarshalKeepingReadable([]byte(body), &f) {
				t.Errorf("unmarshalKeepingReadable(%q) = true; a syntax error is not a type "+
					"drift, and nothing may be read off bytes that are not JSON", body)
			}
			if f != (pauseFrame{}) {
				t.Errorf("frame = %+v, want the zero frame", f)
			}
		})
	}
}

// TestLaunchRun_SendsTheRunBridgesOwnSessionAsParent pins that `parentSessionId` is required on `_kiro/workflow/new`
// since kiro-cli 2.21.4. It must be the run bridge's own session: lifecycle frames route to the parent's
// connection, and any other would silently lose them.
func TestLaunchRun_SendsTheRunBridgesOwnSessionAsParent(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}

	if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	params := br.lastParamsFor(methodKiroWorkflowNew)
	if params == nil {
		t.Fatal("no _kiro/workflow/new call was recorded")
	}
	got, ok := params["parentSessionId"].(string)
	if !ok || got == "" {
		t.Fatalf("parentSessionId = %#v, want the run bridge's own session id; "+
			"0.63.3 throws without it and the launch fails", params["parentSessionId"])
	}
	if want := string(br.SessionID()); got != want {
		t.Errorf("parentSessionId = %q, want %q (the bridge the call travels on)", got, want)
	}
	// workspacePaths stays for pre-0.63.3 engines.
	if _, ok := params[keyWorkspacePaths]; !ok {
		t.Error("workspacePaths was dropped; a pre-0.63.3 engine reads it for the run's roots")
	}
}

// TestLaunchRun_RefusesABridgeWithNoSession pins that a session-less started bridge is refused before the RPC.
func TestLaunchRun_RefusesABridgeWithNoSession(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
	}
	br.mu.Lock()
	br.sessionID = ""
	br.mu.Unlock()

	_, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil)
	if err == nil {
		t.Fatal("Launch succeeded on a bridge with no session; want a refusal")
	}
	if br.called(methodKiroWorkflowNew) {
		t.Error("the RPC went out anyway; the refusal must precede it")
	}
	if !br.isStopped() {
		t.Error("the started bridge was left running after the refusal")
	}
}
