package command

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/securityprofile"
)

// CancelGrace is how long a cooperative session/cancel gets to end the turn before marotte unblocks
// it itself (KiroCrew's `_CANCEL_GRACE_SECS`). A var only so a test can shrink it.
var CancelGrace = 10 * time.Second

// ErrCancelGraceExpired is the cause the grace timer attaches to the prompt context it cancels, so
// the failure site concludes `cancelled` rather than `interrupted`. Shutdown and cancelPromptCall
// leave the cause at context.Canceled, and the first cancellation sets the cause, so whichever of
// shutdown and the grace fires first decides the outcome.
var ErrCancelGraceExpired = errors.New("cancel grace expired")

// cmdCreateChat creates a new chat, opens its tab, and returns both, so the caller can address what
// it created. It mints the id when the envelope carries none; a client-minted id is still accepted
// (validChatID gates it).
func cmdCreateChat(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CreateChatCommand
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
		}
	}
	name := p.Name
	if name == "" {
		name = marotte.DefaultChatName
	}
	if len(name) > marotte.MaxChatNameBytes {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validIdent(p.Model) || !validIdent(p.OpID) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	opened, err := mem.CreateChatAndOpen(ctx, ChatCreate{
		OpID:   p.OpID,
		ChatID: cmd.ChatID,
		Init: func(c *marotte.Chat) {
			c.Name = name
			c.Model = p.Model
		},
	})
	if err != nil {
		return nil, err
	}
	return openedResponse(&opened, nil), nil
}

// openedResponse is the shape every create-and-open answers with: the chat
// header, the tab opened for it, and the version that open produced, plus
// whatever the command adds. Subject is omitted when no tab store is wired
// (a zero-valued subject would name a tab with an empty id).
func openedResponse(opened *ChatOpened, extra map[string]any) any {
	body := make(map[string]any)
	maps.Copy(body, extra)
	body["chat"] = opened.Chat.Header()
	body[keyVersion] = opened.Version
	if opened.Subject.ID != "" {
		body["subject"] = opened.Subject
	}
	return responseWith(body)
}

// cmdDeleteChat removes a chat through Membership.DeleteChatAndCloseTabs, which owns the order.
func cmdDeleteChat(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CloseTabCommand
	if len(cmd.Payload) > 0 {
		// Optional and carries only an op_id; ignored rather than refused if
		// unreadable, since the subject is the envelope's chat id.
		_ = json.Unmarshal(cmd.Payload, &p)
	}
	if !validIdent(p.OpID) {
		p.OpID = ""
	}
	if err := mem.DeleteChatAndCloseTabs(ctx, cmd.ChatID, p.OpID); err != nil {
		return nil, err
	}
	return responseOK, nil
}

// cmdCancel cancels the active turn, if any. The turn's end leaves its unread
// steers for the next send; a payload lead names the row the reader wants first. It
// never waits on the steer lock, so a stop never queues behind a delete.
func cmdCancel(
	ctx context.Context,
	bridges bridgeAccess,
	perms pendingPermAccess,
	terms terminalAccess,
	stops turnStopper,
	queue SteerQueue,
	cmd *marotte.ClientCommand,
) (any, error) {
	// An absent or empty payload is a plain cancel.
	var p marotte.CancelCommand
	if len(cmd.Payload) > 0 && json.Unmarshal(cmd.Payload, &p) != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// First, before any signal: a retry reading the stop after it registered its
	// prompt call is then guaranteed a grace budget this cancel can arm.
	stops.RequestStop(cmd.ChatID)

	// Only pending permissions are cleared; KAS owns the write gate and
	// cancelling a turn already reverts its own approval.
	perms.ClearPendingPermsForChat(cmd.ChatID)

	// Stopping the model does not stop the processes its turn already
	// spawned. Scoped to the turn's own terminals only.
	terms.KillForTurn(cmd.ChatID)

	sb := bridges.Bridge(cmd.ChatID)
	if sb == nil {
		return responseOK, nil
	}
	undoLead := func() {}
	if queue != nil && p.Lead != "" {
		undoLead = queue.SetSteerLead(cmd.ChatID, p.Lead)
	}
	if err := sb.Notify(ctx, marotte.MethodCancel, sessionParams(sb)); err != nil {
		undoLead()
		slog.Error("cancel failed", "chat_id", cmd.ChatID, keyError, err)
	}
	// session/cancel is a notification nothing acks: the turn ends only when KAS answers
	// session/prompt. If it never does, the grace budget cancels the prompt's context so the
	// ordinary prompt-failure path finalizes the turn.
	if !sb.ArmCancelGrace(sb.PromptGeneration(), CancelGrace) {
		slog.Debug("cancel: no in-flight prompt to arm a grace budget against", "chat_id", cmd.ChatID)
	}
	return responseOK, nil
}

