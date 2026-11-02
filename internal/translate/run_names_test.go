package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func inspectReply(t *testing.T, run, runLabel, workflowName string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": run,
		"state": map[string]any{
			"workflowId": run, "workflowName": workflowName, "runLabel": runLabel, "status": "running",
			"root": map[string]any{"nodeId": run, "type": "sequence"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *wakeHarness) runNamed() (names []marotte.EntryRunNamed, turns []string) {
	h.t.Helper()
	for _, e := range h.deps.turns.chatLog[testChat] {
		if e.Kind == marotte.EntryKindRunNamed {
			names = append(names, decodePayload[marotte.EntryRunNamed](h.t, &e))
			turns = append(turns, e.Turn)
		}
	}
	return names, turns
}

func (h *wakeHarness) wakeTurnID() string {
	h.t.Helper()
	for _, e := range h.deps.turns.chatLog[testChat] {
		if e.Kind == marotte.EntryKindTurnWake {
			return e.Turn
		}
	}
	h.t.Fatal("no turn_wake in the chat's log")
	return ""
}

func TestRunName_AWakeRecordedBeforeItsRunsNameAsksOnceAndIsNamedWhenTheReadLands(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")

	if w := h.onlyWake(); w.Steers[0].RunName != "" {
		t.Errorf("turn_wake run name = %q, want none yet: no read has named the run", w.Steers[0].RunName)
	}
	if !slices.Equal(h.deps.runReads, []string{wakeRun}) {
		t.Errorf("reads asked = %v, want one read of %s", h.deps.runReads, wakeRun)
	}
	h.content("chunk_send_message_wake")
	if len(h.deps.runReads) != 1 {
		t.Errorf("reads asked = %v, want still one: the run is already being read", h.deps.runReads)
	}

	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", "workflow-from-prompt"))
	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", "workflow-from-prompt"))

	names, turns := h.runNamed()
	if len(names) != 1 || names[0] != (marotte.EntryRunNamed{Run: wakeRun, Name: "publish-docs"}) {
		t.Fatalf("run_named entries = %+v, want one naming %s publish-docs", names, wakeRun)
	}
	if turns[0] != h.wakeTurnID() {
		t.Errorf("run_named filed in turn %s, want the open wake turn %s", turns[0], h.wakeTurnID())
	}
}

func TestRunName_ANameLearnedAfterTheWakeTurnClosedIsFiledBetweenTurns(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")
	closeChatTurn(t, h.deps, testChat)

	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", ""))

	between := h.deps.between[testChat]
	last := len(between) - 1
	if last < 0 || between[last].Kind != marotte.EntryKindRunNamed ||
		decodePayload[marotte.EntryRunNamed](t, &between[last]).Name != "publish-docs" {
		t.Errorf("between-turns entries = %+v, want the run_named filed after the wake turn's close", between)
	}
}

func TestRunName_KASsGenericPromptRecipeNamesNoRun(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")

	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "", "workflow-from-prompt"))

	if names, _ := h.runNamed(); len(names) != 0 {
		t.Errorf("run_named entries = %+v, want none: workflow-from-prompt is every prompt-built run's recipe", names)
	}
	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "", "release-notes"))
	if names, _ := h.runNamed(); len(names) != 1 || names[0].Name != "release-notes" {
		t.Errorf("run_named entries = %+v, want the recipe's own name", names)
	}
}

func TestRunName_ARunNamedBeforeItsWakeIsNamedInTheWake(t *testing.T) {
	h := newWakeHarness(t)
	h.send("run_complete_repeat_cap")
	h.send("notify_repeat_cap")
	h.wake("chunk_send_message_wake")

	if w := h.onlyWake(); w.Steers[0].RunName != "publish-docs" {
		t.Errorf("turn_wake run name = %q, want the run_complete's runLabel", w.Steers[0].RunName)
	}
	if len(h.deps.runReads) != 0 {
		t.Errorf("reads asked = %v, want none: the name was known", h.deps.runReads)
	}
	if names, _ := h.runNamed(); len(names) != 0 {
		t.Errorf("run_named entries = %+v, want none: the wake carries the name", names)
	}
}

