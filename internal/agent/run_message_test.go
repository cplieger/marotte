package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// stepVerbCase is one row of testdata/step_message_verbs.json, the availability table
// static-src/run-composer.ts's composerMode is held to as well.
type stepVerbCase struct {
	ID      string                        `json:"id"`
	Run     marotte.RunStatus             `json:"run"`
	Node    marotte.RunNodeStatus         `json:"node"`
	Verb    marotte.RunStepMessageVerb    `json:"verb"`
	Refusal marotte.RunStepMessageRefusal `json:"refusal"`
	Session bool                          `json:"session"`
	Asked   bool                          `json:"asked"`
}

func TestStepMessageVerb_PicksTheVerbFromTheLiveState(t *testing.T) {
	raw, err := os.ReadFile("testdata/step_message_verbs.json")
	if err != nil {
		t.Fatalf("Setup: reading testdata/step_message_verbs.json: %v", err)
	}
	var cases []stepVerbCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("Setup: testdata/step_message_verbs.json = %d cases, err %v", len(cases), err)
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			now := stepNow{status: tc.Node, run: tc.Run}
			if tc.Session {
				now.at.session = "s"
			}
			verb, refuse := stepMessageVerb(&now, tc.Asked)
			if verb != tc.Verb || refuse != tc.Refusal {
				t.Errorf("stepMessageVerb(%+v, asked=%v) = (%q, %q), want (%q, %q)",
					now, tc.Asked, verb, refuse, tc.Verb, tc.Refusal)
			}
		})
	}
}

func oneStepInspect(t *testing.T, run marotte.RunStatus, step marotte.RunNodeStatus, session string) json.RawMessage {
	t.Helper()
	return stepTreeInspect(t, run, step, "root", "review", session)
}

func stepTreeInspect(t *testing.T, run marotte.RunStatus, step marotte.RunNodeStatus, rootID, stepID, session string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"status": run,
			"root": map[string]any{
				"nodeId": rootID, "type": "sequence", "status": step,
				"children": []any{map[string]any{
					"nodeId": stepID, "type": stepNodeType, "status": step, "sessionId": session,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	return raw
}

func messageHub(t *testing.T, run marotte.RunStatus, step marotte.RunNodeStatus) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	h.runs.log = newRunLog(t.TempDir())
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	br.setCallResult(methodKiroWorkflowInspect, oneStepInspect(t, run, step, "sess_step"))
	br.setCallResult(marotte.MethodSessionSteer, json.RawMessage(`{"queued":true}`))
	h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "")
	return h, br
}

func stepSteersIn(t *testing.T, h *Runtime) []marotte.EntrySteer {
	t.Helper()
	l, err := h.runs.log.log(t.Context(), "wf_1")
	if err != nil || l == nil {
		t.Fatalf("Setup: the run log is unreadable: %v", err)
	}
	all, err := l.All()
	if err != nil {
		t.Fatalf("Setup: reading the run log: %v", err)
	}
	var out []marotte.EntrySteer
	for i := range all {
		if all[i].Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		if json.Unmarshal(all[i].Payload, &s) != nil {
			t.Fatalf("Setup: steer payload %s did not decode", all[i].Payload)
		}
		out = append(out, s)
	}
	return out
}

func TestMessageStep_SteersARunningStepOnItsOwnSession(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)

	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "  use the fixture  ", "m-abc")
	if err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}
	if out.Verb != marotte.RunStepMessageSteer || out.SteerID != "steer-m-abc" {
		t.Errorf("MessageStep reply = %+v, want verb steer with steer id steer-m-abc", out)
	}
	params := br.paramsFor(marotte.MethodSessionSteer)
	if params["sessionId"] != marotte.SessionID("sess_step") || params["message"] != "use the fixture" || params["messageId"] != "m-abc" {
		t.Errorf("_session/steer params = %v, want the step's session, the trimmed words and the bare message id", params)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no session/prompt: a prompt aborts a running step", br.callLog())
	}
}

