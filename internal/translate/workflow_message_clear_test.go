package translate

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// allChatEntries is the chat's open turn's entries, then those filed after its newest close.
func allChatEntries(deps *baseDeps, chatID marotte.ChatID) []marotte.Entry {
	return append(deps.chatEntries(chatID), deps.between[chatID]...)
}

// stampsByRow is the chat's take-ups keyed by the row each settles.
func stampsByRow(t *testing.T, deps *baseDeps) map[string][]marotte.EntrySteerDelivered {
	t.Helper()
	out := map[string][]marotte.EntrySteerDelivered{}
	for _, d := range deliveredOf(t, allChatEntries(deps, testChat)) {
		out[d.SteerID] = append(out[d.SteerID], d)
	}
	return out
}

// clearedOnce reports one settlement, cleared: the clear's outcome for a row nothing proved read.
func clearedOnce(d []marotte.EntrySteerDelivered) bool {
	return len(d) == 1 && d[0].Cleared && d[0].ReadTs != 0
}

func stepReport(t *testing.T, tr *Translator, severity, text string) {
	t.Helper()
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, testParent, severity, text))
}

func clearSteering(t *testing.T, tr *Translator, ids ...string) {
	t.Helper()
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": ids,
	}), FrameAttribution{})
}

// stopTurn stages the chat's prompt turn the reader stopped, which the cancel's clear ends.
func stopTurn(deps *baseDeps, chatID marotte.ChatID) {
	stopTurnBy(deps, chatID, marotte.TurnStopReader)
}

// stopTurnBy stages the chat's prompt turn stopped for cause.
func stopTurnBy(deps *baseDeps, chatID marotte.ChatID, cause marotte.TurnStopCause) {
	turn := deps.turns.chatTurn(chatID)
	deps.prompts[chatID] = turn
	deps.stopped = map[string]marotte.TurnStopCause{turn.ID(): cause}
}

// resumeAfterStop stages KAS's rewake turn after a cancel: the next clear is no longer the reader's stop.
func resumeAfterStop(deps *baseDeps) {
	deps.stopped = map[string]marotte.TurnStopCause{}
}

// A boundary clear settles every message waiting since the previous clear cleared, whatever ids it names: no frame says
// which cleared id is which row, nor whether KAS dropped it, so none reads dropped and none stays waiting.
func TestWorkflowMessage_ABoundaryClearSettlesItsWholeEpochCleared(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "success", "older report")
	stepReport(t, tr, "success", "first current report")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	clearSteering(t, tr, "notify-a100495c-584a-45ce-a54b-389dce412341")

	stamps := stampsByRow(t, deps)
	for _, r := range rows {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("row %q stamps = %+v, want one cleared", r.Text, d)
		}
	}
	if got := steerRows(t, deps, testChat); len(got) != 2 {
		t.Errorf("chat rows = %+v, want the two messages' rows and no row for the cleared id", got)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 2 || !d[0].Cleared || !d[1].Cleared {
		t.Errorf("step stamps = %+v, want the sender's two rows cleared too", d)
	}
}

// A cancel's clear settles its epoch cleared too: KAS re-prompts what it cleared, but no frame names what that turn
// re-delivered, so none reads delivered. The run notice it named is forgotten, as KAS's re-prompt carries it.
func TestWorkflowMessage_ACancelsClearSettlesItsEpochClearedAndForgetsTheRunNotice(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "first current report")
	stepReport(t, tr, "warning", "pending decision")
	deps.runNotices = map[marotte.ChatID][]stagedRunNotice{testChat: {{workflowID: "wf_9", label: "wf_9"}}}
	stopTurn(deps, testChat)

	clearSteering(t, tr,
		"notify-cdef0c75-94a4-43b3-b8e1-8f803a000001",
		"notify-90b2a33e-2d77-4844-a24c-1994a8000002",
		"notify-wf-5b0e2b1c-0d55-4c55-9a51-000000000003",
	)

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %+v, want the two messages' rows and no dropped row", rows)
	}
	stamps := stampsByRow(t, deps)
	for _, r := range rows {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("row %q stamps = %+v, want one cleared at the stop", r.Text, d)
		}
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 2 || !d[0].Cleared || !d[1].Cleared {
		t.Errorf("step stamps = %+v, want the sender's two rows cleared too", d)
	}
	if f := clearFacts(t, allChatEntries(deps, testChat)); len(f) != 1 || f[0].Cause != marotte.SteerClearCauseCancel {
		t.Errorf("clear facts = %+v, want one recording the reader's cancel", f)
	}
	if q := deps.runNotices[testChat]; len(q) != 0 {
		t.Errorf("run notices left = %+v, want wf_9 forgotten: KAS's re-wake prompt carries it, and no frame takes it up", q)
	}
}

