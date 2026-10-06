package agent

// The unattended floor: a scheduled run's permission request has nobody to answer, so it is refused on a
// short budget rather than parking the run (and, through the single-run rule, its whole schedule).
// Scheduled runs only. Deny by default; `scheduled_auto_approve` opts in.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/schedule"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/runesafe/v2"
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

// permissionWithUnattendedFloor wraps the permission handler: the request reaches the client unchanged, plus
// a deadline after which marotte answers.
func (rs *Runs) permissionWithUnattendedFloor(inner chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		inner(ctx, chatID, msg)

		// The lease's mark, so the floor survives a restart. The key yields "" for anything but a `run:` chat id.
		l, held := rs.lease(workflowIDOf(chatID))
		if !held || !l.Unattended || msg.ID == nil {
			return
		}
		scheduleID := l.ScheduleID
		requestID := *msg.ID
		tool := permissionToolName(msg.Params)
		// AfterFunc parks no goroutine and is a no-op once answered: both paths claim from the tracker.
		params := msg.Params
		time.AfterFunc(unattendedApprovalBudget, func() {
			rs.answerUnattended(chatID, requestID, scheduleID, tool, params)
		})
	}
}

// answerUnattended settles a still-pending request for an absent user: refuse, or approve when opted in.
func (rs *Runs) answerUnattended(chatID marotte.ChatID, requestID int64, scheduleID, tool string, msgParams json.RawMessage) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()

	sb := rs.bridges.get(chatID)
	if sb == nil {
		return
	}
	approve := scheduledAutoApprove(ctx, rs.lifecycle.configDir)
	// An administrator `ask` needs a person; KAS would count any allow_once, so the switch must not answer it.
	adminAsk := adminConsentAsk(msgParams)
	if approve && adminAsk {
		slog.Warn("unattended auto-approve: refusing, an administrator rule requires a person to approve",
			"chat_id", chatID, "tool", tool)
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
				"chat_id", chatID, "tool", tool)
		} else {
			outcome = marotte.PermissionOutcomeSelected(opt)
			verb = outcomeApproved
		}
	}
	// The claim decides the race with a human on the page; losing it gives up. It retires the entry and
	// announces the answer as the machine's.
	if !rs.perms.TakePendingPerm(chatID, requestID, marotte.SettledByUnattended) {
		return
	}
	// A fixed message with the outcome as a field, so alert rules can match it.
	slog.Warn(logMsgUnattendedPermission,
		"outcome", verb, "chat_id", chatID, "tool", tool,
		"budget", unattendedApprovalBudget, "schedule_id", scheduleID)
	if err := sb.Respond(ctx, requestID, outcome, nil); err != nil {
		slog.Error("unattended permission answer failed", "chat_id", chatID, "error", err)
		return
	}
	if verb == outcomeApproved {
		// An approval is not a failure.
		return
	}

	// Surface it on the schedule row, or the run fails silently every night.
	reason := "needed approval for " + tool + " with nobody watching. Add a permission rule to allow it"
	if tool == "" {
		reason = "needed an approval with nobody watching. Add a permission rule to allow it"
	}
	if adminAsk {
		// No workspace or user rule outranks an administrator one, so the usual remedy would be false.
		reason = "your organization requires a person to approve " + tool + ", and nobody was watching"
		if tool == "" {
			reason = "your organization requires a person to approve this, and nobody was watching"
		}
	}
	rs.recordScheduleOutcome(ctx, scheduleID, schedule.Outcome{Status: schedule.StatusFailed, Reason: reason})
}

// permissionToolName names what a request asks about, machine-authored names first, the model's prose last.
// Best-effort: an unnamed request is still denied.
func permissionToolName(params json.RawMessage) string {
	var p struct {
		ToolCall struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
		Meta struct {
			// Decoded here: translate.ACPPermissionKiroBlock carries neither name.
			Kiro struct {
				// ToolID is KAS's own tool id, always present on a tool approval.
				ToolID string `json:"toolId"`
				// HookName comes from a hook file on disk.
				HookName string `json:"hookName"`
				Type     string `json:"type"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	kiro := p.Meta.Kiro
	switch {
	case kiro.ToolID != "":
		return safeToolName(kiro.ToolID)
	case kiro.HookName != "":
		return safeToolName(kiro.HookName)
	case kiro.Type == approvalTypeTurn:
		return turnApprovalName
	}
	if p.ToolCall.Title != "" {
		return safeToolName(p.ToolCall.Title)
	}
	return safeToolName(p.ToolCall.Kind)
}

// safeToolName defuses a wire name for a one-line surface: the model composes titles. Replacing rather than
// deleting keeps legitimate names byte-identical and makes bidi reversal visible.
func safeToolName(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxToolNameBytes)
}

// consentScopeAdministration is the `_meta.kiro.consent.scope` KAS stamps on an administrator ask.
const consentScopeAdministration = "administration"

// adminConsentAsk reports whether a permission request is an administrator ask.
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

// scheduledAutoApprove reads the opt-out; absent or unreadable is off.
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
