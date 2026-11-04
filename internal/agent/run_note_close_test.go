package agent

import (
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// A run outlives its parent chat's close: a step note that lands after a reopen goes into the reopened chat's open
// turn, and one that lands after a delete writes nothing.
func TestRunNote_AfterAReopenLandsInTheReopenedChat(t *testing.T) {
	refused := stepClose{outcome: marotte.TurnOutcomeRefused, refusal: &marotte.RefusalInfo{Category: "policy"}}
	path := []string{"wf_1", "build"}
	lease := runlease.Lease{WorkflowID: "wf_1", ChatID: "c1", Origin: runlease.OriginAgent}

	t.Run("reopened", func(t *testing.T) {
		h, cs, _, tab := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		leaseRun(t, h, lease)
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("Setup: open session: %v", err)
		}
		if code := postCloseTab(t, h, tab); code != http.StatusOK {
			t.Fatalf("close_tab = %d, want 200", code)
		}
		awaitTeardown(t, h, "c1")
		reopenTab(t, h, "op-reopen")
		turnID, _ := h.stagePromptTurn(t, "c1")

		h.runs.noteGradedStepEnd(t.Context(), &lease, path, refused)
		endTurn(t, h, "c1", turnID)

		entries, err := cs.All(t.Context(), "c1")
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		var notes []marotte.Entry
		for _, e := range entries {
			if e.Kind == marotte.EntryKindSteer {
				notes = append(notes, e)
			}
		}
		if len(notes) != 1 || notes[0].Turn != turnID {
			t.Errorf("step notes = %+v, want one in the reopened chat's turn %s", notes, turnID)
		}
	})

	t.Run("deleted", func(t *testing.T) {
		h, cs, _, _ := closeTabHub(t)
		t.Cleanup(func() { shutdownHub(t, h) })
		leaseRun(t, h, lease)
		if err := h.Membership().DeleteChatAndCloseTabs(t.Context(), "c1", "op-del", nil); err != nil {
			t.Fatalf("DeleteChatAndCloseTabs = %v", err)
		}
		awaitTeardown(t, h, "c1")

		h.runs.noteGradedStepEnd(t.Context(), &lease, path, refused)

		if p := cs.Presence("c1"); p != chat.PresenceAbsent {
			t.Errorf("the deleted chat's presence after the note = %v, want no record", p)
		}
		if entries, _ := cs.All(t.Context(), "c1"); len(entries) != 0 {
			t.Errorf("the deleted chat holds %+v after the note, want nothing", entries)
		}
	})
}
