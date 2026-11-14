package marotte

import (
	"strconv"
	"strings"

	"github.com/cplieger/go/keyenc"
)

// Push types: Web Push subscription shapes and push notification kind
// constants. Consumed by the push package and the runtime's push-notification
// path.

// PushSubscription is a Web Push subscription from the browser (RFC 8030).
type PushSubscription struct {
	Endpoint string               `json:"endpoint"`
	Keys     PushSubscriptionKeys `json:"keys"`
}

// PushSubscriptionKeys holds the client-side encryption keys.
type PushSubscriptionKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// PushKind identifies a push notification category. The underlying
// string is the wire value stored in settings and matched by the
// client's service worker.
type PushKind string

// Push notification kind constants. These live in this package so
// agent callers can reference them without importing the push package
// (eliminating the runtime→push import that existed solely for constant
// access).
const (
	PushKindAgentFinished PushKind = "agent_finished"
	PushKindPermission    PushKind = "permission"
	// PushKindPRStatus fires when a pull request the connected identity opened
	// flips green or red. Unlike the other two it has no chat behind it: CI
	// finishes minutes after the turn that pushed, often with nothing open, which
	// is exactly why the poll is server-side.
	PushKindPRStatus PushKind = "pr_status"
	// PushKindRunOutcome fires when a workflow run reaches a terminal state. Like
	// PushKindPRStatus it outlives the turn that launched it — run_workflow returns
	// as soon as the run is created, so the launching turn ends while the run carries
	// on for minutes — and a manual or scheduled run never had a chat at all, so
	// nothing else tells anyone it finished.
	PushKindRunOutcome PushKind = "run_outcome"
)

// pushKinds is the authoritative set of push notification kinds, from which PushKind.Valid()
// derives. Add a kind HERE before push.kindRegistry: the registry's init panics on a kind Valid()
// rejects, while the reverse order silently drops the kind in preflightSend.
var pushKinds = map[PushKind]struct{}{
	PushKindAgentFinished: {},
	PushKindPermission:    {},
	PushKindPRStatus:      {},
	PushKindRunOutcome:    {},
}

// Valid reports whether k is a known push notification kind.
// Used at the settings-load boundary to reject corrupted keys
// at load time rather than at send time.
func (k PushKind) Valid() bool {
	_, ok := pushKinds[k]
	return ok
}

// PushSubject names what a notification is ABOUT; the service worker derives both the OS coalescing
// tag and the click target from it. ChatID is a notification about one chat; Key is one with no
// chat, carrying a kind prefix (`pr:`) rather than a URL, since route-path.ts owns routes. Exactly
// one is set; empty is the workspace-global case.
type PushSubject struct {
	ChatID ChatID `json:"chat_id,omitempty"`
	Key    string `json:"subject,omitempty"`
}

