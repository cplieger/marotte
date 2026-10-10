package agent

// This pins marotte's bookkeeping: which frames become asks, one answering surface, a failed send
// handing the ask back, and no card left for a finished wait.

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// notifyAsk builds a `_kiro/session/notify` frame KAS would send for a step's
// question.
func notifyAsk(workflowID, nodeID, message, notifyID string) *marotte.RPCResponse {
	return runNotif(methodKiroSessionNotify, map[string]any{
		"sessionId":       "sess_parent",
		"callerSessionId": "sess_step",
		"message":         message,
		"severity":        "warning",
		"sender":          "step",
		"workflowId":      workflowID,
		"nodeId":          nodeID,
		"agentName":       "reviewer",
		"notifyId":        notifyID,
	})
}

func askOf(chatID marotte.ChatID, workflowID, askID, nodeID string) *runAsk {
	return &runAsk{
		chatID: chatID,
		payload: marotte.RunInputNeededPayload{
			WorkflowID: workflowID,
			AskID:      askID,
			NodeID:     nodeID,
		},
	}
}

func TestPendingRunAsks_AddReportsNewOnly(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	if !r.add(askOf("c1", "wf_1", "a1", "review")) {
		t.Error("Add(a fresh ask) = false, want true")
	}
	// A redelivered frame must not re-broadcast.
	if r.add(askOf("c1", "wf_1", "a1", "review")) {
		t.Error("Add(the same ask twice) = true, want false")
	}
	// Missing identity is not an ask.
	if r.add(askOf("c1", "", "a1", "review")) {
		t.Error("Add(no workflow id) = true, want false")
	}
	if r.add(askOf("c1", "wf_1", "", "review")) {
		t.Error("Add(no ask id) = true, want false")
	}
}

func TestPendingRunAsks_TakeIsOncePerAsk(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.add(askOf("c1", "wf_1", "a1", "review"))

	got, ok := r.takeIfPresent("wf_1", "a1")
	if !ok {
		t.Fatal("TakeIfPresent(the recorded ask) ok = false, want true")
	}
	if got.payload.NodeID != "review" {
		t.Errorf("the claimed ask's node = %q, want review", got.payload.NodeID)
	}
	// KAS accepts one answer; a loser's session/prompt would become an ordinary prompt.
	if _, second := r.takeIfPresent("wf_1", "a1"); second {
		t.Error("TakeIfPresent twice ok = true, want false on the second claim")
	}
}

func TestPendingRunAsks_TakeIsKeyedOnThePair(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	// Two runs of one recipe share a synthesised ask id, so the key must include the run.
	r.add(askOf("c1", "wf_1", "reconciled:root/review", "review"))
	r.add(askOf("c2", "wf_2", "reconciled:root/review", "review"))

	a, ok := r.takeIfPresent("wf_1", "reconciled:root/review")
	if !ok {
		t.Fatal("TakeIfPresent(wf_1) ok = false, want true")
	}
	if a.chatID != "c1" {
		t.Errorf("claimed the ask of chat %q, want c1", a.chatID)
	}
	if _, still := r.takeIfPresent("wf_2", "reconciled:root/review"); !still {
		t.Error("the OTHER run's ask was taken too, want it left in place")
	}
}

func TestPendingRunAsks_RestorePutsAClaimBack(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	a := askOf("c1", "wf_1", "a1", "review")
	r.add(a)
	claimed, _ := r.takeIfPresent("wf_1", "a1")
	r.restore(claimed)
	// Without the restore, a failed answer leaves the run parked with its card gone everywhere.
	if _, ok := r.takeIfPresent("wf_1", "a1"); !ok {
		t.Error("Restore then TakeIfPresent ok = false, want the ask answerable again")
	}
}

