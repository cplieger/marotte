package translate

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
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
		"from": "claude-opus-4.7", "trigger": "availability", "reason": "noServedTarget",
	}), FrameAttribution{})

	got := modelFallbacksOf(t, deps.chatEntries("c1"))
	if len(got) != 1 {
		t.Fatalf("model_fallback entries = %d, want 1", len(got))
	}
	if want := (marotte.EntryModelFallback{From: "claude-opus-4.7", Trigger: "availability", Reason: "noServedTarget"}); got[0] != want {
		t.Errorf("declined payload = %+v, want %+v", got[0], want)
	}
}

// A sub-agent's fallback never moves the chat's model, so neither kind of it leaves a row, nor
// seals the lane the chat is streaming.
func TestHandleSessionInfoUpdate_ModelFallbackOfASubagentIsNotShown(t *testing.T) {
	for _, kind := range []string{infoKindModelFallback, infoKindModelFallbackPromoted} {
		for _, subtask := range []string{"sub-1", ""} {
			t.Run(kind+" agentSubtaskId="+strconv.Quote(subtask), func(t *testing.T) {
				deps, _ := newEventCaptureDeps()
				tr := New(rolesOf(deps))
				tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
					"content": map[string]any{"type": "text", "text": "working"},
				}), false, FrameAttribution{})

				tr.HandleSessionInfoUpdate(t.Context(), "c1", modelFallbackInfo(t, map[string]any{
					"kind": kind, "from": "a", "to": "b", "scope": "session", "trigger": "refusal", "agentSubtaskId": subtask,
				}), FrameAttribution{})

				if got := deps.chatEntries("c1"); len(got) != 0 {
					t.Errorf("sealed entries = %v, want none: the sub-agent's notice is not shown", entryKinds(got))
				}
				if open := deps.turns.chats["c1"].OpenEntries(); len(open) != 1 || open[0].Text != "working" {
					t.Errorf("open entries = %+v, want the chat's lane still open", open)
				}
			})
		}
	}
}

