// The utility session is the shared kiro-cli subprocess behind marotte's ambient AI features: this file holds the
// session, utility_rpc.go the RPC wrappers, utility_agent.go the text agent. acquire() a lease and use its bridge
// outside the session mutex; resetIf(gen) is idempotent, so a stale failure cannot tear down a recycled session.

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

// utilityRuntime bundles the session holder and the text agent the runtime constructs together.
type utilityRuntime struct {
	session *utilitySession
	// textgen is the text-generation agent; a field named agent would invite a local shadowing the package.
	textgen *utilityAgent
}

// newUtilityRuntime wires a session and its agent.
func newUtilityRuntime(shutdownCtx context.Context, factory ACPBridgeFactory, models func() []marotte.SessionModel, hooks *utilitySessionHooks, secrets *secretstore.Store, enableHooks bool) *utilityRuntime {
	session := newUtilitySession(shutdownCtx, factory, models, hooks, secrets, enableHooks)
	return &utilityRuntime{session: session, textgen: newUtilityAgent(session)}
}

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
	// contentCollection resolves StartOpts.ContentCollection: this session sends diffs and error text, so it opts out.
	// nil sends nothing.
	contentCollection func(context.Context) (enabled, readable bool)
	// telemetryOff resolves StartOpts.DisableTelemetry; nil leaves KAS's default, on.
	telemetryOff func() bool
}