func TestPendingRunAsks_TakeNodeIsNodeScoped(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.add(askOf("c1", "wf_1", "a1", "review"))
	r.add(askOf("c1", "wf_1", "a2", "build"))
	// An ask with no node is collected by the terminal clear.
	r.add(askOf("c1", "wf_1", "a3", ""))

	got := r.takeNode("wf_1", "review")
	if len(got) != 1 || got[0].payload.AskID != "a1" {
		t.Fatalf("TakeNode(review) = %+v, want just a1", got)
	}
	// A parallel branch's node can complete while a sibling's step is parked.
	if _, ok := r.takeIfPresent("wf_1", "a2"); !ok {
		t.Error("the sibling node's ask was dropped, want it left in place")
	}
	if _, ok := r.takeIfPresent("wf_1", "a3"); !ok {
		t.Error("the node-less ask was dropped, want it left for the terminal clear")
	}
}

func TestPendingRunAsks_TakeRunAndClearChat(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.add(askOf("c1", "wf_1", "a1", "review"))
	r.add(askOf("c1", "wf_1", "a2", "build"))
	r.add(askOf("c2", "wf_2", "a3", "review"))

	// Returned rather than dropped: the caller announces each.
	got := r.takeRun("wf_1")
	if len(got) != 2 {
		t.Fatalf("TakeRun(wf_1) returned %d asks, want 2", len(got))
	}
	if r.hasRun("wf_1") {
		t.Error("HasRun(wf_1) after TakeRun = true, want false")
	}
	if !r.hasRun("wf_2") {
		t.Error("TakeRun(wf_1) also dropped wf_2's ask")
	}
	// Idempotent: the answer and lifecycle paths both run for one ask.
	if again := r.takeRun("wf_1"); len(again) != 0 {
		t.Errorf("TakeRun(wf_1) a second time returned %d asks, want 0", len(again))
	}

	r.clearChat("c2")
	if r.hasRun("wf_2") {
		t.Error("HasRun(wf_2) after ClearChat(c2) = true, want false")
	}
}

func TestPendingRunAsks_ListFiltersByChatButKeepsRunKeyedAsks(t *testing.T) {
	t.Parallel()
	r := &pendingRunAsks{}
	r.add(askOf("c1", "wf_1", "a1", "review"))
	r.add(askOf("run:wf_2", "wf_2", "a2", "review"))
	r.add(askOf("", "wf_3", "a3", "review"))

	// `run:<id>` is not a chat, so a parentless run's ask stays off chat streams.
	got := r.list("c1")
	if len(got) != 2 {
		t.Fatalf("List(c1) returned %d events, want 2 (c1's ask and the topicless one)", len(got))
	}
	// An unfiltered connection (the run tab) gets everything.
	if all := r.list(""); len(all) != 3 {
		t.Errorf("List(\"\") returned %d events, want 3", len(all))
	}
	for _, evt := range got {
		if evt.Type != marotte.EventRunInputNeeded {
			t.Errorf("List emitted %q, want %q", evt.Type, marotte.EventRunInputNeeded)
		}
	}
}

// TestRunDispatch_SessionNotifyBecomesAnAsk pins the run bridge's door, ahead of the `_kiro/workflow/`
// prefix test, keeping the run bridge's own chat id: an ask must land on a surface.
func TestRunDispatch_SessionNotifyBecomesAnAsk(t *testing.T) {
	h, _, _ := newTestHub()

	h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), notifyAsk("wf_1", "review", "which branch?", "n1"))

	events, notices := withoutNotifications(bufferedEvents(h))
	if len(events) != 1 || notices != 1 {
		t.Fatalf("got %d events and %d notifications, want 1 of each: %+v", len(events), notices, events)
	}
	if events[0].Type != string(marotte.EventRunInputNeeded) {
		t.Fatalf("type = %q, want run_input_needed", events[0].Type)
	}
	if events[0].ChatID != "run:wf_1" {
		t.Errorf("chat_id = %q, want run:wf_1 (the run tab's dock key)", events[0].ChatID)
	}
	p := marshalPayload(t, events[0].Payload)
	if p["workflow_id"] != "wf_1" || p["node_id"] != "review" {
		t.Errorf("payload = %+v, want wf_1/review", p)
	}
	if p["question"] != "which branch?" {
		t.Errorf("question = %q, want the message verbatim", p["question"])
	}
	// The registry holds it too: the event does not re-fire for a later connect.
	if !h.runs.asks.hasRun("wf_1") {
		t.Error("the ask was broadcast but not recorded, so a reconnect would lose it")
	}
}

