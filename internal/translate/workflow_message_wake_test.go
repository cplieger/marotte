package translate

import (
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// chatLogRows is the chat's workflow-message rows in file order across its turns.
func chatLogRows(t *testing.T, deps *baseDeps) []steerRow {
	t.Helper()
	var out []steerRow
	for _, e := range deps.turns.chatLog[testChat] {
		if e.Kind == marotte.EntryKindSteer && strings.HasPrefix(e.ID, marotte.WorkflowMessageIDPrefix) {
			out = append(out, steerRow{ID: e.ID, EntrySteer: decodePayload[marotte.EntrySteer](t, &e)})
		}
	}
	return out
}

// chatLogStamps is the chat's settlements across its turns, keyed by the row each settles.
func chatLogStamps(t *testing.T, deps *baseDeps) map[string][]marotte.EntrySteerDelivered {
	t.Helper()
	out := map[string][]marotte.EntrySteerDelivered{}
	for _, d := range deliveredOf(t, deps.turns.chatLog[testChat]) {
		out[d.SteerID] = append(out[d.SteerID], d)
	}
	return out
}

// deliveredOnce reports one settlement, delivered.
func deliveredOnce(d []marotte.EntrySteerDelivered) bool {
	return len(d) == 1 && !d[0].Cleared && d[0].ReadTs != 0
}

// testStepAttr is the attribution of the test step's frames.
func testStepAttr() FrameAttribution {
	return FrameAttribution{Step: true, SessionID: testStep, RunID: testWfRun, NodePath: workflow.PathKey([]string{testWfRun, "review"})}
}

// turnStartOn is KAS's turn_start on attr's session.
func turnStartOn(t *testing.T, tr *Translator, attr FrameAttribution) {
	t.Helper()
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "turn_start", map[string]any{"turnStart": true}), attr)
}

// wakeChunk is a content frame of a turn KAS started for reason on attr's session.
func wakeChunk(t *testing.T, tr *Translator, reason string, attr FrameAttribution) {
	t.Helper()
	tr.HandleAssistantChunk(t.Context(), testChat, mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "Picking it up."},
		"_meta":   map[string]any{"kiro": map[string]any{"agentInitiated": true, "agentInitiatedReason": reason}},
	}), false, attr)
}

// pauseStep is KAS's node_paused for the test step, here its pause awaiting a reply; extra rides the payload.
func pauseStep(t *testing.T, tr *Translator, extra map[string]any) {
	t.Helper()
	p := map[string]any{
		"workflowId": testWfRun, "nodeId": "review", "nodePath": []string{testWfRun, "review"},
		"reason": "Awaiting next user message on step session.",
	}
	maps.Copy(p, extra)
	tr.RunProgressHandler(marotte.RunProgressNodePaused)(t.Context(), testChat, notif("_kiro/workflow/node_paused", p))
}

// resumeStep is what a step's resume sends marotte, a parent's wake included: node_start again, then a turn_start.
// No frame names the reason (kiro-cli 2.37.1 driveExecution persists it on the user row only).
func resumeStep(t *testing.T, tr *Translator) {
	t.Helper()
	announceStep(t, tr)
	turnStartOn(t, tr, testStepAttr())
}

// chatWake is the chat's engine turn for reason: its turn_start, then a content frame.
func chatWake(t *testing.T, tr *Translator, reason string) {
	t.Helper()
	turnStartOn(t, tr, FrameAttribution{})
	wakeChunk(t, tr, reason, FrameAttribution{})
}

// A message sent to the idle chat is the one row filed before KAS's wake turn started, so that turn's first frame
// delivers it.
func TestWorkflowMessage_AWakeDeliversTheOneMessageFiledBeforeItsTurn(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "warning", "the waking report")

	chatWake(t, tr, "send-message-wake")

	rows := chatLogRows(t, deps)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if d := chatLogStamps(t, deps)[rows[0].ID]; !deliveredOnce(d) {
		t.Errorf("stamps = %+v, want one delivered at the wake", d)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, testStepAttr().NodePath)); !deliveredOnce(d) {
		t.Errorf("step stamps = %+v, want the sender's copy delivered", d)
	}
}

