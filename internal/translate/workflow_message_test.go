package translate

import (
	"context"
	"fmt"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

const testWfRun = "wf_1"

func announceStep(t *testing.T, tr *Translator) {
	t.Helper()
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": testWfRun, "nodeId": "review", "nodePath": []string{testWfRun, "review"},
			"sessionId": testStep,
		}))
}

func workflowNotify(sender, caller, target, severity, message string) *marotte.RPCResponse {
	return notif("_kiro/session/notify", map[string]any{
		"sessionId": target, "callerSessionId": caller, "message": message, "severity": severity,
		"sender": sender, "workflowId": testWfRun, "nodeId": "review",
	})
}

func deliveredOf(t *testing.T, entries []marotte.Entry) []marotte.EntrySteerDelivered {
	t.Helper()
	var out []marotte.EntrySteerDelivered
	for i := range entries {
		if entries[i].Kind == marotte.EntryKindSteerDelivered {
			out = append(out, decodePayload[marotte.EntrySteerDelivered](t, &entries[i]))
		}
	}
	return out
}

func stepRows(t *testing.T, deps *baseDeps) []steerRow {
	t.Helper()
	var out []steerRow
	for _, e := range deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"})) {
		if e.Kind == marotte.EntryKindSteer {
			out = append(out, steerRow{ID: e.ID, EntrySteer: decodePayload[marotte.EntrySteer](t, &e)})
		}
	}
	return out
}

func messageDeps(t *testing.T) (*baseDeps, *Translator) {
	t.Helper()
	deps, _, _ := depsWithStore(t, testChat)
	deps.parent = testParent
	tr := New(rolesOf(deps))
	announceStep(t, tr)
	return deps, tr
}

func TestWorkflowMessage_BufferedStepMessagesAreOneRowEach(t *testing.T) {
	deps, tr := messageDeps(t)
	const n = 5
	for i := range n {
		tr.HandleWorkflowMessage(t.Context(), testChat,
			workflowNotify("step", testStep, testParent, "success", fmt.Sprintf("part %d done", i)))
	}
	rows := steerRows(t, deps, testChat)
	if len(rows) != n {
		t.Fatalf("after %d notifies the chat holds %d steer rows, want %d", n, len(rows), n)
	}
	for _, r := range rows {
		if r.Origin != marotte.SteerOriginStep || r.Step != "review" || r.ProducedTs == 0 || r.ReadTs != 0 {
			t.Errorf("row at send = %+v, want origin step, step review, a send time and no read time", r.EntrySteer)
		}
	}

	deps.turns.chatTurn(testChat)
	for i := range n {
		tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
			"messageId":            fmt.Sprintf("notify-%d", i),
			"content":              fmt.Sprintf("[notification/success] part %d done", i),
			"notificationSeverity": "success",
		}), FrameAttribution{})
	}

	if got := steerRows(t, deps, testChat); len(got) != n {
		t.Fatalf("after %d take-ups the chat holds %d steer rows, want still %d", n, len(got), n)
	}
	delivered := deliveredOf(t, deps.chatEntries(testChat))
	if len(delivered) != n {
		t.Fatalf("chat holds %d steer_delivered entries, want %d", len(delivered), n)
	}
	for i, d := range delivered {
		if d.SteerID != rows[i].ID || d.ReadTs == 0 {
			t.Errorf("delivered[%d] = %+v, want steer %q with a read time", i, d, rows[i].ID)
		}
	}
	if got := deliveredOf(t, deps.runBetween[runPathKey(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))]); len(got) != 0 {
		t.Errorf("the closed-turn fake took %d stamps, want them in the step's open turn", len(got))
	}
	if got := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(got) != n {
		t.Errorf("the step's log holds %d steer_delivered entries, want %d", len(got), n)
	}
}

// A parent's message to an idle step waits at its send, KAS wakes the step with it, and the wake turn's first frame on
// the step's session settles it delivered in both logs.
func TestWorkflowMessage_ParentToAnIdleStepIsDeliveredByItsWake(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("parent", testParent, testStep, "info", "also check the tests"))

	chat := steerRows(t, deps, testChat)
	step := stepRows(t, deps)
	if len(chat) != 1 || len(step) != 1 {
		t.Fatalf("rows: chat %d, step %d, want one each", len(chat), len(step))
	}
	if chat[0].ID != step[0].ID {
		t.Errorf("row ids differ: chat %q, step %q", chat[0].ID, step[0].ID)
	}
	c := chat[0].EntrySteer
	if c.Origin != marotte.SteerOriginParent || c.Step != "review" || c.State != "" || c.ReadTs != 0 || c.ProducedTs == 0 {
		t.Errorf("chat row = %+v, want a waiting parent message to review with its send time", c)
	}
	if s := step[0].EntrySteer; s.Step != "" || s.Origin != marotte.SteerOriginParent || s.ToSession != testStep {
		t.Errorf("step row = %+v, want a parent message naming no step, sent to the step's session", s)
	}

	attr := testStepAttr()
	turnStartOn(t, tr, attr)
	wakeChunk(t, tr, "send-message-wake", attr)

	if d := deliveredOf(t, deps.runEntries(testWfRun, attr.NodePath)); len(d) != 1 || d[0].SteerID != step[0].ID || d[0].Cleared {
		t.Errorf("step stamps = %+v, want the message delivered at the wake", d)
	}
	if d := deliveredOf(t, allChatEntries(deps, testChat)); len(d) != 1 || d[0].SteerID != chat[0].ID || d[0].Cleared {
		t.Errorf("chat stamps = %+v, want the chat's copy delivered too", d)
	}
}