// TestTranslateACPEvent_SessionNotifyBecomesAnAsk pins the chat bridge's door: KAS parents an
// agent-launched run on the calling chat, so its asks arrive there.
func TestTranslateACPEvent_SessionNotifyBecomesAnAsk(t *testing.T) {
	h, cs, _ := newTestHub()
	cs.seed(t, "c1", nil)
	seeded := h.bus.fanout.Position().Head

	h.translateACPEvent("c1", h.originOf("c1"), notifyAsk("wf_1", "review", "which branch?", "n1"))

	var events []bufferedEvent
	for _, e := range bufferedSince(h, seeded) {
		var evt bufferedEvent
		if json.Unmarshal(e.Event.Data, &evt) == nil {
			events = append(events, evt)
		}
	}
	events, notices := withoutNotifications(events)
	if len(events) != 1 || notices != 1 {
		t.Fatalf("got %d events and %d notifications, want 1 of each: %+v", len(events), notices, events)
	}
	if events[0].ChatID != "c1" {
		t.Errorf("chat_id = %q, want c1 (the launching chat's dock key)", events[0].ChatID)
	}
}

// TestRunDispatch_SessionNotifyDropsNonWarnings pins that `info`, `success` and `error` leave nobody waiting.
func TestRunDispatch_SessionNotifyDropsNonWarnings(t *testing.T) {
	for _, severity := range []string{"info", "success", "error"} {
		t.Run(severity, func(t *testing.T) {
			h, _, _ := newTestHub()
			h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(methodKiroSessionNotify, map[string]any{
				"callerSessionId": "sess_step",
				"message":         "something happened",
				"severity":        severity,
				"workflowId":      "wf_1",
			}))
			if events := bufferedEvents(h); len(events) != 0 {
				t.Errorf("severity %q produced %+v, want no event", severity, events)
			}
			if h.runs.asks.hasRun("wf_1") {
				t.Errorf("severity %q recorded an ask, want none", severity)
			}
		})
	}
}

// TestRunAskCleared pins that no ask outlives its wait.
func TestRunAskCleared(t *testing.T) {
	t.Run("a terminal run_complete clears the run", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), notifyAsk("wf_1", "review", "which branch?", "n1"))
		if !h.runs.asks.hasRun("wf_1") {
			t.Fatal("Setup: the ask was not recorded")
		}
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "completed"}))
		if h.runs.asks.hasRun("wf_1") {
			t.Error("a completed run still holds an ask, want it cleared")
		}
	})

	t.Run("a terminal run_complete announces what it retired", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), notifyAsk("wf_1", "review", "which branch?", "n1"))
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "failed"}))

		// Dropping the entry takes no card off any screen, and a stale head card hides the chat's later asks.
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// Nobody answered, so not SettledByUser.
		if got := settled[0]["settled_by"]; got != string(marotte.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q", got, marotte.SettledByMoot)
		}
	})

	t.Run("a non-terminal run_complete keeps it", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), notifyAsk("wf_1", "review", "which branch?", "n1"))
		// An onMaxIterations stop arrives on this frame and is still resumable, so its ask is live.
		h.dispatch(t.Context(), "run:wf_1", h.originOf("run:wf_1"), runNotif(methodWFRunComplete,
			map[string]any{"workflowId": "wf_1", "status": "paused"}))
		if !h.runs.asks.hasRun("wf_1") {
			t.Error("a paused run lost its ask, want it kept")
		}
	})

	t.Run("the asking node completing retires it and says so", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.seed(t, "c1", nil)
		h.translateACPEvent("c1", h.originOf("c1"), notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", h.originOf("c1"), runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "review", "status": "completed",
		}))
		if h.runs.asks.hasRun("wf_1") {
			t.Error("the asking node completed and its ask survived")
		}
		// The announcement is what takes the card down.
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		// Moot: the answer path settles its own claim before sending, and this frame also fires for failed and aborted nodes.
		if got := settled[0]["settled_by"]; got != string(marotte.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q", got, marotte.SettledByMoot)
		}
	})

	t.Run("a node that FAILED still does not claim an answer", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.seed(t, "c1", nil)
		h.translateACPEvent("c1", h.originOf("c1"), notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", h.originOf("c1"), runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "review", "status": "failed",
		}))
		settled := settledPayloads(t, h)
		if len(settled) != 1 {
			t.Fatalf("run_input_settled events = %d, want 1", len(settled))
		}
		if got := settled[0]["settled_by"]; got != string(marotte.SettledByMoot) {
			t.Errorf("settled_by = %q, want %q — nobody answered a step that failed",
				got, marotte.SettledByMoot)
		}
	})

	t.Run("a sibling node completing leaves it alone", func(t *testing.T) {
		h, cs, _ := newTestHub()
		cs.seed(t, "c1", nil)
		h.translateACPEvent("c1", h.originOf("c1"), notifyAsk("wf_1", "review", "which branch?", "n1"))

		h.translateACPEvent("c1", h.originOf("c1"), runNotif(methodWFNodeComplete, map[string]any{
			"workflowId": "wf_1", "nodeId": "build", "status": "completed",
		}))
		if !h.runs.asks.hasRun("wf_1") {
			t.Error("a sibling node's completion dropped a live ask")
		}
	})
}