// mispredictedPair files A while marotte's turn is still open (KAS had ended its turn and wakes the chat with A),
// then B once that turn closed (KAS buffers it behind A's pending wake), then KAS's wake turn opens.
func mispredictedPair(t *testing.T, deps *baseDeps, tr *Translator) (a, b steerRow) {
	t.Helper()
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "warning", "report A")
	closeChatTurn(t, deps, testChat)
	stepReport(t, tr, "warning", "report B")
	rows := chatLogRows(t, deps)
	if len(rows) != 2 || rows[0].ReadTs != 0 || rows[1].ReadTs != 0 {
		t.Fatalf("Setup: rows = %+v, want A and B waiting", rows)
	}
	chatWake(t, tr, "send-message-wake")
	return rows[0], rows[1]
}

// A wake turn with two rows that could have started it delivers neither, and the boundary naming only B then reads
// both cleared: nothing reads delivered or dropped on marotte's guess at KAS's wake-or-buffer choice.
func TestWorkflowMessage_AWakeWithTwoCandidatesDeliversNeitherAndTheClearClearsBoth(t *testing.T) {
	deps, tr := messageDeps(t)
	a, b := mispredictedPair(t, deps, tr)
	if s := chatLogStamps(t, deps); len(s) != 0 {
		t.Fatalf("stamps at the wake = %+v, want none: either row could have woken the chat", s)
	}

	clearSteering(t, tr, "notify-b")

	stamps := chatLogStamps(t, deps)
	for _, r := range []steerRow{a, b} {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("row %q stamps = %+v, want one cleared", r.Text, d)
		}
	}
}

// A take-up proves B was buffered, which leaves A the one row that can have started the wake turn: A reads delivered.
func TestWorkflowMessage_ATakeUpLeavesTheWakeOneCandidateAndDeliversIt(t *testing.T) {
	deps, tr := messageDeps(t)
	a, b := mispredictedPair(t, deps, tr)

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-b", "content": "[notification/warning] report B", "notificationSeverity": "warning",
	}), FrameAttribution{})

	stamps := chatLogStamps(t, deps)
	if d := stamps[b.ID]; !deliveredOnce(d) || d[0].KASID != "notify-b" {
		t.Errorf("row B stamps = %+v, want its take-up", d)
	}
	if d := stamps[a.ID]; !deliveredOnce(d) {
		t.Errorf("row A stamps = %+v, want one delivered: it alone can have woken the chat", d)
	}
}

// A wake turn settles one message, once: never a step's success (KAS always buffers it), a row filed inside the wake
// turn (buffered behind it), or a second row on a later frame.
func TestWorkflowMessage_AWakeSettlesOnlyAMessageThatCanHaveStartedItOnce(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "success", "a success")
	stepReport(t, tr, "warning", "the waking report")
	turnStartOn(t, tr, FrameAttribution{})
	stepReport(t, tr, "warning", "a report inside the wake")
	rows := chatLogRows(t, deps)
	if len(rows) != 3 {
		t.Fatalf("Setup: rows = %+v, want three", rows)
	}

	wakeChunk(t, tr, "send-message-wake", FrameAttribution{})
	wakeChunk(t, tr, "send-message-wake", FrameAttribution{})

	stamps := chatLogStamps(t, deps)
	if d := stamps[rows[1].ID]; !deliveredOnce(d) {
		t.Errorf("waking report stamps = %+v, want one delivered", d)
	}
	for _, r := range []steerRow{rows[0], rows[2]} {
		if d, ok := stamps[r.ID]; ok {
			t.Errorf("row %q stamped %+v, want untouched", r.Text, d)
		}
	}
}

// Only a send-message-wake names a message as its cause: a run's completion wake, KAS's re-prompt after a cancel and a
// reason this build does not know settle nothing.
func TestWorkflowMessage_AnEngineTurnOfAnotherReasonSettlesNoMessage(t *testing.T) {
	for _, reason := range []string{"workflow-complete-wake", "notification-rewake", "some-later-wake"} {
		t.Run(reason, func(t *testing.T) {
			deps, tr := messageDeps(t)
			stepReport(t, tr, "warning", "a report")

			chatWake(t, tr, reason)

			if d := deliveredOf(t, deps.turns.chatLog[testChat]); len(d) != 0 {
				t.Errorf("stamps = %+v, want none", d)
			}
		})
	}
}