func TestWorkflowMessage_ParentToABusyStepIsDeliveredAtItsTakeUp(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("parent", testParent, testStep, "info", "also check the tests"))
	if r := stepRows(t, deps); len(r) != 1 || r[0].ReadTs != 0 {
		t.Fatalf("step rows at send = %+v, want one undelivered row", r)
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-x", "content": "[notification/info] also check the tests",
		"notificationSeverity": "info",
	}), FrameAttribution{Step: true, SessionID: testStep, RunID: testWfRun, NodePath: workflow.PathKey([]string{testWfRun, "review"})})

	if r := stepRows(t, deps); len(r) != 1 {
		t.Errorf("step rows after the take-up = %d, want 1", len(r))
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 1 {
		t.Errorf("step stamps = %d, want 1", len(d))
	}
	if d := deliveredOf(t, deps.between[testChat]); len(d) != 1 {
		t.Errorf("chat stamps = %d, want 1 (the chat is idle, so after its newest close)", len(d))
	}
}

func TestWorkflowMessage_IgnoresFramesThatAreNotThisChatsMessages(t *testing.T) {
	cases := []struct {
		name string
		chat marotte.ChatID
		msg  *marotte.RPCResponse
	}{
		{"a parent message to an unknown step", testChat, workflowNotify("parent", testParent, "sess_other", "info", "hi")},
		{"another chat's target", testChat, workflowNotify("step", testStep, "sess_elsewhere", "info", "hi")},
		{"a parent message from elsewhere", testChat, workflowNotify("parent", "sess_elsewhere", testStep, "info", "hi")},
		{"no sender", testChat, workflowNotify("", testStep, testParent, "info", "hi")},
		{"a run host session", marotte.ChatID("run:" + testWfRun), workflowNotify("step", testStep, testParent, "info", "hi")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps, tr := messageDeps(t)
			tr.HandleWorkflowMessage(context.Background(), c.chat, c.msg)
			if r := steerRows(t, deps, testChat); len(r) != 0 {
				t.Errorf("chat rows = %+v, want none", r)
			}
			if r := stepRows(t, deps); len(r) != 0 {
				t.Errorf("step rows = %+v, want none", r)
			}
		})
	}
}

// A message waits at its send whatever the receiver is doing: marotte sees nothing then that says KAS woke or buffered
// it.
func TestWorkflowMessage_AMessageWaitsAtItsSendWhateverTheReceiverIsDoing(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, testParent, "info", "idle"))
	deps.turns.chatTurn(testChat)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, testParent, "info", "busy"))

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.ReadTs != 0 || r.State != "" {
			t.Errorf("row %q at send = %+v, want waiting", r.Text, r.EntrySteer)
		}
	}
}

func TestWorkflowMessage_IdenticalMessagesAreTakenUpOldestFirst(t *testing.T) {
	deps, tr := messageDeps(t)
	for range 2 {
		tr.HandleWorkflowMessage(t.Context(), testChat,
			workflowNotify("step", testStep, testParent, "success", "tests pass"))
	}
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("after two notifies the chat holds %d rows, want 2", len(rows))
	}

	deps.turns.chatTurn(testChat)
	for _, id := range []string{"notify-1", "notify-2"} {
		tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
			"messageId": id, "content": "[notification/success] tests pass", "notificationSeverity": "success",
		}), FrameAttribution{})
	}

	delivered := deliveredOf(t, deps.chatEntries(testChat))
	if len(delivered) != 2 {
		t.Fatalf("chat holds %d stamps, want 2", len(delivered))
	}
	for i, want := range []string{"notify-1", "notify-2"} {
		if delivered[i].SteerID != rows[i].ID || delivered[i].KASID != want {
			t.Errorf("stamp %d = %+v, want row %q under KAS id %q", i, delivered[i], rows[i].ID, want)
		}
	}
	if got := steerRows(t, deps, testChat); len(got) != 2 {
		t.Errorf("after the take-ups the chat holds %d rows, want still 2", len(got))
	}
}