func hasEventType(events []bufferedEvent, want string) bool {
	for _, e := range events {
		if e.Type == want {
			return true
		}
	}
	return false
}

// The attribution is what the cases assert.
func settledPayloads(t *testing.T, h *Runtime) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, e := range bufferedEvents(h) {
		if e.Type == string(marotte.EventRunInputSettled) {
			out = append(out, marshalPayload(t, e.Payload))
		}
	}
	return out
}

// pauseFixture mirrors testdata/need_input_pauses.json, shared with the TypeScript side.
type pauseFixture struct {
	Cases []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
		Want   bool   `json:"want"`
	} `json:"cases"`
}

// TestNeedInputPauseContract is half of a cross-language pin with run-store-pause.node.test.ts:
// a KAS wording change applied in one language fails the other.
func TestNeedInputPauseContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/need_input_pauses.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx pauseFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("fixture carries no cases; a silently-empty table would pass forever")
	}
	// Both verdicts must appear, or a constant predicate passes.
	var trues, falses int
	for _, tc := range fx.Cases {
		if tc.Want {
			trues++
		} else {
			falses++
		}
	}
	if trues == 0 || falses == 0 {
		t.Fatalf("fixture has %d true and %d false cases; both are needed", trues, falses)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := needInputPause(tc.Reason); got != tc.Want {
				t.Errorf("needInputPause(%q) = %v, want %v", tc.Reason, got, tc.Want)
			}
		})
	}
}

func TestPausedLeaf(t *testing.T) {
	t.Parallel()
	// The root reports `paused` while the step holding the question is below it; its session is the answer address.
	root := &askNode{
		NodeID: "root",
		Status: "paused",
		Children: []askNode{
			{NodeID: "build", Status: "completed"},
			{
				NodeID: "loop",
				Status: "paused",
				Children: []askNode{
					{NodeID: "iter", Status: "completed"},
					{NodeID: "iter", Status: "paused", SessionID: "sess_step", AgentName: "reviewer"},
				},
			},
		},
	}
	leaf, path := pausedLeaf(root, nil)
	if leaf == nil {
		t.Fatal("pausedLeaf found nothing, want the parked step")
	}
	if leaf.SessionID != "sess_step" {
		t.Errorf("leaf session = %q, want sess_step", leaf.SessionID)
	}
	// The path, not the node id: a repeat's iterations share an id.
	want := []string{"root", "loop", "iter"}
	if len(path) != len(want) {
		t.Fatalf("path = %v, want %v", path, want)
	}
	for i := range want {
		if path[i] != want[i] {
			t.Fatalf("path = %v, want %v", path, want)
		}
	}
	// Nothing parked yields nothing.
	if l, _ := pausedLeaf(&askNode{NodeID: "root", Status: "running"}, nil); l != nil {
		t.Errorf("pausedLeaf(a running run) = %+v, want nil", l)
	}
}

