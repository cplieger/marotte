// The utility session is the shared kiro-cli subprocess that answers marotte's workspace reads and writes with no
// chat open; it is never prompted. This file holds the session, utility_rpc.go the RPC wrappers. acquire() a lease
// and use its bridge outside the session mutex; resetIf(gen) is idempotent, so a stale failure cannot tear down a
// recycled session.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/secretstore"
	"github.com/cplieger/marotte/internal/translate"
)

// utilitySessionHooks are the runtime callbacks the forward goroutine invokes, immutable after construction so it
// reads them without locks.
type utilitySessionHooks struct {
	// onHooksChanged broadcasts an hooks_changed SSE on _kiro/hooks/didChange.
	onHooksChanged func()
	// onPowersChanged takes _kiro/powers/items_changed; chat bridges route theirs to the same handler.
	onPowersChanged func(params json.RawMessage)
	// onRecipesChanged takes _kiro/workflow/recipes_changed, which this session owns; chat bridges noop it.
	onRecipesChanged func()
	// onGovernanceState captures the _kiro/governance/state sent on session/new, so GET /api/governance is warm with
	// no chat open.
	onGovernanceState func(json.RawMessage)
	// onConfigurationState takes _kiro/configuration/state, which only this session declares.
	onConfigurationState func(json.RawMessage)
	// onPolicyNotification routes _kiro/policy/{changed,error} into the chat dispatch's translator: this session's
	// notifications bypass that dispatcher, so a write with no chat open would be lost.
	onPolicyNotification func(*marotte.RPCResponse)
	// onSlashCommands takes this session's available_commands_update, so the slash menu has a catalog before any chat.
	onSlashCommands func(update json.RawMessage)
	// onSteeringDocs takes _kiro/steering/documents_changed for its configIssues.
	onSteeringDocs func(params json.RawMessage)
	// onSystemNotify takes _kiro/system/notify, which names no session.
	onSystemNotify func(*marotte.RPCResponse)
	// onForeignUpdate offers another session's session/update frame to its reader and reports whether it was consumed:
	// session/load replays a workflow step's transcript here under that session's id. Consulted before the Warn.
	onForeignUpdate func(sessionID string, kind marotte.ACPUpdateKind, update json.RawMessage) bool
	// onFrameDrained reports the read-loop position this session has folded and its attachment; force marks the final
	// call after the drain loop ends. It closes a step replay (stepReplays.settleConsumed).
	onFrameDrained func(at drainPoint, force bool)
	// presets resolves the active security profile into KAS policy preset ids for StartOpts. This session answers GET
	// /api/permissions, so without them the policy view cannot report what a profile grants. nil sends none.
	presets func(context.Context) []string
	// ignoreFiles resolves the ignore-file list for StartOpts, so every session resolves it the same way. nil sends
	// none, and then no notification is sent.
	ignoreFiles func(context.Context) []string
	// contentCollection resolves StartOpts.ContentCollection, the Settings switch every chat spawns under too. nil
	// sends nothing.
	contentCollection func(context.Context) (enabled, readable bool)
	// telemetryOff resolves StartOpts.DisableTelemetry; nil leaves KAS's default, on.
	telemetryOff func() bool
}

// utilitySession owns the dedicated kiro-cli subprocess and ACP session, so a workspace read needs no chat open and
// never rebinds a chat's session. Started lazily on first acquire, culled after 30 minutes idle.
type utilitySession struct {
	// shutdownCtx is the runtime's lifetime, never a request context: acquire's ctx dies with the request, and the
	// subprocess must outlive it.
	shutdownCtx   context.Context
	bridgeFactory ACPBridgeFactory
	// secrets is the runtime's shared credential store, so a registration from any bridge is visible to all. Nil
	// without a configDir.
	secrets *secretstore.Store
	hooks   utilitySessionHooks

	// Guarded by mu, held only for bookkeeping and never across a bridge Call, or a slow call starves a stop.
	lastActiveAt time.Time
	bridge       utilityBridge
	forwardDone  chan struct{}
	// liveIgnore is the ignore list this process last confirmed, guarded by mu.
	liveIgnore []string
	// liveLock, not mu, serializes the live pushes to this process (syncLive).
	liveLock surfaceLock
	gen      uint64
	mu       sync.Mutex

	// enableHooks opts into KAS's v2 hook engine (true in production) for the hooks list/setEnabled RPCs.
	enableHooks bool
	started     bool
}

