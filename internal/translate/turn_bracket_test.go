package translate

// Serial, never parallel: the census tests swap slog's process-global default.

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// The bracket fields kiro-cli 2.37's live turn_start carries (session_info_update built by
// KAS's xa over AHo's facts): messageId and timestamp, executionId and turnId, boundarySource
// and the prompt's userMessageTag.
func liveTurnStart(t *testing.T, extra map[string]any) json.RawMessage {
	t.Helper()
	kiro := map[string]any{
		"kind": "turn_start", "turnStart": true,
		"messageId": "rec-1", "timestamp": "2026-11-01T10:00:00.000Z", "boundarySource": "observed",
	}
	maps.Copy(kiro, extra)
	return mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": kiro}})
}

func TestHandleSessionInfoUpdate_TheTurnStartNamesItsTurnAndTag(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]any
		want  TurnStartFrame
	}{
		{
			name:  "a tagged prompt's bracket",
			extra: map[string]any{"executionId": "exec-1", "turnId": "turn-1", "userMessageTag": "t-0123456789abcdef"},
			want:  TurnStartFrame{KASTurnID: "turn-1", Tag: "t-0123456789abcdef"},
		},
		{
			name:  "an agent turn's bracket with only an execution id",
			extra: map[string]any{"executionId": "exec-2"},
			want:  TurnStartFrame{KASTurnID: "exec-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSessionInfoUpdate(t.Context(), "c1", liveTurnStart(t, tc.extra), FrameAttribution{})

			want := []turnBracket{{chat: "c1", kind: "start", frame: tc.want}}
			if !slices.Equal(deps.brackets, want) {
				t.Errorf("brackets = %+v, want %+v", deps.brackets, want)
			}
		})
	}
}

// Every member of 2.37's live bracket and of the turn-scoped chunk meta is decoded or declined,
// so none reaches the census as UNKNOWN.
func TestTurnBracket_The237MembersAreQuietInTheCensus(t *testing.T) {
	resetCensus(t)
	var logbuf bytes.Buffer
	captureSlog(t, &logbuf)
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSessionInfoUpdate(t.Context(), "c1", liveTurnStart(t, map[string]any{
		"executionId": "exec-1", "turnId": "turn-1", "userMessageTag": "t-0123456789abcdef",
	}), FrameAttribution{})
	tr.HandleAssistantChunk(t.Context(), "c1", mustJSON(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "an answer"},
		"_meta":         map[string]any{"kiro": map[string]any{"messageId": "rec-2", "executionId": "exec-1", "turnId": "turn-1"}},
	}), false, FrameAttribution{})
	tr.HandleSessionInfoUpdate(t.Context(), "c1", mustJSON(t, map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind": "turn_end", "turnEnd": map[string]any{"stopReason": "end_turn"}, "stopReason": "end_turn",
		"messageId": "rec-3", "timestamp": "2026-11-01T10:00:05.000Z", "boundarySource": "observed",
		"executionId": "exec-1", "turnId": "turn-1",
	}}}), FrameAttribution{})

	if out := logbuf.String(); strings.Contains(out, "UNKNOWN") {
		t.Errorf("the census reported a 2.37 bracket or chunk member: %s", out)
	}
}

// replayBracket is a replayed bracket as KAS's AHo builds it: the record's messageId and
// timestamp, the execution and turn ids, and boundarySource.
func replayBracket(t *testing.T, kind string, extra map[string]any) [2]any {
	t.Helper()
	kiro := map[string]any{"executionId": "exec-1", "turnId": "turn-1", "boundarySource": "observed"}
	if kind == "turn_start" {
		kiro["turnStart"] = true
	} else {
		kiro["turnEnd"] = map[string]any{"stopReason": "end_turn"}
		kiro["stopReason"] = "end_turn"
	}
	maps.Copy(kiro, extra)
	return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", kind, kiro))
}

func epochMillis(t *testing.T, rfc3339 string) int64 {
	t.Helper()
	at, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}
	return at.UnixMilli()
}

func TestEntryProjection_AReplayedBracketStampsItsTurnIDTagAndTimes(t *testing.T) {
	const opened, ended = "2026-11-01T10:00:00.000Z", "2026-11-01T10:00:09.000Z"
	turns := entryProject([][2]any{
		replayBracket(t, "turn_start", map[string]any{"userMessageTag": "t-0123456789abcdef", "timestamp": opened}),
		agentChunkFrame(t, "an answer"),
		replayBracket(t, "turn_end", map[string]any{"timestamp": ended}),
	})

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	if turns[0].Tag != "t-0123456789abcdef" {
		t.Errorf("Tag = %q, want the turn_start's userMessageTag", turns[0].Tag)
	}
	open := entryOfKind(t, turns[0], marotte.EntryKindTurnOpen)
	var payload marotte.EntryTurnOpen
	payloadOf(t, open, &payload)
	if payload.KASTurnID != "turn-1" {
		t.Errorf("turn_open.kas_turn_id = %q, want the bracket's turnId", payload.KASTurnID)
	}
	if want := epochMillis(t, opened); open.Ts != want {
		t.Errorf("turn_open ts = %d, want the turn_start's %d", open.Ts, want)
	}
	if want, got := epochMillis(t, ended), entryOfKind(t, turns[0], marotte.EntryKindTurnClose).Ts; got != want {
		t.Errorf("turn_close ts = %d, want the turn_end's %d", got, want)
	}
}

// A prompt turn is dated by its user row, which precedes the bracket.
func TestEntryProjection_APromptTurnKeepsThePromptsTime(t *testing.T) {
	const asked = "2026-11-01T09:59:58.000Z"
	turns := entryProject([][2]any{
		replayUserRow{id: "kas-rec-1", ts: asked, text: "a question"}.frame(t),
		replayBracket(t, "turn_start", map[string]any{"timestamp": "2026-11-01T10:00:00.000Z"}),
	})

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	if want, got := epochMillis(t, asked), entryOfKind(t, turns[0], marotte.EntryKindTurnOpen).Ts; got != want {
		t.Errorf("turn_open ts = %d, want the prompt's %d", got, want)
	}
}

// KAS synthesizes a cancelled turn_end, marked recovered, for a turn whose record has no end; it is
// no observed outcome, so the projection files no closer and the merge keeps marotte's own.
func TestEntryProjection_ARecoveredTurnEndFilesNoCloser(t *testing.T) {
	turns := entryProject([][2]any{
		replayBracket(t, "turn_start", nil),
		agentChunkFrame(t, "cut off"),
		pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
			"turnEnd": map[string]any{"stopReason": "cancelled"}, "stopReason": "cancelled",
			"executionId": "exec-1", "boundarySource": "recovered",
		})),
	})

	if len(turns) != 1 {
		t.Fatalf("projected %d turns, want 1:\n%s", len(turns), dumpTurns(turns))
	}
	if slices.Contains(kindsOf(turns[0]), marotte.EntryKindTurnClose) {
		t.Errorf("a recovered turn_end was projected as a closer:\n%s", dumpTurns(turns))
	}
}
