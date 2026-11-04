package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func rewindAnswer(t *testing.T, rec *httptest.ResponseRecorder) (code int, reason string) {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Reason
}

func rewindTo(t *testing.T, h *Runtime, messageID string) *httptest.ResponseRecorder {
	t.Helper()
	return postCmdWithin(t, h, marotte.ClientCommand{
		Type: marotte.CmdRewindChat, ChatID: "c1", Payload: json.RawMessage(`{"message_id":"` + messageID + `"}`),
	})
}

// A close's drain holds the chat's slot while it looks for a queued prompt, with no turn running; a rewind landing in
// that window waits for the slot as a prompt does, and reverts.
func TestRewindChat_WaitsOutAReservationThatOpensNoTurn(t *testing.T) {
	h, cs, br := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	runPrompt(t, h, cs, "c1", "m-1")
	br.setCallResult(marotte.MethodCheckpointRevertMultiple, json.RawMessage(`{"success":true}`))
	res, ok := h.coord.TryReserveTurn("c1", marotte.TurnSourcePrompt, h.coord.LifeStamp())
	if !ok {
		t.Fatal("Setup: the chat's admission slot was already held")
	}
	time.AfterFunc(50*time.Millisecond, func() { h.coord.ReleaseTurnReservation(res) })

	rec := rewindTo(t, h, "m-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("rewind beside a reservation that opened no turn = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !slices.ContainsFunc(logOf(t, cs, "c1"), func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindTurnRevert }) {
		t.Error("the rewind answered 200 with no revert recorded")
	}
}

// A rewind while a turn runs is refused at once, with a reason the client can tell apart.
func TestRewindChat_ARunningTurnRefusesAtOnceWithItsReason(t *testing.T) {
	shrinkAdmissionWait(t, 5*time.Second)
	h, _, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	h.stagePromptTurn(t, "c1")

	start := time.Now()
	rec := rewindTo(t, h, "m-c1")

	if code, reason := rewindAnswer(t, rec); code != http.StatusConflict || reason != "turn_open" {
		t.Errorf("rewind beside a running turn = %d reason %q, want 409 turn_open", code, reason)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the refusal took %v, want it at once rather than after the %v wait", elapsed, 5*time.Second)
	}
}

// A slot held past the wait refuses with its own reason, not as a running turn.
func TestRewindChat_ASlotHeldPastTheWaitRefusesAsBusy(t *testing.T) {
	shrinkAdmissionWait(t, 60*time.Millisecond)
	h, cs, _ := newTestHub()
	t.Cleanup(func() { shutdownHub(t, h) })
	openChat(t, h, "c1")
	runPrompt(t, h, cs, "c1", "m-1")
	res, ok := h.coord.TryReserveTurn("c1", marotte.TurnSourceRevert, h.coord.LifeStamp())
	if !ok {
		t.Fatal("Setup: the chat's admission slot was already held")
	}
	t.Cleanup(func() { h.coord.ReleaseTurnReservation(res) })

	rec := rewindTo(t, h, "m-1")

	if code, reason := rewindAnswer(t, rec); code != http.StatusConflict || reason != "busy" {
		t.Errorf("rewind beside a held slot = %d reason %q (%s), want 409 busy", code, reason, rec.Body.String())
	}
	if slices.ContainsFunc(logOf(t, cs, "c1"), func(e marotte.Entry) bool { return e.Kind == marotte.EntryKindTurnRevert }) {
		t.Error("a refused rewind recorded a revert")
	}
}
