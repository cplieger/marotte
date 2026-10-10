// Package notice owns every notification's words, kind and subject, for the Web Push and the
// in-page notification alike. The title is the name of the tab a click opens; the body is
// kiro-cli's own phrase for what happened, then a short detail.
package notice

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
)

// kiro-cli's own notification phrases (its OSC 9 text and its workflow transcript rows), so
// marotte and web-terminal-kiro say the same thing about the same event.
const (
	phraseResponseComplete   = "Response complete"
	phrasePermissionRequired = "Permission required"
	phraseInputRequired      = "Input required"
	phraseTurnFailed         = "Turn failed"
	phraseChecksPassed       = "Checks passed"
	phraseChecksFailed       = "Checks failed"
)

// Rune bounds: a tray shows one title line and two to four body lines.
const (
	titleRunes  = 60
	detailRunes = 120
	bodyRunes   = 160
)

const (
	separator = " · "
	ellipsis  = "…"
	// fallbackRunName is the run tab's own label for a run nobody named.
	fallbackRunName = "Workflow run"
)

// pauseKindMaxIterations is KAS's `finalState.pause.kind` for a repeat's onMaxIterations stop.
const pauseKindMaxIterations = "maxIterations"

// Target is the tab a notification's click opens, and that tab's name.
type Target struct {
	Name    string
	Subject marotte.PushSubject
}

// ChatTarget is one chat's tab; an unnamed chat reads as the tab strip's default name.
func ChatTarget(chatID marotte.ChatID, name string) Target {
	if strings.TrimSpace(name) == "" {
		name = marotte.DefaultChatName
	}
	return Target{Subject: marotte.ChatSubject(chatID), Name: name}
}

// RunTarget is one workflow run's tab; an unlabelled run reads as the run tab's default label.
func RunTarget(workflowID, label string) Target {
	if strings.TrimSpace(label) == "" {
		label = fallbackRunName
	}
	return Target{Subject: marotte.RunSubject(workflowID), Name: label}
}

// AskRun is the run an ask belongs to: the attributed run, else the run whose bridge key
// (`run:<id>`) the ask travelled on, else "" for a chat's own ask. The client's askTarget
// draws the same line, so a settled ask retracts the tag it was shown under.
func AskRun(chatID marotte.ChatID, runID string) string {
	if runID != "" {
		return runID
	}
	if id, ok := strings.CutPrefix(string(chatID), marotte.RunSubjectPrefix); ok {
		return id
	}
	return ""
}

// AskSubject is the subject an ask's notification is shown and retracted under.
func AskSubject(chatID marotte.ChatID, runID string) marotte.PushSubject {
	if id := AskRun(chatID, runID); id != "" {
		return marotte.RunSubject(id)
	}
	return marotte.ChatSubject(chatID)
}

// TurnFinished is a chat turn that ended cleanly; focus is the agent's own description of
// what it was doing.
func TurnFinished(t Target, focus string) marotte.NotificationPayload {
	return build(marotte.PushKindAgentFinished, t, phraseResponseComplete, focus)
}

// TurnFailed is a chat turn that ended broken, with the reason its card shows.
func TurnFailed(t Target, reason string) marotte.NotificationPayload {
	return build(marotte.PushKindAgentFinished, t, phraseTurnFailed, reason)
}

// Permission is a tool permission ask. files > 0 marks a turn approval, which names its
// changed files rather than a tool. step names the asking workflow step, "" for a chat's ask.
func Permission(t Target, step, toolTitle string, files int) marotte.NotificationPayload {
	detail := toolTitle
	if files > 0 {
		detail = "Review " + strconv.Itoa(files) + " changed file"
		if files != 1 {
			detail += "s"
		}
	}
	return build(marotte.PushKindPermission, t, phrasePermissionRequired, stepped(step, detail))
}

// Question is an ask for a person's words: a `_kiro/userInput` question, an MCP elicitation's
// message, or a workflow step's question.
func Question(t Target, step, question string) marotte.NotificationPayload {
	return build(marotte.PushKindPermission, t, phraseInputRequired, stepped(step, question))
}

// RunEnded is a run's `run_complete`. parent names the chat that launched it, "" for a
// parentless run. stopReason is KAS's `finalState.stopReason` and pauseKind its
// `finalState.pause.kind`. False means no notification: a live status, a pause other than
// the iteration limit (a step's own ask notifies as a Question), or an iteration-limit
// pause of a run whose parent agent is expected to intervene itself.
func RunEnded(t Target, status marotte.RunStatus, parent, stopReason, pauseKind string) (marotte.NotificationPayload, bool) {
	var phrase, detail string
	switch status {
	case marotte.RunStatusCompleted:
		phrase = "Workflow completed"
	case marotte.RunStatusFailed:
		phrase, detail = "Workflow failed", stopReason
	case marotte.RunStatusAborted:
		phrase, detail = "Workflow aborted", stopReason
	case marotte.RunStatusCancelled:
		phrase, detail = "Workflow cancelled", stopReason
	case marotte.RunStatusPaused:
		if parent != "" || pauseKind != pauseKindMaxIterations {
			return marotte.NotificationPayload{}, false
		}
		return build(marotte.PushKindRunOutcome, t, "Workflow paused", "iteration limit reached"), true
	default:
		return marotte.NotificationPayload{}, false
	}
	if detail == "" && parent != "" {
		detail = "from " + parent
	}
	return build(marotte.PushKindRunOutcome, t, phrase, detail), true
}

// PRStatus is a pull request's CI verdict settling green or red.
func PRStatus(subject marotte.PushSubject, repo string, number int, prTitle string, passed bool) marotte.NotificationPayload {
	phrase := phraseChecksFailed
	if passed {
		phrase = phraseChecksPassed
	}
	t := Target{Subject: subject, Name: repo + " #" + strconv.Itoa(number)}
	return build(marotte.PushKindPRStatus, t, phrase, prTitle)
}

// stepped prefixes a run ask's detail with the step that asks; a bare step stands alone.
func stepped(step, detail string) string {
	step, detail = clip(step, detailRunes), clip(detail, detailRunes)
	switch {
	case step == "":
		return detail
	case detail == "":
		return step
	}
	return step + ": " + detail
}

func build(kind marotte.PushKind, t Target, phrase, detail string) marotte.NotificationPayload {
	body := phrase
	if d := clip(detail, detailRunes); d != "" {
		body = clip(phrase+separator+d, bodyRunes)
	}
	return marotte.NotificationPayload{
		PushSubject: t.Subject,
		Kind:        kind,
		Title:       clip(t.Name, titleRunes),
		Body:        body,
	}
}

// clip makes s one sanitized line of at most n runes, whitespace runs collapsed, a cut
// ending in the ellipsis.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(runesafe.SanitizeSingleLine(s)), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + ellipsis
}