// newUtilitySession constructs a stopped session (started=false, gen=0, zero lastActiveAt). shutdownCtx is
// positional: any default lifetime would be one nothing can cancel.
func newUtilitySession(shutdownCtx context.Context, factory ACPBridgeFactory, hooks *utilitySessionHooks, secrets *secretstore.Store, enableHooks bool) *utilitySession {
	return &utilitySession{
		shutdownCtx:   shutdownCtx,
		bridgeFactory: factory,
		secrets:       secrets,
		hooks:         *hooks,
		enableHooks:   enableHooks,
	}
}

// sessionLease is a caller's snapshot of the live session: the bridge to Call and the generation for resetIf.
type sessionLease struct {
	bridge acpSessionCaller
	gen    uint64
}

// acquire bumps the idle clock. Use the bridge outside the session mutex and report failures via
// resetIf(lease.gen).
func (us *utilitySession) acquire(ctx context.Context) (sessionLease, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		if err := us.startLocked(ctx); err != nil {
			return sessionLease{}, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	us.lastActiveAt = time.Now()
	return sessionLease{bridge: us.bridge, gen: us.gen}, nil
}

// ensureStarted lazily starts the session without a request, for callers that only need it live (warming GET
// /api/governance, whose state arrives on session/new).
func (us *utilitySession) ensureStarted(ctx context.Context) error {
	_, err := us.acquire(ctx)
	return err
}

// No Model in StartOpts: the session is never prompted, so KAS's default costs nothing. Caller holds us.mu.
func (us *utilitySession) startLocked(ctx context.Context) error {
	bridge := us.bridgeFactory()

	// Forward must drain NotifCh before Start: the handshake sends notifications and host requests there. A local, so a
	// failed Start leaves no state.
	forwardDone := make(chan struct{})
	// Bumped before the goroutine: positions compare only within one attachment, and the lease carries the same
	// number. A failed Start is harmless: started stays false and resetIf returns early.
	us.gen++
	go us.forward(bridge, us.gen, bridge.NotifCh(), forwardDone)

	// shutdownCtx, not a request ctx: this runs under us.mu, so a session/new that never answers would hold the mutex
	// for good. Start bounds the handshake itself.
	if err := bridge.Start(us.shutdownCtx, &marotte.StartOpts{Lifetime: us.shutdownCtx, AgentEngine: resolveAgentEngine(), EnableHooks: us.enableHooks, SecretStorage: us.secrets != nil, Presets: us.sessionPresets(ctx), IgnoreFiles: us.sessionIgnoreFiles, ContentCollection: us.hooks.contentCollection, DisableTelemetry: us.hooks.telemetryOff != nil && us.hooks.telemetryOff(), ConfigurationState: true}); err != nil {
		return err
	}
	us.liveIgnore = startedLive(bridge).ignoreFiles
	us.bridge = bridge
	us.started = true
	us.lastActiveAt = time.Now()
	us.forwardDone = forwardDone

	// Logged because this session appears in session/list owned by no chat.
	slog.Info("utility bridge started", "session_id", string(bridge.SessionID()))
	return nil
}

// stopLocked stops the subprocess and waits for the forward goroutine to exit, so a recycle leaks nothing. Caller
// holds us.mu.
func (us *utilitySession) stopLocked() {
	us.bridge.Stop()
	if us.forwardDone != nil {
		<-us.forwardDone
	}
	us.started = false
	us.forwardDone = nil
}

// resetIf stops the session only when gen is still the live generation; a complaint about a recycled session is
// dropped.
func (us *utilitySession) resetIf(gen uint64) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started || us.gen != gen {
		return
	}
	us.stopLocked()
}

// Stop stops the session if it is running; the next acquire starts a fresh one. Safe for concurrent use.
func (us *utilitySession) Stop() {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		return
	}
	us.stopLocked()
	slog.Info("utility bridge stopped")
}

// stopIfIdle stops the session when inactive since before cutoff, reporting whether it did. The stop is
// asynchronous: the cull must never block on a slow teardown.
func (us *utilitySession) stopIfIdle(cutoff time.Time) bool {
	us.mu.Lock()
	shouldStop := us.started && !us.lastActiveAt.IsZero() && us.lastActiveAt.Before(cutoff)
	var victim acpStopper
	if shouldStop {
		// Under the lock: startLocked reassigns us.bridge, so a later read could stop a freshly restarted bridge.
		victim = us.bridge
		us.started = false
		us.forwardDone = nil
	}
	us.mu.Unlock()
	if shouldStop {
		go victim.Stop()
	}
	return shouldStop
}

