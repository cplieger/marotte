package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// allChatEntries is the chat's open turn's entries, then those filed after its newest close.
func allChatEntries(deps *baseDeps, chatID marotte.ChatID) []marotte.Entry {
	return append(deps.chatEntries(chatID), deps.between[chatID]...)
}

// The recorded shape of a stopped turn's clear: a message left waiting from an earlier day, then the one KAS holds, then
// a steering_cleared naming only KAS's id for the second. No id joins either row, so the clear settles neither.
func TestWorkflowMessage_AClearSettlesNoRowItCannotName(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "Landed web-terminal-ui PR #588 (merged f7334f9)"))
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "FIX-PASS-DONE p=2. Commit 33e16298 is not pushed."))
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-a100495c-584a-45ce-a54b-389dce412341"},
	}), FrameAttribution{})

	if d := deliveredOf(t, allChatEntries(deps, testChat)); len(d) != 0 {
		t.Errorf("chat stamps = %+v, want none: the clear's id names neither row", d)
	}
	if got := steerRows(t, deps, testChat); len(got) != 2 {
		t.Errorf("chat rows = %+v, want the two messages' rows and no row for the cleared id", got)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 0 {
		t.Errorf("step stamps = %+v, want none", d)
	}
}

// stopTurn stages the chat's prompt turn the reader stopped, which the cancel's clear ends.
func stopTurn(deps *baseDeps, chatID marotte.ChatID) {
	turn := deps.turns.chatTurn(chatID)
	deps.prompts[chatID] = turn
	deps.stopped = map[string]bool{turn.ID(): true}
}

// The recorded shape of a stopped turn's cancel: the clear names the notifications KAS holds, and KAS
// re-wakes the session with every one (cancelLocalTurn), so none was dropped.
func TestWorkflowMessage_ACancelsClearDropsNothingKASRewakes(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "Turn-display fix: both seats approved"))
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "warning", "Decision needed (review finding WTNC-GPT-R3-1)"))
	deps.runNotices = map[marotte.ChatID][]stagedRunNotice{testChat: {{workflowID: "wf_9", label: "wf_9"}}}
	stopTurn(deps, testChat)

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{
			"notify-cdef0c75-94a4-43b3-b8e1-8f803a000001",
			"notify-90b2a33e-2d77-4844-a24c-1994a8000002",
			"notify-wf-5b0e2b1c-0d55-4c55-9a51-000000000003",
		},
	}), FrameAttribution{})

	if d := deliveredOf(t, allChatEntries(deps, testChat)); len(d) != 0 {
		t.Errorf("chat stamps = %+v, want none: KAS re-wakes the session with every cleared notification", d)
	}
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %+v, want the two messages' rows and no dropped row", rows)
	}
	for _, r := range rows {
		if r.State == marotte.SteerStateDropped {
			t.Errorf("row %q = %+v, want it still waiting", r.ID, r.EntrySteer)
		}
	}
	if q := deps.runNotices[testChat]; len(q) != 0 {
		t.Errorf("run notices left = %+v, want wf_9 forgotten: KAS's re-wake prompt carries it, and no frame takes it up", q)
	}
}

// The contrast: a clear the reader did not cause (an error, the continuation cap, _session/steer/clear) drops a run
// notice it names, recorded text-less for the merge to fill.
func TestWorkflowMessage_AClearNoStopCausedRecordsTheRunNoticeDropped(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-wf-2"},
	}), FrameAttribution{})

	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 || rows[0].ID != "notify-wf-2" || rows[0].State != marotte.SteerStateDropped {
		t.Errorf("chat rows = %+v, want the run notice recorded dropped", rows)
	}
}