// The contrast: a clear the reader did not cause (an error, the continuation cap, _session/steer/clear) drops a run
// notice it names, recorded text-less for the merge to fill.
func TestWorkflowMessage_AClearNoStopCausedRecordsTheRunNoticeDropped(t *testing.T) {
	deps, tr := messageDeps(t)
	clearSteering(t, tr, "notify-wf-2")

	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 || rows[0].ID != "notify-wf-2" || rows[0].State != marotte.SteerStateDropped {
		t.Errorf("chat rows = %+v, want the run notice recorded dropped", rows)
	}
}

// sessionlessRow files a step's report with no to_session: no session's buffer received it, so it is outside every
// session's epoch.
func sessionlessRow(t *testing.T, deps *baseDeps, text string) {
	t.Helper()
	_, err := deps.turns.chatTurn(testChat).Steer(t.Context(), marotte.WorkflowMessageIDPrefix+text, &marotte.EntrySteer{
		Text: text, Origin: marotte.SteerOriginStep, Severity: "success", OriginRun: testWfRun, Step: "review",
		StepPath: workflow.PathKey([]string{testWfRun, "review"}), ProducedTs: 1,
	})
	if err != nil {
		t.Fatalf("Setup: session-less row: %v", err)
	}
}

// The recorded stop: an older waiting row no session's buffer received, then the two KAS holds; the reader's cancel
// clears the two, which KAS re-prompts with no frame naming them.
func TestWorkflowMessage_ACancelClearsTheEpochItEnded(t *testing.T) {
	deps, tr := messageDeps(t)
	sessionlessRow(t, deps, "older report")
	stepReport(t, tr, "success", "first current report")
	stepReport(t, tr, "success", "second current report")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	stopTurn(deps, testChat)

	clearSteering(t, tr, "notify-b", "notify-c")

	stamps := stampsByRow(t, deps)
	t.Run("older_row_untouched", func(t *testing.T) {
		if d, ok := stamps[rows[0].ID]; ok {
			t.Errorf("older row stamped %+v; KAS no longer held it", d)
		}
	})
	t.Run("rewoken_rows_read_cleared", func(t *testing.T) {
		for _, r := range rows[1:] {
			if d := stamps[r.ID]; !clearedOnce(d) {
				t.Errorf("row %q stamps = %+v, want one cleared: the stop ended its epoch", r.Text, d)
			}
		}
	})
}

// The recorded turn end: an older waiting success row no session's buffer received, then the step's warning ask
// buffered mid-turn; the boundary clear names only the ask.
func TestWorkflowMessage_ABoundaryClearsTheEpochItEnded(t *testing.T) {
	deps, tr := messageDeps(t)
	sessionlessRow(t, deps, "older report")
	stepReport(t, tr, "warning", "Decision needed")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 || rows[1].ReadTs != 0 {
		t.Fatalf("rows = %+v, want two, the ask waiting", rows)
	}

	clearSteering(t, tr, "notify-347a6dd1")

	stamps := stampsByRow(t, deps)
	t.Run("older_row_untouched", func(t *testing.T) {
		if d, ok := stamps[rows[0].ID]; ok {
			t.Errorf("older row stamped %+v; the clear named the ask", d)
		}
	})
	t.Run("ask_reads_cleared", func(t *testing.T) {
		if d := stamps[rows[1].ID]; !clearedOnce(d) {
			t.Errorf("ask stamps = %+v, want one cleared: KAS emptied its buffer of it unread", d)
		}
	})
}

