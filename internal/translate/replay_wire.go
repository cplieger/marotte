package translate

// The wire shapes a REPLAYED session/update carries, shared by the entry projection
// and any reader of a replay: the live path's own sub-block types where the frame
// reuses them, so a field cannot be forgotten in a second declaration.

import (
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// replayInfoMeta decodes the `_meta.kiro` block of a replayed session_info_update. Only
// the sub-kinds the projection consumes are declared, and the three turn fields reuse the
// LIVE path's types rather than twins — the replay carries the same payloads, so a second
// declaration would be a second place a field can be forgotten.
type replayInfoMeta struct {
	Meta struct {
		Kiro struct {
			SummaryMessage *struct {
				Content string `json:"content"`
			} `json:"summaryMessage"`
			TurnEnd             *turnEndBlock       `json:"turnEnd"`
			Kind                string              `json:"kind"`
			PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
			ElapsedTime         float64             `json:"elapsedTime"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// replayTS converts KAS's RFC3339 timestamp to the epoch millis an entry's Ts
// carries. A missing or unparseable value yields 0 for the caller's own fallback —
// never time.Now(), which would stamp replayed history with the load's clock.
func replayTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// replayChunk decodes a replayed user/agent/thought message chunk.
type replayChunk struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Meta ACPKiroMeta `json:"_meta"`
}

// workflowProgressIDPrefix is the id KAS gives a workflow-progress row it writes onto
// the LAUNCHING chat's transcript. It replays as a user_message_chunk whose content is
// a JSON blob, so rendering it as prose would claim the user typed JSON.
const workflowProgressIDPrefix = "wf-progress-"

// workflowProgressKind is the semantic discriminator on the same row.
const workflowProgressKind = "workflow-progress"

// isWorkflowProgress reports whether a replayed user chunk is really a workflow
// progress row. BOTH discriminators: the id prefix is the one measured to reach the
// wire, and the semantic field starts working if the nested block survives too.
func isWorkflowProgress(m *ACPKiroMeta) bool {
	return strings.HasPrefix(m.Kiro.MessageID, workflowProgressIDPrefix) ||
		m.Kiro.Notification.Kind == workflowProgressKind
}

// replayUserChunkKind is KAS's user-message replay frame. marotte declares no
// ACPUpdate* constant for it because the LIVE path deliberately has no handler
// (marotte echoes its own user bubbles).
const replayUserChunkKind marotte.ACPUpdateKind = "user_message_chunk"
