package agent

// Run host tests: the synthetic-id plumbing, the dispatch split and the teardown rules.

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/trigger"
)

type bufferedEvent struct {
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id"`
	Payload json.RawMessage `json:"payload"`
}

// bufferedEvents decodes the SSE replay buffer, so a test asserts what a client sees.
// bufferedEvents is every buffered event but engine_status, which the engine's own start publishes asynchronously.
func bufferedEvents(h *Runtime) []bufferedEvent {
	return bufferedEventsSince(h, 0)
}

// bufferedEventsSince is bufferedEvents after the replay position since, leaving out what the boot published.
func bufferedEventsSince(h *Runtime, since uint64) []bufferedEvent {
	var out []bufferedEvent
	for _, e := range bufferedSince(h, since) {
		var evt bufferedEvent
		if json.Unmarshal(e.Event.Data, &evt) == nil && evt.Type != string(marotte.EventEngineStatus) {
			out = append(out, evt)
		}
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
	before := h.bus.fanout.Position().Head

	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"),
		runNotif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}))

	events := bufferedEventsSince(h, before)
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
	before := h.bus.fanout.Position().Head

	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(marotte.MethodSessionUpdate, map[string]any{
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

	events := bufferedEventsSince(h, before)
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
	if h.runs.log.turn("wf_1", "seq:coder") == nil {
		t.Error("the step chunk opened no run turn for its node path")
	}
	// No chat turn.
	if h.liveTurn("run:wf_1") != nil {
		t.Error("a step chunk opened a chat turn for the synthetic chat id")
	}
	// The unhandled-notification line stays quiet, so it still flags genuinely unknown frames.
	const unhandled = "run host session: unhandled notification"
	if out := logs.String(); strings.Contains(out, `"msg":"`+unhandled+`"`) {
		t.Errorf("a step's session/update was reported as %q: %s", unhandled, out)
	}
}

// TestRunDispatch_UnmarkedStepContentIsDropped pins that a `session/update` without `_meta.kiro.workflow` names no node.
func TestRunDispatch_UnmarkedStepContentIsDropped(t *testing.T) {
	h, _, _ := newTestHub()
	before := h.bus.fanout.Position().Head

	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(marotte.MethodSessionUpdate, map[string]any{
		"sessionId": "sess_step",
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "unattributable"},
		},
	}))

	if events := bufferedEventsSince(h, before); len(events) != 0 {
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
	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), msg)

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
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))

	id := int64(3)
	msg := runNotif("_kiro/spec/getTaskStatuses", map[string]any{})
	msg.ID = &id
	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), msg)

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

	id, name, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id != "wf_9" || name != "publish" {
		t.Errorf("Launch = (%q, %q), want (wf_9, publish)", id, name)
	}
	if h.bridge.mgr.get("run:wf_9") == nil {
		t.Error("the run host session is not registered under its synthetic id")
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

// TestLaunchRun_RefusesAnUnknownSource pins that the source is re-checked against listRecipes, so no arbitrary file can be launched.
func TestLaunchRun_RefusesAnUnknownSource(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish"}]}`),
	}
	if _, _, err := h.runs.launch(t.Context(), "/etc/passwd", nil, marotte.RunDefaults{}); err == nil {
		t.Fatal("an unlisted source launched")
	}
	if !strings.Contains(br.lastCall(), "listRecipes") {
		t.Errorf("last call = %q; the refusal happened after something other than validation", br.lastCall())
	}
}

