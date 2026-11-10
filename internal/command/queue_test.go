package command

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func queueReq(t *testing.T, text, messageID string, attachments ...marotte.Attachment) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.QueuePromptCommand{Text: text, MessageID: messageID, Attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload}
}

// liveRun is a launchingRun behind a running turn: every queue_prompt is queued.
func liveRun() *launchingRun {
	q := runOver(&benchDeps{})
	q.live = true
	return q
}

func TestQueuePrompt_QueuesTheRowWithItsAttachments(t *testing.T) {
	q := liveRun()
	att := marotte.Attachment{Path: "notes.md", Name: "notes.md"}

	for range 2 {
		if _, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, "then run the tests", "m-1", att)); err != nil {
			t.Fatalf("CmdQueuePrompt: %v", err)
		}
	}

	rows := q.queued()
	if len(rows) != 1 {
		t.Fatalf("queued %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].ID != "m-1" || rows[0].Text != "then run the tests" || len(rows[0].Attachments) != 1 || rows[0].Attachments[0].Path != "notes.md" {
		t.Errorf("row = %+v, want m-1 with its text and the attachment path", rows[0])
	}
}

func TestQueuePrompt_AcceptsAnAttachmentOnlyRow(t *testing.T) {
	q := liveRun()
	if _, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, "", "m-1", marotte.Attachment{Path: "shot.png"})); err != nil {
		t.Fatalf("CmdQueuePrompt(attachment only): %v", err)
	}
	if _, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, "  ", "m-2")); statusOf(err) != http.StatusBadRequest {
		t.Errorf("CmdQueuePrompt(blank, no attachment) status = %d, want 400", statusOf(err))
	}
}

func TestQueuePrompt_IdleIsNoTurn(t *testing.T) {
	q := runOver(&benchDeps{})

	_, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, "later", "m-1"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonNoTurn {
		t.Errorf("status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonNoTurn)
	}
	if rows := q.queued(); len(rows) != 0 {
		t.Errorf("an idle chat stored %d rows nothing would ever send", len(rows))
	}
}

func TestQueuePrompt_RefusesPastItsCaps(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		rows := []*marotte.QueuedPrompt{{ID: "m-held", Resends: []string{"steer-0"}}}
		for i := range marotte.MaxQueuedPrompts - 3 {
			rows = append(rows, &marotte.QueuedPrompt{ID: fmt.Sprintf("m-x%d", i)})
		}
		if err := RowCapsRefusal(rows, &marotte.QueuedPrompt{ID: "m-last"}); err != nil {
			t.Fatalf("a row below the cap = %v, want nil", err)
		}
		rows = append(rows, &marotte.QueuedPrompt{ID: "m-last"})
		if err := RowCapsRefusal(rows, &marotte.QueuedPrompt{ID: "m-over"}); err != ErrSubmitFull {
			t.Errorf("a row past the user cap = %v, want ErrSubmitFull", err)
		}
	})
	t.Run("encoded size", func(t *testing.T) {
		big := strings.Repeat("x", maxQueuedEncodedBytes/2)
		rows := []*marotte.QueuedPrompt{{ID: "m-a", Text: big}}
		if err := RowCapsRefusal(rows, &marotte.QueuedPrompt{ID: "m-b", Text: big}); err != ErrSubmitFull {
			t.Errorf("rows past the encoded cap = %v, want ErrSubmitFull", err)
		}
	})
	t.Run("text", func(t *testing.T) {
		q := liveRun()
		_, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, strings.Repeat("x", MaxPromptBytes+1), "m-1"))
		if statusOf(err) != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", statusOf(err))
		}
	})
	t.Run("attachments", func(t *testing.T) {
		q := liveRun()
		atts := make([]marotte.Attachment, marotte.MaxAttachments+1)
		for i := range atts {
			atts[i] = marotte.Attachment{Path: "a.txt", Name: "a.txt"}
		}
		_, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, queueReq(t, "with files", "m-1", atts...))
		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", statusOf(err))
		}
	})
}

