package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

var errBoom = errors.New("persist boom")

// captureSlog redirects the default slog logger to buf, restoring it (and the log package's writer
// and flags, which slog.SetDefault redirects) on cleanup. Not parallel-safe.
func captureSlog(buf *bytes.Buffer) func() {
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}
}

// TestHandlePlan_UnmarshalGuard pins that a plan frame is one plan entry in the
// turn when its JSON parses, and nothing at all when it does not: no entry, no
// frame on the wire, and no header write either way.
func TestHandlePlan_UnmarshalGuard(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantPlans int
	}{
		{name: "ValidJSONIsOnePlanEntry", raw: `{"entries":[]}`, wantPlans: 1},
		{name: "InvalidJSONSkips", raw: `{`, wantPlans: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const chatID marotte.ChatID = "c1"
			deps, events := newEventCaptureDeps()
			store := testsupport.NewRecordingChatStore()
			deps.store = store
			tr := New(rolesOf(deps))

			tr.HandlePlan(t.Context(), chatID, json.RawMessage(tc.raw), FrameAttribution{})

			plans := plansOf(t, deps.chatEntries(chatID))
			if len(plans) != tc.wantPlans {
				t.Errorf("HandlePlan(%s): plan entries = %d, want %d", tc.raw, len(plans), tc.wantPlans)
			}
			if got := hasEntryAppended(events, marotte.EntryKindPlan); got != (tc.wantPlans == 1) {
				t.Errorf("HandlePlan(%s): entry_appended{plan} on the wire = %v, want %v", tc.raw, got, tc.wantPlans == 1)
			}
			if store.Exists(chatID) {
				t.Errorf("HandlePlan(%s) wrote the chat header; a plan is an entry of the turn, never a header write", tc.raw)
			}
		})
	}
}

// TestHandleModeUpdate_CurrentModeIDPersistsAndBroadcasts pins the `currentModeId` key KAS's
// current_mode_update carries (not `modeId`, the outbound request's field).
func TestHandleModeUpdate_CurrentModeIDPersistsAndBroadcasts(t *testing.T) {
	deps, events := newEventCaptureDeps()
	store := testsupport.NewRecordingChatStore()
	deps.store = store
	chatID := marotte.ChatID("c1")
	_, _ = store.Mutate(t.Context(), chatID, func(_ *marotte.Chat, _ bool) bool { return true })

	tr := New(rolesOf(deps))
	tr.HandleModeUpdate(t.Context(), chatID, mustJSON(t, map[string]any{
		"currentModeId": "plan",
	}))

	got, ok := store.Get(t.Context(), chatID)
	if !ok {
		t.Fatal("chat missing after HandleModeUpdate")
	}
	if got.CurrentModeID != "plan" {
		t.Errorf("CurrentModeID = %q, want %q (current_mode_update must read currentModeId, not modeId)", got.CurrentModeID, "plan")
	}

	found := false
	for _, e := range *events {
		if e.Type != marotte.EventModeChanged {
			continue
		}
		p, isModePayload := e.Payload.(marotte.ModeChangedPayload)
		if !isModePayload {
			t.Fatalf("mode_changed payload type = %T, want marotte.ModeChangedPayload", e.Payload)
		}
		if p.ModeID != "plan" {
			t.Errorf("mode_changed ModeID = %q, want %q", p.ModeID, "plan")
		}
		found = true
	}
	if !found {
		t.Errorf("no mode_changed event broadcast; got %v", eventTypes(*events))
	}
}

func refusalChunk(meta map[string]any) map[string]any {
	c := map[string]any{
		"content": map[string]any{"type": marotte.ContentTypeText, "text": "I can't continue."},
	}
	if meta != nil {
		c["_meta"] = map[string]any{"kiro": meta}
	}
	return c
}

// sealedRefusals is the refusal block on every entry_sealed frame, in wire order: the seal is
// the LIVE carrier.
func sealedRefusals(events []marotte.ServerEvent) []*marotte.RefusalInfo {
	var out []*marotte.RefusalInfo
	for _, e := range events {
		if p, ok := e.Payload.(marotte.EntrySealedPayload); ok {
			out = append(out, p.Refusal)
		}
	}
	return out
}

// textsOf is the text of every text entry in entries, in seal order: what a reader
// would see as assistant prose.
func textsOf(t *testing.T, entries []marotte.Entry) []string {
	t.Helper()
	var out []string
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindText {
			continue
		}
		var p marotte.EntryText
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode text %q: %v", entries[i].ID, err)
		}
		out = append(out, p.Text)
	}
	return out
}

// thinkingTextsOf is the text of every thinking entry in entries, in seal order:
// textsOf's sibling for the reasoning stream.
func thinkingTextsOf(t *testing.T, entries []marotte.Entry) []string {
	t.Helper()
	var out []string
	for i := range entries {
		if entries[i].Kind != marotte.EntryKindThinking {
			continue
		}
		var p marotte.EntryThinking
		if err := json.Unmarshal(entries[i].Payload, &p); err != nil {
			t.Fatalf("decode thinking %q: %v", entries[i].ID, err)
		}
		out = append(out, p.Text)
	}
	return out
}