// liveID returns the running session's ACP id, or "" when stopped. The orphan-session sweep exempts it: no chat
// references it, so the sweep would delete it under the live subprocess.
func (us *utilitySession) liveID() string {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		return ""
	}
	return string(us.bridge.SessionID())
}

// Not command.sessionParams: this bridge carries no prompt slot.
func utilitySessionParams(bridge acpSession, extra map[string]any) map[string]any {
	m := map[string]any{marotte.KeySessionID: bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

// A nil hook is valid wiring.
type foreignUpdateHook func(sessionID string, kind marotte.ACPUpdateKind, update json.RawMessage) bool

// updateKind reads the sessionUpdate discriminator off a frame's update object, false when it cannot be decoded: an
// unknown kind is ignored, an undecodable update means the wire changed shape.
func updateKind(update json.RawMessage) (marotte.ACPUpdateKind, bool) {
	var base utilityUpdateBase
	if json.Unmarshal(update, &base) != nil {
		return "", false
	}
	return base.Kind, true
}

// Read off params it is "" for every frame.
type utilityUpdateBase struct {
	Kind marotte.ACPUpdateKind `json:"sessionUpdate"`
}

// Peer requests go to answerHostRequest (session/ new stalls without the shell type); everything
// else but hooks and policy is dropped so readLoop never blocks. bridge is explicit so a recycle
// cannot redirect answers.
func (us *utilitySession) forward(bridge acpSessionResponder, gen uint64, notifCh <-chan marotte.Notification, done chan<- struct{}) {
	defer close(done)
	for n := range notifCh {
		msg := n.Msg
		switch {
		case msg.ID != nil:
			us.answerHostRequest(bridge, msg)
		case us.dispatchNotification(msg):
		case us.hooks.onSlashCommands != nil && us.takeSlashUpdate(msg, string(bridge.SessionID())):
		default:
			// Read per frame: the id is empty until session/new answers. SessionID takes only the bridge's own mutex, which no
			// method holds across a round trip.
			offerForeign(msg, string(bridge.SessionID()), us.hooks.onForeignUpdate)
		}
		// After handling, so the reported position is folded, not merely received.
		us.noteFrameDrained(drainPoint{gen: gen, seq: n.Seq}, false)
	}
	// Closed and drained: this call seals a replay whose trailing frames never came.
	us.noteFrameDrained(drainPoint{gen: gen}, true)
}

// An unwired hook drops it.
func (us *utilitySession) dispatchNotification(msg *marotte.RPCResponse) bool {
	hook, claimed := us.notificationHook(msg.Method)
	if hook != nil {
		hook(msg)
	}
	return claimed
}

func (us *utilitySession) notificationHook(method string) (hook func(*marotte.RPCResponse), claimed bool) {
	h := &us.hooks
	switch method {
	case methodKiroHooksDidChange:
		return bare(h.onHooksChanged), true
	case methodV3Powers:
		return withParams(h.onPowersChanged), true
	case methodKiroWorkflowRecipesChanged:
		return bare(h.onRecipesChanged), true
	case methodV3Governance:
		return withParams(h.onGovernanceState), true
	case methodKiroConfigurationState:
		return withParams(h.onConfigurationState), true
	case methodV3PolicyChanged, methodV3PolicyError:
		return h.onPolicyNotification, true
	case methodV3SteeringDocs:
		return withParams(h.onSteeringDocs), true
	case methodV3SystemNotify:
		return h.onSystemNotify, true
	}
	return nil, false
}

func bare(f func()) func(*marotte.RPCResponse) {
	if f == nil {
		return nil
	}
	return func(*marotte.RPCResponse) { f() }
}

func withParams(f func(json.RawMessage)) func(*marotte.RPCResponse) {
	if f == nil {
		return nil
	}
	return func(msg *marotte.RPCResponse) { f(msg.Params) }
}

func (us *utilitySession) takeSlashUpdate(msg *marotte.RPCResponse, ownSession string) bool {
	if msg.Method != marotte.MethodSessionUpdate {
		return false
	}
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	// An empty own id is the session/new handshake (the catalog arrives first); a foreign id is a step replay's.
	if json.Unmarshal(msg.Params, &env) != nil || (ownSession != "" && env.SessionID != ownSession) {
		return false
	}
	if kind, ok := updateKind(env.Update); !ok || kind != marotte.ACPUpdateAvailableCommands {
		return false
	}
	us.hooks.onSlashCommands(env.Update)
	return true
}

func (us *utilitySession) noteFrameDrained(at drainPoint, force bool) {
	if us.hooks.onFrameDrained != nil {
		us.hooks.onFrameDrained(at, force)
	}
}

// Nil sends no key (Custom).
func (us *utilitySession) sessionPresets(ctx context.Context) []string {
	if us.hooks.presets == nil {
		return nil
	}
	return us.hooks.presets(ctx)
}

// Nil or empty withholds the notification, which would otherwise clear the list.
func (us *utilitySession) sessionIgnoreFiles(ctx context.Context) []string {
	if us.hooks.ignoreFiles == nil {
		return nil
	}
	return us.hooks.ignoreFiles(ctx)
}

// offerForeign hands another session's session/update frame to its reader. KAS can hydrate a session into this
// process (a step replay), and that text belongs to the other transcript. This session is never prompted, so its own
// frames, and every frame before session/new names it, carry nothing to read.
func offerForeign(msg *marotte.RPCResponse, ownSession string, onForeign foreignUpdateHook) {
	if msg.Method != marotte.MethodSessionUpdate || msg.Params == nil {
		return
	}
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return
	}
	if ownSession == "" || env.SessionID == ownSession {
		return
	}
	// An undecodable update goes to nobody: consuming it would hide the shape change.
	if kind, ok := updateKind(env.Update); ok && onForeign != nil && onForeign(env.SessionID, kind, env.Update) {
		return
	}
	slog.Warn("utility bridge: dropping a frame for a foreign session",
		"frame_session", env.SessionID, "utility_session", ownSession)
}

// answerHostRequest answers the utility session's host requests. shell_type is answered for KAS's step reloads.
// executeHook is not answered (it runs a shell command a hook file names) and gets -32601. Tool requests are refused
// rather than left pending.
func (us *utilitySession) answerHostRequest(bridge acpResponder, msg *marotte.RPCResponse) {
	ctx := context.Background()
	switch {
	case msg.Method == methodKiroShellType:
		_ = bridge.Respond(ctx, *msg.ID, kiroShellTypeResult(), nil)
	case msg.Method == methodKiroSecretGet:
		// Unreachable without mcpServers, but secretStorage is declared by the shared initialize whenever secrets is set.
		_ = bridge.Respond(ctx, *msg.ID, secretGetResult(us.secrets, msg.Params), nil)
	case msg.Method == methodKiroSecretStore:
		result, err := secretStoreResult(ctx, us.secrets, msg.Params)
		_ = bridge.Respond(ctx, *msg.ID, result, err)
	case msg.Method == methodKiroSecretDelete:
		result, err := secretDeleteResult(ctx, us.secrets, msg.Params)
		_ = bridge.Respond(ctx, *msg.ID, result, err)
	case msg.Method == marotte.MethodRequestPermission:
		// cancelled rather than a selected reject: nobody is attached to select. KAS maps both to the same reject on every
		// arm (measured on KAS 0.58.7).
		slog.Warn("utility bridge: denying tool permission request (no-tools session)")
		_ = bridge.Respond(ctx, *msg.ID, marotte.PermissionOutcomeCancelled(), nil)
	case msg.Method == marotte.MethodFSRead || msg.Method == marotte.MethodFSWrite ||
		strings.HasPrefix(msg.Method, methodTermPrefix):
		slog.Warn("utility bridge: refusing tool request (no-tools session)", "method", msg.Method)
		// -32601: a capability deliberately not offered, not a fault.
		_ = bridge.Respond(ctx, *msg.ID, nil, &marotte.RPCError{
			Code:    marotte.RPCCodeMethodNotFound,
			Message: "the utility session runs no tools",
		})
	default:
		// Answered rather than left pending, which can wedge the caller.
		slog.Warn("utility bridge: unexpected peer request, refusing", "method", msg.Method, "id", *msg.ID)
		// -32601: a deliberate refusal, where -32603 would blame the wrong side.
		_ = bridge.Respond(ctx, *msg.ID, nil, &marotte.RPCError{
			Code:    marotte.RPCCodeMethodNotFound,
			Message: "unsupported on the utility session: " + msg.Method,
		})
	}
}
