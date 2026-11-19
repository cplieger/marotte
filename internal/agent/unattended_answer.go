package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// unattendedAnswer is what a floor sends for the absent user, and why.
type unattendedAnswer struct {
	outcome *marotte.PermissionOutcome
	// verb is outcomeRefused or outcomeApproved.
	verb  string
	asker refusedAsk
}

// decideUnattended answers an ask nobody answered within the budget, for both floors: refused with note on the
// advertised one-shot reject, or allow_once when approve is set and the ask is one a switch may answer, never an
// `_always` option. delegated says the session hands unsettled requests to Kiro's safety check.
func decideUnattended(chatID marotte.ChatID, params json.RawMessage, approve, delegated bool, note string) unattendedAnswer {
	name := permissionAskName(params)
	a := unattendedAnswer{asker: askerOf(params, delegated), verb: outcomeRefused}
	if refusal := a.asker.refusal(); approve && refusal != "" {
		slog.Warn("unattended auto-approve: refusing, "+refusal,
			"chat_id", chatID, "tool", name.tool, "subject", name.subject)
		approve = false
	}
	// Cancelled only when no reject is offered. KAS keeps a plan approval's note and hands it to the steps of the
	// creator's next approved plan as the user's correction, so the floor's machine text goes on no plan.
	a.outcome = marotte.PermissionOutcomeCancelled()
	if opt := optionIDByKind(params, optionKindRejectOnce); opt != "" {
		a.outcome = marotte.PermissionOutcomeWithRejectionReason(opt, note)
		if planApprovalAsk(params) {
			a.outcome = marotte.PermissionOutcomeSelected(opt)
		}
	}
	if !approve {
		return a
	}
	opt := optionIDByKind(params, optionKindAllowOnce)
	if opt == "" {
		// Never invent an id; fall back to refusal.
		slog.Warn("unattended auto-approve: request offered no allow option, refusing instead",
			"chat_id", chatID, "tool", name.tool)
		return a
	}
	a.outcome, a.verb = marotte.PermissionOutcomeSelected(opt), outcomeApproved
	return a
}
