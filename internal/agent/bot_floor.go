package agent

// The Bot turn floor: a turn a rule's prompt opened, or the wake turn after a run an automatic turn launched, has
// nobody watching, so a permission ask in it is answered for the absent user after unattendedAskBudget rather than
// holding the turn live and every later rule delivery behind it. Deny by default; `bot_auto_approve` opts in.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/obs"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
)

// botTurnRejectionReason is the Bot turn floor's deny note, so the turn's agent does not retry.
const botTurnRejectionReason = "This turn started with no one watching it, " +
	"so the permission was refused automatically. Finish the task without it, " +
	"or stop and name the permission the task needs."

const logMsgBotUnattendedPermission = "automatic Bot turn's permission answered with no user present"

// permissionWithBotFloor wraps the permission handler: the request reaches the client unchanged and, in a turn
// nobody watches, gets a deadline after which marotte answers.
func (rt *Runtime) permissionWithBotFloor(inner askHandler) askHandler {
	return func(ctx context.Context, chatID marotte.ChatID, origin translate.AskOrigin, msg *marotte.RPCResponse) {
		inner(ctx, chatID, origin, msg)
		if msg.ID == nil {
			return
		}
		turnID, marked := rt.coord.turns.unattendedTurn(chatID)
		if !marked {
			return
		}
		acpID, params := *msg.ID, msg.Params
		rt.lifecycle.afterUnlessDraining(unattendedAskBudget, func() {
			rt.answerBotTurn(chatID, origin, acpID, turnID, params)
		})
	}
}

// answerBotTurn settles a still-pending ask of turnID for an absent user, unless a person stepped into the turn.
func (rt *Runtime) answerBotTurn(chatID marotte.ChatID, origin acpResponder, acpID int64, turnID string, params json.RawMessage) {
	ctx, cancel := rt.lifecycle.derivedContext()
	defer cancel()
	approve, delegated := readBotAutoApprove(ctx, rt.lifecycle.configDir), rt.botSessionDelegated(chatID)
	// After the reads: a person's steer accepted during them keeps the ask. The claim decides the race with a person
	// on the page, as the run floor's does.
	if !rt.coord.turns.stillUnattended(chatID, turnID) {
		return
	}
	a := decideUnattended(chatID, params, approve, delegated, botTurnRejectionReason)
	reply, ok := rt.bus.TakePendingPermOn(origin, acpID, marotte.SettledByUnattended)
	if !ok {
		return
	}
	switch answered, err := reply.Respond(ctx, a.outcome); answered {
	case command.AnswerDelivered:
	case command.AnswerWithdrawn:
		return
	default:
		slog.Error("automatic Bot turn's permission answer failed", "chat_id", chatID, "error", err)
		return
	}
	name := permissionAskName(params)
	slog.Warn(logMsgBotUnattendedPermission, "outcome", a.verb, "chat_id", chatID, "turn_id", turnID,
		"tool", name.tool, "subject", name.subject, "budget", unattendedApprovalBudget)
	answer := obs.BotAnswerRefused
	if a.verb == outcomeApproved {
		answer = obs.BotAnswerApproved
	}
	obs.BotUnattendedAnswer(answer)
}

// botSessionDelegated reports whether chatID's session hands unsettled requests to Kiro's safety check.
func (rt *Runtime) botSessionDelegated(chatID marotte.ChatID) bool {
	sb := rt.bridge.mgr.get(chatID)
	return sb == nil || sb.delegatesToSafetyCheck()
}

// readBotAutoApprove is the floor's read of `bot_auto_approve`: botAutoApprove, a var for tests.
var readBotAutoApprove = botAutoApprove

// Off by default, and off over a config.json the config watch settled as absent or unreadable.
func botAutoApprove(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyBotAutoApprove, &b) {
		return false
	}
	return b
}

// unattendedTurn names chatID's own turn while nobody watches it.
func (r *turnRegistry) unattendedTurn(chatID marotte.ChatID) (string, bool) {
	lc, ok := r.lookup(chatID)
	if !ok {
		return "", false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if t := lc.own; t != nil && t.unattended && !t.finalizing {
		return t.ID, true
	}
	return "", false
}

// stillUnattended reports whether turnID still has nobody watching it.
func (r *turnRegistry) stillUnattended(chatID marotte.ChatID, turnID string) bool {
	lc, ok := r.lookup(chatID)
	if !ok {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	t := lc.turnLocked(turnID)
	return t != nil && t.unattended
}

// startsUnattended reports whether a turn opened from caller, with the wake hold and lease depth its open took,
// starts with nobody watching: a rule's prompt, or the wake after a run an automatic turn launched.
func startsUnattended(caller *marotte.Caller, wake bool, leaseDepth int) bool {
	return (caller != nil && caller.Kind == marotte.CallerKindRule) || (wake && leaseDepth >= 1)
}

// recordOpenedPresets records the security presets sb's session door sent.
func (sb *sharedBridge) recordOpenedPresets(presets []string) {
	sb.mu.Lock()
	sb.openedPresets, sb.presetsKnown = slices.Clone(presets), true
	sb.mu.Unlock()
}

// delegatesToSafetyCheck reports whether sb's session hands unsettled requests to Kiro's safety check. A session
// whose presets were never recorded cannot show the check was off, so it counts as delegated.
func (sb *sharedBridge) delegatesToSafetyCheck() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return !sb.presetsKnown || policyfile.DelegatesToSafetyCheck(sb.openedPresets)
}