// TestListRecipes_CarriesKASsValidationError pins that a recipe KAS cannot launch reaches the
// client with KAS's reason, and a launchable one with none.
func TestListRecipes_CarriesKASsValidationError(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[
			{"name":"fix-and-review","source":"/w/fix-and-review.workflow.json","validationError":"Invalid workflow: include 'review' (./review-seats.workflow.json): recipe was not resolved"},
			{"name":"publish","source":"bundled://publish"}]}`),
	}
	got, err := h.runs.recipeList(t.Context())
	if err != nil {
		t.Fatalf("listRecipes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listRecipes = %d recipes, want 2", len(got))
	}
	if want := "Invalid workflow: include 'review' (./review-seats.workflow.json): recipe was not resolved"; got[0].ValidationError != want {
		t.Errorf("invalid recipe ValidationError = %q, want %q", got[0].ValidationError, want)
	}
	if got[1].ValidationError != "" {
		t.Errorf("launchable recipe ValidationError = %q, want empty", got[1].ValidationError)
	}
}

// workflowStandingCapture is testdata/workflow_standing_capture.json: kiro-cli 2.31.1's getState and listRecipes over
// a workspace holding wsonly, a home layer holding homeonly, and shared in both.
type workflowStandingCapture struct {
	GetState    json.RawMessage `json:"getState"`
	ListRecipes json.RawMessage `json:"listRecipes"`
}

func (c workflowStandingCapture) withWorkflowStanding(t *testing.T, name string, standing map[string]any) json.RawMessage {
	t.Helper()
	var state map[string]any
	if err := json.Unmarshal(c.GetState, &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range state["resources"].([]any) {
		res := r.(map[string]any)
		if res["type"] != "workflow" {
			continue
		}
		for _, i := range res["items"].([]any) {
			it := i.(map[string]any)
			if it["value"].(map[string]any)["name"] == name {
				it["standing"], found = standing, true
			}
		}
	}
	if !found {
		t.Fatalf("the capture holds no workflow item %q", name)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func badgedRows(t *testing.T, getState json.RawMessage, keys map[string]string) []string {
	t.Helper()
	standing, ok := translate.DecodeConfigurationStanding(getState)
	if !ok {
		t.Fatal("getState does not decode")
	}
	var badged []string
	for _, s := range standing {
		if s.Type != "workflow" {
			continue
		}
		for name, key := range keys {
			if key == s.Key {
				badged = append(badged, name)
			}
		}
	}
	return badged
}

func TestListRecipes_KeysEachRecipeAsTheCapturedStandingKeysItsFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "workflow_standing_capture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capture workflowStandingCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowListRecipes: capture.ListRecipes}
	recipes, err := h.runs.recipeList(t.Context())
	if err != nil {
		t.Fatalf("listRecipes = %v", err)
	}
	keys := map[string]string{}
	for _, r := range recipes {
		if r.BuiltIn {
			if r.DocKey != "" {
				t.Errorf("bundled recipe %q DocKey = %q, want empty", r.Name, r.DocKey)
			}
			continue
		}
		keys[r.Name] = r.DocKey
	}
	if keys["shared"] != "workspace/project/.kiro/workflows/shared.workflow.json" {
		t.Errorf("shared DocKey = %q, want the workspace file listRecipes resolves it to", keys["shared"])
	}

	if got := badgedRows(t, capture.GetState, keys); len(got) != 0 {
		t.Errorf("captured standing badges %v, want none: kiro-cli reports every workflow it lists active", got)
	}

	// The home shared is the file listRecipes shadows; its standing names that file, never the workspace winner.
	shadowed := capture.withWorkflowStanding(t, "shared", map[string]any{"kind": "displaced", "compose": "override", "decidedBy": "workspace"})
	if got := badgedRows(t, shadowed, keys); len(got) != 0 {
		t.Errorf("rows joined by the home shared item's standing = %v, want none", got)
	}

	off := capture.withWorkflowStanding(t, "homeonly", map[string]any{"kind": "disabled", "decidedBy": "workspace"})
	if got := badgedRows(t, off, keys); !slices.Equal(got, []string{"homeonly"}) {
		t.Errorf("rows joined by the home-only item's standing = %v, want [homeonly]", got)
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
	_, name, err := h.runs.launch(t.Context(), "agent://wf-coder", map[string]string{"prompt": "fix the build"}, marotte.RunDefaults{})
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
			if _, _, err := h.runs.launch(t.Context(), tc.source, tc.inputs, marotte.RunDefaults{}); err == nil {
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
	_, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if err == nil || !strings.Contains(err.Error(), "live run") {
		t.Fatalf("err = %v, want the single-run refusal", err)
	}
	// A terminal run of the same recipe does not block; it carries `workflowName` so the status half is reached.
	br.callResults[methodKiroWorkflowList] = json.RawMessage(
		`{"runs":[{"workflowId":"wf_1","name":"publish","workflowName":"publish","status":"completed"}]}`,
	)
	br.callResults[methodKiroWorkflowNew] = json.RawMessage(`{"workflowId":"wf_2"}`)
	br.callResults[methodKiroWorkflowInvoke] = json.RawMessage(`{}`)
	if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("a terminal run blocked a relaunch: %v", err)
	}
}

// TestBridgeManagerInsert_RefusesReplacement pins that inserting over a live entry would orphan its session.
func TestBridgeManagerInsert_RefusesReplacement(t *testing.T) {
	h, _, br := newTestHub()
	first := testBridge(br, bridgeIdle)
	if !h.insertBridgeNow("run:wf_1", first) {
		t.Fatal("first insert refused")
	}
	if h.insertBridgeNow("run:wf_1", testBridge(br, bridgeIdle)) {
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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))

	// The run as the bounds left it: terminated, reason recorded, claim taken.
	h.runs.claimTermination(id)
	h.runs.recordEnd(id, runEndOverran)

	// The zero affordance is a parentless run's gate result; run_retry_test.go covers the chat-parented one.
	if _, err := h.runs.retry(t.Context(), id, &runAffordance{}); err != nil {
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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))

	h.runs.claimTermination(id)
	h.runs.recordEnd(id, runEndOverran)

	if _, err := h.runs.retry(t.Context(), id, &runAffordance{}); err == nil {
		t.Fatal("a refused retry reported success")
	}
	if got := h.runs.endReason(id); got != runEndOverran {
		t.Errorf("the refused retry cleared the reason to %q; the run is still aborted", got)
	}
	if h.runs.bounded(id) {
		t.Error("a refused retry bounded a run that is not executing")
	}
}

// TestRetry_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable pins that a reloaded parentless run's first frame
// can beat the retry reply; blockOn holds the call open so the frame lands inside the window.
func TestRetry_AFrameArrivingDuringTheRetryCannotMakeTheRunUnsweepable(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		// The run list supplies a reloaded run's recipe.
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","workflowName":"nightly","status":"aborted",` +
				`"parentSessionId":"` + testLaunchSession + `"}]}`,
		),
	}
	held := make(chan struct{})
	br.blockOn = map[string]chan struct{}{methodKiroWorkflowRetry: held}
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))
	if o := h.runs.originOf(t.Context(), id); o.recipe != "nightly" {
		t.Fatalf("Setup: the run list resolves recipe %q, want nightly", o.recipe)
	}
	aff := h.runs.affordance(id, &runHead{Status: "aborted"})

	done := make(chan error, 1)
	go func() {
		_, rErr := h.runs.retry(t.Context(), id, aff)
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

// TestRetry_ALoadedHostRunTakesItsRecipeFromTheRunList pins that the lease needs the recipe the single-run rule compares.
func TestRetry_ALoadedHostRunTakesItsRecipeFromTheRunList(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList: json.RawMessage(
			`{"runs":[{"workflowId":"wf_1","name":"nightly","workflowName":"nightly","status":"aborted",` +
				`"parentSessionId":"` + testLaunchSession + `"}]}`,
		),
	}
	// No host session in the manager: the reload path.
	if h.bridge.mgr.get(runChatID(id)) != nil {
		t.Fatal("the fixture registered a bridge, so this exercises the wrong branch")
	}
	if o := h.runs.originOf(t.Context(), id); o.recipe != "nightly" {
		t.Fatalf("Setup: the run list resolves recipe %q, want nightly", o.recipe)
	}
	aff := h.runs.affordance(id, &runHead{Status: "aborted"})

	if _, err := h.runs.retry(t.Context(), id, aff); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	l, ok := h.runs.lease(id)
	if !ok {
		t.Fatal("the reloaded run holds no lease, so nothing bounds it")
	}
	if l.Recipe != "nightly" {
		t.Errorf("recipe = %q, want nightly off KAS's run list", l.Recipe)
	}
	if l.Origin != runlease.OriginManual {
		t.Errorf("origin = %q, want manual: the user clicked Retry, so this run is the user's "+
			"own and must stay sweepable", l.Origin)
	}
	if !l.Bounded() {
		t.Error("the reloaded run took no deadline")
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

	_, err := h.runs.retry(t.Context(), id, h.runs.affordance(id, &runHead{Status: "aborted"}))
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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))

	// A bound got there first.
	if !h.runs.claimTermination(id) {
		t.Fatal("the fixture could not take the claim it needs to hold")
	}
	h.runs.recordEnd(id, runEndOverran)

	if err := h.runs.cancel(t.Context(), id, callerPage); err != nil {
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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), id, "publish", manualLaunch(), hostSec())
	h.runs.armDeadline(t.Context(), id)

	if err := h.runs.cancel(t.Context(), id, callerPage); err != nil {
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
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))

	if err := h.runs.cancel(t.Context(), id, callerPage); err == nil {
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
			h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))

			id := int64(11)
			msg := runNotif(c.method, c.params)
			msg.ID = &id
			h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), msg)

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
				t.Errorf("%s on a run host session emitted %v, want a %s; the step is blocked on an "+
					"answer the user was never shown", c.method, got, c.want)
			}
			if br.respondCount() != 0 {
				t.Errorf("%s was answered by the refusal ladder, which strands the step on an "+
					"\"unsupported\" reply", c.method)
			}
		})
	}
}

