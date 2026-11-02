package agent

// The unattended floor: a scheduled run's permission request has nobody to answer, so it is refused on a
// short budget rather than parking the run (and, through the single-run rule, its whole schedule), and a
// pause nobody resumes ends the run on the same budget. Scheduled runs only. Deny by default;
// `scheduled_auto_approve` opts in.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/runesafe/v3"
)

// unattendedApprovalBudget is how long a scheduled run's request waits before refusal: 180s, as KiroCrew's
// `_BACKGROUND_APPROVAL_TIMEOUT_SECS`. Nonzero: the user may be watching.
const unattendedApprovalBudget = 180 * time.Second

// maxToolNameBytes bounds the tool name in the log attribute and the schedule row, both one-line and persisted on every fire.
const maxToolNameBytes = 128

// unattendedRejectionReason is the deny note, so the step's agent does not retry.
const unattendedRejectionReason = "This is a scheduled run and no one is watching it, " +
	"so the permission was refused automatically. Finish the task without it, " +
	"or stop and name the permission the task needs."

// approvalTypeTurn is the `_meta.kiro.type` marking a TURN APPROVAL.
const approvalTypeTurn = "turn_approval"

// turnApprovalName names a turn approval, which carries no tool name and titles itself "Review changes".
const turnApprovalName = "this turn's file changes"

// The two one-shot option kinds; the `_always` twins persist a rule.
const (
	optionKindAllowOnce  = "allow_once"
	optionKindRejectOnce = "reject_once"
)

// logMsgUnattendedPermission is a constant because an external alert rule keys on it; change both together.
const logMsgUnattendedPermission = "unattended permission answered with no user present"

// The log's `outcome` values; an alert rule matches outcomeRefused.
const (
	outcomeRefused  = "refused"
	outcomeApproved = "approved"
)

// logMsgRunOverran is a constant an alert rule matches: the one signal a schedule stopped producing. Change both together.
const logMsgRunOverran = "scheduled run still going when its next slot came due; cancelling"

// reasonOverran is the schedule row's failure text, written for the person: what happened and what to do.
const reasonOverran = "still running when its next slot came due, so it was cancelled. " +
	"Give the schedule a longer interval, or make the workflow finish inside it"

// reasonStalled and reasonBackstop are the row's text for the other two bounds (cancelExpired).
var (
	reasonStalled = "made no progress and had no live shell for " +
		strconv.Itoa(int(runIdleWindow.Minutes())) + " minutes, so it was cancelled. " +
		"Open the run to see the step it stopped in"
	reasonBackstop = "ran for " + strconv.Itoa(int(runBackstop.Hours())) +
		" hours in total, the most one run may, so it was cancelled. Make the workflow finish sooner"
)

// reasonCancelRefused is the row's text once retryTermination gives up on a stop marotte decided.
var reasonCancelRefused = "reached a limit that stops it, but kiro-cli refused the cancel " +
	strconv.Itoa(1+maxCancelRetries) + " times, so it may still be running. Cancel it from its run page"

// unattendedPauseBudget is how long a scheduled run may sit paused with no resume before it is ended
// (endUnattended): an unattended ask's budget, which also outlasts the three heals' backoff. A var for tests.
var unattendedPauseBudget = unattendedApprovalBudget

// logMsgRunPausedUnattended is a constant an alert rule matches. Change both together.
const logMsgRunPausedUnattended = "scheduled run paused with nobody to resume it; cancelling"

// The row's text for endUnattended, by the pause it found and what it waited out.
var (
	pauseBudgetText   = strconv.Itoa(int(unattendedApprovalBudget.Minutes())) + " minutes"
	reasonPausedFault = "paused on a fault that marotte's automatic resumes did not clear within " + pauseBudgetText +
		", so it was cancelled. Open the run to see the step it paused in"
	reasonHealsSpent = "paused on a fault again after each of marotte's " + strconv.Itoa(maxAutoHeals) +
		" automatic resumes, so it was cancelled. Open the run to see the step it paused in"
)

// reasonPausedOther is the row's text for any other pause (seenOtherPause), quoting KAS's pause reason: only it says
// which pause this was, a step's question, a repeat's limit or a fault KAS does not class as transient.
func reasonPausedOther(pauseReason string) string {
	quoted := ""
	if r := rowText(pauseReason); r != "" {
		quoted = " (" + r + ")"
	}
	return "paused with nobody watching" + quoted + ", so it was cancelled after " + pauseBudgetText +
		". Open the run to see the step it paused in"
}

// permissionWithUnattendedFloor wraps the permission handler: the request reaches the client unchanged, plus
// a deadline after which marotte answers.
func (rs *Runs) permissionWithUnattendedFloor(inner askHandler) askHandler {
	return func(ctx context.Context, chatID marotte.ChatID, origin translate.AskOrigin, msg *marotte.RPCResponse) {
		inner(ctx, chatID, origin, msg)

		// The lease's mark, so the floor survives a restart. The key yields "" for anything but a `run:` chat id.
		l, held := rs.lease(workflowIDOf(chatID))
		if !held || !l.Unattended || msg.ID == nil {
			return
		}
		run := scheduleRun(&l)
		acpID := *msg.ID
		name := permissionAskName(msg.Params)
		// A timer parks no goroutine and is a no-op once answered: both paths claim from the tracker.
		params := msg.Params
		rs.lifecycle.afterUnlessDraining(unattendedApprovalBudget, func() {
			rs.answerUnattended(chatID, origin, acpID, run, name, params)
		})
	}
}

