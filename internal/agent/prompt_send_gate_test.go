package agent

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

// admitPromptBehindSteerLock opens c1's bridge and posts a prompt that is admitted and acknowledged while the steer
// lock keeps it short of its session/prompt; the caller reports KAS's state, then calls the returned unlock.
func admitPromptBehindSteerLock(t *testing.T, h *Runtime, cs *testChatStore, text string) func() {
	t.Helper()
	seedChat(t, cs, "c1")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: open bridge: %v", err)
	}
	unlock, err := h.steerQueue.LockSteerOps(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Setup: lock steer ops: %v", err)
	}
	rec := postCmd(t, h, marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: json.RawMessage(`{"text":"` + text + `","message_id":"m-2"}`),
	})
	if rec.Code != http.StatusOK {
		unlock()
		t.Fatalf("Setup: prompt = %d %s, want admitted on an idle chat", rec.Code, rec.Body.String())
	}
	return unlock
}

// KAS can report a run (a notification re-wake) after the prompt took its admission and before its session/prompt
// went out: the send waits for KAS to report idle, then goes exactly once, as written.
func TestCmdPrompt_ARunReportedAfterAdmissionHoldsTheSend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		prompted := promptCalls(br)
		unlock := admitPromptBehindSteerLock(t, h, cs, "Please tidy the settings page")

		h.coord.WireTurnActivity("c1", true)
		unlock()
		synctest.Wait()
		if got := prompted(); len(got) != 0 {
			t.Fatalf("session/prompt blocks = %v while KAS reported another run, want none", got)
		}

		h.coord.WireTurnActivity("c1", false)
		synctest.Wait()
		if got := prompted(); len(got) != 1 || !strings.Contains(got[0], "Please tidy the settings page") {
			t.Errorf("session/prompt blocks = %v once KAS reported idle, want the user's text once", got)
		}
	})
}

// While the send waits, KAS's own run can take the prompt's turn (its turn_start bound it and no agent-initiated frame
// revised that): the prompt never reached kiro-cli, so it fails loudly, unsent, rather than going out with no turn.
func TestCmdPrompt_APromptWhoseTurnKASTookDuringTheWaitFailsUnsent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cs, br := newTestHub()
		defer shutdownHub(t, h)
		countDrains(h)
		prompted := promptCalls(br)
		unlock := admitPromptBehindSteerLock(t, h, cs, "Please tidy the settings page")

		h.coord.WireTurnActivity("c1", true)
		h.coord.WireTurnStart(t.Context(), "c1")
		unlock()
		synctest.Wait()
		h.coord.WireTurnActivity("c1", false)
		h.coord.WireTurnEnd(t.Context(), "c1", marotte.StopReasonEndTurn)
		synctest.Wait()

		if got := prompted(); len(got) != 0 {
			t.Errorf("session/prompt blocks = %v after KAS's run took the prompt's turn, want none", got)
		}
		var failed []string
		for _, e := range eventsOfType(h, marotte.EventError) {
			var p marotte.ErrorPayload
			if json.Unmarshal(e.Payload, &p) == nil && p.Code == marotte.ErrCodePromptFailed {
				failed = append(failed, p.Message)
			}
		}
		if len(failed) != 1 || !strings.Contains(failed[0], "send it again") {
			t.Errorf("prompt_failed messages = %q, want one telling the user to send it again", failed)
		}
	})
}