// TestRunDispatch_TerminalCompletionClosesTheRunsHostSession pins the close on run_complete; it runs on its own
// goroutine (it closes the forward loop's channel), hence the bounded poll.
func TestRunDispatch_TerminalCompletionClosesTheRunsHostSession(t *testing.T) {
	h, _, br := newTestHub()
	const id = "wf_1"
	h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))

	h.dispatch(t.Context(), runChatID(id), br,
		runNotif(methodWFRunComplete, map[string]any{"workflowId": id, "status": "completed"}))

	stop := time.Now().Add(5 * time.Second)
	for h.bridge.mgr.get(runChatID(id)) != nil {
		if time.Now().After(stop) {
			t.Fatal("a run that reported completion kept its host session, so the session " +
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

	_, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if err == nil {
		t.Fatal("a launch KAS refused reported success")
	}
	if !strings.Contains(err.Error(), "inputs.branch: Required") {
		t.Errorf("Launch error = %q, want it to carry KAS's own reason; the reply's error was "+
			"dropped and the operator is told nothing actionable", err)
	}
}

// TestStopChatRuns_CancelsARunOnAnySessionOfTheChain: a run launched from a retired session of the chain, or from a
// tangent's only session, is the chat's too.
func TestStopChatRuns_CancelsARunOnAnySessionOfTheChain(t *testing.T) {
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
			setStopTiming(t, time.Hour, time.Hour)
			h, cs, br := newTestHub()
			const id = "wf_1"
			br.callResults = map[string]json.RawMessage{
				methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": id, "status": "running", "parentSessionId": tc.parent,
				}),
				methodKiroWorkflowCancel: json.RawMessage(`{}`),
			}
			h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))
			cs.seed(t, "c1", func(c *marotte.Chat) {
				for _, s := range tc.chain {
					c.RecordSession(s)
				}
			})

			h.runs.stopChatRuns(t.Context(), "c1", &chat.Claim{})

			if !slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
				t.Errorf("the run launched from %s was not cancelled; calls were %v", tc.parent, br.callLog())
			}
		})
	}
}

