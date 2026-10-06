package command

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// capturingBus records what a handler broadcast. Its own type rather than a
// method on the host double, because these two handlers take the store and the
// bus as separate parameters and the point of several cases is that ONE of them
// was reached.
type capturingBus struct {
	events []marotte.ServerEvent
}

func (b *capturingBus) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.events = append(b.events, evt)
}

// draftFrames returns the draft_changed payloads the bus saw, in order.
func (b *capturingBus) draftFrames(t *testing.T) []marotte.DraftChangedPayload {
	t.Helper()
	var out []marotte.DraftChangedPayload
	for _, evt := range b.events {
		if evt.Type != marotte.EventDraftChanged {
			continue
		}
		p, ok := evt.Payload.(marotte.DraftChangedPayload)
		if !ok {
			t.Fatalf("draft_changed payload = %T, want marotte.DraftChangedPayload", evt.Payload)
		}
		out = append(out, p)
	}
	return out
}

func attachmentsReq(t *testing.T, chatID marotte.ChatID, paths []string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetAttachmentsCommand{Paths: paths})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdSetAttachments,
		ChatID:  chatID,
		Payload: payload,
	}
}

func TestCmdSetAttachments(t *testing.T) {
	tests := []struct {
		name       string
		paths      []string
		wantStatus int
		wantStored []string
	}{
		{name: "stores the paths in order", paths: []string{"docs/spec.pdf", "out/shot.png"}, wantStatus: http.StatusOK, wantStored: []string{"docs/spec.pdf", "out/shot.png"}},
		{name: "accepts an empty list as a clear", paths: []string{}, wantStatus: http.StatusOK, wantStored: nil},
		{name: "accepts a list at exactly the cap", paths: manyReqPaths(marotte.MaxAttachments), wantStatus: http.StatusOK, wantStored: manyReqPaths(marotte.MaxAttachments)},
		{name: "refuses one entry over the cap", paths: manyReqPaths(marotte.MaxAttachments + 1), wantStatus: http.StatusRequestEntityTooLarge, wantStored: nil},
		{name: "refuses an empty path", paths: []string{"a.txt", ""}, wantStatus: http.StatusBadRequest, wantStored: nil},
		{name: "refuses a path over the per-path cap", paths: []string{strings.Repeat("x", marotte.MaxAttachmentPathBytes+1)}, wantStatus: http.StatusBadRequest, wantStored: nil},
		{name: "keeps a multibyte path intact", paths: []string{"docs/仕様書.pdf"}, wantStatus: http.StatusOK, wantStored: []string{"docs/仕様書.pdf"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{sessionID: "sess-1"}
			host := newBridgeHost(store, b)
			bus := &capturingBus{}

			_, err := CmdSetAttachments(t.Context(), host, bus, attachmentsReq(t, "c1", tc.paths))

			if statusOf(err) != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.wantStatus, errText(err))
			}
			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if strings.Join(c.Attachments, ",") != strings.Join(tc.wantStored, ",") {
				t.Errorf("stored = %v, want %v", c.Attachments, tc.wantStored)
			}
			if b.callCount != 0 {
				t.Errorf("bridge called %d times; staging a file must not reach the agent", b.callCount)
			}
		})
	}
}

// Why the handler carries no UTF-8 check, same as set_draft's: encoding/json
// coerces every invalid byte sequence in a decoded string literal to U+FFFD, so a
// path arriving through the envelope is valid by construction. Pinned so a future
// reader does not add one back as a missing guard.
func TestCmdSetAttachments_JSONDecodingSanitizesInvalidUTF8(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetAttachments(t.Context(), host, &capturingBus{}, &marotte.ClientCommand{
		Type:    marotte.CmdSetAttachments,
		ChatID:  "c1",
		Payload: append(append([]byte(`{"paths":["`), 0xff, 0xfe), []byte(`"]}`)...),
	})

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, _ := store.Get(t.Context(), "c1")
	if len(c.Attachments) != 1 {
		t.Fatalf("stored %v, want one path", c.Attachments)
	}
	if !utf8.ValidString(c.Attachments[0]) {
		t.Errorf("stored path %q is not valid UTF-8; the chat file would not round-trip", c.Attachments[0])
	}
}