// A message filed before marotte saw any turn start, a restart's included, is no wake's candidate: the wake that
// follows names no row marotte saw it start after.
func TestWorkflowMessage_AWakeWithNoTurnStartSettlesNoMessage(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "warning", "a report")

	wakeChunk(t, tr, "send-message-wake", FrameAttribution{})

	if d := deliveredOf(t, deps.turns.chatLog[testChat]); len(d) != 0 {
		t.Errorf("stamps = %+v, want none", d)
	}
}

// After a reader's cancel cleared a message, KAS can re-deliver it by a send-message-wake with no new notify
// (rewakeWithInfo). That turn delivers no later row: Z, filed in between, is settled by its own take-up, once.
func TestWorkflowMessage_ASendMessageWakeAfterAReadersCancelSettlesNoLaterMessage(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "info", "report R")
	stopTurn(deps, testChat)
	clearSteering(t, tr, "notify-r")
	resumeAfterStop(deps)
	stepReport(t, tr, "info", "report Z")
	closeChatTurn(t, deps, testChat)

	chatWake(t, tr, "send-message-wake")
	z := chatLogRows(t, deps)[1]
	if d, ok := chatLogStamps(t, deps)[z.ID]; ok {
		t.Fatalf("Z stamped %+v at the re-wake, want it waiting: no notify started that turn", d)
	}
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-z", "content": "[notification/info] report Z", "notificationSeverity": "info",
	}), FrameAttribution{})

	rows := chatLogRows(t, deps)
	if len(rows) != 2 {
		t.Errorf("rows = %+v, want R and Z only: Z's take-up settles its own row", rows)
	}
	if d := chatLogStamps(t, deps)[z.ID]; !deliveredOnce(d) || d[0].KASID != "notify-z" {
		t.Errorf("Z stamps = %+v, want its take-up alone", d)
	}
}

// A step woken by the parent's answer reads it delivered at the step's wake turn; a later message KAS buffered for the
// busy step and emptied at the step's boundary reads cleared, and nothing stays waiting.
func TestWorkflowMessage_AStepsBoundaryAfterAWokenAnswerClearsTheBufferedMessage(t *testing.T) {
	deps, tr := messageDeps(t)
	attr := testStepAttr()
	pauseStep(t, tr, nil)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "the answer to your ask"))
	resumeStep(t, tr)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "also check the tests"))
	step := stepRows(t, deps)
	if len(step) != 2 {
		t.Fatalf("Setup: step rows = %+v, want two", step)
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-x"},
	}), attr)

	stamps := map[string][]marotte.EntrySteerDelivered{}
	for _, d := range deliveredOf(t, deps.runEntries(testWfRun, attr.NodePath)) {
		stamps[d.SteerID] = append(stamps[d.SteerID], d)
	}
	if d := stamps[step[0].ID]; !deliveredOnce(d) {
		t.Errorf("answer stamps = %+v, want one delivered at the step's wake", d)
	}
	if d := stamps[step[1].ID]; !clearedOnce(d) {
		t.Errorf("buffered message stamps = %+v, want one cleared", d)
	}
}

// KAS buffers a message the chat looked idle for (behind a pending wake prompt, or on the info wake's busy fallback);
// the reader then stops the turn that holds it. The stop's clear settles it cleared, never delivered at its send.
func TestWorkflowMessage_ACancelClearsAMessageKASBufferedWhileTheChatLookedIdle(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "info", "report B")
	deps.turns.chatTurn(testChat)
	stopTurn(deps, testChat)

	clearSteering(t, tr, "notify-b")

	rows := chatLogRows(t, deps)
	if len(rows) != 1 || rows[0].ReadTs != 0 {
		t.Fatalf("rows = %+v, want B with no read time of its own", rows)
	}
	if d := chatLogStamps(t, deps)[rows[0].ID]; !clearedOnce(d) {
		t.Errorf("B stamps = %+v, want one cleared at the stop", d)
	}
}

// parentToStep files a parent's message to the test step.
func parentToStep(t *testing.T, tr *Translator, text string) {
	t.Helper()
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", text))
}

// stepStamps is the step log's settlements, keyed by the row each settles.
func stepStamps(t *testing.T, deps *baseDeps) map[string][]marotte.EntrySteerDelivered {
	t.Helper()
	out := map[string][]marotte.EntrySteerDelivered{}
	for _, d := range deliveredOf(t, deps.runEntries(testWfRun, testStepAttr().NodePath)) {
		out[d.SteerID] = append(out[d.SteerID], d)
	}
	return out
}