// The run's session is the answer address. One builder for the reconcile and the answer path's
// fallback.
func parkedInspect(t *testing.T, status marotte.RunStatus, pauseReason, stepSession string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"status":      status,
			"pauseReason": pauseReason,
			"pauseDetail": map[string]any{"occurredAt": "2026-09-03T10:00:00Z"},
			"root": map[string]any{
				"nodeId": "root", "type": "sequence", "status": "paused",
				"children": []any{map[string]any{
					"nodeId": "review", "type": stepNodeType, "status": "paused",
					"sessionId": stepSession, "agentName": "reviewer",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

func eventOfType(t *testing.T, h *Runtime, want string) bufferedEvent {
	t.Helper()
	for _, e := range bufferedEvents(h) {
		if e.Type == want {
			return e
		}
	}
	t.Fatalf("no %s event; got %+v", want, bufferedEvents(h))
	return bufferedEvent{}
}

// TestReconcileNeedInput pins the restart path: the in-memory registry lost the question, and only
// a reconstructed ask avoids cancelling.
func TestReconcileNeedInput(t *testing.T) {
	inspect := func(status marotte.RunStatus, pauseReason string) json.RawMessage {
		return parkedInspect(t, status, pauseReason, "sess_step")
	}

	t.Run("a need_input pause with no ask gets one", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		events, notices := withoutNotifications(bufferedEvents(h))
		if len(events) != 1 || events[0].Type != string(marotte.EventRunInputNeeded) || notices != 1 {
			t.Fatalf("got %+v and %d notifications, want one run_input_needed and its notification", events, notices)
		}
		p := marshalPayload(t, events[0].Payload)
		if p["step_session_id"] != "sess_step" {
			t.Errorf("step_session_id = %q, want sess_step (the answer address)", p["step_session_id"])
		}
		// Empty: the text was in the lost registry.
		if p["question"] != "" {
			t.Errorf("question = %q, want empty on a reconstructed ask", p["question"])
		}
	})

	t.Run("it is idempotent across reads", func(t *testing.T) {
		h, _, _ := newTestHub()
		raw := inspect("paused", needInputPauseReason)
		// Run on every refetch, so a fresh id per read would stack duplicates.
		h.runs.reconcileNeedInput(t.Context(), "wf_1", raw)
		h.runs.reconcileNeedInput(t.Context(), "wf_1", raw)
		if events, notices := withoutNotifications(bufferedEvents(h)); len(events) != 1 || notices != 1 {
			t.Errorf("two reads produced %d events and %d notifications, want 1 of each", len(events), notices)
		}
	})

	// The composer dock matches the chat id alone, and answering needs the launching chat's bridge;
	// keyed to the chat the card renders in both docks.
	t.Run("it keys the ask to the launching chat when one still hosts the run", func(t *testing.T) {
		h, cs, br := newTestHub()
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_1", "status": "paused", "parentSessionId": "sess_owned",
			}),
		}
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
			c.Name = "A"
			c.RecordSession("sess_owned")
			return true
		}); err != nil {
			t.Fatalf("Setup: seeding the launching chat: %s", err)
		}
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: opening the launching chat's bridge: %s", err)
		}
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		evt := eventOfType(t, h, string(marotte.EventRunInputNeeded))
		if evt.ChatID != "c1" {
			t.Errorf("chat_id = %q, want c1: keyed to run:wf_1 the card renders only in "+
				"the run tab and never in the launching chat's composer dock", evt.ChatID)
		}
	})

	// After a restart no launching chat's dock exists, and the run tab is the way in.
	t.Run("it keys the ask to the run when nothing hosts it", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))

		evt := eventOfType(t, h, string(marotte.EventRunInputNeeded))
		if evt.ChatID != string(runChatID("wf_1")) {
			t.Errorf("chat_id = %q, want run:wf_1", evt.ChatID)
		}
	})

	// The window AnswerInput opens before it claims: without it a refetch mints a text-less twin nothing retires.
	t.Run("an answer in flight gets no twin", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.asks.beginAnswer("wf_1")
		t.Cleanup(func() { h.runs.asks.endAnswer("wf_1") })
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("paused", needInputPauseReason))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event while an answer is in flight", events)
		}
	})

	t.Run("a run paused for a transient error gets none", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1",
			inspect("paused", "Transient connection error (EAI_AGAIN); the run is paused and can be resumed."))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event for an involuntary pause", events)
		}
	})

	t.Run("a running run gets none", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", inspect("running", ""))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event for a running run", events)
		}
	})
}

