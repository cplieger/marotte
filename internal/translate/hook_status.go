package translate

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// hookUpdateBlock is the kind=="hook_update" sub-block of a session_info_update, one
// frame per hook execution. Name is untrusted workspace file content.
type hookUpdateBlock struct {
	HookID      string `json:"hookId"`
	OperationID string `json:"operationId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ActionType  string `json:"actionType"`
}

// hookStatusCompleted is the wire status of a hook execution KAS reports as successful.
const hookStatusCompleted = "completed"

// handleHookUpdate appends a `Hook fired` card to the chat's open turn as a
// tool_call entry in lane "", the way HandleToolCall appends a real tool call,
// gated on hooks.showStatus. A step's hook update never reaches here: the
// attribution gate drops it before the hook branch runs.
func (t *Translator) handleHookUpdate(ctx context.Context, chatID marotte.ChatID, h *hookUpdateBlock) {
	if !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	turn := t.turns.TurnFoldTarget(ctx, chatID)
	if turn == nil {
		return
	}
	call := hookToolCall(h, time.Now().UnixMilli())
	entry := marotte.EntryToolCallOf(&call)
	sealed, err := turn.ToolCall(ctx, "", &entry)
	t.publishSealed(ctx, chatScope(chatID), sealed)
	if err != nil {
		appendFailed(chatScope(chatID), "hook tool_call", err)
	}
}

// hookToolCall is the settled tool call one hook_update frame becomes: no input, no
// output, no content.
func hookToolCall(h *hookUpdateBlock, ts int64) marotte.ToolCall {
	return marotte.ToolCall{
		ID:     "hook-" + h.OperationID,
		Title:  "Hook fired: " + displayText(h.Name),
		Kind:   marotte.ToolKindHook,
		Status: hookToolStatus(h.Status),
		Ts:     ts,
	}
}

// hookToolStatus maps the frame's status onto the card's terminal state.
//
// No outcome text is rendered: KAS 2.21.4's hook emitter hardcodes actionState:"Success"
// and sends no output, so a hook that exited non-zero still arrives "completed"
// (kirodotdev/Kiro#11369). The state is mapped from the wire rather than fixed so a
// build that starts reporting failures paints red with no code change.
func hookToolStatus(s string) marotte.ToolStatus {
	if s == hookStatusCompleted {
		return marotte.ToolCompleted
	}
	return marotte.ToolFailed
}
