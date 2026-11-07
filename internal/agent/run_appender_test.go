package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// runLoggingHub is a runtime whose Runs has a run log, set directly rather than through the order-sensitive wiring.
func runLoggingHub(t *testing.T) (*Runtime, *testChatStore, *fakeBridge) {
	t.Helper()
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	h.runs.log = newRunLog(t.TempDir())
	return h, cs, br
}

func refuseStep(t *testing.T, rs *Runs, runID string, path []string, r *marotte.RefusalInfo) {
	t.Helper()
	nodePath := workflow.PathKey(path)
	rs.RunNodeStart(t.Context(), &translate.RunStep{RunID: runID, NodePath: nodePath, SessionID: "step-session-1"}, "")
	turn := rs.log.turn(runID, nodePath)
	if turn == nil {
		t.Fatalf("no open turn for %s/%s", runID, nodePath)
	}
	if r != nil {
		turn.SetRefusal(r)
	}
	if !rs.log.stopReason(runID, nodePath, marotte.StopReasonRefusal) {
		t.Fatalf("StopReason refused on the open turn %s/%s", runID, nodePath)
	}
}

func chatSteers(t *testing.T, cs *testChatStore, chatID marotte.ChatID) []marotte.EntrySteer {
	t.Helper()
	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var out []marotte.EntrySteer
	for _, e := range entries {
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		var s marotte.EntrySteer
		if err := json.Unmarshal(e.Payload, &s); err != nil {
			t.Fatalf("decode the steer %s: %v", e.ID, err)
		}
		out = append(out, s)
	}
	return out
}

// inspectWithLoopStep is an inspect reply whose tree holds one step inside iteration n of a repeat under the run's
// root sequence, at path [workflowID, repeatID, iter-n, nodeID].
func inspectWithLoopStep(t *testing.T, workflowID, repeatID string, n int, nodeID, status string) json.RawMessage {
	t.Helper()
	step := map[string]any{"nodeId": nodeID, "type": "step", "status": status, "sessionId": "step-session-1"}
	iter := map[string]any{
		"nodeId": repeatID + "#" + strconv.Itoa(n), "type": "sequence", "status": status, "iteration": n,
		"children": []any{step},
	}
	repeat := map[string]any{"nodeId": repeatID, "type": "repeat", "status": "running", "children": []any{iter}}
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": "running",
			"root": map[string]any{"nodeId": workflowID, "type": "sequence", "status": "running", "children": []any{repeat}},
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

// TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat pins that KAS grades a refused step `completed`, and that the
// note follows the grade recorded under KAS's record, never the seal alone.
func TestRunNodeComplete_ARefusedStepTellsTheLaunchingChat(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{"wf_1", "loop", "iter-1", "build"}
	h, cs, br := runLoggingHub(t)
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{
		Category:    "policy",
		Explanation: "I will not continue with this request.",
	})

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
		t.Fatalf("an unreadable record left %d notes, want none: %+v", len(steers), steers)
	}
	br.setCallResult(methodKiroWorkflowInspect, inspectWithLoopStep(t, id, "loop", 1, "build", "completed"))
	stepEndsOf(t, h, id)
	joinInflight(t, h)

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	if want := stepNoteID(id, stepNoteRefused, workflow.PathKey(path)); entries[i].ID != want {
		t.Errorf("note id = %q, want %q (the step's own path, so each refused step gets its own row)",
			entries[i].ID, want)
	}
	var steer marotte.EntrySteer
	if err := json.Unmarshal(entries[i].Payload, &steer); err != nil {
		t.Fatalf("decode the steer: %v", err)
	}
	if steer.Origin != marotte.SteerOriginAgent || steer.State != marotte.SteerStateRead {
		t.Errorf("note origin/state = %q/%q, want agent/read", steer.Origin, steer.State)
	}
	if steer.Severity != "warning" {
		t.Errorf("note severity = %q, want warning", steer.Severity)
	}
	if steer.OriginRun != id {
		t.Errorf("note provenance = run %q, want %q", steer.OriginRun, id)
	}
	// The note names the step by its workflow.PathKey.
	for _, want := range []string{"nightly", "step wf_1:loop:iter-1:build was declined", "policy", "I will not continue with this request."} {
		if !strings.Contains(steer.Text, want) {
			t.Errorf("note text = %q, want it to carry %q", steer.Text, want)
		}
	}
	// A deterministic refusal must not read "try again".
	if !strings.Contains(steer.Text, "declined again") {
		t.Errorf("note text = %q, want it to say a re-run is declined again", steer.Text)
	}
}

