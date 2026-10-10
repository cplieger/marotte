package translate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func modelRoutedInfo(t *testing.T, message string) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": map[string]any{"kind": "model_routed", "message": message}},
	})
}

func modelRoutedOf(t *testing.T, entries []marotte.Entry) []marotte.EntryModelRouted {
	t.Helper()
	var out []marotte.EntryModelRouted
	for _, e := range entriesOfKind(entries, marotte.EntryKindModelRouted) {
		var p marotte.EntryModelRouted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode model_routed %q: %v", e.ID, err)
		}
		out = append(out, p)
	}
	return out
}

func TestHandleSessionInfoUpdate_ModelRoutedLandsWhereTheRouteHappened(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	chunk := func(text string) {
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": "text", "text": text},
		}), false, FrameAttribution{})
	}

	chunk("before")
	tr.HandleSessionInfoUpdate(t.Context(), chatID,
		modelRoutedInfo(t, "Found a better model for this task. Switching to it now."), FrameAttribution{})
	chunk("after")
	tr.HandleSessionInfoUpdate(t.Context(), chatID, modelRoutedInfo(t, "Moved up.\nline\u0085two"), FrameAttribution{})

	entries := deps.chatEntries(chatID)
	kinds := entryKinds(entries)
	want := []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindModelRouted, marotte.EntryKindText, marotte.EntryKindModelRouted}
	if len(kinds) < len(want) {
		t.Fatalf("turn entries = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("turn entries = %v, want %v", kinds, want)
		}
	}
	got := modelRoutedOf(t, entries)
	if got[0].Message != "Found a better model for this task. Switched to it." {
		t.Errorf("first route message = %q, want KAS's default reworded to the past tense", got[0].Message)
	}
	if got[1].Message != "Moved up. line two" {
		t.Errorf("second route message = %q, want the newline and C1 control flattened by the decode door", got[1].Message)
	}
}

func TestModelRoutedPayload_TunedNoticeKeepsItsWording(t *testing.T) {
	const tuned = "Switching to it now. Please wait."
	if got := modelRoutedPayload(tuned).Message; got != tuned {
		t.Errorf("modelRoutedPayload(%q).Message = %q, want the tuned notice unchanged", tuned, got)
	}
}

func TestHandleSessionInfoUpdate_StepModelRoutedStaysOffTheChat(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", modelRoutedInfo(t, "Found a better model."), FrameAttribution{Step: true})

	if got := entriesOfKind(deps.chatEntries("c1"), marotte.EntryKindModelRouted); len(got) != 0 {
		t.Errorf("chat entries after a step's route = %d model_routed, want none", len(got))
	}
}

func TestEntryProjection_ReplaysModelRoutedAtItsPositionWithTheLivePayload(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "before"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "model_routed", map[string]any{
			"message": "Found a better model for this task. Switching to it now.",
		})),
		agentChunkFrame(t, "after"),
		turnEndFrame(t, "end_turn"),
	})

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen, marotte.EntryKindText, marotte.EntryKindModelRouted,
		marotte.EntryKindText, marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Fatalf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
	got := entryOfKind(t, turns[0], marotte.EntryKindModelRouted)
	live, err := json.Marshal(modelRoutedPayload("Found a better model for this task. Switching to it now."))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(live) {
		t.Errorf("replayed payload = %s, want the live payload %s byte for byte", got.Payload, live)
	}
}