// The recorded stop of a reader's turn: eleven rows left waiting from before this build recorded a session, sixteen
// buffered after the previous clear, then the reader's own steer. The cancel's clear names the sixteen and the steer,
// and KAS re-prompts the sixteen; the eleven are no longer KAS's.
func TestWorkflowMessage_ACancelAfterLeakedRowsClearsOnlyTheSixteenItCleared(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	for i := range 11 {
		sessionlessRow(t, deps, fmt.Sprintf("leaked report %d", i))
	}
	clearSteering(t, tr, "steer-m-previous")
	for i := range 16 {
		stepReport(t, tr, "success", fmt.Sprintf("epoch report %d", i))
	}
	rows := steerRows(t, deps, testChat)
	if len(rows) != 27 {
		t.Fatalf("rows = %d, want 27", len(rows))
	}
	stopTurn(deps, testChat)

	cleared := make([]string, 0, 17)
	for i := range 16 {
		cleared = append(cleared, fmt.Sprintf("notify-%02d", i))
	}
	clearSteering(t, tr, append(cleared, "steer-m-mvchgagr")...)

	stamps := stampsByRow(t, deps)
	for _, r := range rows[:11] {
		if d, ok := stamps[r.ID]; ok {
			t.Errorf("leaked row %q stamped %+v, want it untouched", r.Text, d)
		}
	}
	for _, r := range rows[11:] {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("cleared row %q stamps = %+v, want one cleared at the stop", r.Text, d)
		}
	}
}

// The recorded boundary after that stop: a row the cancel cleared and KAS re-delivered, then the step's ask buffered
// in the rewake turn and the main agent's message to the idle step; the turn's boundary clear names only the ask.
func TestWorkflowMessage_ABoundaryAfterARewakeClearsOnlyTheAsk(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "earlier delivered report")
	stopTurn(deps, testChat)
	clearSteering(t, tr, "notify-6f06")
	resumeAfterStop(deps)
	stepReport(t, tr, "warning", "pending decision")
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "take option B"))
	rows := steerRows(t, deps, testChat)
	if len(rows) != 3 || rows[1].ReadTs != 0 || rows[2].Origin != marotte.SteerOriginParent {
		t.Fatalf("rows = %+v, want the re-delivered row, the buffered ask and the parent's message", rows)
	}
	before := stampsByRow(t, deps)[rows[0].ID]
	if !clearedOnce(before) {
		t.Fatalf("Setup: re-delivered row stamps after the stop = %+v, want one cleared", before)
	}

	clearSteering(t, tr, "notify-347a6dd1")

	stamps := stampsByRow(t, deps)
	if d := stamps[rows[0].ID]; len(d) != 1 || d[0] != before[0] {
		t.Errorf("re-delivered row stamps = %+v, want only its settlement at the stop %+v", d, before[0])
	}
	if d := stamps[rows[1].ID]; !clearedOnce(d) {
		t.Errorf("ask stamps = %+v, want one cleared", d)
	}
	if d, ok := stamps[rows[2].ID]; ok {
		t.Errorf("parent message stamped %+v, want none: the chat sent it", d)
	}
}

// The epoch is the log's: a translator started after a clear settles only what was sent since that recorded clear.
func TestWorkflowMessage_AClearAfterARestartSettlesOnlyTheEpochSinceTheRecordedClear(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "success", "before the restart")
	clearSteering(t, tr, "steer-m-earlier")

	restarted := New(rolesOf(deps))
	announceStep(t, restarted)
	stepReport(t, restarted, "success", "after the restart")
	clearSteering(t, restarted, "notify-after")

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}
	stamps := stampsByRow(t, deps)
	if d := stamps[rows[0].ID]; !clearedOnce(d) {
		t.Errorf("row from the ended epoch stamps = %+v, want only the earlier clear's", d)
	}
	if d := stamps[rows[1].ID]; !clearedOnce(d) {
		t.Errorf("row of the current epoch stamps = %+v, want one cleared", d)
	}
}

// A step's own clear settles the parent's waiting message to it cleared in the run log, and the chat's copy beside it.
func TestWorkflowMessage_AStepsClearSettlesTheParentsWaitingMessageCleared(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "also check the tests"))
	step := stepRows(t, deps)
	if len(step) != 1 || step[0].ReadTs != 0 {
		t.Fatalf("step rows at send = %+v, want one waiting row", step)
	}

	path := workflow.PathKey([]string{testWfRun, "review"})
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-x"},
	}), FrameAttribution{Step: true, SessionID: testStep, RunID: testWfRun, NodePath: path})

	if d := deliveredOf(t, deps.runEntries(testWfRun, path)); !clearedOnce(d) || d[0].SteerID != step[0].ID {
		t.Errorf("step stamps = %+v, want the parent's message cleared", d)
	}
	if d := deliveredOf(t, allChatEntries(deps, testChat)); !clearedOnce(d) || d[0].SteerID != step[0].ID {
		t.Errorf("chat stamps = %+v, want the chat's copy cleared too", d)
	}
}