// TestStopChatRuns_ReportsARunListItCouldNotRead pins that the close proceeds regardless, so the line is the only
// record.
func TestStopChatRuns_ReportsARunListItCouldNotRead(t *testing.T) {
	const chatID marotte.ChatID = "c1"
	const wantLine = "close: the run list could not be read; stopping only the runs marotte leases"

	t.Run("an unreadable run list is reported", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, br := newTestHub()
		cs.seed(t, chatID, func(c *marotte.Chat) { c.RecordSession("sess_owned") })
		br.callErrs = map[string]error{methodKiroWorkflowList: errors.New("kas gone")}

		h.runs.stopChatRuns(t.Context(), chatID, &chat.Claim{})

		if out := logs.String(); !strings.Contains(out, `"msg":"`+wantLine+`"`) {
			t.Errorf("a close that could not read the run list said nothing; want a line reading %q. Got: %s", wantLine, out)
		}
	})

	t.Run("an ordinary close is quiet about it", func(t *testing.T) {
		logs := captureLogs(t)
		h, cs, br := newTestHub()
		cs.seed(t, chatID, func(c *marotte.Chat) { c.RecordSession("sess_owned") })
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`)}

		h.runs.stopChatRuns(t.Context(), chatID, &chat.Claim{})

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
			interruptedReason + `","pauseDetail":"transient-error"}`,
		"the detail became a number": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedReason + `","pauseDetail":7}`,
		"the detail became an array": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedReason + `","pauseDetail":["transient-error"]}`,
		"class became an object": `{"workflowId":"wf_1","pauseReason":"` +
			interruptedReason + `","pauseDetail":{"class":{"kind":"transient-error"}}}`,
		// The drifted key also comes first, for a streaming decoder.
		"the drifted detail comes first": `{"pauseDetail":"transient-error","workflowId":"wf_1",` +
			`"pauseReason":"` + interruptedReason + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := frame(t, params)
			if f.WorkflowID != "wf_1" {
				t.Errorf("WorkflowID = %q, want %q; a drifted detail took the run's identity "+
					"with it, so healPaused returns at its addressing gate and logs nothing",
					f.WorkflowID, "wf_1")
			}
			if f.PauseReason != interruptedReason {
				t.Errorf("PauseReason = %q, want %q; the reason arms are what the heal degrades "+
					"TO, so losing the reason is what turns a degradation into blindness",
					f.PauseReason, interruptedReason)
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
		body := []byte(`{"workflowId":"wf_1","pauseReason":"` + interruptedReason +
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

// TestLaunchRun_SendsTheHostSessionAsParent pins that `parentSessionId` is required on `_kiro/workflow/new` since
// kiro-cli 2.21.4. It must be the host session: lifecycle frames name the parent and arrive on its lane.
func TestLaunchRun_SendsTheHostSessionAsParent(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}

	if _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	params := br.lastParamsFor(methodKiroWorkflowNew)
	if params == nil {
		t.Fatal("no _kiro/workflow/new call was recorded")
	}
	got, ok := params["parentSessionId"].(string)
	if !ok || got == "" {
		t.Fatalf("parentSessionId = %#v, want the host session id; "+
			"0.63.3 throws without it and the launch fails", params["parentSessionId"])
	}
	if want := string(br.SessionID()); got != want {
		t.Errorf("parentSessionId = %q, want %q (the session the call names)", got, want)
	}
	// workspacePaths stays for pre-0.63.3 engines.
	if _, ok := params[keyWorkspacePaths]; !ok {
		t.Error("workspacePaths was dropped; a pre-0.63.3 engine reads it for the run's roots")
	}
}

// TestLaunchRun_RefusesAHostSessionDoorWithNoSessionID pins that a host door answering no id is refused before the
// RPC, and nothing is registered for a run that does not exist.
func TestLaunchRun_RefusesAHostSessionDoorWithNoSessionID(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
	}
	br.mu.Lock()
	br.sessionID = ""
	br.mu.Unlock()

	_, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	if !errors.Is(err, errRunHostStart) {
		t.Fatalf("Launch on a door with no session id = %v, want errRunHostStart", err)
	}
	if br.called(methodKiroWorkflowNew) {
		t.Error("the RPC went out anyway; the refusal must precede it")
	}
	if len(h.bridge.mgr.all()) != 0 {
		t.Errorf("sessions registered after the refusal = %v, want none", h.bridge.mgr.all())
	}
}

// TestHandleLaunch_AStalledEngineAnswersStillApplyingAndTheLaunchLands pins that a launch the engine has not answered
// within the browser's budget is reported as still applying, and that the browser walking away does not abandon a
// run KAS may already have created.
func TestHandleLaunch_AStalledEngineAnswersStillApplyingAndTheLaunchLands(t *testing.T) {
	answerBudgetOf(t, 50*time.Millisecond)
	h, _, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
		methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
	}
	release := holdCall(t, br, methodKiroWorkflowNew)
	reqCtx, walkAway := context.WithCancel(context.Background())
	defer walkAway()
	req := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/api/runs", strings.NewReader(`{"source":"bundled://publish"}`))

	rec := answeredWithin(t, func(w http.ResponseWriter) { h.runRoutes.handleLaunch(w, req) })

	assertStillApplying(t, rec)
	walkAway()
	release()
	waitUntil(t, "the late launch's host registration", func() bool { return h.bridge.mgr.get("run:wf_9") != nil })
}

// declaringRecipe lists bundled://publish declaring lane and cap, with an idle run list and a launch that succeeds.
func declaringRecipe(br *fakeBridge) {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowListRecipes: json.RawMessage(
			`{"recipes":[{"name":"publish","source":"bundled://publish","inputs":{"lane":"string","cap":"string"}}]}`,
		),
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
}

// sentInputs is the inputs object workflow/new carried.
func sentInputs(t *testing.T, br *fakeBridge) map[string]any {
	t.Helper()
	params := br.paramsFor(methodKiroWorkflowNew)
	if params == nil {
		t.Fatal("no workflow/new was sent")
	}
	in, ok := params["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("workflow/new inputs = %#v, want an object", params["inputs"])
	}
	return in
}

func TestScheduledLaunchDropsUndeclaredInputs(t *testing.T) {
	h, _, br := newTestHub()
	declaringRecipe(br)
	rec := capture.Default(t)
	inputs := map[string]string{"lane": "fast", "stray": "x", "other": "y"}
	if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"}, inputs,
		marotte.RunDefaults{}, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("LaunchScheduled: %v", err)
	}
	if got, want := sentInputs(t, br), map[string]any{"lane": "fast"}; !maps.Equal(got, want) {
		t.Errorf("workflow/new inputs = %v, want %v", got, want)
	}
	if got, ok := rec.AttrValueExact("workflow run launched", "dropped_inputs"); !ok || got != "other,stray" {
		t.Errorf("launch line dropped_inputs = %q (logged %v), want other,stray", got, ok)
	}
	if len(inputs) != 3 {
		t.Errorf("the caller's inputs = %v, want them untouched", inputs)
	}
}

func TestScheduledLaunchSendsDeclaredInputs(t *testing.T) {
	h, _, br := newTestHub()
	declaringRecipe(br)
	rec := capture.Default(t)
	if _, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"},
		map[string]string{"lane": "fast", "cap": "3"}, marotte.RunDefaults{}, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("LaunchScheduled: %v", err)
	}
	if got, want := sentInputs(t, br), map[string]any{"lane": "fast", "cap": "3"}; !maps.Equal(got, want) {
		t.Errorf("workflow/new inputs = %v, want %v", got, want)
	}
	if got, ok := rec.AttrValueExact("workflow run launched", "dropped_inputs"); ok {
		t.Errorf("launch line dropped_inputs = %q, want the attribute absent when nothing was dropped", got)
	}
}

// An agent:// source has no listed recipe, so no declared inputs: its prompt must still reach workflow/new.
func TestAgentLaunchKeepsPrompt(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList:   json.RawMessage(`{"runs":[]}`),
		methodKiroWorkflowNew:    json.RawMessage(`{"workflowId":"wf_9"}`),
		methodKiroWorkflowInvoke: json.RawMessage(`{}`),
	}
	if _, _, err := h.runs.launch(t.Context(), "agent://wf-coder", map[string]string{"prompt": "fix the build"},
		marotte.RunDefaults{}); err != nil {
		t.Fatalf("launch(agent://wf-coder): %v", err)
	}
	if got, want := sentInputs(t, br), map[string]any{"prompt": "fix the build"}; !maps.Equal(got, want) {
		t.Errorf("workflow/new inputs = %v, want %v", got, want)
	}
}