// KAS grades a step its iteration limit stopped `completed` with no signal, so only this note tells the launching chat
// it did not finish.
func TestRunNodeComplete_AStepStoppedAtTheModelCallLimitTellsTheLaunchingChat(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{"wf_1", "build"}
	nodePath := workflow.PathKey(path)
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
	if !h.runs.log.stopReason(id, nodePath, marotte.StopReasonToolUse) {
		t.Fatal("StopReason(tool_use) refused on the open turn")
	}

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	entries, err := cs.All(t.Context(), chatID)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	i := slices.IndexFunc(entries, func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindSteer })
	if i < 0 {
		t.Fatalf("no steer entry reached the launching chat; entries: %+v", entries)
	}
	if want := stepNoteID(id, stepNoteModelCallLimit, nodePath); entries[i].ID != want {
		t.Errorf("note id = %q, want %q", entries[i].ID, want)
	}
	var steer marotte.EntrySteer
	if err := json.Unmarshal(entries[i].Payload, &steer); err != nil {
		t.Fatalf("decode the steer: %v", err)
	}
	if want := "nightly step wf_1:build stopped early. " + marotte.ModelCallLimitStepReason; steer.Text != want {
		t.Errorf("note text = %q, want %q", steer.Text, want)
	}
	if steer.Severity != "warning" || steer.OriginRun != id {
		t.Errorf("note severity/run = %q/%q, want warning/%s", steer.Severity, steer.OriginRun, id)
	}
}

// Every refusal field is optional, so absence is not evidence the model did not decline.
func TestRunNodeComplete_ARefusalWithNoMetadataStillLeavesANote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
	refuseStep(t, h.runs, id, path, nil)

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	steers := chatSteers(t, cs, chatID)
	if len(steers) != 1 {
		t.Fatalf("got %d notes, want one", len(steers))
	}
	if !strings.Contains(steers[0].Text, "build was declined by the model.") {
		t.Errorf("note text = %q, want a sentence that stands with no category", steers[0].Text)
	}
}

// The run's own frames already say it finished.
func TestRunNodeComplete_AStepThatRanLeavesNoNote(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	for _, tc := range []struct {
		desc   string
		raw    marotte.StopReason
		status string
	}{
		{"a completed step", marotte.StopReasonEndTurn, "completed"},
		{"a failed step", marotte.StopReasonError, "failed"},
		{"a cancelled step", marotte.StopReasonCancelled, "cancelled"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			h, cs, br := runLoggingHub(t)
			seedChat(t, cs, chatID)
			h.runs.grantLease(t.Context(), id, "nightly",
				launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
			step := &translate.RunStep{RunID: id, NodePath: "build", SessionID: "step-session-1"}
			h.runs.RunNodeStart(t.Context(), step, "")
			answerStep(t, h.runs.log, step)
			if !h.runs.log.stopReason(id, "build", tc.raw) {
				t.Fatalf("StopReason(%q) refused on the open turn", tc.raw)
			}
			br.forgetCalls()

			h.runs.RunNodeComplete(t.Context(), id, []string{"build"}, tc.status, "")
			joinInflight(t, h)

			if steers := chatSteers(t, cs, chatID); len(steers) != 0 {
				t.Errorf("%s left %d notes, want none: %+v", tc.desc, len(steers), steers)
			}
			// No grade of this close can call for a note, so the run's state is not read for one.
			if calls := br.callLog(); slices.Contains(calls, methodKiroWorkflowInspect) {
				t.Errorf("%s read the run's state for a note it cannot leave: calls %v", tc.desc, calls)
			}
		})
	}
}

