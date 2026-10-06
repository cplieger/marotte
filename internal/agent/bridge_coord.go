package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/push"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// BridgeCoordinator owns bridge lifecycle, notification forwarding, model
// switching, and turn finalization.
type BridgeCoordinator struct {
	bridge    *bridges
	chatStore bridgeChatRecords
	// Workspace mode + model vocabulary, rather than a copy on every chat record.
	catalog *Catalog
	// Per-chat turn lifecycle; the exclusion every terminal step claims through.
	// See turn.go.
	turns          *turnRegistry
	broadcast      func(ctx context.Context, e marotte.ServerEvent)
	translateEvent func(chatID marotte.ChatID, msg *marotte.RPCResponse)
	// Optional; nil means no notification rather than a refusal to run.
	push        pushNotifier `wiring:"optional"`
	mcpRegistry *mcpRegistry
	lifecycle   *lifetime
	// installed later by SetPreBridgeSpawn from the composition root.
	preBridgeSpawn func(context.Context) `wiring:"optional"`
	// ensureIdentity confirms the account before a bridge attaches to it, installed
	// from the composition root once the auth registrar exists. Nil makes direct
	// package tests explicitly inert.
	ensureIdentity func(context.Context) `wiring:"optional"`
	// retireUtility resets the utility session at the same identity boundary as
	// chat bridges.
	retireUtility func()
	// replayProjection is the session/load replay-projection lifecycle. Nil in
	// tests that do not exercise a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the spawn
	// path: the runtime heals the runs that chat's dying process paused.
	onSessionRehydrated func(marotte.ChatID)
	// dischargeWaiting drops a chat's retained waiting_on_user claim when the agent
	// that raised it dies. Nil in tests.
	dischargeWaiting func(context.Context, marotte.ChatID)
	// onTurnClosed fires from the WINNING closer once a turn has finalized; the
	// agent-terminal registry evicts that turn's retired output. A closure rather
	// than the collaborator, which is built after this literal. Nil in tests.
	onTurnClosed func(marotte.ChatID, string)
	// applyPendingModel is the header's pending_model idle arm, dispatched by every
	// closer on its own goroutine; it re-reads the idle predicate itself. Nil in tests.
	applyPendingModel func(context.Context, marotte.ChatID) `wiring:"optional"`
	// runs is the run registry, for the death closer's run arm: every open step turn
	// of every run this chat's bridge hosted closes with the chat's turns.
	runs *runLog `wiring:"optional"`
	// takeAgentSteers drains the agent rows a dead bridge leaves unread: one id per
	// entry, because the record of that loss is an empty-text steer entry. A user
	// row is the steer record's, resent by its turn end. Nil in tests.
	takeAgentSteers func(marotte.ChatID) []string `wiring:"optional"`
	// steerTurnEnded, steerTurnStarted, steerTurnBound and steerTurnRevised are the
	// steer record's turn hooks; steerBridgeGone tells it a buffer died with no turn
	// to collect its rows. Nil in tests. steerTurnStarted runs under the chat's
	// lifecycle lock, so it never waits on the record's publication queue.
	steerTurnEnded   func(marotte.ChatID, command.SteerTurnEnd) `wiring:"optional"`
	steerTurnStarted func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnBound   func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnRevised func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerBridgeGone  func(marotte.ChatID)                       `wiring:"optional"`
	// secretStorage reports whether the runtime holds a credential store, read at
	// SPAWN time: a bool captured here runs before NewHub opens the store, so it
	// would be false for every bridge this process ever starts.
	secretStorage func() bool `wiring:"optional"`
	// chatHasLiveRun reports whether a run this chat launched is still on the wire,
	// so pushTurnOutcome does not claim the work is over while that run carries on.
	// A closure rather than a *Runs field: this type holds no run surface, the
	// pattern onSessionRehydrated already uses. Nil means "nothing outstanding".
	chatHasLiveRun func(marotte.ChatID) bool `wiring:"optional"`
	// unknownStops records the stop reasons already warned about, so an unmapped
	// wire value produces one line rather than one per turn.
	unknownStops sync.Map
	// agentEngine is the kiro-cli agent engine, hard-pinned to v3 by
	// resolveAgentEngine.
	agentEngine string
	// acpArgs are the filtered operator launch flags (MAROTTE_KIRO_ACP_ARGS), set on
	// CHAT spawns only: an `--effort max` on the utility bridge would spend real
	// credits on a two-word title.
	acpArgs []string `wiring:"optional"`
	// noSubscribers latches that the no-subscriber drop has been reported, so the
	// line is one per episode rather than one per notification. See
	// reportNoSubscribers.
	noSubscribers atomic.Bool
}

// newBridgeCoordinator constructs a BridgeCoordinator from the Runtime's fields,
// once, from NewHub after all options are applied.
func newBridgeCoordinator(h *Runtime) *BridgeCoordinator {
	return &BridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		catalog:        h.catalog,
		turns:          newTurnRegistry(),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		push:           h.push,
		mcpRegistry:    h.mcpRegistry,
		lifecycle:      h.lifecycle,
		retireUtility:  h.stopUtilityBridge,
		// h implements replayProjector via load_projection.go.
		replayProjection: h.replay,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		chatHasLiveRun: func(chatID marotte.ChatID) bool {
			return chatHoldsLiveRun(h.runs.leaseStore().List(), chatID)
		},
		onSessionRehydrated: func(chatID marotte.ChatID) {
			ctx, cancel := h.lifecycle.derivedContext()
			defer cancel()
			h.runs.resumeInterruptedRuns(ctx, chatID)
		},
		onTurnClosed: func(chatID marotte.ChatID, turnID string) {
			h.agentTerms.CloseTurn(chatID, turnID)
			// Its own goroutine: a bridge Call has no client-side deadline and a
			// closer must not wait on one.
			go func() {
				ctx, cancel := h.lifecycle.derivedContext()
				defer cancel()
				h.runs.clearStaleNotices(ctx, chatID)
			}()
		},
		dischargeWaiting:  h.DischargeWaiting,
		applyPendingModel: h.applyPendingModel,
		runs:              h.runs.log,
		takeAgentSteers:   h.bus.steers.TakeAgentRows,
		steerTurnEnded:    h.bus.steers.TurnEnded,
		steerTurnStarted:  h.bus.steers.TurnStarted,
		steerTurnBound:    h.bus.steers.TurnBound,
		steerTurnRevised:  h.bus.steers.TurnRevised,
		steerBridgeGone:   h.bus.steers.BridgeGone,
	}
}

