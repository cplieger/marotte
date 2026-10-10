package agent

import (
	"context"
	"net/http"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// The runtime's dependency contracts, declared at the consumer, each naming only what it invokes.

// ConfiguredNames is ownership; EnabledNames gates the live-control routes.
type mcpNameSets interface {
	EnabledNames(ctx context.Context) map[string]struct{}
	ConfiguredNames(ctx context.Context) map[string]struct{}
}

// kasMCPRenderer is *mcp.Store, called on the chat-open path: RenderKASConfig gives up when ctx ends, and
// RenderedWaitForReady must not block.
type kasMCPRenderer interface {
	RenderKASConfig(ctx context.Context) error
	RenderedWaitForReady() (waitForReady, known bool)
}

// RouteRegistrar mounts its own routes under a sub-tree of /api/*.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// Delete is absent: only cmdDeleteChat may remove a chat.
type bridgeChatRecords interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// Presence is the chat-gone read (the digest's, and a failed generation's), under no per-chat
	// mutex: Mutate holds it across an fsync.
	Presence(id marotte.ChatID) chat.Presence
	// PublishIfPresent publishes a ready generation under the chat's lock once its record loads, so
	// no delete lands between the verdict and the publication.
	PublishIfPresent(ctx context.Context, id marotte.ChatID, publish func()) chat.Presence
	// Mutate is the header write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// Dequeue takes a queued row whose turn opened off the header; Opened reports
	// such a row, even while its removal is still pending.
	Dequeue(ctx context.Context, chatID marotte.ChatID, id string) error
	Opened(chatID marotte.ChatID, id string) bool
	// All answers every entry of the chat's log in file order.
	All(ctx context.Context, chatID marotte.ChatID) ([]marotte.Entry, error)
	// OpenTurn appends a turn_open and answers it; init writes a missing header, nil refuses.
	OpenTurn(ctx context.Context, chatID marotte.ChatID, spec *chat.TurnSpec, init func(c *marotte.Chat)) (*marotte.Entry, error)
	// Sink is the chat's log as the accumulator's sink.
	Sink(chatID marotte.ChatID) chat.EntrySink
	// AppendBetweenTurns files a lane-less entry after the newest turn's close.
	AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) ([]*marotte.Entry, error)
	// WriteCounters rewrites the header's turn_count and last_turn_outcome, outside every registry lock.
	WriteCounters(ctx context.Context, chatID marotte.ChatID) error
}

type chatRecords interface {
	bridgeChatRecords

	List(ctx context.Context) []marotte.ChatHeader
	// SessionClaimed is the membership coordinator's fresh chain read, passed on
	// as command.sessionClaims.
	SessionClaimed(ctx context.Context, sessionID string) (claimed, complete bool)
	ListComplete(ctx context.Context) ([]marotte.ChatHeader, bool)
	// SetDraft and SetAttachments are passed on to the command dispatcher.
	SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error)
	SetAttachments(ctx context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error)
	// Delete removes the chat directory; only cmdDeleteChat calls it.
	Delete(ctx context.Context, id marotte.ChatID) (sessionChain []string, err error)
	// Revert and the other log reads are command.chatStore's and translate.chatRecords'.
	Revert(ctx context.Context, chatID marotte.ChatID, turn, kasMessageID string) (record *marotte.Entry, minted []*marotte.Entry, err error)
	RewindTarget(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error)
	PromptReceipt(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.PromptReceipt, error)
	PromptAttachmentPaths(ctx context.Context, chatID marotte.ChatID, watermark string) ([]string, error)
	PromptTexts(ctx context.Context, id marotte.ChatID) ([]string, error)
	EmptyCompactions(ctx context.Context, id marotte.ChatID) (int, error)
	WaitingWorkflowMessages(ctx context.Context, id marotte.ChatID) ([]marotte.WorkflowMessage, error)
	DepartedName(id marotte.ChatID) (string, bool)
	TurnCount(ctx context.Context, chatID marotte.ChatID) (uint64, bool)
	TurnPage(ctx context.Context, chatID marotte.ChatID, turn string, from uint64) (*chat.TurnPage, error)
	// TurnSeq is a turn's newest sealed seq: the digest's live_turn version.
	TurnSeq(ctx context.Context, chatID marotte.ChatID, turn string) (uint64, bool)
	// NewestRevert is the provenance a resume's projection snapshots at open.
	NewestRevert(ctx context.Context, chatID marotte.ChatID) (string, bool)
	// Reconcile is the resume merge's locked swap over the chat's log and header.
	Reconcile(ctx context.Context, chatID marotte.ChatID, swap func(l *chat.EntryLog, h chat.EntryHeader) (bool, error)) (version string, changed bool, err error)
}