// A parentless lease carries no chat. The store would refuse the append anyway, so the logged
// failure is what proves nothing was attempted. No t.Parallel: captureLogs swaps the slog default.
func TestRunNodeComplete_AParentlessRunLeavesNoNote(t *testing.T) {
	const id = "wf_1"
	path := []string{id, "build"}
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
	leased(t, h.runs, id)
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})
	logs := captureLogs(t)

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	// The grade is recorded, so only the note was withheld.
	if got := gradesOf(t, h, id); len(got) != 1 {
		t.Errorf("step_graded entries = %+v, want one", got)
	}

	if strings.Contains(logs.String(), "a steer was not recorded") {
		t.Errorf("a parentless run's refusal tried to write a note: %s", logs.String())
	}
	if chats := cs.List(t.Context()); len(chats) != 0 {
		t.Errorf("a parentless run's refusal created a chat: %+v", chats)
	}
}

// CloseNode closes a turn once, so a repeated node_complete seals no second note.
func TestRunNodeComplete_ARefusalIsNotedOnce(t *testing.T) {
	const (
		id     = "wf_1"
		chatID = marotte.ChatID("c1")
	)
	path := []string{id, "build"}
	h, cs, br := runLoggingHub(t)
	br.setCallResult(methodKiroWorkflowInspect, inspectWithStep(t, id, "build", "completed", ""))
	seedChat(t, cs, chatID)
	h.runs.grantLease(t.Context(), id, "nightly",
		launchOrigin{origin: runlease.OriginAgent, chatID: string(chatID)}, hostSec())
	refuseStep(t, h.runs, id, path, &marotte.RefusalInfo{Category: "policy"})

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)

	if steers := chatSteers(t, cs, chatID); len(steers) != 1 {
		t.Errorf("got %d notes for one refusal, want one", len(steers))
	}
}

// An included step's id carries its include's id and a '/', so a '/' join of its path reads
// wf_1/inc/inc/a, the same text as a four-deep path; its key wf_1:inc:inc/a is not wf_1:inc:inc:a.
func TestStepNoteText_KeepsAnIncludedStepsPathUnambiguous(t *testing.T) {
	path := []string{"wf_1", "inc", "inc/a"}
	if got, want := stepModelCallLimitNoteText("nightly", workflow.PathKey(path)), "nightly step wf_1:inc:inc/a stopped early. "+marotte.ModelCallLimitStepReason; got != want {
		t.Errorf("model-call-limit note = %q, want %q", got, want)
	}
	if got := stepRefusalNoteText("nightly", workflow.PathKey(path), nil); !strings.HasPrefix(got, "nightly step wf_1:inc:inc/a was declined by the model.") {
		t.Errorf("refusal note = %q, want it to name the step's path", got)
	}
}

func TestRunOpenTurn_AnswersOnlyAnOpenTurnAndOpensNone(t *testing.T) {
	const id = "wf_1"
	path := []string{"wf_1", "build"}
	nodePath := workflow.PathKey(path)
	h, _, _ := runLoggingHub(t)

	if _, ok := h.runs.RunOpenTurn(id, nodePath); ok {
		t.Fatal("RunOpenTurn answered a turn before the step started")
	}
	if h.runs.log.turn(id, nodePath) != nil {
		t.Fatal("RunOpenTurn opened a turn")
	}

	h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: id, NodePath: nodePath, SessionID: "step-session-1"}, "")
	got, ok := h.runs.RunOpenTurn(id, nodePath)
	if !ok || got != h.runs.log.turn(id, nodePath) {
		t.Errorf("RunOpenTurn = %p, %v, want the step's open turn", got, ok)
	}

	h.runs.RunNodeComplete(t.Context(), id, path, "completed", "")
	joinInflight(t, h)
	if _, ok := h.runs.RunOpenTurn(id, nodePath); ok {
		t.Error("RunOpenTurn answered the step's closed turn")
	}
}