// KAS buffers a parent's message to a running step and wakes a paused one with it, so a resume delivers only the
// message filed during the pause; the one buffered before it waits for its take-up.
func TestWorkflowMessage_AStepsResumeDeliversOnlyTheMessageFiledWhilePaused(t *testing.T) {
	deps, tr := messageDeps(t)
	parentToStep(t, tr, "buffered while running")
	pauseStep(t, tr, nil)
	parentToStep(t, tr, "the answer to your ask")
	rows := stepRows(t, deps)
	if len(rows) != 2 {
		t.Fatalf("Setup: step rows = %+v, want two", rows)
	}

	resumeStep(t, tr)

	stamps := stepStamps(t, deps)
	if d := stamps[rows[0].ID]; len(d) != 0 {
		t.Errorf("buffered message stamps = %+v, want none: only its take-up settles it", d)
	}
	if d := stamps[rows[1].ID]; !deliveredOnce(d) {
		t.Errorf("answer stamps = %+v, want one delivered at the resume", d)
	}
	if d := chatLogStamps(t, deps)[rows[1].ID]; !deliveredOnce(d) {
		t.Errorf("chat copy stamps = %+v, want the answer delivered there too", d)
	}
}

// A retry-wait's node_paused leaves the node running, where KAS buffers a parent's message, so neither a later
// node_start nor a turn_start delivers it.
func TestWorkflowMessage_ARetryWaitIsNoPauseAStepResumesFrom(t *testing.T) {
	deps, tr := messageDeps(t)
	pauseStep(t, tr, map[string]any{"reason": "Waiting 30s to retry after a network error.", "kind": "retry-wait"})
	parentToStep(t, tr, "also check the tests")

	resumeStep(t, tr)

	if s := stepStamps(t, deps); len(s) != 0 {
		t.Errorf("step stamps = %+v, want none", s)
	}
}

// A turn_start with no node_start after the pause is no resume and delivers nothing.
func TestWorkflowMessage_APausedStepsTurnWithNoNodeStartSettlesNoMessage(t *testing.T) {
	deps, tr := messageDeps(t)
	pauseStep(t, tr, nil)
	parentToStep(t, tr, "the answer to your ask")

	turnStartOn(t, tr, testStepAttr())

	if s := stepStamps(t, deps); len(s) != 0 {
		t.Errorf("step stamps = %+v, want none", s)
	}
}

// Two messages filed during one pause: KAS wakes the step with the first and buffers the second behind the pending
// wake, so the resume delivers neither until the second's take-up leaves the first the only waker.
func TestWorkflowMessage_AResumeWithTwoPausedMessagesWaitsForATakeUp(t *testing.T) {
	deps, tr := messageDeps(t)
	pauseStep(t, tr, nil)
	parentToStep(t, tr, "first answer")
	parentToStep(t, tr, "second thought")
	rows := stepRows(t, deps)
	if len(rows) != 2 {
		t.Fatalf("Setup: step rows = %+v, want two", rows)
	}

	resumeStep(t, tr)
	if s := stepStamps(t, deps); len(s) != 0 {
		t.Fatalf("stamps at the resume = %+v, want none: either message can have woken the step", s)
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-2", "content": "[notification/info] second thought", "notificationSeverity": "info",
	}), testStepAttr())

	stamps := stepStamps(t, deps)
	if d := stamps[rows[1].ID]; !deliveredOnce(d) || d[0].KASID != "notify-2" {
		t.Errorf("second message stamps = %+v, want its take-up", d)
	}
	if d := stamps[rows[0].ID]; !deliveredOnce(d) {
		t.Errorf("first message stamps = %+v, want one delivered: it alone can have woken the step", d)
	}
}

// A run's end forgets its step sessions' pauses, so no later frame on the session settles a row filed during one.
func TestWorkflowMessage_ARunsEndForgetsItsStepsPause(t *testing.T) {
	deps, tr := messageDeps(t)
	pauseStep(t, tr, nil)
	parentToStep(t, tr, "the answer to your ask")
	tr.ForgetRunSteps(testWfRun)

	resumeStep(t, tr)

	if s := stepStamps(t, deps); len(s) != 0 {
		t.Errorf("step stamps = %+v, want none after the run ended", s)
	}
}
