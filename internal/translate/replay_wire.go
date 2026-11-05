package translate

// The wire shapes a REPLAYED session/update carries, reusing the live path's sub-block types.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// replayInfoMeta decodes a replayed session_info_update's `_meta`: only the consumed sub-kinds,
// with the turn fields on the LIVE path's types.
type replayInfoMeta struct {
	Meta struct {
		Kiro replayInfoKiro `json:"kiro"`
	} `json:"_meta"`
}

// replayInfoKiro is replayInfoMeta's `_meta.kiro` block.
type replayInfoKiro struct {
	SummaryMessage *struct {
		Content string `json:"content"`
	} `json:"summaryMessage"`
	TurnEnd *turnEndBlock `json:"turnEnd"`
	// TurnID, ExecutionID, UserMessageTag, Timestamp and BoundarySource ride the replayed brackets;
	// UserMessageTag only turn_start. BoundarySource is "recovered" on a bracket KAS synthesized
	// because its record holds none, such as the cancelled end of a turn cut by an engine death.
	TurnID              string                    `json:"turnId"`
	ExecutionID         string                    `json:"executionId"`
	UserMessageTag      string                    `json:"userMessageTag"`
	Timestamp           string                    `json:"timestamp"`
	BoundarySource      string                    `json:"boundarySource"`
	DisplayError        *displayErrorBlock        `json:"displayError"`
	PendingInteraction  *pendingInteractionBlock  `json:"pendingInteraction"`
	InteractionResolved *interactionResolvedBlock `json:"interactionResolved"`
	TTFT                *acpTurnTiming            `json:"ttft"`
	modelFallbackFields
	ContextBreakdown    json.RawMessage     `json:"contextBreakdown"`
	Kind                string              `json:"kind"`
	Message             string              `json:"message"`
	PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
	SteeringDocuments   []json.RawMessage   `json:"steeringDocuments"`
	RequestIDs          []string            `json:"requestIds"`
	Recoveries          []string            `json:"recoveries"`
	ElapsedTime         float64             `json:"elapsedTime"`
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

type replayChunk struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
	Meta acpKiroMeta `json:"_meta"`
}

// workflowProgressIDPrefix is the id of a workflow-progress row KAS writes onto the launching
// chat's transcript; it replays as a user chunk holding a JSON blob.
const workflowProgressIDPrefix = "wf-progress-"

// workflowProgressKind is the semantic discriminator on the same row.
const workflowProgressKind = "workflow-progress"

// isWorkflowProgress reports whether a replayed user chunk is a workflow-progress row, by BOTH
// discriminators (the id prefix is the one measured to reach the wire).
func isWorkflowProgress(m *acpKiroMeta) bool {
	return strings.HasPrefix(m.Kiro.MessageID, workflowProgressIDPrefix) ||
		m.Kiro.Notification.Kind == workflowProgressKind
}

// replayUserChunkKind is KAS's user-message replay frame; the LIVE path has no handler for it
// (marotte echoes its own user rows).
const replayUserChunkKind marotte.ACPUpdateKind = "user_message_chunk"