// utilitySession owns the dedicated kiro-cli subprocess and ACP session, so ambient work never pollutes a chat's
// context. Started lazily on first acquire, culled after 30 minutes idle.
type utilitySession struct {
	// shutdownCtx is the runtime's lifetime, never a request context: acquire's ctx dies with the request, and the
	// subprocess must outlive it.
	shutdownCtx   context.Context
	bridgeFactory ACPBridgeFactory
	models        func() []marotte.SessionModel
	// secrets is the runtime's shared credential store, so a registration from any bridge is visible to all. Nil
	// without a configDir.
	secrets *secretstore.Store
	hooks   utilitySessionHooks

	// Guarded by mu, held only for bookkeeping and never across a bridge Call, or a slow turn starves a recycle.
	lastActiveAt time.Time
	bridge       utilityBridge
	responseCh   chan utilityChunkPayload
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
func newUtilitySession(shutdownCtx context.Context, factory ACPBridgeFactory, models func() []marotte.SessionModel, hooks *utilitySessionHooks, secrets *secretstore.Store, enableHooks bool) *utilitySession {
	return &utilitySession{
		shutdownCtx:   shutdownCtx,
		bridgeFactory: factory,
		models:        models,
		secrets:       secrets,
		hooks:         *hooks,
		enableHooks:   enableHooks,
	}
}

// sessionLease is a caller's snapshot of the live session: the bridge to Call, the generation for resetIf, and the
// chunk channel (nil until started; a closed one ends a drain early).
type sessionLease struct {
	bridge acpSessionCaller
	chunks <-chan utilityChunkPayload
	gen    uint64
}

// acquire ensures the session is started and returns a lease, bumping the idle clock. Use the bridge outside the
// session mutex and report failures via resetIf(lease.gen).
func (us *utilitySession) acquire(ctx context.Context) (sessionLease, error) {
	us.mu.Lock()
	defer us.mu.Unlock()
	if !us.started {
		if err := us.startLocked(ctx); err != nil {
			return sessionLease{}, fmt.Errorf("utility bridge start: %w", err)
		}
	}
	us.lastActiveAt = time.Now()
	return sessionLease{bridge: us.bridge, gen: us.gen, chunks: us.responseCh}, nil
}

// ensureStarted lazily starts the session without a request, for callers that only need it live (warming GET
// /api/governance, whose state arrives on session/new).
func (us *utilitySession) ensureStarted(ctx context.Context) error {
	_, err := us.acquire(ctx)
	return err
}

// startLocked spawns a fresh subprocess + session. Caller holds us.mu.
func (us *utilitySession) startLocked(ctx context.Context) error {
	bridge := us.bridgeFactory()
	model := cheapestModel(ctx, us.models())

	// Forward must drain NotifCh before Start: the handshake sends notifications and host requests there. Locals, so a
	// failed Start leaves no state.
	responseCh := make(chan utilityChunkPayload, 64)
	forwardDone := make(chan struct{})
	// Bumped before the goroutine: positions compare only within one attachment, and the lease carries the same
	// number. A failed Start is harmless: started stays false and resetIf returns early.
	us.gen++
	go us.forward(bridge, us.gen, bridge.NotifCh(), responseCh, forwardDone)

	// shutdownCtx, not a request ctx: this runs under us.mu, so a session/new that never answers would hold the mutex
	// for good. Start bounds the handshake itself.
	if err := bridge.Start(us.shutdownCtx, &marotte.StartOpts{Lifetime: us.shutdownCtx, Model: model, AgentEngine: resolveAgentEngine(), EnableHooks: us.enableHooks, SecretStorage: us.secrets != nil, Presets: us.sessionPresets(ctx), IgnoreFiles: us.sessionIgnoreFiles, ContentCollection: us.hooks.contentCollection, DisableTelemetry: us.hooks.telemetryOff != nil && us.hooks.telemetryOff()}); err != nil {
		return err
	}
	us.liveIgnore = startedLive(bridge).ignoreFiles
	us.bridge = bridge
	us.started = true
	us.lastActiveAt = time.Now()
	us.responseCh = responseCh
	us.forwardDone = forwardDone

	// Logged because this session appears in session/list owned by no chat.
	slog.Info("utility bridge started", "model", model, "session_id", string(bridge.SessionID()))
	return nil
}

// stopLocked stops the subprocess and waits for the forward goroutine to exit, so a recycle leaks nothing.
// forwardChunk never blocks, so a full responseCh cannot wedge it. Caller holds us.mu.
func (us *utilitySession) stopLocked() {
	us.bridge.Stop()
	if us.forwardDone != nil {
		<-us.forwardDone
	}
	us.started = false
	us.responseCh = nil
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
		us.responseCh = nil
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

// shuttingDown reports whether the runtime's lifecycle context is cancelled; the drain skips a reset that would
// race Stop.
func (us *utilitySession) shuttingDown() bool {
	return us.shutdownCtx.Err() != nil
}

// utilitySessionParams builds an ACP parameter map with the session id injected. Not command.SessionParams: this
// bridge carries no prompt slot.
func utilitySessionParams(bridge acpSession, extra map[string]any) map[string]any {
	m := map[string]any{marotte.KeySessionID: bridge.SessionID()}
	maps.Copy(m, extra)
	return m
}

// foreignUpdateHook is utilitySessionHooks.onForeignUpdate as forwardChunk takes it; a nil hook is valid wiring.
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

// utilityUpdateBase decodes the sessionUpdate kind from the update object; read off params it is "" for every frame.
type utilityUpdateBase struct {
	Kind marotte.ACPUpdateKind `json:"sessionUpdate"`
}

// utilityChunkPayload is the text content of an agent_message_chunk notification.
type utilityChunkPayload struct {
	Content struct {
		Text string `json:"text"`
	} `json:"content"`
}

// forward drains NotifCh, forwarding agent chunk text to responseCh. Peer requests go to answerHostRequest (session/
// new stalls without the shell type); everything else but hooks and policy is dropped so readLoop never blocks.
// bridge is explicit so a recycle cannot redirect answers.
func (us *utilitySession) forward(bridge acpSessionResponder, gen uint64, notifCh <-chan marotte.Notification, responseCh chan<- utilityChunkPayload, done chan<- struct{}) {
	defer close(done)
	defer close(responseCh)
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
			forwardChunk(msg, string(bridge.SessionID()), responseCh, us.hooks.onForeignUpdate)
		}
		// After handling, so the reported position is folded, not merely received.
		us.noteFrameDrained(drainPoint{gen: gen, seq: n.Seq}, false)
	}
	// Closed and drained: this call seals a replay whose trailing frames never came.
	us.noteFrameDrained(drainPoint{gen: gen}, true)
}

// dispatchNotification hands msg to its hook and reports whether it was one; an unwired hook drops it.
func (us *utilitySession) dispatchNotification(msg *marotte.RPCResponse) bool {
	switch msg.Method {
	case methodKiroHooksDidChange:
		if us.hooks.onHooksChanged != nil {
			us.hooks.onHooksChanged()
		}
	case methodV3Powers:
		if us.hooks.onPowersChanged != nil {
			us.hooks.onPowersChanged(msg.Params)
		}
	case methodKiroWorkflowRecipesChanged:
		if us.hooks.onRecipesChanged != nil {
			us.hooks.onRecipesChanged()
		}
	case methodV3Governance:
		if us.hooks.onGovernanceState != nil {
			us.hooks.onGovernanceState(msg.Params)
		}
	case methodV3PolicyChanged, methodV3PolicyError:
		if us.hooks.onPolicyNotification != nil {
			us.hooks.onPolicyNotification(msg)
		}
	case methodV3SteeringDocs:
		if us.hooks.onSteeringDocs != nil {
			us.hooks.onSteeringDocs(msg.Params)
		}
	case methodV3SystemNotify:
		if us.hooks.onSystemNotify != nil {
			us.hooks.onSystemNotify(msg)
		}
	default:
		return false
	}
	return true
}

