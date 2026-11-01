package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// inspectReply is a `_kiro/workflow/inspect` reply for run carrying KAS's runLabel and workflowName.
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

// runNamed is every run_named in the chat's log with the turn it was filed in, in file order.
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
