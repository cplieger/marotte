package agent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// promptRecipe is the workflowName KAS gives every run built from a prompt rather than a recipe.
const promptRecipe = "workflow-from-prompt"

// labelledInspect is a KAS inspect reply for a running run with the given runLabel and workflowName.
func labelledInspect(t *testing.T, workflowID, runLabel, workflowName string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"workflowId": workflowID,
		"state": map[string]any{
			"workflowId": workflowID, "status": "running", "runLabel": runLabel, "workflowName": workflowName,
		},
	})
	if err != nil {
		t.Fatalf("marshal inspect: %v", err)
	}
	return raw
}

func busyRun(t *testing.T, h *Runtime, workflowID string) marotte.EngineBusyRun {
	t.Helper()
	runs := h.engine.status().Busy.Runs
	i := slices.IndexFunc(runs, func(r marotte.EngineBusyRun) bool { return r.WorkflowID == workflowID })
	if i < 0 {
		t.Fatalf("busy runs = %+v, want %s among them", runs, workflowID)
	}
	return runs[i]
}

func TestEngineStatus_NamesABusyRunByItsDisplayNameNeverItsRecipe(t *testing.T) {
	h, _, _ := restartFixture(t)
	deadline := time.Now().Add(time.Hour)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_read", Recipe: promptRecipe, Deadline: deadline})
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_unread", Recipe: promptRecipe, Deadline: deadline})
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_recipe", Recipe: "release-notes", Deadline: deadline})
	h.translator.RecordInspect(t.Context(), labelledInspect(t, "wf_read", "Fix the run bar", promptRecipe))

	for id, want := range map[string]string{
		"wf_read":   "Fix the run bar",
		"wf_unread": "",
		"wf_recipe": "release-notes",
	} {
		if got := busyRun(t, h, id).Name; got != want {
			t.Errorf("busy run %s is named %q, want %q", id, got, want)
		}
	}
}

func TestEngineStatus_NamesABusyRunsLaunchingChat(t *testing.T) {
	h, _, _ := restartFixture(t)
	if _, err := h.chatStore.Mutate(t.Context(), "c-1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Findings cleanup"
		return true
	}); err != nil {
		t.Fatalf("Setup: chat: %v", err)
	}
	deadline := time.Now().Add(time.Hour)
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_chat", ChatID: "c-1", Recipe: "review", Deadline: deadline})
	leaseRun(t, h, runlease.Lease{WorkflowID: "wf_parentless", Recipe: "review", Deadline: deadline})

	if got := busyRun(t, h, "wf_chat").ChatName; got != "Findings cleanup" {
		t.Errorf("chat-launched run's chat = %q, want Findings cleanup", got)
	}
	if got := busyRun(t, h, "wf_parentless").ChatName; got != "" {
		t.Errorf("parentless run's chat = %q, want none", got)
	}
}

func TestLiveRuns_NamesALeasedRunByItsDisplayName(t *testing.T) {
	h, _ := heldRun(t, "wf_held", promptRecipe)
	h.translator.RecordInspect(t.Context(), labelledInspect(t, "wf_held", "Fix the run bar", promptRecipe))

	got := h.runs.LiveRuns([]string{"wf_held"})
	want := []command.LiveRunRef{{ID: "wf_held", Label: "Fix the run bar"}}
	if !slices.Equal(got, want) {
		t.Errorf("LiveRuns = %+v, want %+v", got, want)
	}
}

func TestToWorkflowRuns_NamesARunByItsLabelAndNeverByThePromptRecipe(t *testing.T) {
	h := ownedBy(t, map[string][]string{})
	got := h.runs.toWire(h.claimedSessions(t.Context()), []kasWorkflowRun{
		{WorkflowID: "wf_labelled", Name: "Fix the run bar", WorkflowName: promptRecipe},
		{WorkflowID: "wf_unlabelled", Name: promptRecipe, WorkflowName: promptRecipe},
		{WorkflowID: "wf_recipe", Name: "release-notes", WorkflowName: "release-notes"},
	})

	type named struct{ name, recipe string }
	want := map[string]named{
		"wf_labelled":   {"Fix the run bar", ""},
		"wf_unlabelled": {"", ""},
		"wf_recipe":     {"release-notes", "release-notes"},
	}
	for i := range got {
		r := &got[i]
		if w := want[r.WorkflowID]; (named{r.Name, r.WorkflowName}) != w {
			t.Errorf("%s = (name %q, recipe %q), want (%q, %q)", r.WorkflowID, r.Name, r.WorkflowName, w.name, w.recipe)
		}
	}
}

func TestHandleRun_CarriesTheRunsDisplayName(t *testing.T) {
	for _, tc := range []struct {
		name, runLabel, workflowName, want string
	}{
		{"a labelled run", "Fix the run bar", promptRecipe, "Fix the run bar"},
		{"a prompt-built run with no label", "", promptRecipe, ""},
		{"a recipe run", "", "release-notes", "release-notes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := seedChatParentedRun(t, true)
			br.setCallResult(methodKiroWorkflowInspect, labelledInspect(t, "wf_1", tc.runLabel, tc.workflowName))

			_, body := getRun(t, h, "wf_1")

			var reply struct {
				DisplayName *string `json:"display_name"`
			}
			if err := json.Unmarshal([]byte(body), &reply); err != nil {
				t.Fatalf("decoding the run reply: %s", err)
			}
			if reply.DisplayName == nil || *reply.DisplayName != tc.want {
				t.Errorf("display_name in %s, want %q", body, tc.want)
			}
		})
	}
}