func TestMessageStep_ResumesAPausedStepWithALabelledPromptAndRecordsIt(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)

	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-1")
	if err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}
	if out.Verb != marotte.RunStepMessagePrompt {
		t.Errorf("verb = %q, want prompt", out.Verb)
	}
	params := br.paramsFor(marotte.MethodPrompt)
	if params["sessionId"] != "sess_step" {
		t.Errorf("session/prompt sessionId = %v, want sess_step", params["sessionId"])
	}
	meta, _ := params["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	if kiro["displayText"] != "carry on" {
		t.Errorf("_meta.kiro = %v, want displayText carrying the words", kiro)
	}
	if _, ok := kiro["contextBreakdown"]; ok {
		t.Errorf("_meta.kiro = %v, want no contextBreakdown on a step's prompt", kiro)
	}
	got := stepSteersIn(t, h)
	want := []marotte.EntrySteer{{Text: "carry on", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("run log steers = %+v, want %+v (the words in the step's continued turn)", got, want)
	}
}

func TestMessageStep_AnswersAnOpenAskThroughTheAnswerPath(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
		WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
	}})

	out, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "the main branch", "m-2")
	if err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}
	if out.Verb != marotte.RunStepMessageAnswer {
		t.Errorf("verb = %q, want answer", out.Verb)
	}
	if h.runs.asks.hasRun("wf_1") {
		t.Error("the ask is still open, want it settled by the answer")
	}
	meta, _ := br.paramsFor(marotte.MethodPrompt)["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	if kiro["displayText"] != "the main branch" {
		t.Errorf("_meta.kiro = %v, want the answer labelled", kiro)
	}
	if got := stepSteersIn(t, h); len(got) != 1 || got[0].Text != "the main branch" {
		t.Errorf("run log steers = %+v, want the answer recorded once", got)
	}
}

func TestMessageStep_RefusesAFinishedStepWithItsReason(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusCompleted)

	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "too late", "m-3")

	rec := httptest.NewRecorder()
	h.runRoutes.writeStepErr(rec, "message", "wf_1", reviewPath, err)
	if rec.Code != http.StatusConflict || reasonOf(t, rec.Body.Bytes()) != "finished" {
		t.Errorf("reply = %d %s, want 409 with reason finished", rec.Code, rec.Body.String())
	}
	if calls := br.callLog(); slices.Contains(calls, marotte.MethodPrompt) || slices.Contains(calls, marotte.MethodSessionSteer) {
		t.Errorf("calls = %v, want nothing sent to a finished step", calls)
	}
}

func TestMessageStep_RefusesANotificationShapedMessage(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "[notification/info] hi", "m-4")
	rec := httptest.NewRecorder()
	h.runRoutes.writeStepErr(rec, "message", "wf_1", reviewPath, err)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for text KAS would read as a system notice", rec.Code)
	}
}