// hasSecretStorage reports whether this process holds a credential store, and
// therefore whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe: no
// resolver declares the capability off.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the runtime's shutdown context, which bounds a spawned
// kiro-cli subprocess. Never the caller's ctx: CmdPrompt's is per-turn and cancels
// on return, so a bridge must outlive the turn that created it.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns the kiro-cli agent engine, hard-pinned to v3 (KAS). A
// stray KIRO_AGENT_ENGINE=v1/v2 is ignored: marotte cannot talk to a legacy engine
// at all, so honouring it stalls session/new and fails every turn.
func resolveAgentEngine() string {
	return marotte.AgentEngineV3
}

// OpenBridge returns an existing bridge for chatID, or creates one. Concurrent
// callers for the same chatID coalesce via singleflight, keyed by bridgeSpawnKey.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within agent use the methods on it. Exporting would leak ACP wiring outside the runtime package.
func (bc *BridgeCoordinator) OpenBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	// Before the fast path: an account switch has to retire the existing bridge
	// rather than be noticed after this call handed it back.
	if bc.ensureIdentity != nil {
		bc.ensureIdentity(ctx)
	}
	if sb := bc.bridge.mgr.get(chatID); sb != nil {
		if reopen, stopped := bc.bridge.mgr.closeIfRetired(chatID, sb); reopen {
			if stopped {
				bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
			}
			return bc.OpenBridge(ctx, chatID, modelOverride)
		}
		bc.repairEffort(ctx, chatID, sb)
		return sb, nil
	}

	// The model override is in the key, so callers with different model parameters
	// never coalesce onto the wrong bridge.
	sfKey := bridgeSpawnKey(chatID, modelOverride)
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	// A retire that landed DURING the spawn: the fresh bridge is already stale, so
	// it is closed and reopened rather than handed back on the previous account.
	if reopen, stopped := bc.bridge.mgr.closeIfRetired(chatID, b); reopen {
		if stopped {
			bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
		}
		return bc.OpenBridge(ctx, chatID, modelOverride)
	}
	return b, nil
}

// bridgeSpawnKey composes the bridge-spawn singleflight key over (chatID,
// modelOverride). keyenc keeps it injective even if either alphabet widens: a
// collision would hand a coalesced caller another chat's bridge.
func bridgeSpawnKey(chatID marotte.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates (or returns the just-created) bridge for chatID, inside the
// singleflight. On any start failure it rolls the half-registered bridge back out
// of the map and returns the error.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	// Double-check after winning the singleflight race.
	sb, existed := bc.bridge.mgr.orInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		// Ends a forward loop already attached to it, whose stream a Start that
		// failed before spawning never closes.
		sb.bridge.Stop()
		sb.setIdle()
		return err
	}

	rec, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}
	// A pick parked while the chat had no bridge lands here rather than at a store-open
	// scan: the log opens lazily, and this is the last header read before a session
	// takes its model.
	if rec.PendingModel != "" {
		bc.persistModelPick(ctx, chatID, rec.PendingModel)
		if rec, exists = bc.chatStore.Get(ctx, chatID); !exists {
			return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
		}
	}

	model := rec.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}
	// Withhold a model the account cannot run, SILENTLY: this is an inherited value
	// rather than a pick the user just made, and kiro-cli would reject it mid-prompt
	// on every later turn. An explicit pick is refused loudly, in cmdSwitchModel. An
	// empty advertised set means unknowable and allows.
	if !marotte.ModelServed(model, rec.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		model = ""
	}

	// No mcpServers parameter: KAS reads its own hot-reloading config file, which
	// marotte renders (internal/mcp/kasfile.go), and an inline copy would WIN over
	// the file and make every config edit look like a no-op.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	// A model OVERRIDE differing from the record is the switch-by-restart path: the
	// chat's stored tier was chosen under the model being switched AWAY from, so
	// resolve effort against the target instead.
	effort := bc.effortFor(ctx, rec)
	if model != "" && model != rec.Model {
		effort = bc.EffortForSwitch(ctx, model)
	}

	if rec.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, rec.ACPSessionID, model, effort, rec.SupervisedMode) {
			return sb, nil
		}
	}

	// EnableHooks:true opts chat bridges into KAS's v2 hook engine, so workspace
	// hooks autofire without marotte serving executeHook.
	//
	// Forward MUST drain NotifCh before Start: the relay owns authentication now,
	// but _kiro/terminal/shell_type is still a server->client REQUEST on the
	// session-creation path, so attaching Forward after Start deadlocks every
	// fresh session.
	bc.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), Model: model, Mode: rec.CurrentModeID, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: rec.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)
	// The session door's half of the supervised fail-open: the assert is best-effort
	// inside Start, so a refusal used to open the chat unsupervised with the record and
	// every client's checkbox still saying supervised.
	bc.reportSupervisedNotApplied(ctx, chatID, rec.SupervisedMode, sb.bridge.SupervisedApplied())
	sb.setIdle()

	return sb, nil
}

// reportSupervisedNotApplied tells the user when the session refused `autopilot: off`,
// so the chat runs unsupervised. A no-op unless the chat asked for supervised mode AND
// the session did not take it: the bridge's flag alone cannot tell, since false also
// means nobody asked. Unlike reportModeNotApplied the record KEEPS the request
// (supervised is the safer intent), so the next spawn re-asserts it and this is a
// report rather than the only chance to act.
func (bc *BridgeCoordinator) reportSupervisedNotApplied(ctx context.Context, chatID marotte.ChatID, requested, applied bool) {
	if !requested || applied {
		return
	}
	slog.Error("supervised mode not applied at the session door; this chat will NOT ask before writing",
		"chat_id", chatID)
	bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeSupervisedNotApplied,
		Message: "This chat asked to review writes before they land, but the session refused. " +
			"It is running unsupervised. Toggle supervised mode again to retry.",
	}))
}