func unqueueReq(id string) *marotte.ClientCommand {
	return &marotte.ClientCommand{ChatID: "c1", Payload: json.RawMessage(`{"message_id":"` + id + `"}`)}
}

func TestUnqueuePrompt_AnAbsentIDIsSuccessAndASendingRowIsConflict(t *testing.T) {
	q := liveRun()
	q.rows = []marotte.QueuedPrompt{{ID: "m-1", Text: "a"}, {ID: "m-2", Text: "b"}}
	for range 2 {
		if _, err := cmdUnqueuePrompt(t.Context(), q, unqueueReq("m-1")); err != nil {
			t.Fatalf("unqueue m-1: %v", err)
		}
	}
	if rows := q.queued(); len(rows) != 1 || rows[0].ID != "m-2" {
		t.Errorf("rows = %+v, want only m-2", rows)
	}

	q.unqueue = func(string) error { return ErrRowSending }
	_, err := cmdUnqueuePrompt(t.Context(), q, unqueueReq("m-2"))
	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonSending {
		t.Errorf("unqueue of a sending row: status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonSending)
	}
}

func TestSetInterruptMode_PersistsAndRejectsUnknown(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	q := &launchingRun{}
	req := func(mode string) *marotte.ClientCommand {
		return &marotte.ClientCommand{ChatID: "c1", Payload: json.RawMessage(`{"mode":"` + mode + `"}`)}
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, q, req("queue")); err != nil {
		t.Fatalf("set queue: %v", err)
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok || c.InterruptMode != marotte.InterruptQueue {
		t.Fatalf("record = %+v, want an auto-created chat in queue mode", c)
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, q, req("auto")); statusOf(err) != http.StatusBadRequest {
		t.Errorf("unknown mode status = %d, want 400", statusOf(err))
	}
	if c, _ := store.Get(t.Context(), "c1"); c.InterruptMode != marotte.InterruptQueue {
		t.Errorf("an unknown mode changed the record to %q", c.InterruptMode)
	}
	if _, err := cmdSetInterruptMode(t.Context(), store, q, req("queue")); err != nil {
		t.Fatalf("set queue again: %v", err)
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, q, req("steer")); err != nil {
		t.Fatalf("set steer: %v", err)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.InterruptMode != "" {
		t.Errorf("steer stored as %q, want the empty default", c.InterruptMode)
	}
	if !slices.Equal(q.modes, []marotte.ChatID{"c1", "c1"}) {
		t.Errorf("run order republished for %v, want once per stored change", q.modes)
	}
}

// A queued row's launch sends it with its label.
func TestQueuePrompt_ALabelledRowIsSentWithItsLabel(t *testing.T) {
	q := liveRun()
	payload, err := json.Marshal(marotte.QueuePromptCommand{Text: "the findings", MessageID: "m-1", DisplayText: "Merging findings"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmdQueuePrompt(t.Context(), q, q.roles.admission, &marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload}); err != nil {
		t.Fatalf("CmdQueuePrompt: %v", err)
	}
	row := q.queued()[0]
	if row.Label != "Merging findings" {
		t.Fatalf("queued row label = %q, want the label kept", row.Label)
	}

	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	roles := drainRoles(h, newStubSteerQueue())
	_, _ = launchQueuedRow(t.Context(), roles, &EntryLaunch{
		ID: row.ID, Kind: marotte.RunKindRow, Row: row, Res: testReservation("c1", true),
	})
	h.waitInflight(t)

	if len(h.opened) != 1 || h.opened[0].Text != "the findings" || h.opened[0].Label != "Merging findings" {
		t.Errorf("launched prompts = %+v, want the row sent with its text and label", h.opened)
	}
}

// sendNowRun answers a send-now with arm: a stop of the running turn requested on d, or nothing.
func sendNowRun(d *cancelDeps, arm StopArm, err error) *launchingRun {
	q := runOver(&benchDeps{})
	q.sendNow = func(string, string, bool) (RunStop, error) {
		if err != nil || arm == StopNothing {
			return RunStop{}, err
		}
		return RunStop{Stop: d.RequestStop("c1", marotte.TurnStopReader), Arm: arm}, nil
	}
	return q
}

func sendNow(t *testing.T, q *launchingRun, d *cancelDeps, payload string) (any, error) {
	t.Helper()
	cmd := &marotte.ClientCommand{Type: marotte.CmdSendQueuedNow, ChatID: "c1", Payload: json.RawMessage(payload)}
	return cmdSendQueuedNow(t.Context(), q, d, d, d, d, cmd)
}

// stoppedOf reads a stop's answer: whether it stopped anything.
func stoppedOf(t *testing.T, got any) bool {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Stopped bool `json:"stopped"`
	}
	return json.Unmarshal(raw, &body) == nil && body.Stopped
}

func TestSendQueuedNow_StopsTheTurnTheListNamed(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)

	got, err := sendNow(t, sendNowRun(d, StopCancel, nil), d, `{"message_id":"m-q3","stop":"t-1"}`)
	if err != nil {
		t.Fatalf("send_queued_now = %v", err)
	}
	if !stoppedOf(t, got) {
		t.Errorf("answer = %v, want stopped", got)
	}
	if len(b.notified) != 1 || b.notified[0] != marotte.MethodCancel {
		t.Errorf("notifications = %v, want [%s]", b.notified, marotte.MethodCancel)
	}
	if d.killed != 1 {
		t.Errorf("KillForTurn calls = %d, want the turn's terminals stopped as cancel stops them", d.killed)
	}
}

func TestSendQueuedNow_ARowTheListStopsNothingForSendsNoCancel(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)

	got, err := sendNow(t, sendNowRun(d, StopNothing, nil), d, `{"message_id":"m-q1"}`)
	if err != nil {
		t.Errorf("send_queued_now = %v, want success", err)
	}
	if stoppedOf(t, got) || len(b.notified) != 0 || len(d.scopes) != 0 {
		t.Errorf("answer %v, notifications = %v, permission scopes = %v; want nothing stopped", got, b.notified, d.scopes)
	}
}

func TestSendQueuedNow_AHeldRowIsConflict(t *testing.T) {
	b := &cancelBridge{}
	d := newCancelDeps(b)

	_, err := sendNow(t, sendNowRun(d, StopNothing, ErrRowHeld), d, `{"message_id":"m-h"}`)

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonHeld {
		t.Errorf("send_queued_now(held) = %d %q, want 409 %q", statusOf(err), reasonOf(err), reasonHeld)
	}
	if len(b.notified) != 0 {
		t.Errorf("notifications = %v, want nothing stopped", b.notified)
	}
}

func TestSendQueuedNow_AnEmptyIDIsBadRequest(t *testing.T) {
	d := newCancelDeps(&cancelBridge{})
	for _, payload := range []string{`{}`, `{"message_id":""}`, `not json`} {
		if _, err := sendNow(t, sendNowRun(d, StopNothing, nil), d, payload); statusOf(err) != http.StatusBadRequest {
			t.Errorf("send_queued_now(%s) status = %d, want 400", payload, statusOf(err))
		}
	}
}

// A cancel that never reached KAS ends no turn: the list undoes the stop's marks and the row's send-now mark, and the
// answer is 502.
func TestSendQueuedNow_AnUndeliveredCancelUndoesTheStopAndIsBadGateway(t *testing.T) {
	d := newCancelDeps(&cancelBridge{})
	d.err = fmt.Errorf("%w: write refused", ErrCancelNotDelivered)
	q := sendNowRun(d, StopCancel, nil)

	_, err := sendNow(t, q, d, `{"message_id":"m-q2"}`)

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d (%v), want 502", statusOf(err), err)
	}
	if len(q.undone) != 1 || len(q.cleared) != 1 || q.cleared[0] != "m-q2" {
		t.Errorf("stops undone = %d, marks cleared = %v; want the stop undone and m-q2's mark cleared", len(q.undone), q.cleared)
	}
}
