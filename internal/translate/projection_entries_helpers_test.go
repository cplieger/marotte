package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// seqIDs returns a deterministic id generator so projected transcripts can be
// compared by value.
func seqIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("m%d", n)
	}
}

// replayFrame builds one session/update `update` object. sub is the
// _meta.kiro.kind for a session_info_update; extra merges into _meta.kiro.
func replayFrame(t *testing.T, kind marotte.ACPUpdateKind, text, sub string, extra map[string]any) (marotte.ACPUpdateKind, json.RawMessage) {
	t.Helper()
	kiro := map[string]any{"replay": true}
	if sub != "" {
		kiro["kind"] = sub
	}
	maps.Copy(kiro, extra)
	u := map[string]any{
		"sessionUpdate": string(kind),
		"_meta":         map[string]any{"kiro": kiro},
	}
	if text != "" {
		u["content"] = map[string]any{"type": "text", "text": text}
	}
	return kind, mustJSON(t, u)
}

// pair packs replayFrame's two returns into the shape entryProject takes, so a
// frame list reads as one line per frame.
func pair(kind marotte.ACPUpdateKind, raw json.RawMessage) [2]any {
	return [2]any{kind, raw}
}

// replaySteerFrame builds a replayed user_message_chunk on KAS's steering channel: the
// `source` discriminator sits at `_meta.kiro.source`, never on the update object.
func replaySteerFrame(t *testing.T, id, text string) (marotte.ACPUpdateKind, json.RawMessage) {
	t.Helper()
	return replayFrame(t, replayUserChunkKind, text, "", map[string]any{
		"messageId": id,
		"timestamp": "2026-09-08T20:01:00.000Z",
		"source":    "steer",
	})
}

func turnStartFrame(t *testing.T) [2]any {
	t.Helper()
	return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_start", map[string]any{"turnStart": true}))
}

func turnEndFrame(t *testing.T, stop string) [2]any {
	t.Helper()
	return pair(replayFrame(t, marotte.ACPUpdateSessionInfo, "", "turn_end", map[string]any{
		"turnEnd":    map[string]any{"stopReason": stop},
		"stopReason": stop,
	}))
}

func agentChunkFrame(t *testing.T, text string) [2]any {
	t.Helper()
	return pair(replayFrame(t, marotte.ACPUpdateAgentChunk, text, "", nil))
}

// replayUserRow is one replayed user record: the fields a resend rule reads, named
// rather than positional so two adjacent strings cannot be transposed.
type replayUserRow struct {
	id    string
	ts    string
	text  string
	tag   string
	steer bool
}

func (r replayUserRow) frame(t *testing.T) [2]any {
	t.Helper()
	extra := map[string]any{"messageId": r.id, "timestamp": r.ts}
	if r.steer {
		extra["source"] = "steer"
	}
	if r.tag != "" {
		extra["userMessageTag"] = r.tag
	}
	return pair(replayFrame(t, replayUserChunkKind, r.text, "", extra))
}