func TestManualLaunchInputsUnfiltered(t *testing.T) {
	_, br, mux := runDefaultsHub(t)
	declaringRecipe(br)
	rec := postRoute(t, mux, "/api/runs", `{"source":"bundled://publish","inputs":{"lane":"fast","stray":"x"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/runs = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got, want := sentInputs(t, br), map[string]any{"lane": "fast", "stray": "x"}; !maps.Equal(got, want) {
		t.Errorf("workflow/new inputs = %v, want %v", got, want)
	}
}

// hostGone waits for the run's host session to be closed.
func hostGone(t *testing.T, h *Runtime, workflowID string) bool {
	t.Helper()
	stop := time.Now().Add(5 * time.Second)
	for h.bridge.mgr.get(runChatID(workflowID)) != nil {
		if time.Now().After(stop) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

func routed(h *Runtime, workflowID string) bool {
	h.runs.router.mu.Lock()
	defer h.runs.router.mu.Unlock()
	_, ok := h.runs.router.owner[workflowID]
	return ok
}

// Two launches of one recipe released together pass one single-run check at a time: the second reads the run the
// first created, listed active before its invoke, and is refused as busy.
func TestConcurrentLaunchesOneRecipeOneRun(t *testing.T) {
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_1")
	var news atomic.Int32
	var created atomic.Bool
	// Holds each run-list read until both launches are reading, or 500 ms pass: launches with no lock between the
	// check and workflow/new then both read the list before either creates a run.
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case methodKiroWorkflowList:
			seen := created.Load()
			arrived.Done()
			select {
			case <-both:
			case <-time.After(500 * time.Millisecond):
			}
			if seen {
				return json.RawMessage(`{"runs":[{"workflowId":"wf_1","name":"publish","workflowName":"publish",` +
					`"status":"running"}]}`), nil, true
			}
		case methodKiroWorkflowNew:
			news.Add(1)
			created.Store(true)
		}
		return nil, nil, false
	}

	errs := make(chan error, 2)
	go func() {
		_, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
		errs <- err
	}()
	go func() {
		_, _, err := h.runs.LaunchScheduled(t.Context(), "bundled://publish", launchrule.Row{ID: "sched-1"}, nil,
			marotte.RunDefaults{}, time.Now().Add(time.Hour))
		errs <- err
	}()
	var busy, ok int
	for range 2 {
		err := <-errs
		var be *schedule.BusyError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &be) && be.WorkflowID == "wf_1":
			busy++
		default:
			t.Errorf("a launch failed with %v, want one launched and one BusyError naming wf_1", err)
		}
	}
	if n := news.Load(); n != 1 {
		t.Errorf("workflow/new sent %d times, want 1: one recipe got two live runs", n)
	}
	if ok != 1 || busy != 1 {
		t.Errorf("%d launched and %d refused as busy, want 1 and 1", ok, busy)
	}
}

// A triggered invoke that fails returns the run's id with the error and keeps its lease and host session: a lost
// reply can follow a run_start that already confirmed the event.
func TestTriggeredInvokeErrorReturnsWorkflowID(t *testing.T) {
	h, br, st, entry := scheduledHub(t)
	h.runs.confirmer = &fakeConfirmer{}
	launchableRecipe(br, "wf_1")
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callErrs = map[string]error{methodKiroWorkflowInvoke: errHoldFault}
	o := triggeredLaunch(entry.ID, "github.com/o/r/run/8/failure", "github.com/o/r/run/8", "run 8")

	id, _, err := h.runs.launchAs(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, o)
	if id != "wf_1" || !errors.Is(err, errHoldFault) {
		t.Fatalf("launchAs = %q, %v; want wf_1 with the invoke error", id, err)
	}
	if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered {
		t.Errorf("lease = %+v held %v, want the triggered lease kept", l, held)
	}
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Error("the run's host session was closed, so a run KAS may have started has no lane")
	}
	if row := st.Clocks()[0]; row.LastRunID != "" || row.LastStatus != "" {
		t.Errorf("row = %+v, want nothing written for a triggered launch", row)
	}

	h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish",
	}))
	if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered || l.RuleID != entry.ID {
		t.Errorf("after run_start lease = %+v held %v, want the triggered lease, not a fresh manual one", l, held)
	}
}

// releaseOrderFixture is the probe's recording of a cancel landing before the run's host session closed.
type releaseOrderFixture struct {
	CancelResult      json.RawMessage `json:"cancel_result"`
	FramesBeforeClose []struct {
		Params map[string]any `json:"params"`
		Method string         `json:"method"`
	} `json:"frames_before_close"`
}

func readKASProbe(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "kas-probe", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// ReleaseTriggered releases the lease and closes the run's host session. Replayed in the recorded order (the cancel's
// run_complete on the host lane, then the cancel's answer, then the release), the run ends aborted with nothing left.
func TestReleaseTriggeredReleasesLeaseAndHost(t *testing.T) {
	t.Run("releases the lease, the termination claim and the host session", func(t *testing.T) {
		h, _, br := newTestHub()
		h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))
		h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
		if !h.runs.claimTermination("wf_1") {
			t.Fatal("the fixture could not take the claim the landed cancel held")
		}

		h.runs.ReleaseTriggered(t.Context(), "wf_1")

		if _, held := h.runs.lease("wf_1"); held {
			t.Error("the lease survived ReleaseTriggered")
		}
		if !h.runs.claimTermination("wf_1") {
			t.Error("the released run's termination claim outlived it")
		}
		if !hostGone(t, h, "wf_1") {
			t.Error("the run's host session survived ReleaseTriggered")
		}
	})
	t.Run("in the recorded order", func(t *testing.T) {
		var fx releaseOrderFixture
		readKASProbe(t, "release-order.json", &fx)
		var complete map[string]any
		for _, f := range fx.FramesBeforeClose {
			if f.Method == methodWFRunComplete {
				complete = f.Params
			}
		}
		if complete == nil {
			t.Fatal("the fixture holds no run_complete before the close")
		}
		id, _ := complete["workflowId"].(string)
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: fx.CancelResult}
		h.insertBridgeNow(runChatID(id), testBridge(br, bridgeIdle))
		h.runs.grantLease(t.Context(), id, "trigger-probe", eventLaunch(), hostSec())
		h.runs.armDeadline(t.Context(), id)
		h.dispatch(t.Context(), runChatID(id), br, runNotif(methodWFRunComplete, complete))

		if err := h.runs.CancelAbandoned(t.Context(), id); err != nil && !errors.Is(err, errTerminationClaimed) {
			t.Fatalf("CancelAbandoned: %v", err)
		}
		h.runs.ReleaseTriggered(t.Context(), id)

		if _, held := h.runs.lease(id); held {
			t.Error("the lease survived the release")
		}
		if !hostGone(t, h, id) {
			t.Error("the host session survived the release")
		}
		if h.runs.bounded(id) {
			t.Error("the released run is still bounded")
		}
	})
}

// A triggered lease that is not persisted, or not granted at all, stops the launch before the run is routed or
// invoked: after a restart nothing would name it. The cancel is the attempt table's, not the launch's.
func TestTriggeredLeaseGrantFailureWritesNoInvoke(t *testing.T) {
	for name, tc := range map[string]struct {
		workflowID string
		durable    bool
	}{
		"a lease that does not persist":          {workflowID: "wf_1"},
		"a workflow id that is not a plain name": {workflowID: "wf 1", durable: true},
	} {
		t.Run(name, func(t *testing.T) {
			var opts []Option
			if !tc.durable {
				opts = append(opts, WithRunLeases(undurableLeaseStore(t)))
			}
			h, _, br := newTestHub(opts...)
			h.runs.confirmer = &fakeConfirmer{}
			launchableRecipe(br, tc.workflowID)

			id, _, err := h.runs.launchAs(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, eventLaunch())
			if id != tc.workflowID || !errors.Is(err, trigger.ErrLeaseNotGranted) {
				t.Fatalf("launchAs = %q, %v; want %q with trigger.ErrLeaseNotGranted", id, err, tc.workflowID)
			}
			wire := br.wireLog()
			if slices.Contains(wire, methodKiroWorkflowInvoke) {
				t.Error("the run was invoked though its lease could not be written")
			}
			if slices.Contains(wire, methodKiroWorkflowCancel) {
				t.Error("the launch sent a cancel; the attempt table owns it")
			}
			if routed(h, tc.workflowID) {
				t.Error("the run was routed to its host though it will never be invoked")
			}
			if !hostGone(t, h, tc.workflowID) {
				t.Error("the host session of a run that will never be invoked stayed open")
			}
		})
	}
}

// A scheduled launch whose invoke KAS refuses keeps dev's arm: the row records the failure, the lease is released and
// the host session closed.
func TestScheduledInvokeErrorKeepsDevArm(t *testing.T) {
	h, br, st, entry := nightlyLaunch(t)
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowInvoke: {Code: -32603, Message: "invoke refused"}}

	id, _, err := h.runs.LaunchScheduled(t.Context(), entry.Source, entry.Row(), nil, marotte.RunDefaults{},
		time.Now().Add(time.Hour))
	if id != "wf_1" || err == nil {
		t.Fatalf("LaunchScheduled = %q, %v; want wf_1 with the refusal", id, err)
	}
	if row := st.Clocks()[0]; row.LastRunID != "wf_1" || row.LastStatus != launchrule.StatusFailed {
		t.Errorf("row = %+v, want wf_1 failed", row)
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the lease of a run KAS refused to start survived")
	}
	if !hostGone(t, h, "wf_1") {
		t.Error("the host session of a run KAS refused to start stayed open")
	}
}

// cancelUninvokedFixture is the probe's engine-lane cancel of a created, never-invoked run.
type cancelUninvokedFixture struct {
	Uninvoked struct {
		CancelResult json.RawMessage `json:"cancel_result"`
		ListedBefore json.RawMessage `json:"listed_before"`
		ListedAfter  json.RawMessage `json:"listed_after"`
	} `json:"uninvoked"`
}

// An abandoned run that was created and never invoked is cancelled on the engine lane with no initiator, leaves the
// list's active set, and the recipe's next launch is not refused as busy.
func TestCancelAbandonedEndsUninvokedRun(t *testing.T) {
	var fx cancelUninvokedFixture
	readKASProbe(t, "cancel-uninvoked.json", &fx)
	var before struct {
		WorkflowID   string `json:"workflowId"`
		WorkflowName string `json:"workflowName"`
	}
	if err := json.Unmarshal(fx.Uninvoked.ListedBefore, &before); err != nil {
		t.Fatalf("decode listed_before: %v", err)
	}
	h, _, br := newTestHub()
	launchableRecipe(br, "wf_next")
	br.callResults[methodKiroWorkflowListRecipes] = json.RawMessage(
		`{"recipes":[{"name":"` + before.WorkflowName + `","source":"bundled://probe"}]}`,
	)
	br.callResults[methodKiroWorkflowCancel] = fx.Uninvoked.CancelResult
	var cancelled atomic.Bool
	br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		switch method {
		case methodKiroWorkflowCancel:
			cancelled.Store(true)
		case methodKiroWorkflowList:
			listed := fx.Uninvoked.ListedBefore
			if cancelled.Load() {
				listed = fx.Uninvoked.ListedAfter
			}
			return json.RawMessage(`{"runs":[` + string(listed) + `]}`), nil, true
		}
		return nil, nil, false
	}
	if err := h.runs.recipeIdle(t.Context(), before.WorkflowName, ""); err == nil {
		t.Fatal("the fixture's created run did not hold its recipe before the cancel")
	}

	if err := h.runs.CancelAbandoned(t.Context(), before.WorkflowID); err != nil {
		t.Fatalf("CancelAbandoned: %v", err)
	}
	params := br.paramsFor(methodKiroWorkflowCancel)
	if params[keyWorkflowID] != before.WorkflowID {
		t.Errorf("cancel params %v, want the abandoned run's id", params)
	}
	if _, attributed := params["initiator"]; attributed {
		t.Errorf("cancel params %v carry an initiator; an abandoned run's cancel is nobody's", params)
	}
	if !h.runs.claimTermination(before.WorkflowID) {
		t.Error("the cancelled run's termination claim survived, though it sends no run_complete to release it")
	}
	if _, _, err := h.runs.launch(t.Context(), "bundled://probe", nil, marotte.RunDefaults{}); err != nil {
		t.Errorf("the next launch after the cancel = %v, want it launched", err)
	}
}

// A refused cancel of a run that was never invoked, so never armed, is still re-attempted while its lease is held.
func TestCancelAbandonedRefusedRetriesUnarmedRun(t *testing.T) {
	h, _, br := newTestHub()
	setCancelRetryDelay(h.runs, time.Millisecond)
	br.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowCancel: {Code: -32603, Message: "cancel refused"}}
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())

	if err := h.runs.CancelAbandoned(t.Context(), "wf_1"); err == nil {
		t.Fatal("a refused cancel reported success")
	}

	deadline := time.Now().Add(2 * time.Second)
	for callsOf(br, methodKiroWorkflowCancel) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("workflow/cancel sent %d times, want a re-attempt after the refusal",
				callsOf(br, methodKiroWorkflowCancel))
		}
		time.Sleep(time.Millisecond)
	}
}

// A cancel KAS refuses leaves the run and its lease, so the recipe stays busy and the next launch names the run.
func TestCancelAbandonedRefusedKeepsRecipeBusy(t *testing.T) {
	h, _, br := newTestHub()
	h.runs.cancelRetryBase = time.Hour
	launchableRecipe(br, "wf_2")
	br.callResults[methodKiroWorkflowList] = json.RawMessage(
		`{"runs":[{"workflowId":"wf_1","name":"publish","workflowName":"publish","status":"running"}]}`,
	)
	br.callRPCErrs = map[string]*marotte.RPCError{methodKiroWorkflowCancel: {Code: -32603, Message: "cancel refused"}}
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
	h.runs.armDeadline(t.Context(), "wf_1")

	if err := h.runs.CancelAbandoned(t.Context(), "wf_1"); err == nil {
		t.Fatal("a refused cancel reported success")
	}
	if _, held := h.runs.lease("wf_1"); !held {
		t.Error("the refused cancel released the run's lease")
	}
	_, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{})
	var be *schedule.BusyError
	if !errors.As(err, &be) || be.WorkflowID != "wf_1" {
		t.Errorf("the next launch = %v, want a BusyError naming wf_1", err)
	}
}

// A cancel that loses the termination claim sent nothing, so it is an error and not a landed cancel.
func TestCancelAbandonedLostClaimIsAnError(t *testing.T) {
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{methodKiroWorkflowCancel: json.RawMessage(`{}`)}
	h.insertBridgeNow(runChatID("wf_1"), testBridge(br, bridgeIdle))
	h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
	if !h.runs.claimTermination("wf_1") {
		t.Fatal("the fixture could not take the claim another terminator holds")
	}

	err := h.runs.CancelAbandoned(t.Context(), "wf_1")
	if !errors.Is(err, errTerminationClaimed) {
		t.Errorf("CancelAbandoned = %v, want errTerminationClaimed", err)
	}
	if slices.Contains(br.callLog(), methodKiroWorkflowCancel) {
		t.Error("a cancel went out though another terminator holds the claim")
	}
}

func callCount(br *fakeBridge, method string) int {
	br.mu.Lock()
	defer br.mu.Unlock()
	n := 0
	for _, m := range br.calls {
		if m == method {
			n++
		}
	}
	return n
}

// lostInvokeHub launches rule-1's event as wf_1 over a KAS that consumes the run's run_start on the host lane while
// invoke blocks, then fails the invoke.
func lostInvokeHub(t *testing.T) (*Runtime, *fakeBridge, *fakeConfirmer, error) {
	t.Helper()
	fc := &fakeConfirmer{}
	h, _, br := newTestHub(WithTriggerConfirmer(fc))
	launchableRecipe(br, "wf_1")
	br.mu.Lock()
	delete(br.callResults, methodKiroWorkflowInvoke)
	br.callResults[methodKiroWorkflowCancel] = json.RawMessage(`{}`)
	br.onCallFails = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool, error) {
		if method != methodKiroWorkflowInvoke {
			return nil, nil, false, nil
		}
		h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
			"workflowId": "wf_1", "workflowName": "publish",
		}))
		return nil, nil, false, errHoldFault
	}
	br.mu.Unlock()
	id, _, err := h.runs.launchAs(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, eventLaunch())
	if id != "wf_1" {
		t.Fatalf("launchAs id = %q (%v), want wf_1", id, err)
	}
	return h, br, fc, err
}

// A run_start consumed while invoke blocks confirms the event; the invoke's error then cancels nothing and launches
// nothing again, and the run keeps its lease.
func TestLostInvokeRunStartIsKept(t *testing.T) {
	h, br, fc, err := lostInvokeHub(t)

	if !errors.Is(err, errHoldFault) {
		t.Errorf("launchAs err = %v, want the invoke error", err)
	}
	if got := fc.launchedKeys(); !slices.Equal(got, []string{"github.com/o/r/run/7/success"}) {
		t.Errorf("launched = %q, want the event confirmed by its run_start", got)
	}
	if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered {
		t.Errorf("lease = %+v held %v, want the triggered lease kept", l, held)
	}
	if n := callCount(br, methodKiroWorkflowCancel); n != 0 {
		t.Errorf("workflow/cancel sent %d times, want none: the run started", n)
	}
	if n := callCount(br, methodKiroWorkflowNew); n != 1 {
		t.Errorf("workflow/new sent %d times, want 1", n)
	}
}

// After a confirmed run's invoke error marotte leaves the run to end by itself: a resume confirms nothing more, no
// cancel is sent, and its own terminal frame releases its lease.
func TestInvokeErrorAfterConfirmKeepsRun(t *testing.T) {
	h, br, fc, _ := lostInvokeHub(t)

	h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_1", "workflowName": "publish",
	}))
	if h.bridge.mgr.get(runChatID("wf_1")) == nil {
		t.Error("the confirmed run's host session was closed after the invoke error")
	}
	h.runs.observeComplete(t.Context(), runChatID("wf_1"), runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if n := fc.count("ConfirmEvent"); n != 1 {
		t.Errorf("ConfirmEvent called %d times, want 1: the first run_start confirms, a resume's confirms nothing more", n)
	}
	if n := callCount(br, methodKiroWorkflowCancel); n != 0 {
		t.Errorf("workflow/cancel sent %d times, want none", n)
	}
	if _, held := h.runs.lease("wf_1"); held {
		t.Error("the run's terminal frame left its lease held")
	}
}

// triggeredRetryHub is a runtime whose failed run wf_1 holds rule-1's triggered lease on a lease store in dir.
func triggeredRetryHub(t *testing.T, fc *fakeConfirmer, dir string) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowRetry: json.RawMessage(`{}`),
		methodKiroWorkflowList: json.RawMessage(`{"runs":[{"workflowId":"wf_1","name":"publish",` +
			`"status":"failed","parentSessionId":"` + testLaunchSession + `"}]}`),
	}
	st, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("runlease.NewStore: %v", err)
	}
	h.runs.mu.Lock()
	h.runs.leases = st
	h.runs.confirmer = fc
	h.runs.mu.Unlock()
	h.runs.grantLease(t.Context(), "wf_1", "publish", eventLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), "wf_1", testLaunchSession)
	return h, br
}

func executedRecorded(rs *Runs, workflowID string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	_, ok := rs.bounds.executed[workflowID]
	return ok
}

func retryFailed(t *testing.T, h *Runtime) error {
	t.Helper()
	_, err := h.runs.retry(t.Context(), "wf_1", h.runs.affordance("wf_1", &runHead{Status: "failed"}))
	return err
}

// A person's retry of a run its rule abandoned is the person's, whether or not the cancel already released the
// triggered lease: a manual lease is held before the verb, its run_start confirms nothing, and a restart finds no
// triggered lease to cancel. A failed hand-back refuses the retry and leaves the lease as it was.
func TestRetryOfAbandonedRunTakesRetryLease(t *testing.T) {
	t.Run("handed back", func(t *testing.T) {
		dir := t.TempDir()
		fc := &fakeConfirmer{retried: true}
		h, br := triggeredRetryHub(t, fc, dir)
		h.runs.armDeadline(t.Context(), "wf_1")
		h.runs.disarmDeadline(t.Context(), "wf_1")
		if !executedRecorded(h.runs, "wf_1") {
			t.Fatal("the fixture's abandoned run banked no executed time")
		}

		if err := retryFailed(t, h); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if executedRecorded(h.runs, "wf_1") {
			t.Error("the abandoned run's executed time carried into the person's retry, shortening its backstop")
		}
		if !br.called(methodKiroWorkflowRetry) {
			t.Error("the retry verb was not sent")
		}
		if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginManual || l.RuleID != "" {
			t.Errorf("lease = %+v held %v, want a manual lease naming no rule", l, held)
		}
		h.runs.observeStart(t.Context(), "", runNotif(methodWFRunStart, map[string]any{
			"workflowId": "wf_1", "workflowName": "publish", "parentSessionId": testLaunchSession,
		}))
		if n := fc.count("ConfirmEvent"); n != 0 {
			t.Errorf("ConfirmEvent called %d times for the person's run, want none", n)
		}
		reopened, err := runlease.NewStore(dir)
		if err != nil {
			t.Fatalf("reopen runlease.NewStore: %v", err)
		}
		if l, held := reopened.Get("wf_1"); held && (l.Origin == runlease.OriginTriggered || l.RuleID != "") {
			t.Errorf("lease on disk after the retry = %+v, want no triggered lease a restart would cancel", l)
		}
	})
	t.Run("hand-back refused", func(t *testing.T) {
		errWrite := errors.New("could not write launch-state/rule-1.json")
		fc := &fakeConfirmer{retriedErr: errWrite}
		h, br := triggeredRetryHub(t, fc, t.TempDir())

		if err := retryFailed(t, h); !errors.Is(err, errWrite) {
			t.Errorf("retry err = %v, want the confirmer's write error", err)
		}
		if br.called(methodKiroWorkflowRetry) {
			t.Error("the retry verb was sent after the hand-back failed")
		}
		if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered || l.RuleID != "rule-1" {
			t.Errorf("lease = %+v held %v, want the triggered lease untouched", l, held)
		}
	})
	t.Run("handed back after the cancel released the lease", func(t *testing.T) {
		fc := &fakeConfirmer{retried: true}
		h, br := triggeredRetryHub(t, fc, t.TempDir())
		if !h.runs.claimTermination("wf_1") {
			t.Fatal("the fixture could not take the claim the landed cancel held")
		}
		h.runs.ReleaseTriggered(t.Context(), "wf_1")

		if err := retryFailed(t, h); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if n := fc.count("Retried wf_1"); n != 1 {
			t.Errorf("Retried called %d times, want 1: the abandoned record outlives the released lease", n)
		}
		if !br.called(methodKiroWorkflowRetry) {
			t.Error("the retry verb was not sent")
		}
		if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginManual || l.RuleID != "" {
			t.Errorf("lease = %+v held %v, want a manual lease naming no rule", l, held)
		}
	})
	t.Run("hand-back refused with no lease held", func(t *testing.T) {
		errWrite := errors.New("could not write launch-state/rule-1.json")
		fc := &fakeConfirmer{retriedErr: errWrite}
		h, br := triggeredRetryHub(t, fc, t.TempDir())
		h.runs.ReleaseTriggered(t.Context(), "wf_1")

		if err := retryFailed(t, h); !errors.Is(err, errWrite) {
			t.Errorf("retry err = %v, want the confirmer's write error", err)
		}
		if br.called(methodKiroWorkflowRetry) {
			t.Error("the retry verb was sent after the hand-back failed")
		}
		if l, held := h.runs.lease("wf_1"); held {
			t.Errorf("lease = %+v, want none minted for a refused retry", l)
		}
	})
}

// A retry of a run whose attempt is still live keeps the triggered lease and starts a new life, so the retried run's
// run_start reaches the confirmer again.
func TestRetryOfLiveTriggeredRunRestartsBound(t *testing.T) {
	fc := &fakeConfirmer{}
	h, br := triggeredRetryHub(t, fc, t.TempDir())
	start := func() {
		h.runs.observeStart(t.Context(), runChatID("wf_1"), runNotif(methodWFRunStart, map[string]any{
			"workflowId": "wf_1", "workflowName": "publish",
		}))
	}
	start()

	if err := retryFailed(t, h); err != nil {
		t.Fatalf("retry: %v", err)
	}
	start()

	if n := fc.count("Retried wf_1"); n != 1 {
		t.Errorf("Retried called %d times, want 1", n)
	}
	if !br.called(methodKiroWorkflowRetry) {
		t.Error("the retry verb was not sent")
	}
	if l, held := h.runs.lease("wf_1"); !held || l.Origin != runlease.OriginTriggered || l.RuleID != "rule-1" {
		t.Errorf("lease = %+v held %v, want the triggered lease kept, no manual one", l, held)
	}
	if n := fc.count("ConfirmEvent rule-1"); n != 2 {
		t.Errorf("ConfirmEvent called %d times, want 2: the retried run's run_start was not handed over", n)
	}
}