// tryLoadSession attempts session/load against the stored ACP session id, carrying
// the chat's supervised gate so the load can re-assert it (why a resume needs that
// at all: bridge.loadSession).
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge,
	acpSessionID, model, effort string, supervised bool,
) bool {
	// Forward attaches BEFORE Start, as on the session/new path: session/load also
	// asks for the shell type before it returns. On load failure the old bridge is
	// swapped out of sb before it is stopped, so this goroutine's identity-compared
	// exit cleanup cannot evict the replacement. Open the projection first: KAS
	// starts replaying inside Start below.
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, acpSessionID)
	}
	// The attachment is taken HERE, not inside the goroutine: the load's read-loop
	// position below is only comparable within one attachment. See replay_drain.go.
	gen := bc.turns.attachForward(chatID)
	// Captured before the goroutine: the failure branch below swaps sb.bridge.
	loading := sb.bridge
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, loading, gen) })
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), SessionID: acpSessionID, Model: model, Effort: effort, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: supervised, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryEnabled(ctx, bc.lifecycle.configDir)}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		// A failed load has no transcript to adopt, so a partial replay must not
		// survive into the fresh session.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		old := sb.bridge
		sb.bridge = bc.bridge.mgr.factory()
		old.Stop()
		if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
			if !ex {
				return false
			}
			// Detach but KEEP the session in the chain: its directory holds that
			// period's transcript, and blanking the id makes the reaper sweep it
			// as an orphan.
			c.RecordSession("")
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false
	}
	// Record the load's read-loop position, the bound replay completion is measured
	// against, WITH one settle attempt: a replay Forward has already drained has no
	// later frame coming to notice it. See MarkReplayLoadedAt.
	if bc.replayProjection != nil {
		bc.replayProjection.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: sb.bridge.SessionLoadSeq()})
	}
	title := sb.bridge.SessionTitle()
	bc.catalog.SetModes(sb.bridge.Modes())
	bc.catalog.SetModels(sb.bridge.Models())
	if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		applyLoadedSessionFacts(c, sb.bridge, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	sb.setIdle()
	// Heal the chat's restart-paused runs off the spawn path, so the user's prompt
	// never waits behind a run-list round trip, and AFTER the state flip, so the
	// resume's own bridge Call finds an idle bridge.
	if bc.onSessionRehydrated != nil {
		go bc.onSessionRehydrated(chatID)
	}
	return true
}

// adoptKASTitle names a chat from KAS's own session title, only while the chat has
// no name of its own and the title passes the focus channel's door treatment. Naming
// precedence is focus_update title > local first-prompt label > this. The WHOLE
// treatment runs here, not just the shape rules: a stored title was bounded and
// sanitized by nothing, and a pre-gate or IDE-renamed session re-offers it on every
// resume. Every refusal is Warn (the focus door drops the routine one to Debug):
// this rung is reached only while a chat is still default-named, so it has no volume.
func adoptKASTitle(c *marotte.Chat, stored string) {
	title := translate.SanitizeTitle(stored)
	if title == "" || c.Name != marotte.DefaultChatName {
		return
	}
	if reason := translate.TitleRefusal(title); reason != "" {
		slog.Warn("stored session title refused", "title", title, "reason", reason)
		return
	}
	c.Name = title
}

// applyLoadedSessionFacts copies what a RESUMED session reported onto the chat
// record, writing each field only when the load result carried it: the bridge is
// freshly constructed, so an omitted field reads as the zero value, and
// `session/load` omits the model catalog routinely. Modes have no repair channel, so
// an emptied mode list stays empty for the rest of the session.
func applyLoadedSessionFacts(c *marotte.Chat, facts acpSessionFacts, title string) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	// Keeps its own previous set on an absent catalog: ApplyServedModels answers
	// false for an empty one, so a load that omitted it refuses nothing later.
	marotte.ApplyServedModels(c, facts.Catalog())
	summarization, truncation := facts.ContextThresholds()
	if summarization > 0 {
		c.Usage.SummarizationThresholdPct = summarization
	}
	if truncation > 0 {
		c.Usage.TruncationThresholdPct = truncation
	}
	adoptKASTitle(c, title)
}

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID marotte.ChatID, bridge acpSessionFacts) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	catalog := bridge.Catalog()
	title := bridge.SessionTitle()
	// The vocabulary this session advertised is a workspace fact, so it goes to the
	// one holder. Outside the Mutate: holding the chat lock across it would order
	// two unrelated locks for nothing.
	bc.catalog.SetModes(bridge.Modes())
	bc.catalog.SetModels(bridge.Models())
	// requestedMode is read before the line below overwrites it with what landed.
	var requestedMode string
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		requestedMode = c.CurrentModeID
		c.RecordSession(string(newSessionID))
		if newModelID != "" {
			c.Model = string(newModelID)
		}
		c.CurrentModeID = currentMode
		marotte.ApplyServedModels(c, catalog)
		adoptKASTitle(c, title)
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
	bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
}

// reportModeNotApplied tells the user when the session did not get the mode the chat
// asked for. A banner rather than an automatic retry: the record keeps the ACTUAL
// mode, so the request is no longer stored and the next spawn's requested id equals
// the current one, which applyInitialMode's own guard reads as nothing to do.
func (bc *BridgeCoordinator) reportModeNotApplied(ctx context.Context, chatID marotte.ChatID, requested, actual string) {
	if requested == "" || requested == actual {
		return
	}
	slog.Error("session mode not applied; the chat's mode was reset to the session's",
		"chat_id", chatID, "requested", requested, "actual", actual)
	bc.broadcast(ctx, marotte.NewEvent(marotte.EventError, chatID, marotte.ErrorPayload{
		Code: marotte.ErrCodeModeNotApplied,
		Message: "Could not start this chat in \"" + requested + "\" mode; it is running as \"" +
			actual + "\". Pick the mode again to retry.",
	}))
}

// Bridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see OpenBridge above.
func (bc *BridgeCoordinator) Bridge(chatID marotte.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// HasLiveBridge reports whether a chat currently has a bridge. Retention's exemption
// reads this: a chat with a live bridge is never purged, however old
// (archive.WithLiveChats).
func (rt *Runtime) HasLiveBridge(chatID marotte.ChatID) bool {
	return rt.bridge.mgr.get(chatID) != nil
}

// TurnLive reports whether the chat has a turn the reader must treat as running: its
// own turn, an admitted prompt awaiting its bracket, or a held admission slot.
// Composition injects it into the chat store (chat.WithLiveTurn) so the GET states
// `live` rather than inferring it from the log.
func (rt *Runtime) TurnLive(chatID marotte.ChatID) bool {
	return rt.coord.turns.live(chatID)
}

// OpenTurns returns every open turn of the chat with the entries still coalescing
// in its lanes, for the GET's open_entries and its live_turn stamps
// (chat.WithOpenTurns). The registry lookup runs under the lifecycle mutex and
// each accumulator read under its own, so the folder can be extending a lane
// while this copies it out.
func (rt *Runtime) OpenTurns(chatID marotte.ChatID) []chat.OpenTurnTail {
	turns := rt.coord.turns.openTurnIDs(chatID)
	out := make([]chat.OpenTurnTail, 0, len(turns))
	for _, t := range turns {
		out = append(out, chat.OpenTurnTail{ID: t.ID, Entries: t.Log.OpenEntries()})
	}
	return out
}

// CloseBridge closes every turn the chat's bridge hosted with outcome, then stops
// the bridge and removes it from the map. The closer runs FIRST, while the record
// still says which turns are open; a deliberate stop that skipped it would leave
// them open in the log for the process's life.
func (bc *BridgeCoordinator) CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	bc.closeTurnOnBridgeDeath(ctx, chatID, outcome)
	bc.bridge.mgr.close(chatID)
}

// RetireBridges closes idle chat bridges, marks busy chat bridges for their next
// open, and resets the utility session. A bridge hosting a live run counts as
// busy (retireChatBridges owns the rule); the ones stopped here close their
// wire_turn_start turn and their hosted run turns through closeTurnsOnRetire.
func (bc *BridgeCoordinator) RetireBridges(reason string) {
	victims, marked := bc.bridge.mgr.retireChatBridges()
	for _, chatID := range victims {
		bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
	}
	bc.retireUtility()
	slog.Info("identity changed; retiring live chat bridges",
		"reason", reason, "closed", len(victims), "marked", marked)
}

// replayProjector is the slice of the Runtime's replay-projection lifecycle the
// coordinator drives.
type replayProjector interface {
	OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string)
	MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint)
	DiscardReplayProjection(marotte.ChatID)
	SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool)
}

// Forward is the ACP notification → domain event translator, run as a
// goroutine per bridge. It takes the chat's forward attachment itself, which is
// every caller that has no local decision to order against that attachment.
func (bc *BridgeCoordinator) Forward(chatID marotte.ChatID, bridge ACPBridge) {
	bc.forwardAt(chatID, bridge, bc.turns.attachForward(chatID))
}

// goForward starts a forward loop ON THE GROUP SHUTDOWN WAITS ON, the one spawn door.
// What the loop still owes after its channel closes is closeTurnOnBridgeDeath, the
// only closer for a turn a process exit left open; untracked, that turn_close and its
// turn_closed landed after `runtime shutdown complete`, racing the exit for the
// append, so the turn read back as unknown. Reachable whenever a spawn is in flight
// as the signal arrives: a bridge registered after mgr.drain() is not in the set
// Shutdown stops, so it dies on the cancelled context. No deadlock: Shutdown stops
// every bridge BEFORE this wait, which closes NotifCh and ends the range.
func (bc *BridgeCoordinator) goForward(chatID marotte.ChatID, bridge ACPBridge) {
	bc.lifecycle.inflight.Go(func() { bc.Forward(chatID, bridge) })
}

// forwardAt is Forward on an attachment the CALLER already took, for the one
// caller that has to name it: tryLoadSession orders the load's own read-loop
// position against this goroutine's, so it cannot let the goroutine take the
// attachment asynchronously and then guess which one the position belongs to.
func (bc *BridgeCoordinator) forwardAt(chatID marotte.ChatID, bridge ACPBridge, gen uint64) {
	exited := bc.turns.exitFor(chatID, gen)
	ch := bridge.NotifCh()
	// The generation keeps a straggler from the previous bridge from advancing a
	// counter that restarted at zero.
	for n := range ch {
		bc.consumeFrame(chatID, gen, n)
		// Settle a session/load replay projection here rather than at Start's
		// return: this goroutine is the one folding the frames, so the position
		// it reports is the only one the completion condition can be measured
		// against. Rationale in agent/replay_drain.go.
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen, seq: n.Seq}, false)
		}
	}
	// The channel closed, so no further frame can advance the position. Seal the
	// settle so a load whose trailing catalog frames never came still completes
	// instead of leaking a projection.
	if bc.replayProjection != nil {
		bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen}, true)
	}
	// No frame can advance the position now, so anything parked on one has to be
	// told. Before the death closer, so a woken settle has already deferred by then.
	bc.turns.sealPosition(chatID, gen)
	exited()

	slog.Info("bridge exited", "chat_id", chatID)

	// Still registered means nobody removed it, so the process died on its own: the
	// third actor closes whatever turn is still open, because no other closer will.
	if bc.bridge.mgr.removeIfBridge(chatID, bridge) {
		// The only site that observes every death; readLoop reaps its own paths only.
		bridge.Stop()
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
		// waiting_on_user claims a person owes the AGENT an answer; the agent is gone.
		if bc.dischargeWaiting != nil {
			bc.dischargeWaiting(bc.lifecycle.shutdownCtx, chatID)
		}
	}

	// Flush staged writes for the chat. A bridge exit leaves the supervised
	// fs-handler goroutine parked on its resume channel and a phantom "awaiting
	// approval" pending op that would replay to reconnecting clients.
	lastBridge := bc.bridge.mgr.count() == 0

	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
	// A run chat has no record and no turn lifecycle beyond the position bookkeeping
	// above, and nothing calls cleanupChatState for one, so it is dropped here.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
}

// consumeFrame translates one frame and then advances the chat's observed
// position, whatever the frame did. DEFERRED, and per FRAME rather than per fold:
// the advance acknowledges work that is done, and many paths through the
// session-update cascade consume a frame without touching a turn.
func (bc *BridgeCoordinator) consumeFrame(chatID marotte.ChatID, gen uint64, n marotte.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, n.Msg)
}