// wakeRig is a chat-parented run whose launching chat's session is open, so a note to the launching agent reaches
// the fake bridge, which records each `_session/steer` and each run_outcome notification the run's chat gets.
type wakeRig struct {
	h      *Runtime
	br     *fakeBridge
	push   *runOutcomePush
	steers []map[string]any
	mu     sync.Mutex
}

const (
	wakeRunID = "wf_4712dbcf39b116b2"
	// wakeSession is the step session inspectWithStep's record names.
	wakeSession = "step-session-1"
)

// newWakeRig leases run id to the open chat, with inspect as KAS's record of its steps.
func newWakeRig(t *testing.T, id string, inspect json.RawMessage) *wakeRig {
	t.Helper()
	push := newRunOutcomePush()
	h, cs, br := newTestHub(WithPush(push))
	t.Cleanup(func() { shutdownHub(t, h) })
	g := &wakeRig{h: h, br: br, push: push}
	br.sessionID = parkSession
	h.insertBridgeNow(parkChat, testBridge(br, bridgeIdle))
	h.runs.useLog(newRunLog(t.TempDir()))
	h.coord.runs = h.runs.log
	if _, err := cs.Mutate(t.Context(), parkChat, func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		c.RecordSession(parkSession)
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the chat: %v", err)
	}
	h.runs.grantLease(t.Context(), id, "review", agentLaunch(), hostSec())
	h.runs.recordParentSession(t.Context(), id, parkSession)
	br.setCallResult(methodKiroWorkflowInspect, inspect)
	br.setOnCall(func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
		if method != marotte.MethodSessionSteer {
			return nil, nil, false
		}
		g.mu.Lock()
		g.steers = append(g.steers, params)
		g.mu.Unlock()
		return json.RawMessage(`{"queued":true}`), nil, true
	})
	return g
}

// wakes are the warning notifications marotte steered into the launching agent's session.
func (g *wakeRig) wakes() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, p := range g.steers {
		if msg, _ := p["message"].(string); strings.HasPrefix(msg, "[notification/warning] ") && fmt.Sprint(p[marotte.KeySessionID]) == parkSession {
			out = append(out, msg)
		}
	}
	return out
}

// wakeStepPath is the review seat of wf_4712dbcf39b116b2 that ended on an empty turn (2026-11-06T20:54:22Z).
var wakeStepPath = []string{wakeRunID, "seat-grok"}

// kasStandIn is KAS's ensureAlternatingMessages placeholder, which grok-4.7 echoed as its whole answer.
const kasStandIn = "(No assistant response was recorded for the preceding turn. This placeholder was inserted automatically to preserve message alternation.)"

// endStep drives the seat's one turn as the frames recorded it: text, a tool call to its result, then after, then
// the turn_end and KAS's node_complete.
func (g *wakeRig) endStep(t *testing.T, before, after string, stop marotte.StopReason, status string) {
	t.Helper()
	nodePath := workflow.PathKey(wakeStepPath)
	step := &translate.RunStep{RunID: wakeRunID, NodePath: nodePath, SessionID: wakeSession}
	g.h.runs.RunNodeStart(t.Context(), step, parkChat)
	turn, ok := g.h.runs.RunFoldTarget(t.Context(), step, parkChat)
	if !ok {
		t.Fatal("Setup: the step has no open turn")
	}
	if before != "" {
		if _, err := turn.TextDelta(t.Context(), "", "say-1", before); err != nil {
			t.Fatalf("Setup: text: %v", err)
		}
	}
	if _, err := turn.ToolCall(t.Context(), "", &marotte.EntryToolCall{ID: "call-1", Title: "rg", Kind: marotte.ToolKindOther}); err != nil {
		t.Fatalf("Setup: tool call: %v", err)
	}
	if _, err := turn.ToolResult(t.Context(), "", "call-1", &marotte.EntryToolResult{Status: marotte.ToolCompleted}); err != nil {
		t.Fatalf("Setup: tool result: %v", err)
	}
	if after != "" {
		if _, err := turn.TextDelta(t.Context(), "", "say-2", after); err != nil {
			t.Fatalf("Setup: text: %v", err)
		}
	}
	if !g.h.runs.RunStopReason(wakeRunID, nodePath, stop) {
		t.Fatal("Setup: RunStopReason refused on the open turn")
	}
	g.h.runs.RunNodeComplete(t.Context(), wakeRunID, wakeStepPath, status, "")
	joinInflight(t, g.h)
}