func TestCmdSetAttachments_RefusesAMissingChatID(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetAttachments(t.Context(), host, &capturingBus{}, attachmentsReq(t, "", []string{"a.txt"}))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

func TestCmdSetAttachments_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})

	_, err := CmdSetAttachments(t.Context(), host, &capturingBus{}, &marotte.ClientCommand{
		Type:    marotte.CmdSetAttachments,
		ChatID:  "c1",
		Payload: json.RawMessage(`{"paths":"one.txt"}`),
	})

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// Same rule the draft follows, and for the same reason: a chat is a server record
// from its first prompt onward, so staging a file into a brand-new one has
// nowhere to land and creating the record would put a row in every connected
// client's sidebar for a conversation nobody has started.
func TestCmdSetAttachments_DoesNotCreateAChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, &recordingBridge{})
	bus := &capturingBus{}

	_, err := CmdSetAttachments(t.Context(), host, bus, attachmentsReq(t, "c-never-prompted", []string{"a.txt"}))

	if statusOf(err) != http.StatusOK {
		t.Errorf("status = %d, want 200: staging a file on an unsaved chat is a no-op, not an error", statusOf(err))
	}
	if _, ok := store.Get(t.Context(), "c-never-prompted"); ok {
		t.Error("staging a file created a chat record")
	}
	if got := bus.draftFrames(t); len(got) != 0 {
		t.Errorf("broadcast %d draft_changed frames for a write that did not land", len(got))
	}
}

// The convergence property, and the reason the event exists: an idle device holds
// whatever it last saw, so the frame has to carry the composer state rather than a
// bare invalidation.
func TestComposerCommands_BroadcastDraftChanged(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})
	bus := &capturingBus{}

	if _, err := CmdSetDraft(t.Context(), host, bus, draftReq(t, "c1", "half a question")); err != nil {
		t.Fatalf("CmdSetDraft: %v", err)
	}
	if _, err := CmdSetAttachments(t.Context(), host, bus, attachmentsReq(t, "c1", []string{"docs/spec.pdf"})); err != nil {
		t.Fatalf("CmdSetAttachments: %v", err)
	}

	frames := bus.draftFrames(t)
	if len(frames) != 2 {
		t.Fatalf("draft_changed frames = %d, want 2 (one per write)", len(frames))
	}
	if frames[0].Text != "half a question" {
		t.Errorf("frame 0 text = %q, want the draft that was written", frames[0].Text)
	}
	// BOTH halves ride every frame: a receiver cannot know which command fired, so a one-field
	// frame would blank the other field.
	if frames[1].Text != "half a question" {
		t.Errorf("frame 1 text = %q, want the draft already on the record", frames[1].Text)
	}
	if strings.Join(frames[1].Attachments, ",") != "docs/spec.pdf" {
		t.Errorf("frame 1 attachments = %v, want docs/spec.pdf", frames[1].Attachments)
	}
	for i, evt := range bus.events {
		if evt.ChatID != "c1" {
			t.Errorf("event %d chat id = %q, want c1", i, evt.ChatID)
		}
	}
}

// A save that changed nothing broadcasts nothing. This is the common case rather
// than an edge: the client flushes on blur and on unload behind a 600ms debounce
// that has usually already sent the same value, so a frame per POST would put a
// re-render on every connected client for a change nobody made.
func TestComposerCommands_NoBroadcastWhenNothingChanged(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{})
	bus := &capturingBus{}

	for range 3 {
		if _, err := CmdSetDraft(t.Context(), host, bus, draftReq(t, "c1", "same text")); err != nil {
			t.Fatalf("CmdSetDraft: %v", err)
		}
		if _, err := CmdSetAttachments(t.Context(), host, bus, attachmentsReq(t, "c1", []string{"a.txt"})); err != nil {
			t.Fatalf("CmdSetAttachments: %v", err)
		}
	}

	if got := bus.draftFrames(t); len(got) != 2 {
		t.Errorf("draft_changed frames = %d, want 2: three identical saves each are one write and two no-ops", len(got))
	}
}

// TestSettleComposerOnPrompt_ClearsTheStagedAttachments: the send clears the staged list in the
// draft-clearing header write, or a lost client POST brings sent attachments back; the list rides
// the turn_open's prompt instead.
func TestSettleComposerOnPrompt_ClearsTheStagedAttachments(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	if _, err := store.SetAttachments(t.Context(), "c1", []string{"docs/spec.pdf"}); err != nil {
		t.Fatalf("SetAttachments: %v", err)
	}

	settleComposerOnPrompt(t.Context(), store, &capturingBus{}, "c1", &marotte.PromptCommand{
		Text:        "have a look",
		MessageID:   "m-1",
		Attachments: []marotte.Attachment{{Path: "docs/spec.pdf", Name: "spec.pdf"}},
	})

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if c.Attachments != nil {
		t.Errorf("staged attachments = %#v, want cleared by the send", c.Attachments)
	}
}

// manyReqPaths builds n distinct workspace paths, for the cap cases.
func manyReqPaths(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "dir" + strings.Repeat("x", i) + "/f.txt"
	}
	return out
}
