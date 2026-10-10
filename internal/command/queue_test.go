package command

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// A row whose id is in sending is one whose turn this process opened.
type fakeQueue struct {
	chat    *marotte.Chat
	sending map[string]bool
	live    bool
}

func (q *fakeQueue) AppendIfLive(_ context.Context, _ marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	if !q.live {
		return false, nil
	}
	return true, fn(q.chat)
}

func (q *fakeQueue) Unqueue(_ context.Context, _ marotte.ChatID, id string) (bool, error) {
	if q.sending[id] {
		return true, nil
	}
	q.chat.QueuedPrompts = slices.DeleteFunc(q.chat.QueuedPrompts, func(r marotte.QueuedPrompt) bool { return r.ID == id })
	return false, nil
}

func (q *fakeQueue) NextUserRow(context.Context, marotte.ChatID) (marotte.QueuedPrompt, bool, error) {
	for _, r := range q.chat.QueuedPrompts {
		if !r.Held && !r.Carried() && !q.sending[r.ID] {
			return r, true, nil
		}
	}
	return marotte.QueuedPrompt{}, false, nil
}

func (q *fakeQueue) Dequeue(_ context.Context, _ marotte.ChatID, id string) error {
	if q.sending == nil {
		q.sending = map[string]bool{}
	}
	q.sending[id] = true
	q.chat.QueuedPrompts = slices.DeleteFunc(q.chat.QueuedPrompts, func(r marotte.QueuedPrompt) bool { return r.ID == id })
	return nil
}

// unqueueRoles is the prompt roles an unqueue reads: the steer lock and the queue.
func unqueueRoles(q *fakeQueue) *promptRoles {
	return &promptRoles{queue: newStubSteerQueue(), followups: q}
}

func queueReq(t *testing.T, text, messageID string, attachments ...marotte.Attachment) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.QueuePromptCommand{Text: text, MessageID: messageID, Attachments: attachments})
	if err != nil {
		t.Fatal(err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload}
}

func TestQueuePrompt_AppendsWhileLive(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}
	att := marotte.Attachment{Path: "notes.md", Name: "notes.md"}

	if _, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "then run the tests", "m-1", att)); err != nil {
		t.Fatalf("CmdQueuePrompt: %v", err)
	}
	if _, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "then run the tests", "m-1", att)); err != nil {
		t.Fatalf("CmdQueuePrompt repeat: %v", err)
	}

	rows := q.chat.QueuedPrompts
	if len(rows) != 1 {
		t.Fatalf("queued %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].ID != "m-1" || rows[0].Text != "then run the tests" || len(rows[0].Attachments) != 1 || rows[0].Attachments[0].Path != "notes.md" {
		t.Errorf("row = %+v, want m-1 with its text and the attachment path", rows[0])
	}
	if rows[0].Held || rows[0].Carried() {
		t.Errorf("row = %+v, want a plain user row", rows[0])
	}
}

func TestQueuePrompt_AcceptsAnAttachmentOnlyRow(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}
	if _, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "", "m-1", marotte.Attachment{Path: "shot.png"})); err != nil {
		t.Fatalf("CmdQueuePrompt(attachment only): %v", err)
	}
	if _, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "  ", "m-2")); statusOf(err) != http.StatusBadRequest {
		t.Errorf("CmdQueuePrompt(blank, no attachment) status = %d, want 400", statusOf(err))
	}
}

func TestQueuePrompt_IdleIsNoTurn(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}}

	_, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "later", "m-1"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonNoTurn {
		t.Errorf("status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonNoTurn)
	}
	if len(q.chat.QueuedPrompts) != 0 {
		t.Errorf("an idle chat stored %d rows nothing would ever send", len(q.chat.QueuedPrompts))
	}
}

func TestQueuePrompt_RefusesOverCap(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		q := &fakeQueue{chat: &marotte.Chat{ID: "c1", QueuedPrompts: []marotte.QueuedPrompt{
			{ID: "m-held", Resends: []string{"steer-0"}, Held: true},
			{ID: "m-carry", Resends: []string{"steer-1"}},
		}}, live: true}
		userCap := marotte.MaxQueuedPrompts - 2
		for i := range userCap - 1 {
			q.chat.QueuedPrompts = append(q.chat.QueuedPrompts, marotte.QueuedPrompt{ID: "m-x" + string(rune('a'+i))})
		}
		if _, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "the last one", "m-last")); err != nil {
			t.Fatalf("CmdQueuePrompt below the cap: %v", err)
		}
		_, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "one more", "m-over"))
		if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonFull {
			t.Errorf("status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonFull)
		}
		if len(q.chat.QueuedPrompts) != marotte.MaxQueuedPrompts {
			t.Errorf("queue holds %d rows, want %d", len(q.chat.QueuedPrompts), marotte.MaxQueuedPrompts)
		}
	})
	t.Run("text", func(t *testing.T) {
		q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}
		_, err := cmdQueuePrompt(t.Context(), q, queueReq(t, strings.Repeat("x", marotte.MaxDraftBytes+1), "m-1"))
		if statusOf(err) != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", statusOf(err))
		}
	})
	t.Run("attachments", func(t *testing.T) {
		q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}
		atts := make([]marotte.Attachment, marotte.MaxAttachments+1)
		for i := range atts {
			atts[i] = marotte.Attachment{Path: "a.txt", Name: "a.txt"}
		}
		_, err := cmdQueuePrompt(t.Context(), q, queueReq(t, "with files", "m-1", atts...))
		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", statusOf(err))
		}
	})
}