// closeChatTeardown is the tab-close teardown: the turn, the runs and the process go; the chat
// RECORD stays. Ordering is the contract: the turn is cancelled first (graceful stop), then the
// runs (durable state a dead process would only pause), then the process teardown, which flushes
// the in-flight buffer and kills the chat's agent terminals. Pending asks are left for that bridge's
// end to retire as ended: a clear here would drop the cards without saying why.
func closeChatTeardown(ctx context.Context, bridges bridgeAccess, stops turnStopper, teardown chatTeardown, chatID marotte.ChatID) {
	slog.Info("chat tab closed; tearing down its bridge", "chat_id", chatID)
	stops.RequestStop(chatID)
	teardown.BeginChatTeardown(chatID, true)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, marotte.MethodCancel, sessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.CloseChatState(ctx, chatID)
}

// deleteChatTeardown is closeChatTeardown's delete grade, for a chat the
// retention-off close escalation has already erased: same graceful cancel
// first, then the delete-grade teardown driven from the session chain the
// record's delete answered (a record-reading teardown would no-op on a
// deleted chat). Mirrors closeChatTeardown so the two grades cannot drift.
func deleteChatTeardown(ctx context.Context, bridges bridgeAccess, stops turnStopper, teardown chatTeardown, chatID marotte.ChatID, sessionChain []string) {
	stops.RequestStop(chatID)
	teardown.BeginChatTeardown(chatID, false)
	if sb := bridges.Bridge(chatID); sb != nil {
		if err := sb.Notify(ctx, marotte.MethodCancel, sessionParams(sb)); err != nil {
			slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
		}
	}
	teardown.DeleteChatStateByChain(ctx, chatID, sessionChain, RunStopTabClosed)
}

// cmdPermission forwards the user's permission dialog choice to kiro-cli. An always answer
// saves its rule at user scope: the profile is switched to Custom first (Customize), and only
// once that write landed does kiro-cli get the answer, whose rule it writes into the same file.
func cmdPermission(ctx context.Context, perms pendingPermAccess, profiles profileSwitcher, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.PermissionResponseCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	// Refused before the claim, so a bad note leaves the request answerable.
	reason := strings.TrimSpace(p.RejectionReason)
	if utf8.RuneCountInString(reason) > marotte.MaxRejectionReasonRunes ||
		(reason != "" && len(p.FileDecisions) > 0) {
		return nil, StatusError(http.StatusBadRequest, errRejectionReasonInvalid)
	}
	always, err := alwaysConsent(perms, cmd.ChatID, &p)
	if err != nil {
		return nil, err
	}
	if always != nil {
		if err := profiles.EnsureCustomProfile(ctx); err != nil {
			return nil, customizeRefusal(err)
		}
	}
	// Claim the request before answering it: two tabs on one chat can both
	// see the card, and kiro-cli silently discards the second answer for a
	// request id already resolved.
	reply, pending, offered := perms.TakePendingPermissionOption(cmd.ChatID, p.RequestID, p.OptionID, marotte.SettledByUser)
	if !pending {
		return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
	}
	if !offered {
		return nil, StatusError(http.StatusBadRequest, errPermissionOptionNotOffered)
	}
	// A turn approval answers on the same reply, with per-file decisions in
	// _meta; built through one helper so the omitted-id-means-reject rule
	// lives in one place.
	var outcome *marotte.PermissionOutcome
	switch {
	case always != nil:
		outcome = marotte.PermissionOutcomeAlways(p.OptionID, *always)
	case len(p.FileDecisions) > 0:
		outcome = marotte.PermissionOutcomeWithFileDecisions(p.OptionID, p.FileDecisions)
	default:
		outcome = marotte.PermissionOutcomeWithRejectionReason(p.OptionID, reason)
	}
	return answerResponse(ctx, "permission", cmd.ChatID, reply, outcome)
}