// A sub-agent's frame is one carrying agentSubtaskId as a string, "" included; any other value
// is the chat's own, as the TUI reads it.
func TestModelFallbackFields_OfSubagentIsAStringSubtaskID(t *testing.T) {
	cases := map[string]struct {
		frame string
		want  bool
	}{
		"absent":       {`{"from":"a"}`, false},
		"null":         {`{"from":"a","agentSubtaskId":null}`, false},
		"a number":     {`{"from":"a","agentSubtaskId":7}`, false},
		"empty string": {`{"from":"a","agentSubtaskId":""}`, true},
		"an id":        {`{"from":"a","agentSubtaskId":"sub-1"}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var k modelFallbackFields
			if err := json.Unmarshal([]byte(tc.frame), &k); err != nil {
				t.Fatalf("decode %s: %v", tc.frame, err)
			}
			if got := k.ofSubagent(); got != tc.want {
				t.Errorf("ofSubagent(%s) = %v, want %v", tc.frame, got, tc.want)
			}
		})
	}
}

// A promotion reads as a session-wide refusal fallback, and only with a target and a refusal.
func TestModelFallbackPayload_ReadsAPromotionAsTheSessionsFallback(t *testing.T) {
	cases := []struct {
		name   string
		fields modelFallbackFields
		want   marotte.EntryModelFallback
		show   bool
	}{
		{
			name:   "refusal with a target",
			fields: modelFallbackFields{From: "opus", To: "sonnet", Trigger: "refusal", Reason: "x"},
			want:   marotte.EntryModelFallback{From: "opus", To: "sonnet", Scope: "session", Trigger: "refusal"},
			show:   true,
		},
		{name: "no target", fields: modelFallbackFields{From: "opus", Trigger: "refusal"}},
		{name: "availability", fields: modelFallbackFields{From: "opus", To: "sonnet", Trigger: "availability"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, show := modelFallbackPayload(infoKindModelFallbackPromoted, &tc.fields)
			if show != tc.show || got != tc.want {
				t.Errorf("modelFallbackPayload(promoted, %+v) = %+v, %v; want %+v, %v", tc.fields, got, show, tc.want, tc.show)
			}
		})
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

// A parentless run's host session reads a step's frame through HandleStepInfoUpdate.
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
	payload, _ := modelFallbackPayload(infoKindModelFallback, &modelFallbackFields{
		From: "claude-opus-4.7", To: "claude-sonnet-4.6", Scope: "session", Trigger: "refusal",
	})
	live, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryOfKind(t, turns[0], marotte.EntryKindModelFallback); string(got.Payload) != string(live) {
		t.Errorf("replayed payload = %s, want the live payload %s byte for byte", got.Payload, live)
	}
}

// A step's promotion lands in its run turn as the session wording; a sub-agent's does not.
func TestHandleStepInfoUpdate_ModelFallbackPromotionAndSubagent(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	attr := FrameAttribution{Step: true, RunID: "wf_1", NodePath: workflow.PathKey([]string{"wf_1", "build"})}

	tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", modelFallbackInfo(t, map[string]any{
		"kind": infoKindModelFallbackPromoted, "from": "a", "to": "b", "trigger": "refusal",
	}), attr)
	for _, subtask := range []string{"sub-1", ""} {
		tr.HandleStepInfoUpdate(t.Context(), "run:wf_1", modelFallbackInfo(t, map[string]any{
			"from": "a", "to": "c", "trigger": "refusal", "agentSubtaskId": subtask,
		}), attr)
	}

	got := modelFallbacksOf(t, deps.runEntries("wf_1", attr.NodePath))
	if want := []marotte.EntryModelFallback{{From: "a", To: "b", Scope: "session", Trigger: "refusal"}}; !slices.Equal(got, want) {
		t.Errorf("run turn model_fallback = %+v, want %+v", got, want)
	}
}

// Replay reads a promotion as live does and drops a sub-agent's fallback without a boundary.
func TestEntryProjection_ModelFallbackPromotionAndSubagentReplay(t *testing.T) {
	turns := entryProject([][2]any{
		turnStartFrame(t),
		agentChunkFrame(t, "before"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindModelFallback,
			map[string]any{"from": "a", "to": "c", "trigger": "refusal", "agentSubtaskId": "sub-1"})),
		agentChunkFrame(t, " still"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindModelFallbackPromoted,
			map[string]any{"from": "a", "to": "c", "trigger": "refusal", "agentSubtaskId": ""})),
		agentChunkFrame(t, " going"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", infoKindModelFallbackPromoted,
			map[string]any{"from": "a", "to": "b", "trigger": "refusal"})),
		turnEndFrame(t, "end_turn"),
	})
	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	want := []marotte.EntryKind{
		marotte.EntryKindTurnOpen, marotte.EntryKindText, marotte.EntryKindModelFallback, marotte.EntryKindTurnClose,
	}
	if got := kindsOf(turns[0]); !slices.Equal(got, want) {
		t.Fatalf("entry kinds = %v, want %v:\n%s", got, want, dumpTurns(turns))
	}
	got := modelFallbacksOf(t, turns[0].Entries)
	if want := []marotte.EntryModelFallback{{From: "a", To: "b", Scope: "session", Trigger: "refusal"}}; !slices.Equal(got, want) {
		t.Errorf("replayed model_fallback = %+v, want %+v", got, want)
	}
}

// A chat's promotion is a boundary row with the session wording, and logs no unknown kind.
// Serial: it swaps the slog default and resets the census ledger.
func TestHandleSessionInfoUpdate_ModelFallbackPromotedReadsAsTheSessionsFallback(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	defer captureSlog(&logbuf)()
	deps, _ := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
		"content": map[string]any{"type": "text", "text": "refused attempt"},
	}), false, FrameAttribution{})

	tr.HandleSessionInfoUpdate(t.Context(), "c1", modelFallbackInfo(t, map[string]any{
		"kind": infoKindModelFallbackPromoted, "from": "opus", "to": "sonnet", "trigger": "refusal",
	}), FrameAttribution{})

	entries := deps.chatEntries("c1")
	if got, want := entryKinds(entries), []marotte.EntryKind{marotte.EntryKindText, marotte.EntryKindModelFallback}; !slices.Equal(got, want) {
		t.Fatalf("sealed entries = %v, want %v", got, want)
	}
	if got, want := modelFallbacksOf(t, entries), []marotte.EntryModelFallback{{From: "opus", To: "sonnet", Scope: "session", Trigger: "refusal"}}; !slices.Equal(got, want) {
		t.Errorf("model_fallback = %+v, want %+v", got, want)
	}
	if strings.Contains(logbuf.String(), "UNKNOWN") {
		t.Errorf("a promotion logged an unknown kind or field:\n%s", logbuf.String())
	}
}
