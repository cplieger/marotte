package translate

import (
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
)

// interactionResolvedBlock is the kind=="interaction_resolved" block. KAS emits
// it for every approval it resolves; outcome "cancelled" means the asker ended and
// KAS stopped waiting, so the request's JSON-RPC id is answered by nobody.
type interactionResolvedBlock struct {
	ToolCallID     string `json:"toolCallId"`
	Outcome        string `json:"outcome"`
	SelectedOption string `json:"selectedOption"`
}

// interactionOutcomeCancelled is the outcome of an ask KAS withdrew.
const interactionOutcomeCancelled = "cancelled"

// handleInteractionResolved retires an ask KAS withdrew. Every other outcome
// echoes an answer marotte sent and already claimed. No response is sent: KAS no
// longer awaits the id.
func (t *Translator) handleInteractionResolved(key marotte.ChatID, r *interactionResolvedBlock) {
	if r.Outcome != interactionOutcomeCancelled || r.ToolCallID == "" {
		return
	}
	if !t.pendingPerms.PendingPermsWithdraw(key, r.ToolCallID) {
		slog.Debug("interaction_resolved: no pending ask for the withdrawn tool call",
			"chat_id", key, "tool_call_id", runesafe.SanitizeSingleLineBounded(r.ToolCallID, maxCensusNameBytes))
	}
}
