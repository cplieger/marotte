package agent

import (
	"context"
	"net/http"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// The interfaces below are the runtime's dependency contracts, declared at the
// consumer rather than in a shared package, each naming only what it invokes.

// mcpNameSets is the MCP server-name census. *mcp.Store satisfies it.
//
// All 3 methods are used because the runtime reasons from their NESTING:
// enabled, configured-but-not-enabled, in AllNames only (a Power), in none.
type mcpNameSets interface {
	EnabledNames(ctx context.Context) map[string]struct{}
	ConfiguredNames(ctx context.Context) map[string]struct{}
	// AllNames is best-effort: an unparseable config file reports
	// OriginUnknown for a name it misses rather than dropping it.
	AllNames(ctx context.Context) map[string]struct{}
}

// RouteRegistrar is a component that mounts its own routes under a sub-tree of
// /api/*. The runtime's OUTPUT type, so the registry's own type stays here.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// bridgeChatRecords is the chat store as the BRIDGE LIFECYCLE uses it.
//
// Delete is deliberately absent: only cmdDeleteChat may remove a chat file, so
// the path that tears bridges down on exit and restart must not be able to.
type bridgeChatRecords interface {
	Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool)
	// Mutate is the header write primitive: load, apply, save, broadcast.
	Mutate(ctx context.Context, id marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error)
	// OpenTurn appends a turn_open and answers it; init writes the header for an
	// id with none, and nil refuses such an id.
	OpenTurn(ctx context.Context, chatID marotte.ChatID, spec *chat.TurnSpec, init func(c *marotte.Chat)) (*marotte.Entry, error)
	// Sink is the chat's log as the accumulator's sink.
	Sink(chatID marotte.ChatID) chat.EntrySink
	// AppendBetweenTurns files a lane-less entry after the newest turn's close.
	AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) (*marotte.Entry, error)
	// WriteCounters rewrites the header's turn_count and last_turn_outcome from the
	// log's index, outside every registry lock.
	WriteCounters(ctx context.Context, chatID marotte.ChatID) error
}

// chatRecords is the runtime's field type: a UNION of the narrower views the
// composition root passes on to bridgeChatRecords, command.ChatStore and
// translate.ChatRecords. The runtime itself calls only Get, List, ListComplete,
// Mutate and Exists.
type chatRecords interface {
	bridgeChatRecords

	List(ctx context.Context) []marotte.ChatHeader
	ListComplete(ctx context.Context) ([]marotte.ChatHeader, bool)
	// Exists is the digest resolver's `chat` gone predicate: file present and not
	// tombstoned, read under NO per-chat mutex, because Mutate holds that mutex
	// across an fsynced file rewrite the resolver must never wait out.
	Exists(id marotte.ChatID) bool
	// SetDraft and SetAttachments are passed on to the command dispatcher.
	SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error)
	SetAttachments(ctx context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error)
	// Delete removes the chat directory; only cmdDeleteChat calls it.
	Delete(ctx context.Context, id marotte.ChatID) error
	// The log reads command.ChatStore and translate.ChatRecords name.
	Revert(ctx context.Context, chatID marotte.ChatID, turn, kasMessageID string) (record, opened *marotte.Entry, err error)
	RewindTarget(ctx context.Context, chatID marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error)
	PromptAttachmentPaths(ctx context.Context, chatID marotte.ChatID, watermark string) ([]string, error)
	PromptTexts(ctx context.Context, id marotte.ChatID) ([]string, error)
	EmptyCompactions(ctx context.Context, id marotte.ChatID) (int, error)
	TurnCount(ctx context.Context, chatID marotte.ChatID) (uint64, bool)
	// TurnSeq is a turn's newest sealed seq: the digest's live_turn version.
	TurnSeq(ctx context.Context, chatID marotte.ChatID, turn string) (uint64, bool)
	// NewestRevert is the provenance a resume's projection snapshots at open, in the
	// shape TurnSeq has: the log's answer, read through the store.
	NewestRevert(ctx context.Context, chatID marotte.ChatID) (string, bool)
	// Reconcile is the resume merge's locked swap over the chat's log and header.
	Reconcile(ctx context.Context, chatID marotte.ChatID, swap func(l *chat.EntryLog, h chat.EntryHeader) (bool, error)) (version string, changed bool, err error)
}

// pushNotifier is the notification SEND half. *push.Service satisfies it.
type pushNotifier interface {
	HasSubscribers() bool
	Send(ctx context.Context, title, body string, notifyType marotte.PushKind, subject marotte.PushSubject)
	// Retract drops any push about subject still held for a later delivery: the
	// ask was answered, so a nudge about it has nothing left to say.
	Retract(subject marotte.PushSubject)
}

// pushService is the runtime's whole view of push: the send half plus the two
// lifecycle calls the runtime owns. Subscribe/Unsubscribe are absent, and
// SetPreferences belongs to the settings endpoint in internal/server.
type pushService interface {
	pushNotifier

	// ReloadPreferences re-reads notification toggles from disk, so an
	// externally-edited config.json takes effect without a restart.
	ReloadPreferences(ctx context.Context)
	// Close cancels in-flight pushes so shutdown does not block on their timeout.
	Close()
}