func TestRunName_ASecondWakeOnAnUnnamedRunAsksNoSecondRead(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")
	closeChatTurn(t, h.deps, testChat)
	h.send("notify_info")
	h.wake("chunk_send_message_wake")

	if !slices.Equal(h.deps.runReads, []string{wakeRun}) {
		t.Errorf("reads asked = %v, want one: the first read names both wakes", h.deps.runReads)
	}
}

func TestRunName_AWakeAfterAReadThatNamedNothingAsksAgain(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")
	h.deps.readDone[0]()
	closeChatTurn(t, h.deps, testChat)
	h.send("notify_info")
	h.wake("chunk_send_message_wake")

	if !slices.Equal(h.deps.runReads, []string{wakeRun, wakeRun}) {
		t.Errorf("reads asked = %v, want a second: the first read ended with no name", h.deps.runReads)
	}
	h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", ""))
	if names, _ := h.runNamed(); len(names) != 1 {
		t.Errorf("run_named entries = %+v, want one: the chat awaited the name through both reads", names)
	}
}

func TestRunName_AfterARestartTheLeaseChatsUnnamedWakeIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lease   bool
		named   bool
		wantNew int
	}{
		{name: "the lease chat's log holds the unnamed wake", lease: true, wantNew: 1},
		{name: "no lease names the run's chat", wantNew: 0},
		{name: "a run_named already names the wake", lease: true, named: true, wantNew: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWakeHarness(t)
			h.send("notify_warning")
			h.wake("chunk_send_message_wake")
			closeChatTurn(t, h.deps, testChat)
			if tc.named {
				h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", ""))
			}
			before, _ := h.runNamed()
			h.tr.runNames = newRunNames()
			if tc.lease {
				h.deps.leaseChats = map[string]marotte.ChatID{wakeRun: testChat}
			}

			h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", ""))
			h.tr.RecordInspect(t.Context(), inspectReply(t, wakeRun, "publish-docs", ""))

			after, _ := h.runNamed()
			if got := len(after) - len(before); got != tc.wantNew {
				t.Errorf("run_named entries filed = %d, want %d (%+v)", got, tc.wantNew, after)
			}
		})
	}
}

func TestRunName_ARunStartCarriesTheNameARunReadLearned(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	tr := New(rolesOf(capturing(&events)))
	tr.RecordInspect(t.Context(), inspectReply(t, "wf_1", "Fix the run bar", "workflow-from-prompt"))

	tr.HandleRunStart(t.Context(), testChat,
		notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "workflow-from-prompt"}))

	if p, _ := events[0].Payload.(marotte.RunStartedPayload); p.Name != "Fix the run bar" {
		t.Errorf("run_started name = %q, want the runLabel the read learned", p.Name)
	}
	if got := tr.RunName("wf_1"); got != "Fix the run bar" {
		t.Errorf("RunName = %q, want Fix the run bar", got)
	}
}

func TestRunName_ARunStartOfAnUnnamedRunAsksForOneRead(t *testing.T) {
	t.Parallel()
	var events []marotte.ServerEvent
	d := capturing(&events)
	tr := New(rolesOf(d))
	start := notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "workflow-from-prompt"})

	tr.HandleRunStart(t.Context(), testChat, start)
	tr.HandleRunStart(t.Context(), testChat, start)
	if !slices.Equal(d.runReads, []string{"wf_1"}) {
		t.Fatalf("reads asked = %v, want one read of wf_1: the second start finds it in flight", d.runReads)
	}

	tr.RecordInspect(t.Context(), inspectReply(t, "wf_1", "Fix the run bar", ""))
	d.readDone[0]()
	tr.HandleRunStart(t.Context(), testChat, start)
	if len(d.runReads) != 1 {
		t.Errorf("reads asked = %v, want still one: the run is named", d.runReads)
	}
}

func TestRunName_AfterARestartARunsTerminalFrameNamesItsChatsUnnamedWake(t *testing.T) {
	h := newWakeHarness(t)
	h.send("notify_warning")
	h.wake("chunk_send_message_wake")
	closeChatTurn(t, h.deps, testChat)
	h.tr.runNames = newRunNames()

	h.tr.HandleRunComplete(t.Context(), testChat, notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId": wakeRun, "status": "completed", "finalState": map[string]any{"runLabel": "publish-docs"},
	}))

	if names, _ := h.runNamed(); len(names) != 1 {
		t.Errorf("run_named entries = %+v, want one: the lease is released before run_complete, so the frame's chat is asked", names)
	}
}