// TestRunNodeComplete_AnEmptyAnswerWakesTheLaunchingAgent replays the review seats that ended on an empty turn on
// 2026-11-06 to 07: KAS completed each with no signal and the next gate stalled. marotte fails the step, wakes the
// launching agent with the retry it can call, and resumes or re-runs nothing itself.
func TestRunNodeComplete_AnEmptyAnswerWakesTheLaunchingAgent(t *testing.T) {
	nodePath := workflow.PathKey(wakeStepPath)
	for _, tc := range []struct {
		desc, before, after string
		failed              bool
	}{
		{desc: "placeholder text after a tool result", before: "Reading the diff.", after: kasStandIn, failed: true},
		{desc: "blank text after a tool result", before: "Reading the diff.", after: " \n", failed: true},
		{desc: "placeholder mid-turn then tools then a real answer", before: kasStandIn, after: "Two findings, written to the review file."},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			g := newWakeRig(t, wakeRunID, inspectWithStep(t, wakeRunID, "seat-grok", "completed", ""))
			g.endStep(t, tc.before, tc.after, marotte.StopReasonEndTurn, "completed")
			end := stepEndsOf(t, g.h, wakeRunID)[nodePath]
			waitUntil(t, "the launching agent's wake", func() bool { return !tc.failed || len(g.wakes()) > 0 })
			joinInflight(t, g.h)
			if !tc.failed {
				if end.FailureKind == marotte.FailureKindEmptyAnswer {
					t.Errorf("step_ends[%s] = %+v, want no empty answer", nodePath, end)
				}
				if w := g.wakes(); len(w) != 0 {
					t.Errorf("the launching agent got %d wakes, want none: %q", len(w), w)
				}
				if p := runOutcomePushes(g.push); len(p) != 0 {
					t.Errorf("the run's chat got notifications %+v, want none", p)
				}
				return
			}
			want := marotte.RunStepEnd{
				Outcome: marotte.TurnOutcomeFailed, FailureKind: marotte.FailureKindEmptyAnswer,
				FailureReason: marotte.EmptyAnswerStepReason,
			}
			if end != want {
				t.Errorf("step_ends[%s] = %+v, want %+v", nodePath, end, want)
			}
			if !strings.Contains(marotte.EmptyAnswerStepReason, "ended with an empty model turn and no completion signal") {
				t.Errorf("the failure reason %q does not say the step ended with an empty model turn and no completion signal",
					marotte.EmptyAnswerStepReason)
			}
			wakes := g.wakes()
			if len(wakes) != 1 {
				t.Fatalf("the launching agent got %d wakes, want one: %q", len(wakes), wakes)
			}
			for _, part := range []string{wakeRunID, nodePath, wakeSession, "update_workflow", "replace_remaining"} {
				if !strings.Contains(wakes[0], part) {
					t.Errorf("the wake %q does not name %q", wakes[0], part)
				}
			}
			pushes := runOutcomePushes(g.push)
			if len(pushes) != 1 || pushes[0].subject != marotte.RunSubject(wakeRunID) ||
				!strings.Contains(pushes[0].body, "empty model turn") {
				t.Errorf("the run's chat got notifications %+v, want one run_outcome naming the empty model turn", pushes)
			}
			if got := stateWrites(g.br.callLog()); len(got) != 0 {
				t.Errorf("writes = %v, want no resume, pause or answer of the run", got)
			}
		})
	}
}