// The ACP-bridge interfaces below are one contract seen at different widths:
// *bridge.Bridge satisfies the widest, and every narrower one states what a
// particular function may do with a bridge it was handed.

// acpSession names the ACP session an RPC is addressed to.
type acpSession interface {
	SessionID() marotte.SessionID
}

// acpCaller sends a JSON-RPC request and waits for its response. Returns
// ctx.Err() if ctx is cancelled before the response arrives.
type acpCaller interface {
	Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error)
}

// acpResponder answers an INBOUND request from kiro-cli. The utility session's
// forward goroutine takes only this: an unanswered inbound request wedges the
// turn until the drain ceiling fires, so its whole job is to reply.
type acpResponder interface {
	Respond(ctx context.Context, id int64, result any, err error) error
}

// acpStopper kills the subprocess. NotifCh closes; must be called at most
// once per bridge instance.
type acpStopper interface {
	Stop()
}

// acpSessionCaller is one call addressed to the bridge's own session — what a
// utility-session lease hands its caller to use outside the session mutex.
type acpSessionCaller interface {
	acpCaller
	acpSession

	// CallAt is Call plus the read loop position the response arrived at, for a
	// caller ordering a LOCAL decision against notifications still queued behind
	// it — a turn_end or a session/load replay that PRECEDES the response.
	CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error)
}

// acpSessionResponder is what the utility session's forward goroutine needs of
// the bridge it was started with: answer its inbound requests, and name the
// session whose frames it may keep. Reading the id off the bridge PARAMETER is
// what keeps forward off us.mu, which stopLocked holds across <-forwardDone.
type acpSessionResponder interface {
	acpResponder
	acpSession
}

// acpSessionFacts is everything a freshly started or loaded session knows about
// itself, written onto the chat record on persist. No mutator among them.
type acpSessionFacts interface {
	acpSession

	ModelID() marotte.ModelID
	// CurrentMode returns the active mode id, empty when the agent has none.
	CurrentMode() string
	// SessionTitle returns KAS's own title (flat _meta.title). Advisory:
	// creation always yields "New Session", so adopt it only for a default name.
	SessionTitle() string
	// ContextThresholds returns the session's summarization and truncation
	// percentages. Either reads 0 when the load result omitted it, which is why
	// every caller writes only a positive value.
	ContextThresholds() (summarization, truncation float64)
	// Modes returns the session modes the agent supports; empty if it has none.
	Modes() []marotte.SessionMode
	// Catalog returns every advertised model, UNFILTERED — the entitlement input
	// ApplyServedModels reads, where Models' display filter would refuse a live model.
	Catalog() []marotte.SessionModel
	// Models returns the swappable models, deprecated/internal entries filtered.
	Models() []marotte.SessionModel
}

// utilityBridge is the long-lived utility session's ACP surface. It never
// switches model in session, never sends a bare notification, and never reads
// the mode or model catalogue — the CHAT bridges answer for those.
type utilityBridge interface {
	acpSessionCaller
	acpResponder
	acpStopper

	// Start launches a fresh kiro-cli ACP subprocess. ctx bounds the startup
	// handshake ONLY; the subprocess's lifetime is StartOpts.Lifetime, REQUIRED.
	Start(ctx context.Context, opts *marotte.StartOpts) error
	// NotifCh yields incoming ACP notifications with the read loop's sequence,
	// closing when the subprocess exits, or at Stop when none ever started. The
	// forward goroutine must be draining it BEFORE Start: on v3 session/new blocks on
	// requests that arrive here.
	NotifCh() <-chan marotte.Notification
}

// ACPBridge manages a single kiro-cli ACP subprocess for one chat.
// *bridge.Bridge satisfies it. Methods are safe for concurrent use; Call and
// Notify serialize writes to the subprocess stdin internally.
type ACPBridge interface {
	acpSessionFacts
	utilityBridge

	// Notify sends a JSON-RPC notification (no response expected).
	Notify(ctx context.Context, method string, params any) error
	// SetModel performs an in-session model swap via session/set_config_option
	// (configId "model") — v3 has no session/set_model.
	SetModel(ctx context.Context, modelID string) error
	// EnsureEffort makes the live session run at the given reasoning-effort level
	// (session/set_config_option, configId "effortLevel"). It returns without a
	// round trip when the level the session last reported already matches, and
	// SetModel clears that cache because a swap can CLEAR the level KAS runs at.
	EnsureEffort(ctx context.Context, level string) error
	// ObserveEffort records a level the SESSION reported on
	// `config_option_update`, keeping EnsureEffort's comparison honest.
	ObserveEffort(level string)
	// SessionLoadSeq returns the read loop position the `session/load` response
	// arrived at, the position forward must have folded up to. Zero on a
	// session/new, and zero is also legal, so pair it with the load returning.
	SessionLoadSeq() uint64
	// SupervisedApplied reports whether this session ACCEPTED `autopilot: off`.
	// It says nothing about whether the chat asked for it — that request is on the
	// chat record — so read both, or a chat that never wanted supervised mode is
	// indistinguishable from one whose assert was refused.
	SupervisedApplied() bool
}

// ACPBridgeFactory creates new ACPBridge instances, once per chat and once for
// the utility session; each invocation is a new bridge.
type ACPBridgeFactory func() ACPBridge