// repeatCapNote is KAS's own note to the launching chat about a repeat at its cap: a step sender, the repeat's
// node, and no caller session.
func repeatCapNote(severity string) *marotte.RPCResponse {
	return notif("_kiro/session/notify", map[string]any{
		"sessionId": testParent, "message": `paused: repeat "fix-loop" reached its iteration limit`,
		"severity": severity, "sender": "step", "workflowId": testWfRun, "nodeId": "fix-loop",
	})
}

// A runtime note no step session sent is one row in the launching chat, naming the run and the node, with no copy in
// any step's log.
func TestWorkflowMessage_AStepLessRuntimeNoteIsAChatRow(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, repeatCapNote("info"))

	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 {
		t.Fatalf("chat rows = %+v, want the note's row", rows)
	}
	r := rows[0].EntrySteer
	if r.Origin != marotte.SteerOriginStep || r.Step != "fix-loop" || r.OriginRun != testWfRun || r.StepPath != "" ||
		r.ToSession != testParent || r.Severity != "info" {
		t.Errorf("chat row = %+v, want the repeat's info note from run %s, sent to the chat's session", r, testWfRun)
	}
	for _, c := range deps.runCalls {
		if c.kind == "steer" {
			t.Errorf("run log call %+v, want none: no step session sent the note", c)
		}
	}
}

// A runtime note buffered mid-turn is settled by its epoch's clear like any message, and the clear writes no
// text-less row for its id.
func TestWorkflowMessage_ABoundaryClearSettlesABufferedRuntimeNoteAndWritesNoTextlessRow(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "older report")
	clearSteering(t, tr, "steer-m-earlier")
	tr.HandleWorkflowMessage(t.Context(), testChat, repeatCapNote("info"))
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 || rows[1].ReadTs != 0 {
		t.Fatalf("rows = %+v, want the older report and the note waiting", rows)
	}
	older := stampsByRow(t, deps)[rows[0].ID]

	clearSteering(t, tr, "notify-cap")

	if got := steerRows(t, deps, testChat); len(got) != 2 {
		t.Errorf("chat rows = %+v, want no text-less row for the cleared id", got)
	}
	stamps := stampsByRow(t, deps)
	if d := stamps[rows[1].ID]; !clearedOnce(d) {
		t.Errorf("note stamps = %+v, want one cleared", d)
	}
	if d := stamps[rows[0].ID]; len(d) != len(older) {
		t.Errorf("older row stamps = %+v, want only the earlier clear's %+v", d, older)
	}
}

// KAS's buffer is per session: after the chat's session is replaced, a cancel on the new one leaves a message sent to
// the retired session untouched, and delivers only what the new session held.
func TestWorkflowMessage_ACancelOnANewSessionLeavesTheRetiredSessionsMessageUntouched(t *testing.T) {
	deps, tr := messageDeps(t)
	const retired = "sess_parent_retired"
	deps.parent = retired
	deps.turns.chatTurn(testChat)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, retired, "success", "sent to the old session"))
	deps.parent = testParent
	stepReport(t, tr, "success", "sent to the new session")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}
	stopTurn(deps, testChat)

	clearSteering(t, tr, "notify-new")

	stamps := stampsByRow(t, deps)
	if d, ok := stamps[rows[0].ID]; ok {
		t.Errorf("retired session's row stamped %+v, want it untouched: the new session's buffer never held it", d)
	}
	if d := stamps[rows[1].ID]; !clearedOnce(d) {
		t.Errorf("new session's row stamps = %+v, want one cleared at the stop", d)
	}
}

