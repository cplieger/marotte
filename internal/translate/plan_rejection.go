package translate

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"

	"github.com/cplieger/marotte/internal/marotte"
)

// workflowPlanRejection is _meta.kiro.workflowPlanRejection: KAS reports a person's rejection of
// a launch plan to the run's parent session, and only for a wf-workflow-creator step's top-level
// launch plan answered reject_once or reject_always.
type workflowPlanRejection struct {
	ID              string `json:"id"`
	RejectionReason string `json:"rejectionReason"`
	WorkflowPlan    struct {
		WorkflowID string `json:"workflowId"`
	} `json:"workflowPlan"`
}

type workflowPlanRejectionShadow workflowPlanRejection

// UnmarshalJSON decodes the block and reports any top-level member this type does not read.
// optionKind is declined because both reject kinds read alike, origin because the note never
// names the creator step's session. Of workflowPlan only its workflowId is read.
func (r *workflowPlanRejection) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*workflowPlanRejectionShadow)(r)); err != nil {
		return err
	}
	censusMeta("session_info_update._meta.kiro.workflowPlanRejection", data,
		reflect.TypeFor[workflowPlanRejectionShadow](), "optionKind", "origin")
	return nil
}

func (t *Translator) handlePlanRejection(ctx context.Context, chatID marotte.ChatID, r *workflowPlanRejection) {
	runID := r.WorkflowPlan.WorkflowID
	if r.ID == "" || runID == "" {
		slog.Debug("plan rejection: no id or run, dropped", "chat_id", chatID, "workflow_id", runID)
		return
	}
	reason := []rune(displayLines(r.RejectionReason))
	reason = reason[:min(len(reason), marotte.MaxRejectionReasonRunes)]
	t.runs.RunPlanRejection(ctx, chatID, runID, r.ID, string(reason))
}