// TestReconcileNeedInput_InsideAParallelBranch pins that a branch's sentence goes to a state copy, so only the
// branch node's completionSignal survives.
func TestReconcileNeedInput_InsideAParallelBranch(t *testing.T) {
	// Two paused branches; the first is what pausedLeaf would name, so expecting the second proves the signal decided.
	branched := func(t *testing.T, firstSignal, secondSignal string) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"state": map[string]any{
				"status": "paused",
				// The wrapper sentence, verbatim from executeParallel.
				"pauseReason": "Parallel 'phase1' is waiting on branch 'verify'.",
				"root": map[string]any{
					"nodeId": "root", "status": "paused",
					"children": []any{map[string]any{
						"nodeId": "phase1", "status": "paused",
						"children": []any{
							map[string]any{
								"nodeId": "verify", "status": "paused",
								"sessionId": "sess_verify", "agentName": "verifier",
								"completionSignal": firstSignal,
							},
							map[string]any{
								"nodeId": "plan", "status": "paused",
								"sessionId": "sess_plan", "agentName": "planner",
								"completionSignal": secondSignal,
							},
						},
					}},
				},
			},
		})
		if err != nil {
			t.Fatalf("Setup: marshalling the inspect reply: %s", err)
		}
		return raw
	}

	t.Run("a branch parked on a person gets an ask", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", branched(t, needInputSignal, ""))

		evt := eventOfType(t, h, string(marotte.EventRunInputNeeded))
		p := marshalPayload(t, evt.Payload)
		if p["node_id"] != "verify" {
			t.Errorf("node_id = %q, want verify", p["node_id"])
		}
		if p["step_session_id"] != "sess_verify" {
			t.Errorf("step_session_id = %q, want sess_verify (the answer address)",
				p["step_session_id"])
		}
	})

	// Only the signal says which branch owes an answer; prompting the other steers a step nobody asked.
	t.Run("it names the branch carrying the signal, not the first paused one", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", branched(t, "", needInputSignal))

		evt := eventOfType(t, h, string(marotte.EventRunInputNeeded))
		p := marshalPayload(t, evt.Payload)
		if p["node_id"] != "plan" {
			t.Errorf("node_id = %q, want plan: the ask must follow the signal rather "+
				"than the walk order", p["node_id"])
		}
	})

	// A branch parked on a transient error produces the same wrapper sentence and needs only a resume.
	t.Run("a branch parked on a transient error gets none", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.runs.reconcileNeedInput(t.Context(), "wf_1", branched(t, "", ""))
		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event: no node claims a person owes an answer", events)
		}
	})

	// KAS's queued status update writes `need_input` onto a still-running node, so a signal alone is not a park.
	t.Run("a signal on a RUNNING node is not a park", func(t *testing.T) {
		h, _, _ := newTestHub()
		raw, err := json.Marshal(map[string]any{
			"state": map[string]any{
				"status":      "paused",
				"pauseReason": "Parallel 'phase1' is waiting on branch 'plan'.",
				"root": map[string]any{
					"nodeId": "root", "status": "paused",
					"children": []any{map[string]any{
						"nodeId": "phase1", "status": "paused",
						"children": []any{
							map[string]any{
								"nodeId": "verify", "status": "running",
								"sessionId": "sess_verify", "completionSignal": needInputSignal,
							},
							map[string]any{"nodeId": "plan", "status": "paused", "sessionId": "sess_plan"},
						},
					}},
				},
			},
		})
		if err != nil {
			t.Fatalf("Setup: marshalling the inspect reply: %s", err)
		}

		h.runs.reconcileNeedInput(t.Context(), "wf_1", raw)

		if events := bufferedEvents(h); len(events) != 0 {
			t.Errorf("got %+v, want no event: the only need-input signal is on a node that "+
				"is still executing, so no step is waiting at it", events)
		}
	})
}

