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
	deps.stepReading = map[marotte.ChatID]bool{}
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

func TestWorkflowMessage_ParentToAnIdleStepIsDeliveredAtTheSend(t *testing.T) {
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
	if c.Origin != marotte.SteerOriginParent || c.Step != "review" || c.State != marotte.SteerStateRead ||
		c.ReadTs != c.ProducedTs || c.ProducedTs == 0 {
		t.Errorf("chat row = %+v, want a read parent message to review, read at its send", c)
	}
	if s := step[0].EntrySteer; s.Step != "" || s.Origin != marotte.SteerOriginParent {
		t.Errorf("step row = %+v, want a parent message naming no step", s)
	}
}

func TestWorkflowMessage_ParentToABusyStepIsDeliveredAtItsTakeUp(t *testing.T) {
	deps, tr := messageDeps(t)
	deps.stepReading[marotte.StepSteerKey(testStep)] = true
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
		{"an unknown step", testChat, workflowNotify("step", "sess_other", testParent, "info", "hi")},
		{"another chat's target", testChat, workflowNotify("step", testStep, "sess_elsewhere", "info", "hi")},
		{"a parent message from elsewhere", testChat, workflowNotify("parent", "sess_elsewhere", testStep, "info", "hi")},
		{"no sender", testChat, workflowNotify("", testStep, testParent, "info", "hi")},
		{"a run bridge", marotte.ChatID("run:" + testWfRun), workflowNotify("step", testStep, testParent, "info", "hi")},
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

func TestWorkflowMessage_StepInfoIsDeliveredAtTheSendOnlyToAnIdleParent(t *testing.T) {
	deps, tr := messageDeps(t)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, testParent, "info", "idle"))
	deps.turns.chatTurn(testChat)
	tr.HandleWorkflowMessage(t.Context(), testChat, workflowNotify("step", testStep, testParent, "info", "busy"))

	rows := steerRows(t, deps, testChat)
	if len(rows) != 2 {
		t.Fatalf("chat rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		read := r.ReadTs != 0
		if want := r.Text == "idle"; read != want {
			t.Errorf("row %q delivered at send = %v, want %v", r.Text, read, want)
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

func TestWorkflowMessage_AClearedMessageIsSettledDroppedWithNoNewRow(t *testing.T) {
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
	if rows[0].Origin != marotte.SteerOriginStep || rows[1].ID != "notify-wf-2" {
		t.Errorf("chat rows = %+v, want the step's row then the cleared run notice", rows)
	}
	if d := deliveredOf(t, deps.between[testChat]); len(d) != 1 || d[0].SteerID != rows[0].ID || !d[0].Dropped {
		t.Errorf("chat stamps = %+v, want the message's row settled dropped", d)
	}
	if d := deliveredOf(t, deps.runEntries(testWfRun, workflow.PathKey([]string{testWfRun, "review"}))); len(d) != 1 || !d[0].Dropped {
		t.Errorf("step stamps = %+v, want the sender's row settled dropped too", d)
	}

	tr.HandleWorkflowMessage(t.Context(), testChat,
		workflowNotify("step", testStep, testParent, "success", "tests pass"))
	deps.turns.chatTurn(testChat)
	tr.HandleSessionInfoUpdate(t.Context(), testChat, steerFrame(t, "steering_injected", map[string]any{
		"messageId": "notify-3", "content": "[notification/success] tests pass", "notificationSeverity": "success",
	}), FrameAttribution{})
	again := steerRows(t, deps, testChat)
	if d := deliveredOf(t, deps.chatEntries(testChat)); len(d) != 1 || len(again) != 3 || d[0].SteerID != again[2].ID {
		t.Errorf("take-up stamps = %+v over rows %+v, want the second message's row", d, again)
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
	deps.stepReading[marotte.StepSteerKey(testStep)] = true
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
	deps.stepReading[marotte.StepSteerKey(testStep)] = true
	deps.stepReading[marotte.StepSteerKey(other)] = true
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