// A step's buffer is its execution's session: a later execution at the same path clearing its buffer leaves the
// earlier execution's waiting message untouched.
func TestWorkflowMessage_AStepsClearLeavesAnEarlierExecutionsMessageUntouched(t *testing.T) {
	deps, tr := messageDeps(t)
	const next = "sess_step_next"
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "first execution"))
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": testWfRun, "nodeId": "review", "nodePath": []string{testWfRun, "review"}, "sessionId": next,
		}))
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, next, "info", "second execution"))
	step := stepRows(t, deps)
	if len(step) != 2 {
		t.Fatalf("step rows = %+v, want two", step)
	}

	path := workflow.PathKey([]string{testWfRun, "review"})
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-next"},
	}), FrameAttribution{Step: true, SessionID: next, RunID: testWfRun, NodePath: path})

	if d := deliveredOf(t, deps.runEntries(testWfRun, path)); !clearedOnce(d) || d[0].SteerID != step[1].ID {
		t.Errorf("step stamps = %+v, want only the second execution's message %q cleared", d, step[1].ID)
	}
}

// A clear naming no id still empties KAS's buffer, so it settles its epoch, and the next clear settles only what was
// sent after it.
func TestWorkflowMessage_AnEmptyClearStillSettlesItsEpoch(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "before the empty clear")
	clearSteering(t, tr)
	stepReport(t, tr, "success", "after the empty clear")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}

	clearSteering(t, tr, "notify-after")

	stamps := stampsByRow(t, deps)
	for _, r := range rows {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("row %q stamps = %+v, want one cleared, by its own epoch's clear", r.Text, d)
		}
	}
}

// The clear's fact precedes every other entry the clear writes, its settlements and a run notice's drop, so a log cut
// after it still ends and settles the epoch.
func TestWorkflowMessage_AClearFilesItsFactBeforeItsSettlements(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "a report")

	clearSteering(t, tr, "notify-a", "notify-wf-1")

	var kinds []marotte.EntryKind
	for _, e := range allChatEntries(deps, testChat) {
		if e.Kind == marotte.EntryKindSteerCleared || e.Kind == marotte.EntryKindSteerDelivered || e.ID == "notify-wf-1" {
			kinds = append(kinds, e.Kind)
		}
	}
	want := []marotte.EntryKind{marotte.EntryKindSteerCleared, marotte.EntryKindSteerDelivered, marotte.EntryKindSteer}
	if !slices.Equal(kinds, want) {
		t.Errorf("clear entries in log order = %v, want %v", kinds, want)
	}
}

// A step notify to the chat from a session the step registry does not know (KAS's note about a stopped step, or a
// step not yet seen at its node_start) is one chat row naming the node, so a clear's ids still match the rows.
func TestWorkflowMessage_AStepNotifyFromAnUnknownSessionIsAChatRow(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", "sess_unknown", testParent, "warning", "step stopped"))

	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 {
		t.Fatalf("chat rows = %+v, want the note's row", rows)
	}
	if r := rows[0].EntrySteer; r.Origin != marotte.SteerOriginStep || r.Step != "review" || r.OriginRun != testWfRun ||
		r.StepPath != "" || r.Text != "step stopped" {
		t.Errorf("chat row = %+v, want the step's note from run %s naming node review and no step path", r, testWfRun)
	}
	for _, c := range deps.runCalls {
		if c.kind == "steer" {
			t.Errorf("run log call %+v, want none: no known step session sent it", c)
		}
	}
}

// clearFacts is every steer_cleared in the entries, in log order.
func clearFacts(t *testing.T, entries []marotte.Entry) []marotte.EntrySteerCleared {
	t.Helper()
	var out []marotte.EntrySteerCleared
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindSteerCleared {
			out = append(out, decodePayload[marotte.EntrySteerCleared](t, &entries[i]))
		}
	}
	return out
}

// A tab close or a chat delete stops the turn before its session/cancel: the clear is recorded as a boundary, and
// its epoch reads cleared like any other.
func TestWorkflowMessage_ATeardownsClearRecordsABoundaryAndClearsTheEpoch(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "Landed the fix")
	stepReport(t, tr, "warning", "Decision needed")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}
	stopTurnBy(deps, testChat, marotte.TurnStopTeardown)

	clearSteering(t, tr, "notify-1", "notify-2")

	stamps := stampsByRow(t, deps)
	for _, r := range rows {
		if d := stamps[r.ID]; !clearedOnce(d) {
			t.Errorf("row %q stamps = %+v, want one cleared", r.Text, d)
		}
	}
	if f := clearFacts(t, allChatEntries(deps, testChat)); len(f) != 1 || f[0].Cause != marotte.SteerClearCauseBoundary {
		t.Errorf("clear facts = %+v, want one boundary", f)
	}
}