func TestStepSteer_ATurnEndLeavesUnreadRowsNotReadInTheStepsTurn(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	// KAS holds the steer and never reads it.
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-5"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}
	h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")

	got := stepSteersIn(t, h)
	want := []marotte.EntrySteer{{
		Text: "check the logs", Origin: marotte.SteerOriginUser,
		State: marotte.SteerStateDropped, Reason: marotte.SteerReasonBoundary,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("run log steers = %+v, want %+v: a finished step does not carry its unread steer", got, want)
	}
	if len(h.runs.steers.sessionsAt("wf_1", reviewPath)) != 0 {
		t.Error("the finished step's target survived, want it forgotten")
	}
}

func TestStepSteer_RowFramesAddressTheRunTab(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-6"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}

	var frame *marotte.SteerQueuedPayload
	for _, e := range bufferedEvents(h) {
		if e.Type != string(marotte.EventSteerQueued) {
			continue
		}
		var p marotte.SteerQueuedPayload
		raw, _ := json.Marshal(e.Payload)
		if json.Unmarshal(raw, &p) == nil && p.SteerID == "steer-m-6" {
			if e.ChatID != "" {
				t.Errorf("steer_queued chat id = %q, want none: a step's row is no chat's", e.ChatID)
			}
			frame = &p
		}
	}
	if frame == nil || frame.WorkflowID != "wf_1" || frame.NodePath != reviewPath {
		t.Errorf("row frame = %+v, want it addressed to wf_1 root/review", frame)
	}
	if replay := h.bus.steers.list(""); len(replay) != 1 {
		t.Errorf("connect replay = %d frames, want the step's one row", len(replay))
	}
}

func TestRemoveStepSteer_DeletesARowKASHolds(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-7"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}
	br.setCallResult(marotte.MethodSessionSteerClear, json.RawMessage(`{"messageIds":["steer-m-7"]}`))
	// The clear's reply lands at seq 3, and the carrier's read loop has folded that far: only the
	// carrier's position can release the barrier.
	gen := h.coord.turns.attachForward(runChatID("wf_1"))
	br.mu.Lock()
	br.deliveredSeq = 3
	br.mu.Unlock()
	h.coord.turns.observe(runChatID("wf_1"), gen, 3)

	if err := h.runs.removeStepSteer(t.Context(), "wf_1", reviewPath, "steer-m-7"); err != nil {
		t.Fatalf("RemoveStepSteer = %v, want nil", err)
	}
	if params := br.paramsFor(marotte.MethodSessionSteerClear); params["sessionId"] != marotte.SessionID("sess_step") {
		t.Errorf("_session/steer/clear params = %v, want the step's session", params)
	}
	got := stepSteersIn(t, h)
	if len(got) != 1 || got[0].Reason != marotte.SteerReasonDeleted {
		t.Errorf("run log steers = %+v, want one deleted note", got)
	}
}

func TestRemoveStepSteer_AStepWithNoRowsIsNotWaiting(t *testing.T) {
	h, _, _ := newTestHub()
	err := h.runs.removeStepSteer(t.Context(), "wf_1", reviewPath, "steer-x")
	rec := httptest.NewRecorder()
	h.runRoutes.writeStepErr(rec, "steer remove", "wf_1", reviewPath, err)
	if rec.Code != http.StatusNotFound || reasonOf(t, rec.Body.Bytes()) != command.SteerRefuseNotWaiting {
		t.Errorf("reply = %d %s, want 404 not_waiting", rec.Code, rec.Body.String())
	}
}

func reasonOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Reason string `json:"code"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("error reply %s is not JSON: %v", body, err)
	}
	return out.Reason
}

// A pause ends the execution that read the step's buffer, so an unread steer leaves "not read" while
// the step's run turn stays open for the resume.
func TestStepSteer_APauseLeavesUnreadRowsNotRead(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-10"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}

	h.runs.RunNodePaused(t.Context(), "wf_1", reviewSegments)

	got := stepSteersIn(t, h)
	if len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary {
		t.Errorf("run log steers = %+v, want one not-read note", got)
	}
	if h.runs.log.turn("wf_1", reviewPath) == nil {
		t.Error("the paused step's run turn closed, want it open for the resume")
	}
}

// KAS ends a retry wait by re-sending node_start for the same session and path.
func TestStepSteer_ARetryRestartKeepsTheQueuedSteer(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-11"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}

	h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "")

	if got := stepSteersIn(t, h); len(got) != 0 {
		t.Errorf("run log steers after the restart = %+v, want none: the steer is still queued", got)
	}
	if replay := h.bus.steers.list(""); len(replay) != 1 {
		t.Errorf("connect replay after the restart = %d frames, want the queued row", len(replay))
	}
	if !h.runs.RunStepReading(marotte.StepSteerKey("sess_step")) {
		t.Error("RunStepReading after the restart = false, want the step still reading its buffer")
	}
}

// reviewSegments is the step these tests address; reviewPath is its workflow.PathKey.
var (
	reviewSegments = []string{"root", "review"}
	reviewPath     = workflow.PathKey(reviewSegments)
)

func reviewStep(session string) *translate.RunStep {
	return &translate.RunStep{RunID: "wf_1", NodePath: reviewPath, NodeID: "review", SessionID: session}
}

func runTurnsAt(t *testing.T, h *Runtime, nodePath string) (kinds [][]marotte.EntryKind, prompts []*marotte.EntryPrompt) {
	t.Helper()
	l, err := h.runs.log.log(t.Context(), "wf_1")
	if err != nil || l == nil {
		t.Fatalf("Setup: the run log is unreadable: %v", err)
	}
	all, err := l.All()
	if err != nil {
		t.Fatalf("Setup: reading the run log: %v", err)
	}
	at := map[string]int{}
	for i := range all {
		e := &all[i]
		if e.Kind == marotte.EntryKindTurnOpen {
			var to marotte.EntryTurnOpen
			if json.Unmarshal(e.Payload, &to) != nil || to.NodePath != nodePath {
				continue
			}
			at[e.Turn] = len(kinds)
			kinds = append(kinds, nil)
			prompts = append(prompts, to.Prompt)
		}
		if i, ok := at[e.Turn]; ok {
			kinds[i] = append(kinds[i], e.Kind)
		}
	}
	return kinds, prompts
}

// A parentless run's carrier closes when the run pauses, and the paused step's turn with it. The message
// rehosts the run and KAS opens a fresh turn for the step, which the typed words head as its prompt,
// whether its node_start lands while the send is in flight or after it returns.
func TestStepMessage_AResumeAfterTheTurnClosedHeadsTheNextTurnAsItsPrompt(t *testing.T) {
	for _, via := range []string{"answer_route", "message_route", "prompt_route"} {
		for _, timing := range []string{"node_start_during_send", "node_start_after_send"} {
			t.Run(via+"_"+timing, func(t *testing.T) {
				h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
				h.coord.runs = h.runs.log
				h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)
				if via != "prompt_route" {
					h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
						WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
					}})
				}
				reopen := func() { h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "") }
				if timing == "node_start_during_send" {
					br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
						if method == marotte.MethodPrompt {
							reopen()
						}
						return nil, nil, false
					}
				}

				var err error
				if via == "answer_route" {
					err = h.runs.answerInput(t.Context(), "wf_1", "a1", "teal")
				} else {
					_, err = h.runs.messageStep(t.Context(), "wf_1", reviewPath, "teal", "m-12")
				}
				if err != nil {
					t.Fatalf("%s = %v, want nil", via, err)
				}
				if timing == "node_start_after_send" {
					reopen()
				}
				turn := h.runs.log.turn("wf_1", reviewPath)
				if turn == nil {
					t.Fatal("Setup: no open turn after the step's node_start")
				}
				if _, err := turn.TextDelta(t.Context(), "", "say-1", "The summary will be teal."); err != nil {
					t.Fatalf("Setup: folding the step's reply: %v", err)
				}
				h.runs.RunNodeComplete(t.Context(), "wf_1", reviewSegments, "completed", "")

				kinds, prompts := runTurnsAt(t, h, reviewPath)
				wantKinds := []marotte.EntryKind{marotte.EntryKindTurnOpen, marotte.EntryKindText, marotte.EntryKindTurnClose}
				if len(kinds) != 2 || !slices.Equal(kinds[1], wantKinds) {
					t.Fatalf("root/review turns = %v, want the closed question turn then %v", kinds, wantKinds)
				}
				if prompts[0] != nil {
					t.Errorf("the question turn's prompt = %+v, want none", prompts[0])
				}
				if p := prompts[1]; p == nil || p.Text != "teal" || p.ID == "" {
					t.Errorf("the resumed turn's prompt = %+v, want the typed words with their id", p)
				}
				if got := stepSteersIn(t, h); len(got) != 0 {
					t.Errorf("run log steers = %+v, want none: the words head the turn they resumed", got)
				}
			})
		}
	}
}

// A send KAS refused resumed nothing, so the step's next turn carries no prompt the user's words never became.
func TestStepMessage_ARefusedResumeLeavesTheNextTurnWithoutAPrompt(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.coord.runs = h.runs.log
	h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)
	br.setCallErr(marotte.MethodPrompt, errors.New("bridge died"))

	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-13"); err == nil {
		t.Fatal("MessageStep with a failing send = nil, want the send's error")
	}
	h.runs.RunNodeStart(t.Context(), reviewStep("sess_step"), "")

	if _, prompts := runTurnsAt(t, h, reviewPath); len(prompts) != 2 || prompts[1] != nil {
		t.Errorf("root/review prompts = %+v, want a second turn with none", prompts)
	}
}

// A run that ends before KAS reopens the step still records the words that resumed it.
func TestStepMessage_ARunEndingBeforeTheResumedTurnOpensKeepsTheWords(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.coord.runs = h.runs.log
	h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-14"); err != nil {
		t.Fatalf("MessageStep = %v, want nil", err)
	}

	h.runs.closeRun(t.Context(), "wf_1", string(marotte.RunStatusCancelled))

	got := stepSteersIn(t, h)
	want := []marotte.EntrySteer{{Text: "carry on", Origin: marotte.SteerOriginUser, State: marotte.SteerStateRead}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("run log steers = %+v, want %+v after the step's last turn", got, want)
	}
}

func twoBranchHub(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	h, _, br := newTestHub()
	h.runs.log = newRunLog(t.TempDir())
	h.bridge.mgr.insert(runChatID("wf_1"), &sharedBridge{bridge: br, state: bridgeIdle})
	raw, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"status": marotte.RunStatusPaused,
			"root": map[string]any{
				"nodeId": "fanout", "type": "parallel", "status": "paused",
				"children": []any{
					map[string]any{"nodeId": "branch_a", "type": stepNodeType, "status": "paused", "sessionId": "sess_a"},
					map[string]any{"nodeId": "branch_b", "type": stepNodeType, "status": "paused", "sessionId": "sess_b"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Setup: marshalling the inspect reply: %s", err)
	}
	br.setCallResult(methodKiroWorkflowInspect, raw)
	return h, br
}

// The selected execution is the one addressed: an ask answers only at the step it resolves to, and a
// message to any other paused step resumes that step rather than answering the ask somewhere else.
func TestMessageStep_AnAskAnswersOnlyAtTheExecutionItBelongsTo(t *testing.T) {
	cases := []struct {
		name        string
		ask         marotte.RunInputNeededPayload
		path        string
		wantVerb    marotte.RunStepMessageVerb
		wantSession string
	}{
		{
			name: "an ask naming its session answers there",
			ask:  marotte.RunInputNeededPayload{StepSessionID: "sess_b"},
			path: workflow.PathKey([]string{"fanout", "branch_b"}), wantVerb: marotte.RunStepMessageAnswer, wantSession: "sess_b",
		},
		{
			name: "an ask naming another session is not answered here",
			ask:  marotte.RunInputNeededPayload{StepSessionID: "sess_b"},
			path: workflow.PathKey([]string{"fanout", "branch_a"}), wantVerb: marotte.RunStepMessagePrompt, wantSession: "sess_a",
		},
		{
			name: "an ask naming its node id answers there",
			ask:  marotte.RunInputNeededPayload{NodeID: "branch_b"},
			path: workflow.PathKey([]string{"fanout", "branch_b"}), wantVerb: marotte.RunStepMessageAnswer, wantSession: "sess_b",
		},
		{
			name: "an ask two parked steps could own answers at neither",
			ask:  marotte.RunInputNeededPayload{},
			path: workflow.PathKey([]string{"fanout", "branch_b"}), wantVerb: marotte.RunStepMessagePrompt, wantSession: "sess_b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, br := twoBranchHub(t)
			ask := tc.ask
			ask.WorkflowID, ask.AskID = "wf_1", "a1"
			h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: ask})

			out, err := h.runs.messageStep(t.Context(), "wf_1", tc.path, "the main branch", "m-15")
			if err != nil {
				t.Fatalf("MessageStep(%s) = %v, want nil", tc.path, err)
			}
			if out.Verb != tc.wantVerb {
				t.Errorf("MessageStep(%s) verb = %q, want %q", tc.path, out.Verb, tc.wantVerb)
			}
			if got := br.paramsFor(marotte.MethodPrompt)["sessionId"]; got != tc.wantSession {
				t.Errorf("session/prompt sessionId = %v, want %s", got, tc.wantSession)
			}
			stillOpen := len(h.runs.asks.snapshotRun("wf_1")) == 1
			if stillOpen != (tc.wantVerb != marotte.RunStepMessageAnswer) {
				t.Errorf("ask still open = %v, want it settled only by an answer", stillOpen)
			}
		})
	}
}

// An ask several parked executions could own is not guessed past by the answer route either.
func TestAnswerInput_AnAskSeveralParkedStepsCouldOwnIsNotSent(t *testing.T) {
	h, br := twoBranchHub(t)
	h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
		WorkflowID: "wf_1", AskID: "a1",
	}})

	if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); err == nil {
		t.Error("AnswerInput for an unattributable ask = nil, want a refusal")
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no session/prompt to a guessed step", br.callLog())
	}
	if !h.runs.asks.hasRun("wf_1") {
		t.Error("the ask was consumed, want it offered again")
	}
}

func unrecordableLog(t *testing.T) *runLog {
	t.Helper()
	root := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatalf("Setup: writing %s: %s", root, err)
	}
	return newRunLog(root)
}

// Words the run's record cannot hold are not sent: the transcript would never show them.
func TestMessageStep_AMessageTheRecordCannotHoldIsNotSent(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.runs.log = unrecordableLog(t)

	_, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "carry on", "m-22")

	rec := httptest.NewRecorder()
	h.runRoutes.writeStepErr(rec, "message", "wf_1", reviewPath, err)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("reply = %d %s, want 500", rec.Code, rec.Body.String())
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no session/prompt for unrecorded words", br.callLog())
	}
}

func TestAnswerInput_AnAnswerTheRecordCannotHoldIsNotSentAndTheAskStays(t *testing.T) {
	h, br := messageHub(t, marotte.RunStatusPaused, marotte.RunNodeStatusPaused)
	h.runs.log = unrecordableLog(t)
	h.runs.asks.add(&runAsk{chatID: runChatID("wf_1"), payload: marotte.RunInputNeededPayload{
		WorkflowID: "wf_1", AskID: "a1", NodeID: "review", StepSessionID: "sess_step",
	}})

	if err := h.runs.answerInput(t.Context(), "wf_1", "a1", "the main branch"); !errors.Is(err, errStepUnrecorded) {
		t.Errorf("AnswerInput = %v, want errStepUnrecorded", err)
	}
	if slices.Contains(br.callLog(), marotte.MethodPrompt) {
		t.Errorf("calls = %v, want no session/prompt for an unrecorded answer", br.callLog())
	}
	if !h.runs.asks.hasRun("wf_1") {
		t.Error("the ask was consumed, want it offered again")
	}
}

// A carrier's death ends the steering turns of the steps it hosted.
func TestStepSteer_ACarrierDeathLeavesUnreadRowsNotRead(t *testing.T) {
	h, _ := messageHub(t, marotte.RunStatusRunning, marotte.RunNodeStatusRunning)
	if _, err := h.runs.messageStep(t.Context(), "wf_1", reviewPath, "check the logs", "m-11"); err != nil {
		t.Fatalf("Setup: MessageStep = %v", err)
	}

	h.coord.runs = h.runs.log
	h.coord.closeHostedRuns(t.Context(), runChatID("wf_1"), marotte.StopReasonInterrupted)

	got := stepSteersIn(t, h)
	if len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary {
		t.Errorf("run log steers = %+v, want one not-read note filed before the turn closed", got)
	}
}
