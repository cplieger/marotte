package translate

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

func modelFallbackInfo(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	kiro := map[string]any{"kind": "model_fallback"}
	maps.Copy(kiro, fields)
	return mustJSON(t, map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"kiro": kiro}})
}

func modelFallbacksOf(t *testing.T, entries []marotte.Entry) []marotte.EntryModelFallback {
	t.Helper()
	var out []marotte.EntryModelFallback
	for _, e := range entriesOfKind(entries, marotte.EntryKindModelFallback) {
		var p marotte.EntryModelFallback
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode model_fallback %q: %v", e.ID, err)
		}
		out = append(out, p)
	}
	return out
}

// The refused attempt's text and the retry's are separate lanes either side of the row.
func TestHandleSessionInfoUpdate_ModelFallbackRefusalAppendsEntry(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	chunk := func(text string) {
		tr.HandleAssistantChunk(t.Context(), chatID, mustJSON(t, map[string]any{
			"content": map[string]any{"type": "text", "text": text},
		}), false, FrameAttribution{})
	}

	chunk("refused attempt")
	tr.HandleSessionInfoUpdate(t.Context(), chatID, modelFallbackInfo(t, map[string]any{
		"from": "claude-opus-4.7", "to": "claude-sonnet-4.6", "scope": "session", "trigger": "refusal",
	}), FrameAttribution{})
	chunk("retry")

	entries := deps.chatEntries(chatID)
	if got, want := entryKinds(entries), []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindModelFallback}; !slices.Equal(got, want) {
		t.Fatalf("sealed turn entries = %v, want %v: the refused attempt's lane seals before the row", got, want)
	}
	if open := deps.turns.chats[chatID].OpenEntries(); len(open) != 1 || open[0].Text != "retry" {
		t.Errorf("open entries = %+v, want the retry alone in a new lane", open)
	}
	got := modelFallbacksOf(t, entries)
	if want := (marotte.EntryModelFallback{From: "claude-opus-4.7", To: "claude-sonnet-4.6", Scope: "session", Trigger: "refusal"}); got[0] != want {
		t.Errorf("payload = %+v, want %+v", got[0], want)
	}
}

func TestHandleSessionInfoUpdate_ModelFallbackDeclinedHasNoTo(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "working"},
	}), false, FrameAttribution{})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", modelFallbackInfo(t, map[string]any{
		"from": "claude-opus-4.7", "trigger": "availability", "reason": "noServedTarget", "agentSubtaskId": "sub-1",
	}), FrameAttribution{})

	got := modelFallbacksOf(t, deps.chatEntries("c1"))
	if len(got) != 1 {
		t.Fatalf("model_fallback entries = %d, want 1", len(got))
	}
	if got[0].To != "" || got[0].Reason != "noServedTarget" || got[0].Trigger != "availability" || !got[0].Subagent {
		t.Errorf("declined payload = %+v, want no To, the reason, the trigger and Subagent", got[0])
	}
}

// Serial: it swaps the slog default and resets the census ledger.
func TestHandleSessionInfoUpdate_ModelFallbackNoUnknownKindWarn(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", modelFallbackInfo(t, map[string]any{
		"from": "a", "to": "b", "scope": "session", "trigger": "refusal", "reason": "x", "agentSubtaskId": "s",
	}), FrameAttribution{})

	for _, unwanted := range []string{"UNKNOWN kind", "UNKNOWN _meta.kiro field"} {
		if strings.Contains(logbuf.String(), unwanted) {
			t.Errorf("a model_fallback frame logged %q:\n%s", unwanted, logbuf.String())
		}
	}
}

func TestHandleSessionInfoUpdate_ModelFallbackFromStepSessionNotOnChat(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	path := []string{"wf_1", "build"}
	attr := FrameAttribution{Step: true, RunID: "wf_1", NodePath: workflow.PathKey(path)}

	tr.HandleSessionInfoUpdate(t.Context(), "c1", modelFallbackInfo(t, map[string]any{
		"from": "a", "to": "b", "trigger": "refusal",
	}), attr)

	if got := entriesOfKind(deps.chatEntries("c1"), marotte.EntryKindModelFallback); len(got) != 0 {
		t.Errorf("chat entries after a step's fallback = %d model_fallback, want none", len(got))
	}
	if got := modelFallbacksOf(t, deps.runEntries("wf_1", attr.NodePath)); len(got) != 1 || got[0].To != "b" {
		t.Errorf("run turn model_fallback = %+v, want the step's one", got)
	}
}

// A parentless run bridge reads a step's frame through HandleStepInfoUpdate.
func TestHandleStepInfoUpdate_ModelFallbackLandsInTheStepsTurn(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	attr := FrameAttribution{Step: true, RunID: "wf_1", NodePath: workflow.PathKey([]string{"wf_1", "build"})}

	tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", modelFallbackInfo(t, map[string]any{
		"from": "a", "to": "b", "trigger": "availability",
	}), attr)

	if got := modelFallbacksOf(t, deps.runEntries("wf_1", attr.NodePath)); len(got) != 1 || got[0].Trigger != "availability" {
		t.Errorf("run turn model_fallback = %+v, want the step's one", got)
	}
}

func TestEntryProjection_ModelFallbackReplay(t *testing.T) {
	fields := map[string]any{"from": "claude-opus-4.7", "to": "claude-sonnet-4.6", "scope": "session", "trigger": "refusal"}
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "before"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "model_fallback", fields)),
		agentChunkFrame(t, "after"),
		turnEndFrame(t, "end_turn"),
	})

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen, marotte.EntryKindText, marotte.EntryKindModelFallback,
		marotte.EntryKindText, marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Fatalf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
	live, err := json.Marshal(modelFallbackPayload(&modelFallbackFields{
		From: "claude-opus-4.7", To: "claude-sonnet-4.6", Scope: "session", Trigger: "refusal",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryOfKind(t, turns[0], marotte.EntryKindModelFallback); string(got.Payload) != string(live) {
		t.Errorf("replayed payload = %s, want the live payload %s byte for byte", got.Payload, live)
	}
}