// A clear names the buffer of the session that sent it: a session other than the chat's, routed to the chat's scope,
// ends no epoch of the chat's messages, and the chat's own next clear still counts them.
func TestWorkflowMessage_AClearFromAnotherSessionLeavesTheChatsEpochUntouched(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "success", "a report")
	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-cold"},
	}), FrameAttribution{Step: true, SessionID: "sess_cold_step"})

	if d, ok := stampsByRow(t, deps)[rows[0].ID]; ok {
		t.Fatalf("chat row stamped %+v by another session's clear, want it untouched", d)
	}
	clearSteering(t, tr, "notify-a")
	if d := stampsByRow(t, deps)[rows[0].ID]; !clearedOnce(d) {
		t.Errorf("chat row stamps after the chat's own clear = %+v, want one cleared", d)
	}
}

// A clear whose fact the receiver's log refuses writes nothing in either log, a run notice's drop included, so no log
// reads any effect of a clear without the clear that ended its epoch.
func TestWorkflowMessage_AClearWhoseFactIsRefusedSettlesNothing(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "a report")
	deps.turns.refuses = func(_ string, kind marotte.EntryKind) bool { return kind == marotte.EntryKindSteerCleared }

	clearSteering(t, tr, "notify-a", "notify-wf-1")

	if rows := steerRows(t, deps, testChat); len(rows) != 1 {
		t.Errorf("chat rows = %+v, want only the report: the run notice's drop follows the fact", rows)
	}

	if d := deliveredOf(t, allChatEntries(deps, testChat)); len(d) != 0 {
		t.Errorf("chat stamps = %+v, want none", d)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 0 {
		t.Errorf("step stamps = %+v, want none", d)
	}
}

// A refused clear leaves the session's epoch open in the wake ledger too, so the wake its message started still
// delivers it.
func TestWorkflowMessage_ARefusedClearLeavesTheWakeItsWaker(t *testing.T) {
	deps, tr := messageDeps(t)
	stepReport(t, tr, "warning", "the waking report")
	deps.turns.refuses = func(_ string, kind marotte.EntryKind) bool { return kind == marotte.EntryKindSteerCleared }
	stopTurn(deps, testChat)

	clearSteering(t, tr, "notify-a")
	closeChatTurn(t, deps, testChat)
	deps.turns.refuses = nil
	chatWake(t, tr, "send-message-wake")

	rows := chatLogRows(t, deps)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if d := chatLogStamps(t, deps)[rows[0].ID]; !deliveredOnce(d) {
		t.Errorf("stamps = %+v, want one delivered at the wake: the refused clear ended no epoch", d)
	}
}

// Once the clear's fact landed, the receiver's log reads the epoch settled from it alone, so a refused settlement entry
// still leaves both logs agreeing: the receiver's fold clears the row and the sender's copy is stamped cleared.
func TestWorkflowMessage_AClearsSettlementTheReceiverRefusesStillReachesBothLogs(t *testing.T) {
	deps, tr := messageDeps(t)
	chat := deps.turns.chatTurn(testChat)
	stepReport(t, tr, "success", "a report")
	deps.turns.refuses = func(turn string, kind marotte.EntryKind) bool {
		return turn == chat.ID() && kind == marotte.EntryKindSteerDelivered
	}

	clearSteering(t, tr, "notify-a")

	if f := clearFacts(t, allChatEntries(deps, testChat)); len(f) != 1 {
		t.Fatalf("Setup: clear facts = %+v, want the clear's fact landed", f)
	}
	if d := deliveredOf(t, allChatEntries(deps, testChat)); len(d) != 0 {
		t.Fatalf("Setup: chat stamps = %+v, want the refused one absent", d)
	}
	if u, err := deps.UnsettledWorkflowMessages(t.Context(), testChat); err != nil || len(u) != 0 {
		t.Errorf("chat's unsettled rows = %+v (%v), want none: the clear's fact settles the report", u, err)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); !clearedOnce(d) {
		t.Errorf("step stamps = %+v, want the sender's copy cleared", d)
	}
}