// Effort is a field on the chat record (marotte.Chat.Effort), read at spawn and
// written by CmdSetEffort — deliberately not a global setting keyed by the last
// model, which two chats could not disagree about.

// NotifyPush sends a push notification about one CHAT if the push service is
// configured. The chat-id convenience stays because every caller in this file is
// chat-scoped, so the conversion to a subject belongs at this one boundary;
// NotifyPushSubject below is what a notification with NO chat behind it uses.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind marotte.PushKind, chatID marotte.ChatID) {
	bc.NotifyPushSubject(ctx, body, kind, marotte.ChatSubject(chatID))
}

// NotifyPushSubject sends a push notification about any SUBJECT if the push service
// is configured — a chat, a pull request, a workflow run.
//
// A nil service is silent: composition always builds one, so that branch is a
// direct package test rather than a state an operator can be in.
func (bc *BridgeCoordinator) NotifyPushSubject(
	ctx context.Context, body string, kind marotte.PushKind, subj marotte.PushSubject,
) {
	if bc.push == nil {
		return
	}
	if !bc.push.HasSubscribers() {
		bc.reportNoSubscribers(kind, subj)
		return
	}
	bc.noSubscribers.Store(false)
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, subj)
	})
}

// RetractPush drops any push about subject the service still holds for a later
// delivery. Called where a chat's ask is settled on every other surface, so a held
// nudge about it never lands after the answer. A run ask has no retraction here:
// nothing pushes about one, and a run's subject is what its OUTCOME push carries,
// which the run's own teardown would otherwise retract.
func (bc *BridgeCoordinator) RetractPush(subj marotte.PushSubject) {
	if bc.push == nil {
		return
	}
	bc.push.Retract(subj)
}

// reportNoSubscribers states that a notification went nowhere for want of a
// subscriber, ONCE per episode: a permission ask reaches NotifyPush per tool call, so
// a line per drop would bury the log. A subscriber arriving re-arms it. Without the
// line, a dead push pipeline and a workspace nobody subscribed from log identically.
// Both halves of the subject are logged because either is legitimately empty (a chat
// notification carries no key, a run's no chat).
func (bc *BridgeCoordinator) reportNoSubscribers(kind marotte.PushKind, subj marotte.PushSubject) {
	if !bc.noSubscribers.CompareAndSwap(false, true) {
		return
	}
	slog.Info("no push subscribers; notifications are being dropped until a browser subscribes",
		"chat_id", subj.ChatID, "subject", subj.Key, "kind", string(kind))
}

// SettleTurnOnResponse closes the turn turnID names on the response that settled
// it — once the folder has consumed everything queued behind that response, and
// only if the wire's own turn_end did not get there first.
//
// seq is the read loop position the response arrived at. Zero skips the wait,
// which is what the two paths that deliberately reach no bracket want.
func (bc *BridgeCoordinator) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptResponse, Resp: resp, Turn: turnID, Seq: seq})
}

// defaultAgentFinishedBody is the body for a turn whose agent never declared what
// it was doing.
const defaultAgentFinishedBody = "Agent finished"

// agentFinishedBodyFrom picks the body from a chat's self-declared description.
func agentFinishedBodyFrom(description string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return defaultAgentFinishedBody
}

// ApplyModelSwitch swaps the model on the chat's live bridge in place through
// session/set_config_option, then re-applies the level. False when the chat has
// no bridge or the session refused the swap; a switch never touches a turn, so
// there is no restart fallback.
func (bc *BridgeCoordinator) ApplyModelSwitch(ctx context.Context, chatID marotte.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitch(ctx, chatID, sb, model, effort)
}

// applyModelSwitch swaps the model on a bridge the caller ALREADY HOLDS, then
// re-applies the level.
func (bc *BridgeCoordinator) applyModelSwitch(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, model, effort string,
) bool {
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: the session refused the swap",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	// Re-assert the level after the swap, or the swap can take it away: KAS reconciles
	// against the NEW model's tier list (measured on 2.19.1 — a swap to `auto` destroys
	// it). Best-effort, since the swap already landed and is what the user asked for.
	if effort != "" {
		if err := sb.bridge.EnsureEffort(ctx, effort); err != nil {
			slog.Warn("model switch: reasoning effort not re-applied after the swap",
				"chat_id", chatID, "model", model, "effort", effort, "error", err)
		}
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", model)
	return true
}

// repairEffort re-asserts the chat's reasoning-effort level on a bridge that is
// ALREADY OPEN — the one checkpoint that catches a level KAS changed on its own (its
// first-prompt model pin, or a switch made from the Kiro IDE or TUI).
//
// At the prompt rather than reactively: a Call from the Forward goroutine would block
// the drain it waits on. Best-effort and log-only.
func (bc *BridgeCoordinator) repairEffort(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) {
	rec, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	level := bc.effortFor(ctx, rec)
	if level == "" {
		return
	}
	if err := sb.bridge.EnsureEffort(ctx, level); err != nil {
		slog.Warn("reasoning effort not re-applied on the open session",
			"chat_id", chatID, "effort", level, "error", err)
	}
}

// healEffort re-asserts the chat's chosen level when a config_option_update reports
// the session running at a different one. It wraps the config-option handler.
//
// It covers repairEffort's hole: that runs on OpenBridge's ALREADY-OPEN path, so the
// turn that SPAWNS the bridge never takes it — and that is the one moment KAS's
// first-prompt model pin applies another tier. Reactive because the divergence appears
// DURING the turn. LATCHED once per bridge: the repair produces another
// config_option_update, so an unbounded reactive repair is a loop.
func (bc *BridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's OWN frame only: a step's session reports the level IT runs at.
		if !attr.ChatOwned() {
			return
		}
		sb := bc.Bridge(chatID)
		if sb == nil {
			return
		}
		rec, ok := bc.chatStore.Get(ctx, chatID)
		if !ok {
			return
		}
		// The frame IS the session reporting its level, and the bridge forwards this
		// channel unread, so hand the report over before deciding anything: it is what
		// lets EnsureEffort assert here rather than compare equal against the ask.
		running := rec.EffortActive
		sb.bridge.ObserveEffort(running)
		want := bc.effortFor(ctx, rec)
		if want == "" || running == "" || want == running {
			return
		}
		if !sb.claimEffortHeal() {
			slog.Debug("reasoning effort still diverges after one repair; leaving it to the next prompt",
				"chat_id", chatID, "want", want, "running", running)
			return
		}
		slog.Info("re-asserting the chat's reasoning effort: the session reported a different level",
			"chat_id", chatID, "want", want, "running", running)
		// On inflight rather than untracked: Shutdown stops every bridge BEFORE it waits
		// on this group, which is the ordering a blocked bridge Call unblocks through.
		bc.lifecycle.inflight.Go(func() {
			hctx, cancel := bc.lifecycle.derivedContext()
			defer cancel()
			if err := sb.bridge.EnsureEffort(hctx, want); err != nil {
				slog.Warn("reasoning effort not re-asserted after the session reported another level",
					"chat_id", chatID, "want", want, "running", running, "error", err)
			}
		})
	}
}

