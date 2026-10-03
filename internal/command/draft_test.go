package command

// The composer-draft command. What is pinned here is the boundary: the caps and
// the UTF-8 check that keep an unloadable draft off the chat file, and the two
// refusals to create anything — a draft must not turn a client-side chat into a
// sidebar row, and it must not reach the bridge at all.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func draftReq(t *testing.T, chatID marotte.ChatID, text string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetDraftCommand{Text: text})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdSetDraft,
		ChatID:  chatID,
		Payload: payload,
	}
}

func seedEmptyChat(t *testing.T, store ChatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "a chat"
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestCmdSetDraft(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantStatus int
		wantStored string
	}{
		{name: "stores the text", text: "half a question", wantStatus: http.StatusOK, wantStored: "half a question"},
		// Empty is a VALUE, not a missing field: it is how a sent or abandoned
		// message is cleared, so it must be accepted rather than rejected.
		{name: "accepts empty as a clear", text: "", wantStatus: http.StatusOK, wantStored: ""},
		{name: "accepts a draft at exactly the cap", text: strings.Repeat("x", marotte.MaxDraftBytes), wantStatus: http.StatusOK, wantStored: strings.Repeat("x", marotte.MaxDraftBytes)},
		{name: "refuses one byte over the cap", text: strings.Repeat("x", marotte.MaxDraftBytes+1), wantStatus: http.StatusRequestEntityTooLarge, wantStored: ""},
		{name: "keeps multibyte text intact", text: "日本語のドラフト", wantStatus: http.StatusOK, wantStored: "日本語のドラフト"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			_, err := CmdSetDraft(t.Context(), host, host, draftReq(t, "c1", tc.text))

			if statusOf(err) != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.wantStatus, errText(err))
			}
			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Draft != tc.wantStored {
				t.Errorf("stored draft len = %d, want %d", len(c.Draft), len(tc.wantStored))
			}
			// A draft is not a session config option: nothing about it belongs on
			// the wire to KAS, and a call per 600ms of typing would be the busiest
			// traffic in the app.
			if b.callCount != 0 {
				t.Errorf("bridge called %d times; a draft save must not reach the agent", b.callCount)
			}
		})
	}
}

// Why the handler carries no UTF-8 check: encoding/json coerces every invalid
// byte sequence in a string literal to U+FFFD as it decodes, so a draft arriving
// through the envelope is valid by construction and the check could not fail.
// Pinned so a future reader does not add one back as a missing guard.
func TestCmdSetDraft_JSONDecodingSanitizesInvalidUTF8(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})

	// Raw bytes, not json.Marshal: marshalling would sanitize them before the
	// handler ever saw them, which is the same coercion under test.
	_, err := CmdSetDraft(t.Context(), host, host, &marotte.ClientCommand{
		Type:    marotte.CmdSetDraft,
		ChatID:  "c1",
		Payload: append(append([]byte(`{"text":"`), 0xff, 0xfe), []byte(`"}`)...),
	})

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, _ := store.Get(t.Context(), "c1")
	if !utf8.ValidString(c.Draft) {
		t.Errorf("stored draft %q is not valid UTF-8; the chat file would not round-trip", c.Draft)
	}
}

func TestCmdSetDraft_RefusesAMissingChatID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetDraft(t.Context(), host, host, draftReq(t, "", "text"))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

func TestCmdSetDraft_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetDraft(t.Context(), host, host, &marotte.ClientCommand{
		Type:    marotte.CmdSetDraft,
		ChatID:  "c1",
		Payload: json.RawMessage(`{"text":42}`),
	})

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// A chat is a server record from its first prompt onward. Every chat is
// client-side until then, so a draft typed into a brand-new one has nowhere to
// land — and creating the record here would put a row in every connected
// client's sidebar for a conversation nobody has started. Unlike set_mode, which
// DOES auto-create, this is typing rather than a deliberate pick.
func TestCmdSetDraft_DoesNotCreateAChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetDraft(t.Context(), host, host, draftReq(t, "c-never-prompted", "typed but nothing sent"))

	if statusOf(err) != http.StatusOK {
		t.Errorf("status = %d, want 200: a draft on an unsaved chat is a no-op, not an error", statusOf(err))
	}
	if _, ok := store.Get(t.Context(), "c-never-prompted"); ok {
		t.Error("a draft created a chat record")
	}
}

// The prompt path clears the draft in the header write a sent prompt owes: a
// reload would otherwise put the sent message back in the box.
func TestSettleComposerOnPrompt_ClearsTheDraft(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetDraft(t.Context(), "c1", "the message about to be sent"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}

	settleComposerOnPrompt(t.Context(), store, &capturingBus{}, "c1", &marotte.PromptCommand{
		Text:      "the message about to be sent",
		MessageID: "m-1",
	})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Draft != "" {
		t.Errorf("draft = %q, want cleared by the send", c.Draft)
	}
}