// TestWorkflowMessage_ATakenUpMessageTheTurnsClearNamesStaysDelivered pins the frames of a chat's turn whose step
// report was taken up and whose boundary clear then named the take-up's id: the row stays delivered, the clear writes
// no "Workflow result not delivered" row, and the other waiting message reads cleared, never dropped.
func TestWorkflowMessage_ATakenUpMessageTheTurnsClearNamesStaysDelivered(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "Report written to report.md."))
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "A later report."))
	deps.turns.chatTurn(testChat)
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId":            "notify-feedda60",
		"content":              "[notification/success] Report written to report.md.",
		"notificationSeverity": "success",
	}), FrameAttribution{})

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-feedda60"},
	}), FrameAttribution{})

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %+v, want the two reports' rows and nothing the clear wrote", rows)
	}
	d := deliveredOf(t, deps.chatEntries(testChat))
	if len(d) != 2 || d[0].SteerID != rows[0].ID || d[0].Cleared || d[0].KASID != "notify-feedda60" {
		t.Fatalf("chat stamps = %+v, want the first report's take-up, delivered, then the second's clear", d)
	}
	if d[1].SteerID != rows[1].ID || !d[1].Cleared {
		t.Errorf("second report's stamp = %+v, want it cleared", d[1])
	}
}

// A boundary clear empties KAS's buffer, so the message it held reads cleared, and the run notice, which no row holds,
// is recorded dropped beside it.
func TestWorkflowMessage_ABoundaryClearClearsTheWaitingMessageAndRecordsTheRunNotice(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"notify-wf-2", "notify-kas-1"},
	}), FrameAttribution{})

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %+v, want the message's row and the run notice's", rows)
	}
	if rows[0].Origin != marotte.SteerOriginStep || rows[1].ID != "notify-wf-2" || rows[1].State != marotte.SteerStateDropped {
		t.Errorf("chat rows = %+v, want the step's row then the cleared run notice, dropped", rows)
	}
	if d := deliveredOf(t, deps.between[testChat]); !clearedOnce(d) || d[0].SteerID != rows[0].ID {
		t.Errorf("chat stamps = %+v, want the message's row cleared", d)
	}
}

// A take-up pairs only with a message KAS still holds: a row with the same words left waiting before the receiver's
// previous clear is out of KAS's buffer, so the newer row takes the stamp.
func TestWorkflowMessage_ATakeUpSkipsAnIdenticalRowFromAnEndedEpoch(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.turns.chatTurn(testChat)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_cleared", map[string]any{
		"messageIds": []string{"steer-m-earlier"},
	}), FrameAttribution{})
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))
	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %+v, want two", rows)
	}

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-1", "content": "[notification/success] tests pass", "notificationSeverity": "success",
	}), FrameAttribution{})

	stamps := stampsByRow(t, deps)
	if d := stamps[rows[1].ID]; !deliveredOnce(d) || d[0].KASID != "notify-1" {
		t.Errorf("current row stamps = %+v, want its take-up under KAS's id", d)
	}
	if d := stamps[rows[0].ID]; !clearedOnce(d) {
		t.Errorf("ended epoch's row stamps = %+v, want only its own clear's", d)
	}
}

// A repeated take-up frame for one KAS id settles one message, never a second with the same words.
func TestWorkflowMessage_ARepeatedTakeUpSettlesOneRow(t *testing.T) {
	deps, tr := messageDeps(t)
	for range 2 {
		tr.HandleWorkflowMessage(t.Context(), testChat,
			workflowNotify("step", testStep, testParent, "success", "tests pass"))
	}
	deps.turns.chatTurn(testChat)
	for range 2 {
		tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
			"messageId": "notify-1", "content": "[notification/success] tests pass", "notificationSeverity": "success",
		}), FrameAttribution{})
	}
	if d := deliveredOf(t, deps.chatEntries(testChat)); len(d) != 1 {
		t.Errorf("chat stamps = %+v, want one: the second frame repeats the first take-up", d)
	}
}

func TestWorkflowMessage_ATakeUpAfterARestartStampsTheRecordedRow(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))

	restarted := New(rolesOf(deps))
	deps.turns.chatTurn(testChat)
	restarted.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-1", "content": "[notification/success] tests pass", "notificationSeverity": "success",
	}), FrameAttribution{})

	rows := steerRows(t, deps, testChat)
	if len(rows) != 1 {
		t.Fatalf("chat rows after the restarted take-up = %+v, want the one recorded at the send", rows)
	}
	if d := deliveredOf(t, deps.chatEntries(testChat)); len(d) != 1 || d[0].SteerID != rows[0].ID || d[0].KASID != "notify-1" {
		t.Errorf("chat stamps = %+v, want the recorded row stamped under KAS's id", d)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 1 || d[0].SteerID != rows[0].ID {
		t.Errorf("step stamps = %+v, want the sender's row stamped from the chat row's peer", d)
	}
}