// effortFor resolves the level a chat's next session starts at: the chat's own
// choice, else the level remembered for its MODEL (settings.KeyLastEffortByModel),
// else that model's default from the workspace catalog. Empty means send nothing.
// A fallback is never written onto the chat record (it would pin an unchosen chat to
// today's value) and every rung is model-scoped, since a tier is a judgement about
// one model. The catalog rung keeps drift repairable: repairEffort and healEffort
// both return on an empty level, so without it an unchosen chat has nothing to be
// corrected against. Validated here because config.json is user-editable.
func (bc *BridgeCoordinator) effortFor(ctx context.Context, rec *marotte.Chat) string {
	if rec.Effort != "" {
		return rec.Effort
	}
	if level := bc.effortSeedFor(ctx, rec.Model); level != "" {
		return level
	}
	return bc.catalog.DefaultEffortFor(rec.Model)
}

// effortSeedFor answers the level remembered for exactly one model: the
// KeyLastEffortByModel entry keyed by `model`, else "". PER MODEL because one
// remembered level for the whole app retracts every other model's the moment a tier
// is picked anywhere. A model with no entry, or an entry the EffortLevel vocabulary
// does not recognise, has no seed. The one seed read, so session start and model
// switch cannot disagree.
func (bc *BridgeCoordinator) effortSeedFor(ctx context.Context, model string) string {
	if model == "" {
		return ""
	}
	var byModel map[string]string
	if !settings.FieldInto(ctx, bc.lifecycle.configDir, settings.KeyLastEffortByModel, &byModel) {
		return ""
	}
	level := byModel[model]
	if !marotte.EffortLevel(level).Valid() {
		return ""
	}
	return level
}

// EffortForSwitch resolves the level a chat runs at AFTER a model switch: the level
// remembered for the TARGET model, else the target's own default from the workspace
// catalog, else "" (KAS reconciles on its own).
//
// Deliberately NOT effortFor: the chat's stored choice was made under the model
// being left, so honouring it here would carry `max` from one model onto the
// next. Explicit, because KAS KEEPS a fitting level.
func (bc *BridgeCoordinator) EffortForSwitch(ctx context.Context, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	return bc.catalog.DefaultEffortFor(model)
}

// PersistModelSwitch records a landed switch: the model_switched entry into the
// chat's open turn (sealing every lane first) or after the newest turn's close,
// then the header takes the pick, drops the tier chosen under the old model and
// resets its usage counters. On a detached context: a switch that landed on the
// session must reach the record even when the caller has gone.
// It takes the payload rather than its fields: From, To and Effort are three
// adjacent strings, so a transposed pair would record a model id as a reasoning
// tier and compile clean.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModelSwitched, contextSize int) {
	ctx = durable.Context(ctx)
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.ModelSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error("switch_model: record the switch in the turn", "chat_id", chatID, "error", err)
		}
	} else if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModelSwitched, "", sw); err != nil {
		slog.Error("switch_model: record the switch between turns", "chat_id", chatID, "error", err)
	}
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = sw.To
		c.PendingModel = ""
		// The chosen tier was a judgement about the model being switched AWAY from, so it
		// does not survive: resolution falls to the model-scoped seed, else the new
		// model's own default. Switching back re-applies the seed.
		c.Effort = ""
		c.Usage = marotte.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// PersistEffortChange records a reasoning-tier change the reader asked for, as a
// model_switched entry whose From and To are both the chat's current model — see
// marotte.EntryModelSwitched for why that is the whole discriminator.
//
// CmdSetEffort has already persisted the level, so this writes no header field.
// An empty model writes nothing: a chat with no model has no tier the reader
// could have meant, and the renderer reads an empty To as `Context reset`.
func (bc *BridgeCoordinator) PersistEffortChange(ctx context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel) {
	if model == "" {
		return
	}
	ctx = durable.Context(ctx)
	sw := marotte.EntryModelSwitched{From: model, To: model, Effort: string(level)}
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.ModelSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error("set_effort: record the change in the turn", "chat_id", chatID, "error", err)
		}
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModelSwitched, "", sw); err != nil {
		slog.Error("set_effort: record the change between turns", "chat_id", chatID, "error", err)
	}
}

// PersistModeSwitch records a landed mode switch as a mode_switched entry: into the
// chat's open turn (sealing every lane first) or after the newest turn's close.
// CmdSetMode has already persisted CurrentModeID and broadcast mode_changed, so this
// writes no header field — the entry is the transcript's record of where the mode
// moved.
//
// On a detached context, PersistModelSwitch's reason: a switch that landed on the
// session must reach the record even when the caller has gone.
func (bc *BridgeCoordinator) PersistModeSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModeSwitched) {
	ctx = durable.Context(ctx)
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.ModeSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error("set_mode: record the switch in the turn", "chat_id", chatID, "error", err)
		}
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModeSwitched, "", sw); err != nil {
		slog.Error("set_mode: record the switch between turns", "chat_id", chatID, "error", err)
	}
}

// persistModelPick records a model choice on a chat with no live bridge: model
// takes the pick, pending_model clears, no entry, no usage reset. Clears Effort,
// since a tier picked under the previous model does not carry onto this one.
func (bc *BridgeCoordinator) persistModelPick(ctx context.Context, chatID marotte.ChatID, model string) {
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = model
		c.PendingModel = ""
		c.Effort = ""
		return true
	}); err != nil {
		slog.Error("switch_model: persist pick", "chat_id", chatID, "error", err)
	}
}