// closeChatTurn closes the chat's turn so every open entry seals and the turn_close
// lands in the log.
func closeChatTurn(t *testing.T, deps *baseDeps, chatID marotte.ChatID) {
	t.Helper()
	turn := deps.turns.chats[chatID]
	if turn == nil {
		t.Fatalf("chat %q has no turn to close", chatID)
	}
	if _, err := turn.Close(t.Context(), marotte.TurnConclusion{Outcome: marotte.TurnOutcomeCompleted}); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// closedRefusal closes the chat's turn and answers the refusal its turn_close
// recorded: the durable statement of what the turn latched.
func closedRefusal(t *testing.T, deps *baseDeps, chatID marotte.ChatID) *marotte.RefusalInfo {
	t.Helper()
	closeChatTurn(t, deps, chatID)
	return turnCloseOf(t, deps.chatEntries(chatID)).Refusal
}

func TestHandleAssistantChunk_RefusalMeta(t *testing.T) {
	t.Run("the tagged chunk's text is not assistant prose", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := marotte.ChatID("rf1")
		tr := New(rolesOf(deps))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{
				"category":         "safety",
				"explanation":      "I can't continue.",
				"recommendedModel": "model-x",
			},
		})), false, FrameAttribution{})

		// The explanation travels on the refusal record: the chunk opens no entry and emits no text.
		for _, e := range *events {
			switch e.Type {
			case marotte.EventEntryOpened, marotte.EventEntryDelta:
				t.Errorf("a refusal-tagged chunk emitted %s, want no text frame", e.Type)
			}
		}
		closeChatTurn(t, deps, chatID)
		entries := deps.chatEntries(chatID)
		if texts := textsOf(t, entries); len(texts) != 0 {
			t.Errorf("text entries = %q, want none: the refusal explanation is not prose", texts)
		}
		got := turnCloseOf(t, entries).Refusal
		if got == nil {
			t.Fatal("turn_close refusal = nil, want the tagged chunk's refusal")
		}
		if got.Category != "safety" || got.RecommendedModel != "model-x" {
			t.Errorf("turn_close refusal = %+v, want category safety and model-x", got)
		}
		if got.Explanation != "I can't continue." {
			t.Errorf("turn_close refusal explanation = %q, want the wire's explanation", got.Explanation)
		}
	})

	t.Run("prose before a refusal keeps its own entry and prose after opens a fresh one", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		chatID := marotte.ChatID("rf1b")
		tr := New(rolesOf(deps))
		chunk := func(text string) json.RawMessage {
			return mustJSON(t, map[string]any{
				"content": map[string]any{"type": marotte.ContentTypeText, "text": text},
			})
		}
		tr.HandleAssistantChunk(t.Context(), chatID, chunk("before"), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{"category": "safety", "explanation": "I can't continue."},
		})), false, FrameAttribution{})
		// A refusal is not always last: what follows must not extend the entry before it.
		tr.HandleAssistantChunk(t.Context(), chatID, chunk("after"), false, FrameAttribution{})

		closeChatTurn(t, deps, chatID)
		entries := deps.chatEntries(chatID)
		if got, want := textsOf(t, entries), []string{"before", "after"}; !slices.Equal(got, want) {
			t.Errorf("text entries = %q, want %q: the seal must split the prose", got, want)
		}
		if got := turnCloseOf(t, entries).Refusal; got == nil || got.Explanation != "I can't continue." {
			t.Errorf("turn_close refusal = %+v, want the explanation recorded", got)
		}
	})

	t.Run("a delegate's refusal seals that lane alone", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		chatID := marotte.ChatID("rf1c")
		tr := New(rolesOf(deps))
		laneChunk := func(lane, text string) json.RawMessage {
			return mustJSON(t, map[string]any{
				"content": map[string]any{"type": marotte.ContentTypeText, "text": text},
				"_meta":   map[string]any{"kiro": map[string]any{"agentSubtaskId": lane}},
			})
		}
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "parent "},
		}), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, laneChunk("sub-1", "delegate "), false, FrameAttribution{})
		// The lane comes off the chunk: this seals sub-1, not the parent's entry.
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal":        map[string]any{"category": "safety", "explanation": "delegate stopped"},
			"agentSubtaskId": "sub-1",
		})), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "prose"},
		}), false, FrameAttribution{})

		closeChatTurn(t, deps, chatID)
		entries := deps.chatEntries(chatID)
		if got, want := textsOf(t, entries), []string{"delegate ", "parent prose"}; !slices.Equal(got, want) {
			t.Errorf("text entries = %q, want %q: only the delegate's lane is sealed", got, want)
		}
		if got := turnCloseOf(t, entries).Refusal; got == nil || got.Explanation != "delegate stopped" {
			t.Errorf("turn_close refusal = %+v, want the delegate's explanation", got)
		}
	})

	t.Run("the seal frame carries the refusal live", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := marotte.ChatID("rf1d")
		tr := New(rolesOf(deps))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "before"},
		}), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{
				"category":         "safety",
				"explanation":      "I can't continue.",
				"recommendedModel": "model-x",
			},
		})), false, FrameAttribution{})

		// The refusal branch SEALS the lane, so the seal's own frame is the live
		// carrier: the callout mounts on it rather than waiting for turn_close.
		got := sealedRefusals(*events)
		if len(got) != 1 {
			t.Fatalf("entry_sealed frames = %d, want 1: the refusal seals the open entry", len(got))
		}
		if got[0] == nil {
			t.Fatal("entry_sealed refusal = nil, want the tagged chunk's refusal")
		}
		if got[0].Category != "safety" || got[0].RecommendedModel != "model-x" {
			t.Errorf("entry_sealed refusal = %+v, want category safety and model-x", got[0])
		}
		if got[0].Explanation != "I can't continue." {
			t.Errorf("entry_sealed refusal explanation = %q, want the wire's explanation", got[0].Explanation)
		}
	})

	t.Run("an ordinary seal carries no refusal", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := marotte.ChatID("rf1e")
		tr := New(rolesOf(deps))
		// A seal with no refusal behind it must leave the field absent.
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "prose"},
		}), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "thought"},
		}), true, FrameAttribution{})

		got := sealedRefusals(*events)
		if len(got) == 0 {
			t.Fatal("entry_sealed frames = 0, want the kind change to seal the text entry")
		}
		for _, r := range got {
			if r != nil {
				t.Errorf("an untagged seal carried refusal %+v, want none", r)
			}
		}
	})

	t.Run("first refusal wins", func(t *testing.T) {
		deps, _ := newEventCaptureDeps()
		chatID := marotte.ChatID("rf2")
		tr := New(rolesOf(deps))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{"category": "first"},
		})), false, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{"category": "second"},
		})), false, FrameAttribution{})
		if got := closedRefusal(t, deps, chatID); got == nil || got.Category != "first" {
			t.Errorf("turn_close refusal = %+v, want the first chunk's category kept", got)
		}
	})

	t.Run("a tagged reasoning chunk marks the turn and seals its lane", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := marotte.ChatID("rf3")
		tr := New(rolesOf(deps))
		// The tag is turn-level, so a refusal on a thought chunk marks the turn and its text is dropped.
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": marotte.ContentTypeText, "text": "reasoning "},
		}), true, FrameAttribution{})
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(map[string]any{
			"refusal": map[string]any{"category": "safety", "explanation": "I can't continue."},
		})), true, FrameAttribution{})

		if got := sealedRefusals(*events); len(got) != 1 || got[0] == nil || got[0].Category != "safety" {
			t.Errorf("entry_sealed refusals = %+v, want one carrying category safety", got)
		}
		closeChatTurn(t, deps, chatID)
		entries := deps.chatEntries(chatID)
		if got := turnCloseOf(t, entries).Refusal; got == nil || got.Category != "safety" {
			t.Errorf("turn_close refusal = %+v after a tagged reasoning chunk, want category safety", got)
		}
		// The explanation is the turn's metadata, so it is not thinking text either.
		if got := thinkingTextsOf(t, entries); !slices.Equal(got, []string{"reasoning "}) {
			t.Errorf("thinking entries = %q, want only the prose that preceded the refusal", got)
		}
	})

	t.Run("untagged chunk stays clean", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		chatID := marotte.ChatID("rf4")
		tr := New(rolesOf(deps))
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, refusalChunk(nil)), false, FrameAttribution{})
		for _, r := range sealedRefusals(*events) {
			if r != nil {
				t.Errorf("an untagged chunk put refusal %+v on the wire, want none", r)
			}
		}
		if got := closedRefusal(t, deps, chatID); got != nil {
			t.Errorf("turn_close refusal = %+v after an untagged chunk, want nil", got)
		}
	})
}

// TestHandleAssistantChunk_AStepsFrameFoldsIntoTheRunsTurn pins that a Step frame folds into
// the RUN's turn: in the chat's own it made the chat read as working for the run's length.
func TestHandleAssistantChunk_AStepsFrameFoldsIntoTheRunsTurn(t *testing.T) {
	cases := []struct {
		name      string
		attr      FrameAttribution
		wantRunID string
	}{
		{name: "a workflow step's frame", attr: FrameAttribution{Step: true, RunID: "wf-1", NodePath: "root/step"}, wantRunID: "wf-1"},
		{name: "the chat's own frame", attr: FrameAttribution{}, wantRunID: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newBaseDeps()
			tr := New(rolesOf(d))
			frame := map[string]any{"content": map[string]any{"type": "text", "text": "hi"}}

			tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, frame), false, tc.attr)

			got, ok := d.lastFold()
			if !ok {
				t.Fatal("the frame never reached a fold site, so it landed nowhere")
			}
			if got.runID != tc.wantRunID {
				t.Errorf("fold target run = %q, want %q", got.runID, tc.wantRunID)
			}
		})
	}
}