func unqueueReq(id string) *marotte.ClientCommand {
	return &marotte.ClientCommand{ChatID: "c1", Payload: json.RawMessage(`{"message_id":"` + id + `"}`)}
}

func TestUnqueuePrompt_RemovesTheRowAndAnAbsentIDIsSuccess(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1", QueuedPrompts: []marotte.QueuedPrompt{{ID: "m-1", Text: "a"}, {ID: "m-2", Text: "b"}}}}

	if _, err := cmdUnqueuePrompt(t.Context(), unqueueRoles(q), unqueueReq("m-1")); err != nil {
		t.Fatalf("unqueue m-1: %v", err)
	}
	if _, err := cmdUnqueuePrompt(t.Context(), unqueueRoles(q), unqueueReq("m-1")); err != nil {
		t.Errorf("unqueue of an absent row = %v, want success", err)
	}

	if rows := q.chat.QueuedPrompts; len(rows) != 1 || rows[0].ID != "m-2" {
		t.Errorf("rows = %+v, want only m-2", rows)
	}
}

func TestUnqueuePrompt_ARowBeingSentIsConflict(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1", QueuedPrompts: []marotte.QueuedPrompt{{ID: "m-1", Text: "a"}}}, sending: map[string]bool{"m-1": true}}

	_, err := cmdUnqueuePrompt(t.Context(), unqueueRoles(q), unqueueReq("m-1"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != reasonSending {
		t.Errorf("unqueue of a sending row: status = %d reason = %q, want 409 %q", statusOf(err), reasonOf(err), reasonSending)
	}
}

func TestSetInterruptMode_PersistsAndRejectsUnknown(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	req := func(mode string) *marotte.ClientCommand {
		return &marotte.ClientCommand{ChatID: "c1", Payload: json.RawMessage(`{"mode":"` + mode + `"}`)}
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, req("queue")); err != nil {
		t.Fatalf("set queue: %v", err)
	}
	c, ok := store.Get(t.Context(), "c1")
	if !ok || c.InterruptMode != marotte.InterruptQueue {
		t.Fatalf("record = %+v, want an auto-created chat in queue mode", c)
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, req("auto")); statusOf(err) != http.StatusBadRequest {
		t.Errorf("unknown mode status = %d, want 400", statusOf(err))
	}
	if c, _ := store.Get(t.Context(), "c1"); c.InterruptMode != marotte.InterruptQueue {
		t.Errorf("an unknown mode changed the record to %q", c.InterruptMode)
	}

	if _, err := cmdSetInterruptMode(t.Context(), store, req("steer")); err != nil {
		t.Fatalf("set steer: %v", err)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.InterruptMode != "" {
		t.Errorf("steer stored as %q, want the empty default", c.InterruptMode)
	}
}

func TestQueuePrompt_ALabelledRowIsSentWithItsLabel(t *testing.T) {
	q := &fakeQueue{chat: &marotte.Chat{ID: "c1"}, live: true}
	payload, err := json.Marshal(marotte.QueuePromptCommand{Text: "the findings", MessageID: "m-1", DisplayText: "Merging findings"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cmdQueuePrompt(t.Context(), q, &marotte.ClientCommand{Type: marotte.CmdQueuePrompt, ChatID: "c1", Payload: payload}); err != nil {
		t.Fatalf("CmdQueuePrompt: %v", err)
	}
	if got := q.chat.QueuedPrompts[0].Label; got != "Merging findings" {
		t.Fatalf("queued row label = %q, want the label kept", got)
	}

	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	roles := drainRoles(h, newStubSteerQueue())
	roles.followups = q
	drainAfterClose(t.Context(), roles, "c1", cleanClose, EndFacts{})
	h.waitInflight(t)

	if len(h.opened) != 1 || h.opened[0].Text != "the findings" || h.opened[0].Label != "Merging findings" {
		t.Errorf("drained prompts = %+v, want the row sent with its text and label", h.opened)
	}
}