// AbandonInFlightTurn closes a turn whose prompt call could not finish it, through
// the turn end rule like every other close. It waits for no read-loop position: the
// failures that reach it settle locally with the bridge still alive, so no bracket
// is coming. `stop` is the caller's CONCLUSION and only `interrupted` or `cancelled`
// is legal; anything else is normalized to `interrupted` with a Warn, because an
// unset stop would grade `unknown` and report a prompt failure as a mere stop.
func (bc *BridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string) {
	if stop != marotte.StopReasonInterrupted && stop != marotte.StopReasonCancelled {
		slog.Warn("a prompt failure named a stop this close cannot conclude, so it concludes interrupted",
			"chat_id", chatID, "turn", turnID, "stop", stop)
		stop = marotte.StopReasonInterrupted
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptFailure, Stop: stop, Reason: reason, Turn: turnID})
}

// FinalizeLocalShellTurn appends a `!cmd` turn's rendered output as its one text
// entry and closes it: the turn is turn_open, text, turn_close. The append goes
// through the turn's own accumulator so the entry is announced like any other.
func (bc *BridgeCoordinator) FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string) {
	if t, ok := bc.turns.turnByID(chatID, turnID); ok {
		sealed, err := t.Log.TextDelta(ctx, "", "", output)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Warn("a shell turn's output was not recorded", "chat_id", chatID, "turn", turnID, "error", err)
		}
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerLocalShell, Turn: turnID})
}

// WireTurnStart is the engine's own turn_start bracket, bound by the three-case
// bracket rule: it binds the pending prompt turn when one is owed a bracket,
// closes a bracketed own turn whose turn_end was lost with closerBracketLost
// first, and opens turn_open{source: wire_turn_start} as own when the chat holds
// nothing for it. The bind is PROVISIONAL: the bracket cannot tell a prompted turn
// from an agent-initiated one, and the first content frame carrying agentInitiated
// revises it (ReviseTurnBinding).
func (bc *BridgeCoordinator) WireTurnStart(ctx context.Context, chatID marotte.ChatID) {
	bound, lost := bc.turns.bindPending(ctx, chatID)
	if !bound && lost != "" {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBracketLost, Turn: lost})
		bound, _ = bc.turns.bindPending(ctx, chatID)
	}
	if bound {
		if t, ok := bc.turns.ownTurn(chatID); ok && bc.steerTurnBound != nil {
			bc.steerTurnBound(chatID, t.ID)
		}
		return
	}
	bc.openWireTurn(ctx, chatID)
}

// WireTurnEnd is the engine's own turn_end bracket, and the closer whose outcome
// is the wire's rather than an inference. A turn_end for a chat with NO own turn
// is a no-op: a cancel-grace expiry that closed its turn locally meets the later
// bracket, and manufacturing a turn out of it would make a phantom. A replayed
// bracket is filtered upstream, so this is the live path only.
func (bc *BridgeCoordinator) WireTurnEnd(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason, details string) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerWireEnd, Stop: stop, Reason: details, Own: true})
}

// TurnFoldTarget returns the accumulator this chat's own frames fold into,
// opening a wire_turn_start turn when none is open: a fold with no open turn is a
// turn marotte did not prompt, and it needs a record like any other. Nil when the
// open was refused, and the caller drops the frame. Never called with a step
// frame; a step's content is the run log's.
func (bc *BridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn {
	if log, ok := bc.turns.foldTarget(chatID); ok {
		return log
	}
	t := bc.openWireTurn(ctx, chatID)
	if t == nil {
		return nil
	}
	return t.Log
}

// OwnTurn returns the chat's own open turn without opening one, for a frame that
// may fold into a turn but must never start one.
func (bc *BridgeCoordinator) OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.foldTarget(chatID)
}

// PromptTurn returns the chat's prompt-class turn awaiting or holding its bracket,
// the one a turn_bind joins.
func (bc *BridgeCoordinator) PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.promptTurn(chatID)
}

// AppendBetweenTurns files a chat's lane-less entry after its newest turn's close.
func (bc *BridgeCoordinator) AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) (*marotte.Entry, error) {
	return bc.chatStore.AppendBetweenTurns(ctx, chatID, e)
}

// ReviseTurnBinding acts on a frame that PROVES the open turn is the agent's own
// rather than the prompt's: `agentInitiated` rides content frames and never the
// bracket, so this is the only discriminator there is. It appends the agent turn's
// turn_open, records it as own through openLocked directly (slotFreeLocked refuses
// while own holds the prompt), and drops the prompt's turn back to pending. Entries
// already sealed into the prompt's turn stay where they are.
func (bc *BridgeCoordinator) ReviseTurnBinding(ctx context.Context, chatID marotte.ChatID) {
	var agent *Turn
	err := bc.turns.withLifecycle(ctx, chatID, func(lc *chatLifecycle) error {
		pre := lc.revisableLocked()
		if pre == nil {
			return nil
		}
		opened, err := bc.chatStore.OpenTurn(ctx, chatID, &chat.TurnSpec{Source: marotte.TurnOpenNameWireTurnStart}, nil)
		if err != nil {
			return err
		}
		model := bc.turnOpenModel(ctx, chatID, marotte.TurnSourceWireTurnStart)
		log := turnlog.Open(opened.ID, bc.chatStore.Sink(chatID))
		if model != "" {
			log.SetModel(model)
		}
		agent = lc.openLocked(chatID, opened, marotte.TurnSourceWireTurnStart, model, log)
		lc.reviseLocked(pre, agent)
		return nil
	})
	if err != nil {
		slog.Warn("an agent-initiated frame could not open the agent's turn", "chat_id", chatID, "error", err)
		return
	}
	if agent == nil {
		return
	}
	if bc.steerTurnRevised != nil {
		bc.steerTurnRevised(chatID, agent.ID)
	}
	bc.announceTurnOpened(ctx, chatID, agent)
	if err := bc.chatStore.WriteCounters(ctx, chatID); err != nil {
		slog.Warn("turn opened but the header's counters did not follow", "chat_id", chatID, "turn", agent.ID, "error", err)
	}
}