// And SAYS so. CmdSetDraft is not the only writer of the field, so it cannot be
// the only emitter of the frame. Without this the clear above was silent: every
// other client kept its parked copy of text that had already been sent, and so
// did this one after a reload, because the record's draft is what the single-chat
// GET seeds the box from. The client's own clearing set_draft is not a substitute
// for the same reason the clear above is not redundant — that POST can be lost or
// superseded.
func TestSettleComposerOnPrompt_AnnouncesTheClearedComposer(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetDraft(t.Context(), "c1", "the message about to be sent"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	bus := &capturingBus{}

	settleComposerOnPrompt(t.Context(), store, bus, "c1", &marotte.PromptCommand{
		Text:      "the message about to be sent",
		MessageID: "m-1",
	})

	frames := bus.draftFrames(t)
	if len(frames) != 1 {
		t.Fatalf("draft_changed frames = %d, want 1: the send cleared a draft", len(frames))
	}
	if frames[0].Text != "" {
		t.Errorf("frame text = %q, want empty — the draft is spent", frames[0].Text)
	}
	if len(frames[0].Attachments) != 0 {
		t.Errorf("frame attachments = %#v, want none", frames[0].Attachments)
	}
}

// The staged files are the other half of the same composer, so clearing them
// alone still earns the frame: a chat can hold attachments with no text.
func TestSettleComposerOnPrompt_AnnouncesClearedAttachmentsWithNoDraft(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetAttachments(t.Context(), "c1", []string{"docs/spec.pdf"}); err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}
	bus := &capturingBus{}

	settleComposerOnPrompt(t.Context(), store, bus, "c1", &marotte.PromptCommand{
		Text:        "have a look",
		MessageID:   "m-1",
		Attachments: []marotte.Attachment{{Path: "docs/spec.pdf", Name: "spec.pdf"}},
	})

	if frames := bus.draftFrames(t); len(frames) != 1 {
		t.Fatalf("draft_changed frames = %d, want 1: the send cleared the staged files", len(frames))
	}
}

// A prompt sent with an empty composer has nothing to announce, and that is the
// common case — the same rule broadcastComposer applies to a write that changed
// nothing, so the frame keeps meaning something changed.
func TestSettleComposerOnPrompt_SaysNothingWhenTheComposerWasEmpty(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	bus := &capturingBus{}

	settleComposerOnPrompt(t.Context(), store, bus, "c1", &marotte.PromptCommand{
		Text:      "typed and sent without pausing",
		MessageID: "m-1",
	})

	if frames := bus.draftFrames(t); len(frames) != 0 {
		t.Errorf("draft_changed frames = %#v, want none", frames)
	}
}

// The attachments have to reach the RECORD, not only the outbound prompt.
// BuildPromptBlocks consumes PromptCommand.Attachments on the way to KAS and
// folds each one into a content block — a document becomes a `resource`, an image
// becomes an `image`, and only the leftover case becomes an "Attached file: …"
// text line. So for the two inlined kinds the path never appears in the text at
// all, and a turn read back later had no way to say what was attached: the client
// could not linkify what was not there, and the sent request rendered as text
// only. The turn_open's prompt is what lets a turn header draw them.
func TestPromptEntry_CarriesTheAttachments(t *testing.T) {
	atts := []marotte.Attachment{
		{Path: "out/shot.png", Name: "shot.png"},
		{Path: "docs/spec.pdf", Name: "spec.pdf"},
	}
	got := promptEntry(&marotte.PromptCommand{
		Text:        "have a look at these",
		MessageID:   "m-1",
		Attachments: atts,
	}, nil)

	if got.ID != "m-1" || got.Text != "have a look at these" {
		t.Errorf("promptEntry = %+v, want id m-1 and the prompt's text", got)
	}
	if len(got.Attachments) != len(atts) {
		t.Fatalf("attachments = %#v, want %#v", got.Attachments, atts)
	}
	for i, want := range atts {
		if got.Attachments[i] != want {
			t.Errorf("attachment %d = %#v, want %#v", i, got.Attachments[i], want)
		}
	}
	// The image path is deliberately absent from the text: that is the whole
	// reason the field exists rather than the client re-reading the text.
	if strings.Contains(got.Text, "out/shot.png") {
		t.Errorf("text = %q, unexpectedly carries the attachment path", got.Text)
	}
}

// A prompt with no attachments must carry none, so `omitempty` keeps the field
// off the wire and off disk for the overwhelmingly common case.
func TestPromptEntry_NoAttachmentsCarriesNone(t *testing.T) {
	got := promptEntry(&marotte.PromptCommand{Text: "just a question", MessageID: "m-1"}, nil)
	if got.Attachments != nil {
		t.Errorf("attachments = %#v, want nil", got.Attachments)
	}
}
