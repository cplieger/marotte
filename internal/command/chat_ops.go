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

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

// CancelGrace is how long a cooperative session/cancel gets to end the turn before marotte unblocks
// it itself (KiroCrew's `_CANCEL_GRACE_SECS`). A var only so a test can shrink it.
var CancelGrace = 10 * time.Second

// ErrCancelGraceExpired is the cause the grace timer attaches to the prompt context it cancels, so
// the failure site concludes `cancelled` rather than `interrupted`. Shutdown and an interrupt
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
	opened, err := mem.CreateChatAndOpen(ctx, &ChatCreate{
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

// cmdDeleteChat removes a chat through Membership.DeleteChatAndCloseTabs, which owns the order and the check of
// a payload impact against the chats under the deleted chat's tabs.
func cmdDeleteChat(ctx context.Context, mem *Membership, cmd *marotte.ClientCommand) (any, error) {
	var p marotte.CloseTabCommand
	if len(cmd.Payload) > 0 && json.Unmarshal(cmd.Payload, &p) != nil {
		// Optional; an unreadable one is ignored rather than refused, since the subject is the envelope's chat id.
		p = marotte.CloseTabCommand{}
	}
	if !validCloseImpact(p.Impact) {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if !validIdent(p.OpID) {
		p.OpID = ""
	}
	if err := mem.DeleteChatAndCloseTabs(ctx, cmd.ChatID, p.OpID, p.Impact); err != nil {
		return nil, err
	}
	return responseOK, nil
}

// cmdCancel stops the run entry its target names (RunView.Cancel when absent): a live turn starting or running,
// never a compaction. The turn's end leaves its unread steers for the next send; a payload lead names the row the
// reader wants first, and a lead no longer waiting in the dock stops nothing. A plain stop never waits on the steer
// lock, so it never queues behind a delete.
func cmdCancel(
	ctx context.Context,
	runq runQueue,
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
	if queue == nil || p.Lead == "" {
		rs := runq.Stop(ctx, cmd.ChatID, p.Target)
		return carryOutStop(ctx, runq, bridges, perms, terms, stops, cmd.ChatID, rs, func() {})
	}
	// The lead is steer-queue state, set before the stop's section so the close sends it first.
	// The steer lock covers the lead and the stop's section only: the cancel's write goes after it, as a steer's does.
	rs, undo, waiting := leadThenStop(ctx, runq, queue, cmd.ChatID, &p)
	if !waiting {
		return runAnswer(ctx, runq, cmd.ChatID, map[string]any{keyStopped: false}), nil
	}
	return carryOutStop(ctx, runq, bridges, perms, terms, stops, cmd.ChatID, rs, undo)
}

// leadThenStop sets p's lead and applies the stop under the chat's steer lock; waiting is false when the lead's row
// already left the dock, and then nothing is stopped. A stop that stops nothing has its lead undone.
func leadThenStop(ctx context.Context, runq runQueue, queue SteerQueue, chatID marotte.ChatID, p *marotte.CancelCommand) (rs RunStop, undo func(), waiting bool) {
	unlock, err := queue.LockSteerOps(ctx, chatID)
	if err == nil {
		defer unlock()
	}
	undo, waiting = queue.SetSteerLead(chatID, p.Lead)
	if !waiting {
		return RunStop{}, undo, false
	}
	rs = runq.Stop(ctx, chatID, p.Target)
	if rs.Arm == StopNothing {
		undo()
	}
	return rs, undo, true
}

// keyStopped is a stop's answer field: whether anything was stopped.
const keyStopped = "stopped"

// carryOutStop finishes a stop the run order applied: nothing for a target it would not stop, no session/cancel for
// a take whose prompt was never written, else one session/cancel and the grace on each captured target. A cancel
// not delivered (502) restores what the stop marked and runs undo; a session that ended runs undo and answers
// stopped, its turns closing on the death fan-out.
func carryOutStop(
	ctx context.Context, runq runQueue, bridges bridgeAccess, perms pendingPermAccess, terms terminalAccess,
	stops turnStopper, chatID marotte.ChatID, rs RunStop, undo func(),
) (any, error) {
	switch rs.Arm {
	case StopNothing:
		return runAnswer(ctx, runq, chatID, map[string]any{keyStopped: false}), nil
	case StopQuiet:
		stops.ReleaseStop(rs.Stop)
		return runAnswer(ctx, runq, chatID, map[string]any{keyStopped: true}), nil
	case StopCancel:
	}
	stop := rs.Stop
	defer stops.ReleaseStop(stop)
	err := cancelSession(ctx, perms, bridges.Bridge(chatID), chatID, ScopeChatSession)
	if errors.Is(err, ErrCancelNotDelivered) {
		runq.StopUndone(chatID, stop)
		undo()
		return nil, StatusError(http.StatusBadGateway, errCancelNotDelivered)
	}
	// Stopping the model does not stop the processes its turn already spawned.
	terms.KillForTurn(stop.Own)
	if stop.Call != stop.Own {
		terms.KillForTurn(stop.Call)
	}
	if err != nil {
		undo()
		slog.Error("cancel failed", "chat_id", chatID, keyError, err)
		return runAnswer(ctx, runq, chatID, map[string]any{keyStopped: true}), nil
	}
	// session/cancel is a notification nothing acks: a turn ends only when KAS answers or ends it, so the grace
	// budget backstops each captured target (ArmStopGrace).
	if !stops.ArmStopGrace(stop, CancelGrace) {
		slog.Debug("cancel: no live turn to arm a grace budget against", "chat_id", chatID)
	}
	return runAnswer(ctx, runq, chatID, map[string]any{keyStopped: true}), nil
}

// closeChatTeardown is the tab-close teardown: the turn and the session go, and the runs too when
// stop carries a person's close's stop hold; the chat RECORD stays. Ordering is the contract: the
// session's leases are waited for first, so the teardown is the session's only caller, then the turn
// is cancelled (graceful stop), then the runs, then the session teardown, which flushes the in-flight
// buffer and kills the chat's agent terminals.
func closeChatTeardown(ctx context.Context, perms askCanceller, stops turnStopper, teardown chatTeardown, chatID marotte.ChatID, stop *chat.Claim) {
	slog.Info("chat tab closed; tearing down its session", "chat_id", chatID)
	defer stops.ReleaseStop(stops.RequestStop(chatID, marotte.TurnStopTeardown))
	teardown.BeginChatTeardown(chatID, true)
	teardown.AwaitSessionLeases(ctx, chatID)
	teardownCancel(ctx, perms, teardown, chatID)
	teardown.CloseChatState(ctx, chatID, stop)
}

// deleteChatTeardown is closeChatTeardown's delete grade, for a chat the
// retention-off close escalation has already erased: same graceful cancel
// first, then the delete-grade teardown driven from the session chain the
// record's delete answered (a record-reading teardown would no-op on a
// deleted chat). Mirrors closeChatTeardown so the two grades cannot drift.
func deleteChatTeardown(
	ctx context.Context, perms askCanceller, stops turnStopper, teardown chatTeardown,
	chatID marotte.ChatID, sessionChain []string, cause RunStopCause,
) {
	defer stops.ReleaseStop(stops.RequestStop(chatID, marotte.TurnStopTeardown))
	teardown.BeginChatTeardown(chatID, false)
	teardown.AwaitSessionLeases(ctx, chatID)
	teardownCancel(ctx, perms, teardown, chatID)
	teardown.DeleteChatStateByChain(ctx, chatID, sessionChain, cause)
}

// teardownCancel is a teardown's turn cancel: every ask of the chat and its runs, which the teardown cancels next,
// then the session's own cancel through the teardown's closer. A session still opening, closing or closed has no
// turn on the wire, so it is nothing to cancel; any other failure is logged and the teardown continues, because the
// session's release settles what is left.
func teardownCancel(ctx context.Context, perms askCanceller, teardown chatTeardown, chatID marotte.ChatID) {
	// No deadline of its own, as cancelSession: each write is bounded by the bridge, which waits out an engine that
	// reads late.
	ctx = durable.Context(ctx)
	err := perms.CancelPendingAsks(ctx, chatID, ScopeChatAndRuns)
	if err == nil {
		err = teardown.TeardownCancel(ctx, chatID)
	}
	if err != nil && !nothingToCancel(err) {
		slog.Warn("close: turn cancel failed", "chat_id", chatID, keyError, err)
	}
}

// nothingToCancel reports a cancel refused because the session has no turn on the wire.
func nothingToCancel(err error) bool {
	return errors.Is(err, marotte.ErrSessionOpening) || errors.Is(err, marotte.ErrSessionClosing) ||
		errors.Is(err, marotte.ErrSessionClosed)
}

// cmdPermission forwards the user's permission dialog choice to kiro-cli. An always answer leaves
// every chat's profile as it is: kiro-cli writes a user-scope rule into the user permissions file,
// which every chat reads under its own profile.
func cmdPermission(ctx context.Context, perms pendingPermAccess, cmd *marotte.ClientCommand) (any, error) {
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
	// context: the client may have gone since it sent the answer.
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

// alwaysConsent is the rule an always answer saves, nil for any other answer. The chosen scope
// and a chosen pattern other than the ask's own subject are checked before anything is claimed
// or written; an unknown request or option is left to the claim.
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
	scope, scopeOK := consentScope(p.AlwaysScope, effect)
	switch {
	case !isAlways && p.AlwaysScope != "", isAlways && !scopeOK:
		return nil, StatusError(http.StatusBadRequest, errAlwaysScopeInvalid)
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
		Scope: scope, Capability: clean.Capability, Resource: raw,
	}, nil
}

// consentScope maps a requested always_scope onto the KAS scope it saves at. The card offers
// a session scope only beside Allow, so a session-scope deny is refused.
func consentScope(requested, effect string) (string, bool) {
	switch requested {
	case "", marotte.ConsentScopeUser:
		return marotte.ConsentScopeUser, true
	case marotte.ConsentScopeSession:
		return marotte.ConsentScopeSession, effect == policyfile.EffectAllow
	}
	return "", false
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

var alwaysEffects = map[string]string{
	"allow_always":  policyfile.EffectAllow,
	"reject_always": policyfile.EffectDeny,
}