// NotificationPayload is the payload for type="notification", and the Web Push payload's
// content: one notification as both channels show it. internal/notice is its only
// producer, so Title, Body and the subject (the tag and the click target) cannot differ
// between the tray and the page.
type NotificationPayload struct {
	PushSubject
	Kind  PushKind `json:"kind"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
	// ID names this notice on both channels, so a page reports what became of it and shows it once.
	ID string `json:"id"`
	// Ask is the decision a PushKindPermission notification announces, the id its retraction names; it never
	// reaches either channel.
	Ask string `json:"-"`
	// ShowIn lists the pages offered the notice, at most one per browser profile; never set on a push.
	ShowIn []string `json:"show_in,omitempty"`
}

// Tab is the open tab s names, the one a page showing it is looking at: a chat's, a run's, or the run
// tab of a step's wait. ok is false for a pull request, a launch rule, the workspace and a hashed
// wait position. The client's twin is tabOf in handlers/push-message.ts.
func (s PushSubject) Tab() (kind TabKind, ref string, ok bool) {
	switch {
	case s.ChatID != "":
		return TabKindChat, string(s.ChatID), true
	case strings.HasPrefix(s.Key, RunSubjectPrefix):
		return TabKindRun, strings.TrimPrefix(s.Key, RunSubjectPrefix), true
	case strings.HasPrefix(s.Key, runAskSubjectPrefix):
		parts, err := keyenc.Split(strings.TrimPrefix(s.Key, runAskSubjectPrefix))
		if err != nil || len(parts) == 0 || parts[0] == "" {
			return "", "", false
		}
		return TabKindRun, parts[0], true
	default:
		return "", "", false
	}
}

// TabRef names a chat or run tab in a page's view report.
type TabRef struct {
	Kind TabKind `json:"kind"`
	Ref  string  `json:"ref"`
}

// PageView is one page's report to POST /api/events/view: whether it is visible and focused, and the
// chat or run tab it shows (nil for every other tab, or none). Closed ends the page's row and voids
// the other fields.
type PageView struct {
	Tab     *TabRef `json:"tab,omitempty"`
	Page    string  `json:"page"`
	Visible bool    `json:"visible"`
	Focused bool    `json:"focused"`
	Closed  bool    `json:"closed,omitempty"`
}

// NoticeOutcome is what a page did with a notice it was offered or pushed.
type NoticeOutcome string

// The two outcomes a page reports.
const (
	NoticeShown    NoticeOutcome = "shown"
	NoticeDeclined NoticeOutcome = "declined"
)

// NoticeReport is a page's report to POST /api/events/notice about the notice ID.
type NoticeReport struct {
	ID      string        `json:"id"`
	Outcome NoticeOutcome `json:"outcome"`
}

// ChatSubject is the subject of a notification about one chat. Pass a zero
// PushSubject (or ChatSubject("")) for a workspace-global one.
func ChatSubject(id ChatID) PushSubject { return PushSubject{ChatID: id} }

// PRSubjectPrefix marks a subject key naming a pull request. The client keys its
// route on this, so it is declared here rather than assembled at the call site:
// the poller and the service worker have to agree on it, and one of them is
// TypeScript.
const PRSubjectPrefix = "pr:"

// PRSubject is the subject of a notification about one pull request. `repoID` is
// the repository's canonical id on that connection, the one the PRs tab's rows
// carry, so the client finds the row by comparing keys byte for byte; with the
// number it is unique per PR, so two PRs flipping inside one debounce window
// still occupy their own tray slots.
func PRSubject(forgeID, repoID string, number int) PushSubject {
	return PushSubject{Key: PRSubjectPrefix + forgeID + ":" + repoID + "#" + strconv.Itoa(number)}
}

// RunSubjectPrefix marks a subject key naming a workflow run. Declared here for
// PRSubjectPrefix's reason: the service worker keys its route on it, so the two
// halves of the contract are in different languages and one of them is TypeScript.
//
// The literal coincides with internal/agent's own private runChatPrefix
// (run_host.go, the synthetic chat-id namespace a run host session registers under) and
// neither may be derived from the other: that one is a chat-id namespace private to
// that package, this one is a push-subject vocabulary shared with the worker.
const RunSubjectPrefix = "run:"

// RunSubject is the subject of a notification about one workflow run. A workflow id
// is unique per run, so two runs finishing inside one debounce window still occupy
// their own tray slots.
func RunSubject(workflowID string) PushSubject {
	return PushSubject{Key: RunSubjectPrefix + workflowID}
}

// ruleSubjectPrefix marks a subject key naming a launch rule (a schedule or an event trigger). Declared here for
// PRSubjectPrefix's reason.
const ruleSubjectPrefix = "rule:"

// RuleSubject is the subject of a notification about one launch rule, a slot or an event that started no run. A
// rule id is unique per rule, so two rules failing inside one debounce window still occupy their own tray slots.
func RuleSubject(ruleID string) PushSubject {
	return PushSubject{Key: ruleSubjectPrefix + ruleID}
}

// runAskSubjectPrefix marks a subject key naming one step's wait in a run. Declared here for
// PRSubjectPrefix's reason.
const runAskSubjectPrefix = "runask:"

// RunAskSubject is the subject of a notification about one step's wait. position is the ask's id
// without its turn marker, so a re-park of the same wait replaces its banner, and two parked
// branches hold two tray slots.
func RunAskSubject(position string) PushSubject {
	return PushSubject{Key: runAskSubjectPrefix + position}
}