// KAS refuses update_workflow replace_remaining on a finished run (KAS 3.1.0 classifyMutationBoundary), so a step
// graded at the run's run_complete is offered a new run instead.
func TestRunNodeComplete_AnEmptyAnswerGradedAtTheRunsEndOffersANewRun(t *testing.T) {
	g := newWakeRig(t, wakeRunID, inspectWithStep(t, wakeRunID, "seat-grok", "completed", ""))
	g.br.setCallErr(methodKiroWorkflowInspect, errors.New("the engine did not answer"))
	g.endStep(t, "Reading the diff.", kasStandIn, marotte.StopReasonEndTurn, "completed")
	if w := g.wakes(); len(w) != 0 {
		t.Fatalf("Setup: the step was graded before the run ended: %q", w)
	}
	g.br.clearCallErr(methodKiroWorkflowInspect)
	g.h.runs.gradeRun(t.Context(), wakeRunID, gradeAtRunEnd)
	waitUntil(t, "the launching agent's wake", func() bool { return len(g.wakes()) > 0 })
	joinInflight(t, g.h)
	wakes := g.wakes()
	if len(wakes) != 1 {
		t.Fatalf("the launching agent got %d wakes, want one: %q", len(wakes), wakes)
	}
	if strings.Contains(wakes[0], "replace_remaining") {
		t.Errorf("the wake %q offers replace_remaining, which KAS refuses on a finished run", wakes[0])
	}
	if !strings.Contains(wakes[0], "run_workflow") {
		t.Errorf("the wake %q does not offer a new run_workflow", wakes[0])
	}
}

// A step that ended by its own signal is graded by it, so no empty-answer wake follows however blank its last
// answer.
func TestRunNodeComplete_AStepThatSignalledIsNotAnEmptyAnswer(t *testing.T) {
	nodePath := workflow.PathKey(wakeStepPath)
	for _, tc := range []struct {
		desc, signal, status string
		stop                 marotte.StopReason
	}{
		{desc: "a success signal with a blank last answer", signal: "success", status: "completed", stop: marotte.StopReasonEndTurn},
		{desc: "an error signal KAS fails the step on", signal: "error", status: "failed", stop: marotte.StopReasonEndTurn},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			g := newWakeRig(t, wakeRunID, inspectWithStep(t, wakeRunID, "seat-grok", tc.status, tc.signal))
			g.endStep(t, "Reading the diff.", "", tc.stop, tc.status)
			end := stepEndsOf(t, g.h, wakeRunID)[nodePath]
			joinInflight(t, g.h)
			if end.FailureKind == marotte.FailureKindEmptyAnswer {
				t.Errorf("step_ends[%s] = %+v, want no empty answer", nodePath, end)
			}
			if w := g.wakes(); len(w) != 0 {
				t.Errorf("the launching agent got %d wakes, want none: %q", len(w), w)
			}
			for _, p := range runOutcomePushes(g.push) {
				if strings.Contains(p.body, "empty model turn") {
					t.Errorf("the run's chat got %+v, want no empty-turn notification", p)
				}
			}
		})
	}
	t.Run("a warning answered, then a success signal", func(t *testing.T) {
		g := newWakeRig(t, wakeRunID, inspectWithStep(t, wakeRunID, "seat-grok", "completed", "success"))
		s := &stepEnding{t: t, h: g.h, id: wakeRunID, path: wakeStepPath, nodePath: nodePath, session: wakeSession}
		g.h.runs.RunNodeStart(t.Context(), &translate.RunStep{RunID: wakeRunID, NodePath: nodePath, SessionID: wakeSession}, parkChat)
		s.turnStart()
		s.call("send_message")
		s.turnEnd()
		s.paused()
		s.turnStart()
		s.call("rg")
		s.call("send_message")
		s.turnEnd()
		s.complete("completed")
		joinInflight(t, g.h)
		if end := stepEndsOf(t, g.h, wakeRunID)[nodePath]; end.FailureKind == marotte.FailureKindEmptyAnswer {
			t.Errorf("step_ends[%s] = %+v, want no empty answer", nodePath, end)
		}
		joinInflight(t, g.h)
		if w := g.wakes(); len(w) != 0 {
			t.Errorf("the launching agent got %d wakes, want none: %q", len(w), w)
		}
	})
}