// answerUnattended settles a still-pending request for an absent user: refuse, or approve when opted in.
func (rs *Runs) answerUnattended(chatID marotte.ChatID, origin acpResponder, acpID int64, run schedule.Run, name askName, msgParams json.RawMessage) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()

	approve := scheduledAutoApprove(ctx, rs.lifecycle.configDir)
	// An administrator `ask` needs a person; KAS would count any allow_once, so the switch must not answer it.
	adminAsk := adminConsentAsk(msgParams)
	if approve && adminAsk {
		slog.Warn("unattended auto-approve: refusing, an administrator rule requires a person to approve",
			"chat_id", chatID, "tool", name.tool, "subject", name.subject)
		approve = false
	}
	// Refuse with the advertised reject option; Cancelled only when none is offered.
	outcome := marotte.PermissionOutcomeCancelled()
	if opt := optionIDByKind(msgParams, optionKindRejectOnce); opt != "" {
		outcome = marotte.PermissionOutcomeWithRejectionReason(opt, unattendedRejectionReason)
	}
	verb := outcomeRefused
	if approve {
		opt := optionIDByKind(msgParams, optionKindAllowOnce)
		if opt == "" {
			// Never invent an id; fall back to refusal.
			slog.Warn("unattended auto-approve: request offered no allow option, refusing instead",
				"chat_id", chatID, "tool", name.tool)
		} else {
			outcome = marotte.PermissionOutcomeSelected(opt)
			verb = outcomeApproved
		}
	}
	// The claim decides the race with a human on the page; losing it, or the bridge ending first, gives
	// up. Claimed by the bridge and its own id: a successor reuses ids from zero.
	reply, ok := rs.perms.TakePendingPermOn(origin, acpID, marotte.SettledByUnattended)
	if !ok {
		return
	}
	switch answered, err := reply.Respond(ctx, outcome); answered {
	case command.AnswerDelivered:
	case command.AnswerWithdrawn:
		// The ask settled first, so no answer reached the run to report or explain.
		return
	default:
		slog.Error("unattended permission answer failed", "chat_id", chatID, "error", err)
		return
	}
	// A fixed message with the outcome as a field, so alert rules can match it.
	slog.Warn(logMsgUnattendedPermission,
		"outcome", verb, "chat_id", chatID, "tool", name.tool, "subject", name.subject,
		"budget", unattendedApprovalBudget, "schedule_id", run.ScheduleID)
	if verb == outcomeApproved {
		// An approval is not a failure.
		return
	}

	// Surface it on the schedule row, or the run fails silently every night.
	label := name.label()
	reason := "needed approval for " + label + " with nobody watching. Add a permission rule to allow it"
	if label == "" {
		reason = "needed an approval with nobody watching. Add a permission rule to allow it"
	}
	if adminAsk {
		// No workspace or user rule outranks an administrator one, so the usual remedy would be false.
		reason = "your organization requires a person to approve " + label + ", and nobody was watching"
		if label == "" {
			reason = "your organization requires a person to approve this, and nobody was watching"
		}
	}
	rs.recordScheduleOutcome(ctx, run, schedule.Outcome{Status: schedule.StatusFailed, Reason: reason})
}

// watchUnattendedPause ends a scheduled run unattendedPauseBudget after this pause, unless an arm since resumed it.
func (rs *Runs) watchUnattendedPause(workflowID string) {
	l, held := rs.lease(workflowID)
	if !held || !l.Unattended {
		return
	}
	seq := rs.lastArmSeq(workflowID)
	rs.lifecycle.afterUnlessDraining(unattendedPauseBudget, func() {
		if rs.lastArmSeq(workflowID) != seq {
			return
		}
		ctx, cancel := rs.lifecycle.derivedContext()
		defer cancel()
		if _, err := rs.transition(ctx, workflowID, &runRequest{want: pauseExpiry}); err != nil {
			slog.Error("could not end a scheduled run paused with nobody to resume it; retrying, and its next slot ends it if every retry fails",
				"workflow_id", workflowID, "error", err)
		}
	})
}

