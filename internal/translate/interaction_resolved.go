package translate

import (
	"log/slog"

	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// KAS emits it for every approval it resolves. Outcome "cancelled" (the asker ended) and
// "rule_decided" (a policy recheck decided the ask while it waited) both close an ask nobody
// answered, so its JSON-RPC id expects no answer.
type interactionResolvedBlock struct {
	ToolCallID     string `json:"toolCallId"`
	Outcome        string `json:"outcome"`
	SelectedOption string `json:"selectedOption"`
}

// The outcomes of an ask KAS closed without an answer from marotte.
const (
	interactionOutcomeCancelled   = "cancelled"
	interactionOutcomeRuleDecided = "rule_decided"
)

// withdrawnOutcome reports whether outcome closes an ask nobody answered.
func withdrawnOutcome(outcome string) bool {
	return outcome == interactionOutcomeCancelled || outcome == interactionOutcomeRuleDecided
}

// handleInteractionResolved retires an ask KAS closed without an answer. Every other outcome
// echoes an answer marotte sent and already claimed. No response is sent: KAS no
// longer awaits the id.
func (t *Translator) handleInteractionResolved(key marotte.ChatID, r *interactionResolvedBlock) {
	if !withdrawnOutcome(r.Outcome) || r.ToolCallID == "" {
		return
	}
	if !t.pendingPerms.PendingPermsWithdraw(key, r.ToolCallID) {
		slog.Debug("interaction_resolved: no pending ask for the withdrawn tool call",
			"chat_id", key, "tool_call_id", textsafe.SanitizeSingleLineBounded(r.ToolCallID, maxCensusNameBytes))
	}
}
