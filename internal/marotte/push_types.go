package marotte

import "strconv"

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
// (run_host.go, the synthetic chat-id namespace a run bridge registers under) and
// neither may be derived from the other: that one is a chat-id namespace private to
// that package, this one is a push-subject vocabulary shared with the worker.
const RunSubjectPrefix = "run:"

// RunSubject is the subject of a notification about one workflow run. A workflow id
// is unique per run, so two runs finishing inside one debounce window still occupy
// their own tray slots.
func RunSubject(workflowID string) PushSubject {
	return PushSubject{Key: RunSubjectPrefix + workflowID}
}