// endUnattended ends a scheduled run whose pause r found nobody resuming, under the run's resume lock. req is what
// found it, the pause budget's expiry or a heal past its budget, and a refused cancel's retry sends it again unless
// the run was armed since. A fault's pause is left while the gate is closed, since the gate's release resumes the
// heal it held.
func (rs *Runs) endUnattended(ctx context.Context, workflowID string, req *runRequest, r *pauseRead) error {
	seq := rs.lastArmSeq(workflowID)
	l, held := rs.lease(workflowID)
	if !held || !l.Unattended {
		return nil
	}
	fault := r.seen != seenOtherPause
	if fault && !rs.gateOpen() {
		return nil
	}
	if !rs.claimTermination(workflowID) {
		return nil
	}
	reason, waitedKey, waited := reasonPausedFault, "budget", any(unattendedPauseBudget)
	switch {
	case req.want == healResume:
		reason, waitedKey, waited = reasonHealsSpent, "heals", maxAutoHeals
	case !fault:
		reason = reasonPausedOther(r.res.State.PauseReason)
	}
	slog.Error(logMsgRunPausedUnattended, "workflow_id", workflowID, "schedule_id", l.ScheduleID,
		"recipe", l.Recipe, "fault", fault, "pause_reason", scrubLog(r.res.State.PauseReason), waitedKey, waited)
	// The retry is this pause's alone: a pause after an arm (a resume) has its own watch or heal.
	again := func(ctx context.Context) error {
		if rs.lastArmSeq(workflowID) != seq {
			return nil
		}
		_, err := rs.transition(ctx, workflowID, &runRequest{want: req.want, chatID: req.chatID})
		return err
	}
	if err := rs.finishTermination(ctx, workflowID, runEndUnattended, runStop{}, again); err != nil {
		return err
	}
	// Overwrites the cancel's own ending, which may land first and names no pause.
	rs.recordScheduleOutcome(ctx, scheduleRun(&l), schedule.Outcome{Status: schedule.StatusFailed, Reason: reason})
	return nil
}

// consentCapabilityShell is the consent capability whose resource is the command line.
const consentCapabilityShell = "shell"

// askName is what a request asks about: the tool that asked, and the consent subject a permission rule
// would have to cover. Both are defused for a one-line surface.
type askName struct {
	tool    string
	subject string
}

// label is the schedule row's name for the ask: "tool (subject)", or whichever of the two is set.
func (n askName) label() string {
	switch {
	case n.subject == "" || n.subject == n.tool:
		return n.tool
	case n.tool == "":
		return n.subject
	}
	return n.tool + " (" + n.subject + ")"
}

// permissionAskName names what a request asks about, machine-authored names first, the model's prose last.
// Best-effort: an unnamed request is still denied. A shell ask has no subject: its resource is the command
// line, which can carry a credential into the persisted row and the log.
func permissionAskName(params json.RawMessage) askName {
	var p struct {
		ToolCall struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
		Meta struct {
			// Decoded here: translate.acpPermissionKiroBlock carries neither name.
			Kiro struct {
				// ToolID is KAS's own tool id, always present on a tool approval.
				ToolID string `json:"toolId"`
				// HookName comes from a hook file on disk.
				HookName string `json:"hookName"`
				Type     string `json:"type"`
				Consent  struct {
					Capability         string `json:"capability"`
					Resource           string `json:"resource"`
					TriggeringResource string `json:"triggeringResource"`
				} `json:"consent"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return askName{}
	}
	kiro := p.Meta.Kiro
	var name askName
	if c := kiro.Consent; c.Capability != consentCapabilityShell {
		name.subject = safeToolName(credentialFreeSubject(cmp.Or(c.TriggeringResource, c.Resource)))
	}
	switch {
	case kiro.ToolID != "":
		name.tool = safeToolName(kiro.ToolID)
	case kiro.HookName != "":
		name.tool = safeToolName(kiro.HookName)
	case kiro.Type == approvalTypeTurn:
		name.tool = turnApprovalName
	case p.ToolCall.Title != "":
		name.tool = safeToolName(p.ToolCall.Title)
	default:
		name.tool = safeToolName(p.ToolCall.Kind)
	}
	return name
}

// credentialFreeSubject drops the userinfo, query and fragment of a subject that is an absolute URL, where
// a server URL or fetch target carries its token. A URL-shaped subject that does not parse is dropped whole.
func credentialFreeSubject(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		if strings.Contains(s, "://") {
			return ""
		}
		return s
	}
	// Re-serialising rewrites an untouched subject (a scheme's case, an escape), so only a stripped one is.
	if !u.IsAbs() || (u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "") {
		return s
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// safeToolName defuses a wire name for a one-line surface: the model composes titles. Replacing rather than
// deleting keeps legitimate names byte-identical and makes bidi reversal visible.
func safeToolName(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxToolNameBytes)
}

// consentScopeAdministration is the `_meta.kiro.consent.scope` KAS stamps on an administrator ask.
const consentScopeAdministration = "administration"

func adminConsentAsk(params json.RawMessage) bool {
	var p struct {
		Meta struct {
			Kiro struct {
				Consent struct {
					Scope string `json:"scope"`
				} `json:"consent"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	return json.Unmarshal(params, &p) == nil && p.Meta.Kiro.Consent.Scope == consentScopeAdministration
}

// Absent or unreadable is off.
func scheduledAutoApprove(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyScheduledAutoApprove, &b) {
		return false
	}
	return b
}

// optionIDByKind picks the advertised option of one exact kind: a prefix would reach the `_always` kinds,
// which persist a standing rule.
func optionIDByKind(params json.RawMessage, kind string) string {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	for _, o := range p.Options {
		if o.Kind == kind {
			return o.OptionID
		}
	}
	return ""
}