func TestWorkflowMessage_AChatsTakeUpSkipsTheMessageItSent(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("parent", testParent, testStep, "info", "tests pass"))
	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))

	deps.turns.chatTurn(testChat)
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-1", "content": "[notification/success] tests pass", "notificationSeverity": "success",
	}), FrameAttribution{})

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 || rows[1].Origin != marotte.SteerOriginStep {
		t.Fatalf("chat rows = %+v, want the sent message then the received one", rows)
	}
	if d := deliveredOf(t, deps.chatEntries(testChat)); len(d) != 1 || d[0].SteerID != rows[1].ID {
		t.Errorf("chat stamps = %+v, want the received message %q settled", d, rows[1].ID)
	}
}

func TestWorkflowMessage_AStepsTakeUpSettlesOnlyItsOwnMessage(t *testing.T) {
	deps, tr := messageDeps(t)
	const other = "sess_other_step"
	tr.RunProgressHandler(marotte.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": testWfRun, "nodeId": "verify", "nodePath": []string{testWfRun, "verify"}, "sessionId": other,
		}))
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, testStep, "info", "go"))
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("parent", testParent, other, "info", "go"))

	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-x", "content": "[notification/info] go", "notificationSeverity": "info",
	}), FrameAttribution{Step: true, SessionID: other, RunID: testWfRun, NodePath: workflow.PathKey([]string{testWfRun, "verify"})})

	verify := deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "verify"}))
	if d := deliveredOf(t, verify); len(d) != 1 || d[0].SteerID != verify[0].ID {
		t.Errorf("verify's stamps = %+v, want its own message %q settled", d, verify[0].ID)
	}
}

func TestWorkflowMessage_ALargeBarrageStampsEveryRowInOrder(t *testing.T) {
	deps, tr := messageDeps(t)
	const n = 100
	for range n {
		tr.HandleWorkflowMessage(t.Context(), testChat,
			workflowNotify("step", testStep, testParent, "success", "tests pass"))
	}
	rows := steerRows(t, deps, testChat)
	deps.turns.chatTurn(testChat)
	for i := range n {
		tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
			"messageId": fmt.Sprintf("notify-%d", i), "content": "[notification/success] tests pass",
			"notificationSeverity": "success",
		}), FrameAttribution{})
	}

	if got := steerRows(t, deps, testChat); len(got) != n {
		t.Fatalf("chat rows after %d take-ups = %d, want %d", n, len(got), n)
	}
	delivered := deliveredOf(t, deps.chatEntries(testChat))
	if len(delivered) != n {
		t.Fatalf("chat stamps = %d, want %d", len(delivered), n)
	}
	for i := range delivered {
		if delivered[i].SteerID != rows[i].ID || delivered[i].KASID != fmt.Sprintf("notify-%d", i) {
			t.Errorf("stamp %d = %+v, want row %q under notify-%d", i, delivered[i], rows[i].ID, i)
		}
	}
}

// kasNotify is workflowNotify carrying the id KAS buffers the message under.
func kasNotify(sender, caller, target, severity, message, notifyID string) *marotte.RPCResponse {
	return notif("_kiro/session/notify", map[string]any{
		"sessionId": target, "callerSessionId": caller, "message": message, "severity": severity,
		"sender": sender, "workflowId": testWfRun, "nodeId": "review", "notifyId": notifyID,
	})
}

// Every row of a message, the receiver's and the sender's, carries the notify frame's notifyId as kas_id, and none
// when the frame carries none.
func TestWorkflowMessage_EveryRowCarriesTheNotifyFramesID(t *testing.T) {
	cases := []struct {
		name   string
		msg    *marotte.RPCResponse
		wantID string
	}{
		{"step to chat", kasNotify("step", testStep, testParent, "success", "done", "notify-step-1"), "notify-step-1"},
		{"chat to step", kasNotify("parent", testParent, testStep, "info", "go on", "notify-parent-1"), "notify-parent-1"},
		{"no notifyId", workflowNotify("step", testStep, testParent, "success", "done"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps, tr := messageDeps(t)
			tr.HandleWorkflowMessage(t.Context(), testChat, c.msg)
			chat, step := steerRows(t, deps, testChat), stepRows(t, deps)
			if len(chat) != 1 || len(step) != 1 {
				t.Fatalf("rows: chat %+v, step %+v, want one each", chat, step)
			}
			if chat[0].KASID != c.wantID || step[0].KASID != c.wantID {
				t.Errorf("kas_id: chat %q, step %q, want %q on both", chat[0].KASID, step[0].KASID, c.wantID)
			}
		})
	}
}