// TestAnswerInput pins the answer RPC, the take-once refusal, and the restore on a failed send.
func TestAnswerInput(t *testing.T) {
	// setup wires a run bridge holding one recorded ask, a parentless run's shape.
	setup := func(t *testing.T) (*Runtime, *fakeBridge) {
		t.Helper()
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.add(&runAsk{
			chatID: "run:wf_1",
			payload: marotte.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
			},
		})
		return h, br
	}

	t.Run("it prompts the paused step's own session", func(t *testing.T) {
		h, br := setup(t)
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("AnswerInput = %v, want nil", err)
		}
		// A plain session/prompt to the step's own session, which KAS reroutes into the run (tryResumeStepWithMessage).
		params := br.paramsFor(marotte.MethodPrompt)
		if params == nil {
			t.Fatalf("no %s call, calls were %v", marotte.MethodPrompt, br.callLog())
		}
		if params["sessionId"] != "sess_step" {
			t.Errorf("sessionId = %v, want sess_step", params["sessionId"])
		}
		blocks, ok := params["prompt"].([]any)
		if !ok || len(blocks) != 1 {
			t.Fatalf("prompt = %#v, want one content block", params["prompt"])
		}
		block, ok := blocks[0].(map[string]any)
		if !ok || block["type"] != "text" || block["text"] != "the main branch" {
			t.Errorf("prompt block = %#v, want a text block carrying the answer", blocks[0])
		}
		if !hasEventType(bufferedEvents(h), string(marotte.EventRunInputSettled)) {
			t.Error("no run_input_settled event, so the card on every other surface stays live")
		}
	})

	t.Run("only one surface may answer", func(t *testing.T) {
		h, _ := setup(t)
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("Setup: the first answer failed: %v", err)
		}
		// KAS accepts one answer, so the claim is decided here.
		err := h.runs.answerInput(t.Context(), "wf_1", "a1", "no, the release branch")
		if !errors.Is(err, errAskAlreadySettled) {
			t.Errorf("the second answer = %v, want errAskAlreadySettled", err)
		}
	})

	t.Run("a failed send hands the ask back AND re-offers it", func(t *testing.T) {
		h, br := setup(t)
		br.callErrs = map[string]error{marotte.MethodPrompt: errors.New("bridge died")}
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err == nil {
			t.Fatal("AnswerInput = nil, want the transport error")
		}
		// Without the restore, a blip loses the card for good.
		if !h.runs.asks.hasRun("wf_1") {
			t.Error("the ask was lost on a failed send, want it restored")
		}
		// The entry and a re-offered frame both: the click already spliced the card from every dock. Not a settle: still open.
		if !hasEventType(bufferedEvents(h), string(marotte.EventRunInputNeeded)) {
			t.Error("no run_input_needed event, so the restored ask reaches no surface")
		}
		if hasEventType(bufferedEvents(h), string(marotte.EventRunInputSettled)) {
			t.Error("a failed send announced a settle, want the ask re-offered instead")
		}
	})

	// The fallback's success arm: only a reconciled ask (no callerSessionId) reaches it.
	t.Run("an address-less ask resolves the step from a fresh inspect", func(t *testing.T) {
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		br.callResults = map[string]json.RawMessage{
			methodKiroWorkflowInspect: parkedInspect(
				t, marotte.RunStatusPaused, needInputPauseReason, "sess_from_inspect",
			),
		}
		h.runs.asks.add(&runAsk{
			chatID: runChatID("wf_1"),
			payload: marotte.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "reconciled:root/review", NodeID: "review",
			},
		})
		if err := h.runs.answerInput(
			t.Context(), "wf_1", "reconciled:root/review", "the main branch",
		); err != nil {
			t.Fatalf("AnswerInput = %v, want nil", err)
		}
		params := br.paramsFor(marotte.MethodPrompt)
		if params == nil {
			t.Fatalf("no %s call, calls were %v", marotte.MethodPrompt, br.callLog())
		}
		// The paused leaf's session: KAS reroutes only a prompt addressed to the parked step.
		if params["sessionId"] != "sess_from_inspect" {
			t.Errorf("sessionId = %v, want sess_from_inspect (resolved from inspect)",
				params["sessionId"])
		}
		if h.runs.asks.hasRun("wf_1") {
			t.Error("the ask survived a successful answer, so its card is offered again")
		}
	})

	t.Run("an unaddressable step re-offers the ask too", func(t *testing.T) {
		// No step session and no fresh inspect, so the claim goes back.
		h, _, br := newTestHub()
		h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
		h.runs.asks.add(&runAsk{
			chatID:  "run:wf_1",
			payload: marotte.RunInputNeededPayload{WorkflowID: "wf_1", AskID: "a1"},
		})
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err == nil {
			t.Fatal("AnswerInput with no answer address = nil, want a refusal")
		}
		if !h.runs.asks.hasRun("wf_1") {
			t.Fatal("the ask was consumed by a refusal, want it left answerable")
		}
		if !hasEventType(bufferedEvents(h), string(marotte.EventRunInputNeeded)) {
			t.Error("no run_input_needed event, so the restored ask reaches no surface")
		}
	})

	t.Run("an empty answer is refused", func(t *testing.T) {
		h, _ := setup(t)
		// Continue-without-answering is a different verb; an empty box must not reach it.
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "   "); err == nil {
			t.Error("AnswerInput(whitespace) = nil, want a refusal")
		}
		if !h.runs.asks.hasRun("wf_1") {
			t.Error("a refused empty answer consumed the ask")
		}
	})

	// A run nothing hosts is ordinary since a park drops the bridge, so the answer re-hosts.
	t.Run("a run nothing hosts is re-hosted and the answer lands", func(t *testing.T) {
		h, _, br := newTestHub()
		br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: parentlessRunList("wf_1")}
		h.runs.asks.add(&runAsk{
			chatID: "run:wf_1",
			payload: marotte.RunInputNeededPayload{
				WorkflowID: "wf_1", AskID: "a1", StepSessionID: "sess_step",
			},
		})
		if h.bridge.mgr.get(runChatID("wf_1")) != nil {
			t.Fatal("the fixture registered a bridge, so this exercises the wrong branch")
		}
		if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err != nil {
			t.Fatalf("AnswerInput on an unhosted run = %v, want nil", err)
		}
		if h.bridge.mgr.get(runChatID("wf_1")) == nil {
			t.Error("no bridge was registered under the run's synthetic chat id, so its " +
				"lifecycle frames have nowhere to route")
		}
		if !slices.Contains(br.callLog(), marotte.MethodPrompt) {
			t.Errorf("the answer never reached KAS; calls were %v", br.callLog())
		}
		if h.runs.asks.hasRun("wf_1") {
			t.Error("the answered ask is still offered, so the card outlives its answer")
		}
	})
}

// withoutNotifications splits off the `notification` frames, which ride beside the event they announce.
func withoutNotifications(in []bufferedEvent) (out []bufferedEvent, notices int) {
	for _, e := range in {
		if e.Type == string(marotte.EventNotification) {
			notices++
			continue
		}
		out = append(out, e)
	}
	return out, notices
}