// takeSlashUpdate hands this session's available_commands_update to the slash hook, reporting whether it did.
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

// noteFrameDrained reports the folded position to the runtime, tolerating an unwired hook.
func (us *utilitySession) noteFrameDrained(at drainPoint, force bool) {
	if us.hooks.onFrameDrained != nil {
		us.hooks.onFrameDrained(at, force)
	}
}

// sessionPresets resolves the session's policy presets, tolerating an unwired hook. Nil sends no key (Custom).
func (us *utilitySession) sessionPresets(ctx context.Context) []string {
	if us.hooks.presets == nil {
		return nil
	}
	return us.hooks.presets(ctx)
}

// sessionIgnoreFiles resolves the session's ignore-file list, tolerating an unwired hook. Nil or empty withholds
// the notification, which would otherwise clear the list.
func (us *utilitySession) sessionIgnoreFiles(ctx context.Context) []string {
	if us.hooks.ignoreFiles == nil {
		return nil
	}
	return us.hooks.ignoreFiles(ctx)
}

// forwardChunk forwards an agent_message_chunk's text to responseCh and ignores the rest. Two decodes: the envelope
// names the session, the inner frame carries kind and content.
func forwardChunk(msg *marotte.RPCResponse, ownSession string, responseCh chan<- utilityChunkPayload, onForeign foreignUpdateHook) {
	if msg.Method != marotte.MethodSessionUpdate || msg.Params == nil {
		return
	}
	var env translate.ACPSessionUpdateEnvelope
	if json.Unmarshal(msg.Params, &env) != nil || env.Update == nil {
		return
	}
	kind, kindOK := updateKind(env.Update)
	// KAS can hydrate a chat's session into this process, and its text belongs to that transcript. An empty id is the
	// window before session/new, when no prompt drains.
	if ownSession != "" && env.SessionID != ownSession {
		// A frame someone is reading is offered before the Warn. An undecodable update goes to nobody: consuming it would
		// hide the shape change.
		if kindOK && onForeign != nil && onForeign(env.SessionID, kind, env.Update) {
			return
		}
		slog.Warn("utility bridge: dropping a frame for a foreign session",
			"frame_session", env.SessionID, "utility_session", ownSession)
		return
	}
	if !kindOK || kind != marotte.ACPUpdateAgentChunk {
		return
	}
	var chunk utilityChunkPayload
	if json.Unmarshal(env.Update, &chunk) == nil {
		// Non-blocking: a full responseCh is a turn nobody reads, and a blocked forward never sees notifCh close, so
		// stopLocked's <-forwardDone would hang under us.mu.
		select {
		case responseCh <- chunk:
		default:
		}
	}
}

// answerHostRequest answers the utility session's host requests. shell_type is answered for KAS's step reloads.
// executeHook is not answered (it runs a shell command a hook file names) and gets -32601. Tool requests are refused
// rather than left pending, which would wedge the turn.
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
		slog.Warn("utility bridge: denying tool permission request (text-only session)")
		_ = bridge.Respond(ctx, *msg.ID, marotte.PermissionOutcomeCancelled(), nil)
	case msg.Method == marotte.MethodFSRead || msg.Method == marotte.MethodFSWrite ||
		strings.HasPrefix(msg.Method, methodTermPrefix):
		slog.Warn("utility bridge: refusing tool request (text-only session)", "method", msg.Method)
		// -32601: a capability deliberately not offered, not a fault.
		_ = bridge.Respond(ctx, *msg.ID, nil, &marotte.RPCError{
			Code:    marotte.RPCCodeMethodNotFound,
			Message: "utility session is text-generation only; tools are unavailable",
		})
	default:
		// Answered rather than left pending, which can wedge the turn.
		slog.Warn("utility bridge: unexpected peer request, refusing", "method", msg.Method, "id", *msg.ID)
		// -32601: a deliberate refusal, where -32603 would blame the wrong side.
		_ = bridge.Respond(ctx, *msg.ID, nil, &marotte.RPCError{
			Code:    marotte.RPCCodeMethodNotFound,
			Message: "unsupported on the utility session: " + msg.Method,
		})
	}
}