// closeTurnOnBridgeDeath closes everything the chat's bridge hosted, in three
// steps: the chat's own and pending turns with outcome, then every open step turn
// of every run whose host is this chat (the run registry's lock is taken after
// the chat's are released, never nested, because no frame folds into both logs),
// then the chat's unread steers, the one divergence a death leaves between the
// record and KAS's log. It runs for an unexpected exit from the Forward goroutine
// and for every deliberate stop through CloseBridge; the second claim on a turn
// the first closed finds it closed and loses.
func (bc *BridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	// Detached once for all three steps: a cancelled request must not leave the
	// hosted runs' turns open or the parked steers unrecorded while the chat's close.
	ctx = durable.Context(ctx)
	stop := marotte.StopReasonInterrupted
	if outcome == marotte.TurnOutcomeCancelled {
		stop = marotte.StopReasonCancelled
	}
	for _, t := range bc.turns.openTurnIDs(chatID) {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBridgeDeath, Stop: stop, Reason: deathInterruptCause, Turn: t.ID})
	}
	bc.closeHostedRuns(ctx, chatID, stop)
	bc.dropUnreadSteers(ctx, chatID)
}

// closeTurnsOnRetire is the death closer's NARROWER form for the two retire stops,
// which run inside a prompt's own OpenBridge call while that prompt's turn is
// already in the registry: it closes own only when its source is wire_turn_start,
// never pending, and every hosted run turn; the steer step is skipped because an
// idle bridge's parked rows belong to the unstarted prompt and drain at its
// StartTurn on the replacement.
func (bc *BridgeCoordinator) closeTurnsOnRetire(ctx context.Context, chatID marotte.ChatID) {
	if t, ok := bc.turns.ownTurn(chatID); ok && t.Source == marotte.TurnSourceWireTurnStart {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: t.ID})
	}
	bc.closeHostedRuns(ctx, chatID, marotte.StopReasonInterrupted)
}

// closeHostedRuns is the death closer's run arm: every open step turn of every run
// the chat's bridge hosted closes with stop, and each run's sealed entries are
// announced under the run's scope.
func (bc *BridgeCoordinator) closeHostedRuns(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	if bc.runs == nil {
		return
	}
	c := bc.concludeStop(chatID, stop, deathInterruptCause)
	byRun, err := bc.runs.CloseHost(ctx, chatID, c)
	for runID, sealed := range byRun {
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), "", runID, sealed)
	}
	if err != nil {
		slog.Error("close the step turns a dead bridge hosted", "chat_id", chatID, "error", err)
	}
}

// dropUnreadSteers is the death closer's third step: an agent row KAS had queued
// that this process holds no text for is recorded as an EMPTY-TEXT steer entry, one
// per id, and the user rows' buffer is reported gone.
//
// That entry IS the reconcile signal: an empty text is "KAS persisted words this
// process never received", which is what the header flag it replaces meant, read
// from the log by the predicate the next session/load asks.
func (bc *BridgeCoordinator) dropUnreadSteers(ctx context.Context, chatID marotte.ChatID) {
	if bc.steerBridgeGone != nil {
		bc.steerBridgeGone(chatID)
	}
	if bc.takeAgentSteers == nil {
		return
	}
	for _, id := range bc.takeAgentSteers(chatID) {
		bc.recordSteer(ctx, chatID, id, &marotte.EntrySteer{
			Origin: marotte.SteerOriginAgent, State: marotte.SteerStateDropped,
			Reason: marotte.SteerReasonRestart,
		})
	}
}

// recordSteer writes a steer entry this server minted: into the chat's open turn,
// sealing every lane first, else after the newest turn's close.
func (bc *BridgeCoordinator) recordSteer(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.Steer(ctx, steerID, steer)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Warn("a steer was not recorded in the turn", "chat_id", chatID, "steer", steerID, "error", err)
		}
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindSteer, steerID, steer); err != nil {
		slog.Warn("a steer was not recorded between turns", "chat_id", chatID, "steer", steerID, "error", err)
	}
}

// InterruptTurn records why kiro-cli abandoned a turn without answering it, and trips
// that turn's prompt call so the ordinary failure path finalizes it. The cause lands
// on the TURN, scoped to that turn id and first-wins.
//
// The bridge is left ALIVE and the session untouched: only the tool call was
// cancelled, so the chat is immediately promptable. No open turn, no bridge, or a
// cause already claimed is not a failure — a user cancel may have ended the turn.
func (bc *BridgeCoordinator) InterruptTurn(chatID marotte.ChatID, reason string) {
	t, open := bc.turns.ownTurn(chatID)
	if !open {
		slog.Debug("interrupt turn: no turn open", "chat_id", chatID, "reason", reason)
		return
	}
	if !bc.turns.interrupt(chatID, t.ID, marotte.InterruptCause(reason)) {
		slog.Debug("interrupt turn: another cause already claimed this turn",
			"chat_id", chatID, "turn", t.ID, "reason", reason)
		return
	}
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		slog.Debug("interrupt turn: no bridge", "chat_id", chatID)
		return
	}
	if !sb.cancelPromptCall() {
		slog.Debug("interrupt turn: no prompt call in flight",
			"chat_id", chatID, "reason", reason)
	}
}

func extractStopReason(resp *marotte.RPCResponse) marotte.StopReason {
	if resp == nil || resp.Result == nil {
		return ""
	}
	var result struct {
		StopReason marotte.StopReason `json:"stopReason"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Debug("prompt response: parse stopReason", "error", err)
		return ""
	}
	return result.StopReason
}

// BridgeRespond answers an ACP request on the chat's bridge, and is a no-op when
// that chat has none: a response to a request whose bridge already went away has
// nowhere to go and is not an error.
func (rt *Runtime) BridgeRespond(ctx context.Context, chatID marotte.ChatID, requestID int64, result any, err error) error {
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// ParentACPSession returns the ACP session id of the running bridge for chatID, or
// "" when no bridge exists. Translator helpers use it to short-circuit
// notifications whose top-level sessionId belongs to a subagent.
func (bc *BridgeCoordinator) ParentACPSession(chatID marotte.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