type pushNotifier interface {
	HasSubscribers() bool
	Send(ctx context.Context, n *marotte.NotificationPayload)
	// Retract drops any ask push about subject still held for delivery.
	Retract(subject marotte.PushSubject)
}

// pushService is the runtime's view of push: send plus the preference and lifecycle calls it owns.
type pushService interface {
	pushNotifier

	// SetPreferences sets each toggle prefs names; a kind it omits keeps its toggle.
	SetPreferences(prefs map[marotte.PushKind]bool)
	// Preferences returns the per-kind notification toggles in force.
	Preferences() map[marotte.PushKind]bool
	// Close cancels in-flight pushes so shutdown does not block on their timeout.
	Close()
}

// One ACP-bridge contract at several widths; *bridge.Bridge satisfies the widest.

type acpSession interface {
	SessionID() marotte.SessionID
}

// Returns ctx.Err() if ctx is cancelled before the response arrives.
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
}

// acpResponder answers an inbound request from kiro-cli; unanswered, it wedges the turn.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// At most once per bridge.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call on the bridge's own session, used outside the session mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession

	// CallAt is Call plus the read-loop position the response arrived at.
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
}

// acpSessionResponder is what the utility forward goroutine needs. The id comes off the
// parameter so forward never takes us.mu, which stopLocked holds across <-forwardDone.
type acpSessionResponder interface {
	acpResponder
	acpSession
}

// No mutators.
type acpSessionFacts interface {
	acpSession

	ModelID() marotte.ModelID
	// CurrentMode returns the active mode id, empty when the agent has none.
	CurrentMode() string
	// SessionTitle returns KAS's own title; creation always yields "New Session".
	SessionTitle() string
	// SessionTitleSetByUser reports KAS's titleSetByUser latch on the session.
	SessionTitleSetByUser() bool
	// SummarizationThreshold returns the session's summarization percentage, 0 when omitted.
	SummarizationThreshold() float64
	// Modes returns the session modes the agent supports; empty if it has none.
	Modes() []marotte.SessionMode
	// Catalog returns every advertised model unfiltered, for ApplyServedModels.
	Catalog() []marotte.SessionModel
	// Models returns the swappable models, deprecated/internal entries filtered.
	Models() []marotte.SessionModel
}

// utilityBridge is the utility session's ACP surface: no model swap, no notification, no catalogue reads.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a kiro-cli ACP subprocess; ctx bounds the handshake only, StartOpts.Lifetime (required) the process.
	Start(ctx context.Context, opts *marotte.StartOpts) error
	// NotifCh yields notifications with their read-loop sequence and closes on exit, or at Stop
	// if never started. It must be drained BEFORE Start: v3 session/new blocks on requests here.
	NotifCh() <-chan marotte.Notification
	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params any) error
	// RefreshContentCollection re-resolves StartOpts.ContentCollection onto the live session when KAS has not confirmed
	// that value, returning it; an unreadable document or no resolver sends nothing.
	RefreshContentCollection(ctx context.Context) (bool, error)
	// StartLive reports the ignore list (nil: none delivered) and shell timeout Start left KAS holding.
	StartLive() (ignoreFiles []string, terminalTimeoutMs int)
}

// ACPBridge manages one kiro-cli ACP subprocess for one chat (*bridge.Bridge). Safe for
// concurrent use; Call and Notify serialize stdin writes.
type ACPBridge interface {
	acpSessionFacts
	utilityBridge

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params any) error
	// SetModel swaps the model in session via session/set_config_option ("model").
	SetModel(ctx context.Context, modelID string) error
	// EnsureEffort sets the session's effort level (set_config_option "effortLevel"), skipping
	// the call when the last reported level matches; SetModel clears that cache.
	EnsureEffort(ctx context.Context, level string) error
	// ObserveEffort records a level the session reported on `config_option_update`.
	ObserveEffort(level string)
	// EnsureThinking sets thinking "on" or "off", sending only when the session reported otherwise.
	EnsureThinking(ctx context.Context, choice string) error
	// ObserveThinking records a thinking value the session reported.
	ObserveThinking(value string)
	// SessionLoadSeq returns the read-loop position of the `session/load` response; zero on
	// session/new, and also legal, so pair it with the load returning.
	SessionLoadSeq() uint64
	// SupervisedApplied reports whether the session accepted `autopilot: off`; the request itself is on the chat record.
	SupervisedApplied() bool
	// AutoCompactionDisabled reports the disableAutoCompaction value KAS froze for the session.
	AutoCompactionDisabled() bool
}

// ACPBridgeFactory creates a new ACPBridge per invocation.
type ACPBridgeFactory func() ACPBridge
