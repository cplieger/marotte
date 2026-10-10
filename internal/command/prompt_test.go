package command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func TestValidatePromptPayload(t *testing.T) {
	valid := func(text, msgID, model string) []byte {
		b, _ := json.Marshal(marotte.PromptCommand{Text: text, MessageID: msgID, Model: model})
		return b
	}

	tests := []struct {
		name       string
		payload    []byte
		wantStatus int
		wantErr    bool
	}{
		{"valid minimal", valid("hello", "msg-1", ""), 0, false},
		{"valid with model", valid("hi", "msg-2", "claude"), 0, false},
		{"empty text", valid("", "msg-1", ""), http.StatusBadRequest, true},
		{"empty text with an attachment", attachmentOnly(t), 0, false},
		{"empty text with an empty attachment path", withAttachments(t, "", []string{""}), http.StatusBadRequest, true},
		{"empty text with an oversized attachment path", withAttachments(t, "", []string{strings.Repeat("x", marotte.MaxAttachmentPathBytes+1)}), http.StatusBadRequest, true},
		{"empty text with attachments over the cap", withAttachments(t, "", manyReqPaths(marotte.MaxAttachments+1)), http.StatusRequestEntityTooLarge, true},
		{"attachments at exactly the cap", withAttachments(t, "", manyReqPaths(marotte.MaxAttachments)), 0, false},
		{"text beside an empty attachment path", withAttachments(t, "hi", []string{""}), http.StatusBadRequest, true},
		{"text at exact cap", valid(strings.Repeat("a", MaxPromptBytes), "msg-1", ""), 0, false},
		{"oversized text", valid(strings.Repeat("x", MaxPromptBytes+1), "msg-1", ""), http.StatusRequestEntityTooLarge, true},
		{"missing message_id", valid("hi", "", ""), http.StatusBadRequest, true},
		{"invalid message_id", valid("hi", "msg id/bad", ""), http.StatusBadRequest, true},
		{"invalid model", valid("hi", "msg-1", "bad model!"), http.StatusBadRequest, true},
		{"malformed json", []byte(`{not json`), http.StatusBadRequest, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &marotte.ClientCommand{Payload: tc.payload}
			_, status, err := validatePromptPayload(cmd)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

func attachmentOnly(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(marotte.PromptCommand{MessageID: "msg-1", Attachments: []marotte.Attachment{{Path: "/workspace/shot.png"}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withAttachments(t *testing.T, text string, paths []string) []byte {
	t.Helper()
	atts := make([]marotte.Attachment, len(paths))
	for i, p := range paths {
		atts[i] = marotte.Attachment{Path: p}
	}
	b, err := json.Marshal(marotte.PromptCommand{Text: text, MessageID: "msg-1", Attachments: atts})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPromptLabel_AnAttachmentOnlyPromptIsNamedForItsFile(t *testing.T) {
	p := &marotte.PromptCommand{Attachments: []marotte.Attachment{{Path: "/workspace/docs/shot.png"}}}
	if got := promptLabel(p); got != "shot.png" {
		t.Errorf("promptLabel(attachment-only) = %q, want %q", got, "shot.png")
	}
	p.Text = "look"
	if got := promptLabel(p); got != "look" {
		t.Errorf("promptLabel(text) = %q, want %q", got, "look")
	}
}

// seedDefaultNamedChat seeds the record OpenTurn's header fallback leaves behind:
// a chat still carrying the default name, which the first prompt may rename.
func seedDefaultNamedChat(t *testing.T, store chatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = marotte.DefaultChatName
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// A chat's first prompt names it, because a tab labelled "New chat" forever is
// a tab the user cannot find again. Only the FIRST message on a chat still
// carrying the default name may rename it: a chat the user named, or one that
// already holds a turn, keeps what it has.
func TestSettleComposerOnPrompt_DerivesTheChatNameFromTheFirstMessage(t *testing.T) {
	const eighty = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	cases := []struct {
		name     string
		named    bool
		text     string
		wantName string
	}{
		{name: "the first message becomes the name", text: "fix the flaky purge test", wantName: "fix the flaky purge test"},
		{name: "eighty runes is the last length kept whole", text: eighty, wantName: eighty},
		{name: "longer text is cut and marked", text: eighty + " and then some more", wantName: eighty + "..."},
		{name: "a chat that already has a name keeps it", named: true, text: "fix the flaky purge test", wantName: "a chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.named {
				seedEmptyChat(t, store, "c1")
			} else {
				seedDefaultNamedChat(t, store, "c1")
			}

			settleComposerOnPrompt(t.Context(), store, &capturingBus{}, "c1",
				&marotte.PromptCommand{Text: tc.text, MessageID: "m-1"})

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Name != tc.wantName {
				t.Errorf("name after a %d-byte first message = %q, want %q", len(tc.text), c.Name, tc.wantName)
			}
		})
	}
}

// The second message must not rename the chat: the name belongs to the opening
// question, not to whatever was said last.
func TestSettleComposerOnPrompt_LeavesTheNameAloneAfterTheFirstMessage(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedDefaultNamedChat(t, store, "c1")

	for _, m := range []*marotte.PromptCommand{
		{Text: "the opening question", MessageID: "m-1"},
		{Text: "a follow up nobody wants in the tab title", MessageID: "m-2"},
	} {
		settleComposerOnPrompt(t.Context(), store, &capturingBus{}, "c1", m)
	}

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Name != "the opening question" {
		t.Errorf("name = %q, want it fixed by the first message", c.Name)
	}
}