// answerResponse answers the answering surface with how its claimed ask settled. Only a delivered
// answer succeeds, so the dispatcher discharges waiting for nothing else: a withdrawn ask is 410
// ask_withdrawn (the settlement that won retires the card), a reopened one 502 answer_not_delivered
// (the chat stays waiting and the surface re-offers its card).
func answerResponse(ctx context.Context, kind string, chatID marotte.ChatID, reply AskReply, result any) (any, error) {
	// A claimed ask must be answered or the turn waits forever, and Respond refuses a done
	// context: the client may have gone during the switch to Custom.
	switch outcome, err := reply.Respond(durable.Context(ctx), result); outcome {
	case AnswerDelivered:
		return responseOK, nil
	case AnswerWithdrawn:
		return nil, StatusError(http.StatusGone, errAskWithdrawn)
	default:
		slog.Warn("decision answer not delivered; the ask is pending again",
			"kind", kind, "chat_id", chatID, keyError, err)
		return nil, StatusError(http.StatusBadGateway, errAnswerNotDelivered)
	}
}

// alwaysConsent is the user-scope rule an always answer saves, nil for any other answer. A
// chosen pattern other than the ask's own subject is checked against the rule format before
// anything is claimed or written; an unknown request or option is left to the claim.
func alwaysConsent(perms pendingPermAccess, chatID marotte.ChatID, p *marotte.PermissionResponseCommand) (*marotte.PermissionConsentAnswer, error) {
	ask, ok := perms.PendingPermission(chatID, p.RequestID)
	if !ok {
		return nil, nil
	}
	i := slices.IndexFunc(ask.Options, func(o marotte.PermissionOption) bool { return o.OptionID == p.OptionID })
	if i < 0 {
		return nil, nil
	}
	effect, isAlways := alwaysEffects[ask.Options[i].Kind]
	resource := strings.TrimSpace(p.AlwaysResource)
	switch {
	case !isAlways && resource == "":
		return nil, nil
	case !isAlways, ask.Consent == nil, len(p.FileDecisions) > 0, strings.TrimSpace(p.RejectionReason) != "":
		return nil, StatusError(http.StatusBadRequest, errAlwaysResourceInvalid)
	}
	rule := policyfile.Rule{Capability: ask.Consent.Capability, Effect: effect}
	raw, fromSubject := subjectResource(ask.Consent, resource)
	if !fromSubject {
		rule.Match = []string{resource}
	}
	clean, err := policyfile.SanitizeRule(&rule)
	if err != nil || (!fromSubject && len(clean.Match) != 1) {
		return nil, StatusError(http.StatusBadRequest, errAlwaysResourceInvalid)
	}
	if !fromSubject {
		raw = clean.Match[0]
	}
	return &marotte.PermissionConsentAnswer{
		Scope: marotte.ConsentScopeUser, Capability: clean.Capability, Resource: raw,
	}, nil
}

// subjectResource maps a pattern the card offered from the ask itself (its subject, or the
// folder over a path) onto the raw key the server kept for it, so the saved rule matches what
// KAS asked about. ok is false for any other pattern, which is saved as chosen.
func subjectResource(c *marotte.PermissionConsent, chosen string) (string, bool) {
	switch {
	case chosen == "":
		return "", false
	case c.Resource != "" && chosen == strings.TrimSpace(c.Subject):
		return c.Resource, true
	case c.FolderResource != "" && chosen == strings.TrimSpace(c.Folder):
		return c.FolderResource, true
	}
	return "", false
}

// reasonAlwaysRuleNotSaved marks an always answer refused before its rule was saved: the ask
// is still pending, which is what the client offers again on.
const reasonAlwaysRuleNotSaved = "always_rule_not_saved"

var alwaysEffects = map[string]string{
	"allow_always":  policyfile.EffectAllow,
	"reject_always": policyfile.EffectDeny,
}

// customizeRefusal answers an always answer whose switch to Custom failed. The ask stays
// pending and every refusal carries one reason, so the client offers the card again.
func customizeRefusal(err error) error {
	msg := "the rule could not be saved, so the request is still waiting. Allow it once instead, or try again"
	switch {
	case errors.Is(err, securityprofile.ErrNoLivePolicy):
		msg = "the current profile's rules could not be read to switch Permissions to Custom, so no rule was saved. Allow it once instead, or try again"
	case errors.Is(err, securityprofile.ErrUserFileUnreadable):
		msg = "the user permissions file could not be read, so no rule was saved. Fix the file by hand, or allow it once instead"
	case errors.Is(err, securityprofile.ErrUserFileFull):
		msg = "the user permissions file is at its rule limit, so no rule was saved. Remove a rule in Permissions, or allow it once instead"
	}
	slog.Warn("an always answer was refused because Permissions could not switch to Custom", keyError, err)
	return StatusErrorReason(http.StatusConflict, reasonAlwaysRuleNotSaved, errors.New(msg))
}
