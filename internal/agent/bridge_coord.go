package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/turnlog"
)

// bridgeCoordinator owns bridge lifecycle, notification forwarding, model
// switching, and turn finalization.
type bridgeCoordinator struct {
	locks     func() map[string]marotte.GovernanceLock
	bridge    *bridges
	chatStore bridgeChatRecords
	// catalog is the workspace mode and model vocabulary.
	catalog *catalog
	// turns is the per-chat turn lifecycle (turn.go).
	turns            *turnRegistry
	broadcast        func(ctx context.Context, e marotte.ServerEvent)
	translateEvent   func(chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse)
	releaseTerminals func(chatID marotte.ChatID, origin acpResponder)
	// push is optional; nil means no notification.
	push        pushNotifier `wiring:"optional"`
	mcpRegistry *mcpRegistry
	lifecycle   *lifetime
	// preBridgeSpawn is installed by SetPreBridgeSpawn.
	preBridgeSpawn func(context.Context) `wiring:"optional"`
	// chatSteering renders the chat-only steering both chat spawn sites send; never the utility or a run bridge.
	chatSteering func(context.Context) []marotte.ClientSteeringDoc `wiring:"optional"`
	// ensureIdentity confirms the account before a bridge attaches; nil leaves package tests inert.
	ensureIdentity func(context.Context) `wiring:"optional"`
	// reconcileSessions marks every chat for reopen when a setting read only at open moved, so it
	// runs before this open checks the mark. It waits no later than its deadline.
	reconcileSessions func(context.Context, time.Time)
	// afterOpen runs once an open synced its own bridge, whose spawn may be the first chat process a report needs. It
	// waits no later than its deadline.
	afterOpen func(context.Context, marotte.ChatID, time.Time)
	// retireUtility resets the utility session at the chat bridges' identity boundary.
	retireUtility func()
	// replayProjection is the session/load replay lifecycle; nil in tests without a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the spawn path, to heal paused runs.
	onSessionRehydrated func(marotte.ChatID)
	// reapSession removes one KAS session's on-disk state.
	reapSession func(sessionID string)
	// dischargeWaiting drops a retained waiting_on_user claim when its agent dies. Nil in tests.
	dischargeWaiting func(context.Context, marotte.ChatID)
	// endAsks retires the unanswered asks an ended bridge carried: nothing can receive their answers.
	endAsks func(origin acpResponder)
	// onTurnClosed fires from the winning closer so the terminal registry evicts the turn's output. Nil in tests.
	onTurnClosed func(marotte.ChatID, string)
	// applyPendingModel is the pending_model idle arm, dispatched by every closer on its own goroutine. Nil in tests.
	applyPendingModel func(context.Context, marotte.ChatID) `wiring:"optional"`
	// autoCompact is the compaction policy's turn-end and pre-send triggers.
	autoCompact *autoCompactor `wiring:"optional"`
	// resolveEnds and drainAfterClose are first and last in afterTurnClose; the drain is last
	// because a prompt mid-compaction aborts it upstream.
	resolveEnds     func(context.Context, marotte.ChatID, command.TurnFence) command.EndFacts   `wiring:"optional"`
	drainAfterClose func(context.Context, marotte.ChatID, command.CloseFacts, command.EndFacts) `wiring:"optional"`
	// runs lets the death closer close every open step turn of the runs this bridge hosted.
	runs *runLog `wiring:"optional"`
	// Before closeHostedRuns closes the run turns, so the not-read notes land inside them.
	endHostedSteers func(context.Context, marotte.ChatID) `wiring:"optional"`
	// takeAgentSteers drains the agent rows a dead bridge leaves unread. Nil in tests.
	takeAgentSteers func(marotte.ChatID) []string `wiring:"optional"`
	// steerTurnEnded and its siblings are the steer record's turn hooks. Nil in tests.
	// steerTurnStarted runs under the lifecycle lock, so it never waits on the record's queue.
	steerTurnEnded   func(marotte.ChatID, command.SteerTurnEnd) `wiring:"optional"`
	steerTurnStarted func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnBound   func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnRevised func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerBridgeGone  func(marotte.ChatID)                       `wiring:"optional"`
	// secretStorage is read at SPAWN time: a captured bool would predate NewHub opening the store.
	secretStorage func() bool `wiring:"optional"`
	// chatHasLiveRun keeps pushTurnOutcome from claiming the work is over while a launched run continues. Nil means none.
	chatHasLiveRun func(marotte.ChatID) bool `wiring:"optional"`
	// runLabel is the name a run's tab shows, for a notification titled by it. Nil names none.
	runLabel func(workflowID string) string `wiring:"optional"`
	// unknownStops records stop reasons already warned about: one line per unmapped value.
	unknownStops sync.Map
	// agentEngine is hard-pinned to v3 by resolveAgentEngine.
	agentEngine string
	// acpArgs are the operator launch flags, CHAT spawns only.
	acpArgs []string `wiring:"optional"`
	// noSubscribers latches the no-subscriber report once per episode (reportNoSubscribers).
	noSubscribers atomic.Bool
}

// newBridgeCoordinator builds the coordinator once, from NewHub after all options apply.
func newBridgeCoordinator(h *Runtime) *bridgeCoordinator {
	return &bridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		catalog:        h.catalog,
		turns:          newTurnRegistry(),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		// Read per call: agentTerms is built after the coordinator.
		releaseTerminals: func(chatID marotte.ChatID, origin acpResponder) {
			h.agentTerms.releaseForBridge(chatID, origin)
		},
		push:          h.push,
		mcpRegistry:   h.mcpRegistry,
		lifecycle:     h.lifecycle,
		retireUtility: h.stopUtilityBridge,
		// h implements replayProjector (load_projection.go).
		replayProjection: h.replay,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		locks:            h.config.GovernanceLocks,
		chatHasLiveRun: func(chatID marotte.ChatID) bool {
			return chatHoldsLiveRun(h.runs.leaseStore().List(), chatID)
		},
		runLabel: func(workflowID string) string { return h.runs.tabLabel(workflowID) },
		onSessionRehydrated: func(chatID marotte.ChatID) {
			ctx, cancel := h.lifecycle.derivedContext()
			defer cancel()
			h.runs.resumeInterruptedRuns(ctx, chatID)
		},
		reapSession: func(sessionID string) { h.reapSessions([]string{sessionID}) },
		onTurnClosed: func(chatID marotte.ChatID, turnID string) {
			h.agentTerms.closeTurn(chatID, turnID)
			// Its own goroutine: a bridge Call has no deadline and a closer must not wait on one.
			// On inflight, so Shutdown waits for it.
			h.lifecycle.inflight.Go(func() {
				ctx, cancel := h.lifecycle.derivedContext()
				defer cancel()
				h.runs.clearStaleNotices(ctx, chatID)
			})
		},
		dischargeWaiting:  h.DischargeWaiting,
		endAsks:           h.bus.endAsksOf,
		applyPendingModel: h.applyPendingModel,
		resolveEnds: func(ctx context.Context, chatID marotte.ChatID, f command.TurnFence) command.EndFacts {
			return h.dispatcher.ResolveAfterClose(ctx, chatID, f)
		},
		drainAfterClose: func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
			h.dispatcher.DrainAfterClose(ctx, chatID, cl, ends)
		},
		runs:             h.runs.log,
		endHostedSteers:  h.runs.endHostedSteers,
		takeAgentSteers:  h.bus.steers.takeAgentRows,
		steerTurnEnded:   h.bus.steers.turnEnded,
		steerTurnStarted: h.bus.steers.turnStarted,
		steerTurnBound:   h.bus.steers.turnBound,
		steerTurnRevised: h.bus.steers.turnRevised,
		steerBridgeGone:  h.bus.steers.bridgeGone,
	}
}

// hasSecretStorage reports whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe.
func (bc *bridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the runtime's shutdown context, which bounds a kiro-cli
// subprocess; never the per-turn caller ctx.
func (bc *bridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns v3 (KAS), ignoring KIRO_AGENT_ENGINE: marotte cannot talk to a legacy engine.
func resolveAgentEngine() string {
	return marotte.AgentEngineV3
}

// openBridge returns chatID's bridge once its generation is ready (sessionSettle), starting one when
// none is registered. The spawn runs under the process lifetime, since a cancelled one would detach
// the chat's session; ctx bounds only this caller's wait. A failed generation answers its failure and
// is already gone, so the next call starts a fresh one. Before it returns it sends the bridge each
// live setting it has not confirmed (sharedBridge.syncLive) and syncs every other live process in the
// background; those waits end at one deadline ignorePushTimeout after the call.
func (bc *bridgeCoordinator) openBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	syncBy := time.Now().Add(ignorePushTimeout)
	sb, err := bc.openBridgeBy(ctx, chatID, modelOverride, syncBy)
	if err != nil {
		return nil, err
	}
	sb.syncLive(ctx, chatID, bc.readLiveSettings)
	bc.afterOpen(ctx, chatID, syncBy)
	return sb, nil
}

func (bc *bridgeCoordinator) readLiveSettings(ctx context.Context) liveSettings {
	return readLiveSettings(ctx, bc.lifecycle.configDir, readLiveFields)
}

func (bc *bridgeCoordinator) openBridgeBy(ctx context.Context, chatID marotte.ChatID, modelOverride string, syncBy time.Time) (*sharedBridge, error) {
	// Before the registry read, so an account switch retires the existing bridge.
	if bc.ensureIdentity != nil {
		bc.ensureIdentity(ctx)
	}
	bc.reconcileSessions(ctx, syncBy)
	for {
		sb, existed := bc.bridge.mgr.orInsert(chatID)
		if !existed {
			bc.lifecycle.inflight.Go(func() { bc.spawnGeneration(bc.processLifetimeCtx(), chatID, sb, modelOverride) })
		}
		if err := sb.settled.wait(ctx); err != nil {
			return nil, err
		}
		reopen, stopped := bc.bridge.mgr.closeIfRetired(chatID, sb)
		if !reopen {
			if existed {
				bc.repairEffort(ctx, chatID, sb)
			}
			return sb, nil
		}
		if stopped {
			bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
		}
	}
}

// LoadSession answers once chatID's bridge generation is ready (openBridge).
func (bc *bridgeCoordinator) LoadSession(ctx context.Context, chatID marotte.ChatID) error {
	_, err := bc.openBridge(ctx, chatID, "")
	return err
}

func (bc *bridgeCoordinator) spawnGeneration(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, modelOverride string) {
	rec, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		bc.failGeneration(chatID, sb, errChatUnreadable)
		return
	}
	// A pick parked with no bridge lands here: this is the last header read before the session takes its model.
	if rec.PendingModel != "" {
		bc.persistModelPick(ctx, chatID, rec.PendingModel)
		if rec, exists = bc.chatStore.Get(ctx, chatID); !exists {
			bc.failGeneration(chatID, sb, errChatUnreadable)
			return
		}
	}

	// No mcpServers parameter: KAS reads the config file marotte renders (internal/mcp/kasfile.go),
	// and an inline copy would win over the file.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	model, effort, thinking, withheld := bc.sessionChoices(ctx, chatID, rec, modelOverride)
	held := bc.holdUnservedModel(ctx, chatID, withheld)

	if rec.ACPSessionID != "" {
		if loaded, err := bc.tryLoadSession(ctx, chatID, sb, rec.ACPSessionID, model, effort, thinking, rec.SupervisedMode, held); loaded {
			bc.settleResumed(ctx, chatID, sb, err)
			return
		}
	}

	// EnableHooks opts chat bridges into KAS's hook engine. Forward must drain NotifCh before
	// Start: the handshake already delivers notifications and requests.
	bc.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), Steering: bc.renderChatSteering(ctx), Model: model, Mode: rec.CurrentModeID, Effort: effort, Thinking: thinking, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: rec.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, TerminalTimeout: func(c context.Context) int { return terminalCommandTimeoutMs(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryPreference(ctx, bc.lifecycle.configDir), DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, bc.lifecycle.configDir)), Features: agentFeatures(ctx, bc.lifecycle.configDir, currentLocks(bc.locks)), ContentCollection: contentCollectionResolver(bc.lifecycle.configDir, bc.locks), DisableTelemetry: bc.lifecycle.telemetryDisabled(bc.locks)}); err != nil {
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		return
	}
	sb.adoptSpawn()
	if err := bc.persistNewSessionMetadata(ctx, chatID, sb.bridge); err != nil {
		sessionID := string(sb.bridge.SessionID())
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		// A session no record holds is in no delete's chain, so this generation reaps it itself.
		bc.reapSession(sessionID)
		return
	}
	bc.commitUnservedModel(ctx, chatID, held, sb.bridge.ModelID())
	// The session door's half of the supervised fail-open (the assert inside Start is best-effort).
	bc.reportSupervisedNotApplied(ctx, chatID, rec.SupervisedMode, sb.bridge.SupervisedApplied())
	sb.setIdle()
	bc.publishGeneration(ctx, chatID, sb)
}

// errChatUnreadable is a spawn that could not read the record it opens; failGeneration decides
// whether that is the chat's absence.
var errChatUnreadable = errors.New("chat record could not be read")

// publishGeneration publishes sb ready under the chat's lock once its record loads, so a delete
// lands either before it (ErrChatGone) or after it (the delete's teardown closes the bridge), and
// reports whether it published ready; otherwise it retired the generation with the failure.
func (bc *bridgeCoordinator) publishGeneration(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) bool {
	var err error
	switch bc.chatStore.PublishIfPresent(ctx, chatID, func() { sb.settled.settle(nil) }) {
	case chat.PresencePresent:
		return true
	case chat.PresenceAbsent:
		err = fmt.Errorf("%w: %s", command.ErrChatGone, chatID)
	default:
		err = errChatUnreadable
	}
	bc.retireGeneration(chatID, sb, err)
	return false
}

// settleResumed settles a generation that loaded its session, err being the replay's merge. Only
// a published one resumes runs: a resume on a failed one would open the next one, which would fail
// and resume again.
func (bc *bridgeCoordinator) settleResumed(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, err error) {
	switch {
	case err != nil:
		bc.failGeneration(chatID, sb, err)
	case bc.publishGeneration(ctx, chatID, sb) && bc.onSessionRehydrated != nil:
		bc.onSessionRehydrated(chatID)
	}
}

// failGeneration publishes a spawn's failure, as ErrChatGone when the record is absent.
func (bc *bridgeCoordinator) failGeneration(chatID marotte.ChatID, sb *sharedBridge, err error) {
	if bc.chatStore.Presence(chatID) == chat.PresenceAbsent {
		err = fmt.Errorf("%w: %s", command.ErrChatGone, chatID)
	}
	bc.retireGeneration(chatID, sb, err)
}

// retireGeneration removes and stops a failed generation before its error publishes, so the next
// open starts fresh; no caller holds it, since openBridge admits none before readiness.
func (bc *bridgeCoordinator) retireGeneration(chatID marotte.ChatID, sb *sharedBridge, err error) {
	if bc.bridge.mgr.removeIfSame(chatID, sb) {
		slog.Info("bridge retired: its session never reached the chat's log", "chat_id", chatID, "error", err)
		sb.current().Stop()
	}
	sb.settled.settle(err)
}

func (bc *bridgeCoordinator) sessionChoices(
	ctx context.Context, chatID marotte.ChatID, rec *marotte.Chat, modelOverride string,
) (model, effort, thinking, withheld string) {
	model = rec.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}
	// Withhold a model the account cannot run: it is inherited, not a fresh pick (refused in
	// cmdSwitchModel). An empty advertised set allows.
	if !marotte.ModelServed(model, rec.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		withheld, model = model, ""
	}

	// A differing override is switch-by-restart: resolve effort against the target model.
	effort = bc.effortFor(ctx, rec)
	if model != "" && model != rec.Model {
		effort = bc.effortForSwitch(ctx, model)
	}
	if target := cmp.Or(model, rec.Model); bc.catalog.thinkingToggleable(target) {
		thinking = rec.Thinking
		if rec.ThinkingIsOff(bc.catalog.thinkingDefaultOff(target)) {
			effort = marotte.CapEffortForThinkingOff(effort, rec.EffortLevels)
		}
	}
	return model, effort, thinking, withheld
}

// unservedSelection is a model the session opens without, with the record's effort for it.
// onRecord is false for a withheld override the record never held; only a held one is written back.
type unservedSelection struct {
	model, effort string
	onRecord      bool
}

// holdUnservedModel takes the model off the record before the spawn so a racing
// config_option_update reads a first pin, not KAS's repin, leaving commitUnservedModel the
// switch's one writer. The effort stays for a rollback or a same-model commit.
func (bc *bridgeCoordinator) holdUnservedModel(ctx context.Context, chatID marotte.ChatID, model string) unservedSelection {
	held := unservedSelection{model: model}
	if model == "" {
		return held
	}
	if _, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.Model != model {
			return false
		}
		held.effort, held.onRecord = c.Effort, true
		c.Model = ""
		return true
	}); err != nil {
		slog.Error("unserved model: take it off the record", "chat_id", chatID, "model", model, "error", err)
	}
	return held
}

// restoreUnservedModel puts a held selection back after a failed spawn, so the next spawn
// withholds and records it.
func (bc *bridgeCoordinator) restoreUnservedModel(ctx context.Context, chatID marotte.ChatID, held unservedSelection) {
	if !held.onRecord {
		return
	}
	if _, err := bc.chatStore.Mutate(durable.Context(ctx), chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.Model != "" {
			return false
		}
		c.Model, c.Effort = held.model, held.effort
		return true
	}); err != nil {
		slog.Error("unserved model: restore the record", "chat_id", chatID, "model", held.model, "error", err)
	}
}

// commitUnservedModel settles a held or load-dropped selection against the running model. The
// effort survives only when the session runs the same model. An unknown running model writes no
// entry: a model_switched with an empty To reads as a context reset.
func (bc *bridgeCoordinator) commitUnservedModel(ctx context.Context, chatID marotte.ChatID, held unservedSelection, running marotte.ModelID) {
	to := string(running)
	if held.model == "" {
		return
	}
	ctx = durable.Context(ctx)
	if to != "" && to != held.model {
		bc.appendModelSwitched(ctx, chatID, "unserved model",
			marotte.EntryModelSwitched{From: held.model, To: to, Reason: marotte.ModelSwitchReasonUnavailable})
	}
	if !held.onRecord {
		return
	}
	effort := ""
	if to == held.model {
		effort = held.effort
	}
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		// Another value is a pick that landed during the spawn.
		if !ex || (c.Model != "" && c.Model != held.model && c.Model != to) {
			return false
		}
		c.Model, c.Effort = to, effort
		return true
	}); err != nil {
		slog.Error("unserved model: persist the running model", "chat_id", chatID, "error", err)
	}
}

// reportSupervisedNotApplied tells the user the session refused `autopilot: off`. The
// record keeps the request, so the next spawn re-asserts it.
func (bc *bridgeCoordinator) reportSupervisedNotApplied(ctx context.Context, chatID marotte.ChatID, requested, applied bool) {
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

func (bc *bridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge,
	acpSessionID, model, effort, thinking string, supervised bool, held unservedSelection,
) (loaded bool, ready error) {
	// Forward attaches before Start. On failure the old bridge is swapped out before it is
	// stopped, so the exit cleanup cannot evict the replacement. Open the projection first:
	// KAS replays inside Start.
	replayed := newSessionSettle()
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, acpSessionID, replayed)
	}
	// The load's read-loop position is only comparable within this attachment (replay_drain.go).
	gen := bc.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), Steering: bc.renderChatSteering(ctx), SessionID: acpSessionID, Model: model, Effort: effort, Thinking: thinking, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: supervised, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, TerminalTimeout: func(c context.Context) int { return terminalCommandTimeoutMs(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryPreference(ctx, bc.lifecycle.configDir), DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, bc.lifecycle.configDir)), Features: agentFeatures(ctx, bc.lifecycle.configDir, currentLocks(bc.locks)), ContentCollection: contentCollectionResolver(bc.lifecycle.configDir, bc.locks), DisableTelemetry: bc.lifecycle.telemetryDisabled(bc.locks)}); err != nil {
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		// A failed load must not keep a partial replay.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		old := sb.swapBridge(bc.bridge.mgr.factory())
		// Off the user's path: Stop waits out kiro-cli's teardown.
		bc.lifecycle.inflight.Go(old.Stop)
		if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
			if !ex {
				return false
			}
			// Detach but keep the session in the chain: blanking the id makes the reaper sweep its transcript.
			c.RecordSession("")
			return true
		}); mErr != nil {
			slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", mErr)
		}
		return false, nil
	}
	sb.adoptSpawn()
	bc.markReplayLoaded(chatID, gen, sb, replayed)
	title := sb.bridge.SessionTitle()
	bc.catalog.setModes(sb.bridge.Modes())
	bc.catalog.SetModels(sb.bridge.Models())
	var renameTo string
	var dropped unservedSelection
	if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		renameTo, dropped = applyLoadedSessionFacts(c, sb.bridge, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	if held.model == "" {
		held = dropped
	}
	bc.commitUnservedModel(ctx, chatID, held, sb.bridge.ModelID())
	reassertUserName(ctx, chatID, sb.bridge, renameTo)
	sb.setIdle()
	// The generation is ready only once its replay merged.
	return true, replayed.wait(bc.processLifetimeCtx())
}

// markReplayLoaded records the read-loop position replay completion is measured against, with one
// settle attempt (MarkReplayLoadedAt); the projection's swap settles replayed. With no projection
// nothing was replayed.
func (bc *bridgeCoordinator) markReplayLoaded(chatID marotte.ChatID, gen uint64, sb *sharedBridge, replayed *sessionSettle) {
	if bc.replayProjection == nil {
		replayed.settle(nil)
		return
	}
	bc.replayProjection.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: sb.bridge.SessionLoadSeq()})
}

// adoptKASTitle names a still-default chat from KAS's session title after the focus door's
// full treatment: a stored title is unsanitized. Precedence: focus_update > first-prompt
// label > this. Refusals Warn; this rung has no volume.
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

// applyLoadedSessionFacts copies a resumed session's facts onto the record only where the
// load carried them: `session/load` routinely omits the model catalog. dropped is a saved model
// the catalogue no longer serves, cleared here and settled by commitUnservedModel.
func applyLoadedSessionFacts(c *marotte.Chat, facts acpSessionFacts, title string) (renameTo string, dropped unservedSelection) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	// An absent catalog keeps the previous set (ApplyServedModels is false for empty).
	marotte.ApplyServedModels(c, facts.Catalog())
	dropped = dropUnservedModel(c)
	if v := facts.SummarizationThreshold(); v > 0 {
		c.Usage.SummarizationThresholdPct = v
	}
	return reconcileUserName(c, title, facts.SessionTitleSetByUser()), dropped
}

// An empty catalogue decides nothing.
func dropUnservedModel(c *marotte.Chat) unservedSelection {
	if marotte.ModelServed(c.Model, c.ServedModelIDs) {
		return unservedSelection{}
	}
	slog.Info("clearing a saved model the served catalogue no longer offers",
		"chat_id", c.ID, "model", c.Model)
	dropped := unservedSelection{model: c.Model, effort: c.Effort, onRecord: true}
	c.Model = ""
	c.Effort = ""
	return dropped
}

// kasPlaceholderTitle is the title KAS gives every new session.
const kasPlaceholderTitle = "New Session"

// A user name is re-applied when KAS has not latched it; a session renamed elsewhere names a chat
// with no user name of its own, without the model-output shape rules.
func reconcileUserName(c *marotte.Chat, stored string, setByUser bool) (renameTo string) {
	if c.NameSetByUser {
		if setByUser && stored == marotte.KASStoredTitle(c.Name) {
			return ""
		}
		return c.Name
	}
	if setByUser {
		if title := translate.SanitizeTitle(stored); title != "" && title != kasPlaceholderTitle {
			c.Name = title
			c.NameSetByUser = true
		}
		return ""
	}
	adoptKASTitle(c, stored)
	return ""
}

// renameSessionTimeout bounds the door's rename; a failure is repaired at the next open.
const renameSessionTimeout = 10 * time.Second

func reassertUserName(ctx context.Context, chatID marotte.ChatID, bridge renamableSession, name string) {
	if name == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, renameSessionTimeout)
	defer cancel()
	resp, err := bridge.Call(cctx, marotte.MethodSessionRename,
		map[string]any{"sessionId": bridge.SessionID(), "title": name})
	if rErr := command.RenameOutcome(resp, err); rErr != nil {
		slog.Warn("could not re-apply the user's chat name to the session; the next open retries",
			"chat_id", chatID, "acp_session", bridge.SessionID(), "error", rErr)
	}
}

// persistNewSessionMetadata records the new session on the chat. An error is a record that did not
// take it, wrapping command.ErrChatGone when the record is absent.
func (bc *bridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID marotte.ChatID, bridge newSessionFacts) error {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	catalog := bridge.Catalog()
	title := bridge.SessionTitle()
	titleSetByUser := bridge.SessionTitleSetByUser()
	var renameTo string
	// A workspace fact, set outside the Mutate so the chat lock is not ordered against the catalog's.
	bc.catalog.setModes(bridge.Modes())
	bc.catalog.SetModels(bridge.Models())
	// Read before the next line overwrites it with what landed.
	var requestedMode string
	version, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
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
		renameTo = reconcileUserName(c, title, titleSetByUser)
		return true
	})
	switch {
	case err != nil:
		return fmt.Errorf("record new session %s: %w", newSessionID, err)
	case version == "":
		return fmt.Errorf("%w: %s", command.ErrChatGone, chatID)
	}
	reassertUserName(ctx, chatID, bridge, renameTo)
	bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
	return nil
}

type renamableSession interface {
	acpSession
	acpCaller
}

// newSessionFacts is a freshly created session: its facts and its rename call.
type newSessionFacts interface {
	acpSessionFacts
	acpCaller
}

// reportModeNotApplied tells the user the session did not get the requested mode. The
// record keeps the actual mode, so the next spawn asks for the current one.
func (bc *bridgeCoordinator) reportModeNotApplied(ctx context.Context, chatID marotte.ChatID, requested, actual string) {
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

// renderChatSteering returns the chat-only steering for a chat spawn, nil without a renderer.
func (bc *bridgeCoordinator) renderChatSteering(ctx context.Context) []marotte.ClientSteeringDoc {
	if bc.chatSteering == nil {
		return nil
	}
	return bc.chatSteering(ctx)
}

// bridgeFor returns the bridge for chatID, or nil.
func (bc *bridgeCoordinator) bridgeFor(chatID marotte.ChatID) *sharedBridge {
	return bc.bridge.mgr.get(chatID)
}

// HasLiveBridge reports whether a chat has a bridge; retention never purges such a chat (archive.WithLiveChats).
func (rt *Runtime) HasLiveBridge(chatID marotte.ChatID) bool {
	return rt.bridge.mgr.get(chatID) != nil
}

// TurnLive reports whether the reader must treat the chat as running: its own turn, an
// admitted prompt awaiting its bracket, or a held slot (chat.WithLiveTurn).
func (rt *Runtime) TurnLive(chatID marotte.ChatID) bool {
	return rt.coord.turns.live(chatID)
}

// OpenTurns returns the chat's open turns with their coalescing lane entries, for the GET (chat.WithOpenTurns).
func (rt *Runtime) OpenTurns(chatID marotte.ChatID) []chat.OpenTurnTail {
	turns := rt.coord.turns.openTurnIDs(chatID)
	out := make([]chat.OpenTurnTail, 0, len(turns))
	for _, t := range turns {
		out = append(out, chat.OpenTurnTail{ID: t.ID, Entries: t.Log.OpenEntries()})
	}
	return out
}

// closeBridge closes every turn the bridge hosted with outcome, then stops and removes it.
// The closer runs first, while the record still names the open turns.
func (bc *bridgeCoordinator) closeBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	bc.closeTurnOnBridgeDeath(ctx, chatID, outcome)
	bc.bridge.mgr.close(chatID)
}

// retireBridges closes idle chat bridges, marks busy ones for their next open and resets
// the utility session. A bridge hosting a live run counts as busy (retireChatBridges).
func (bc *bridgeCoordinator) retireBridges(reason string) {
	victims, marked := bc.bridge.mgr.retireChatBridges()
	for _, chatID := range victims {
		bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
	}
	bc.retireUtility()
	slog.Info("identity changed; retiring live chat bridges",
		"reason", reason, "closed", len(victims), "marked", marked)
}

type replayProjector interface {
	OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string, settled *sessionSettle)
	MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint)
	DiscardReplayProjection(marotte.ChatID)
	SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool)
}

// goForward starts a forward loop on the group Shutdown waits on: the loop's tail
// (closeTurnOnBridgeDeath) must land before shutdown completes. No deadlock: Shutdown
// stops every bridge before the wait, which ends the range. The attachment is taken before the
// goroutine runs: a settle that captured the old generation reads a later attach as a gone forward
// and drops its close.
func (bc *bridgeCoordinator) goForward(chatID marotte.ChatID, bridge ACPBridge) uint64 {
	gen := bc.turns.attachForward(chatID)
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, bridge, gen) })
	return gen
}

// forwardAt is Forward on an attachment the caller already took: tryLoadSession orders its
// read-loop position against it.
func (bc *bridgeCoordinator) forwardAt(chatID marotte.ChatID, bridge ACPBridge, gen uint64) {
	exited := bc.turns.exitFor(chatID, gen)
	ch := bridge.NotifCh()
	// The generation keeps a straggler from the previous bridge off a restarted counter.
	for n := range ch {
		bc.recordPool(bridge, n.Msg)
		bc.consumeFrame(chatID, bridge, gen, n)
		// Settle a replay projection here: this goroutine folds the frames, so only its position is comparable (replay_drain.go).
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen, seq: n.Seq}, false)
		}
	}
	// No frame can advance the position now: seal the settle so a load missing its trailing frames still completes.
	if bc.replayProjection != nil {
		bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen}, true)
	}
	// Wake anything parked on a position, before the death closer.
	bc.turns.sealPosition(chatID, gen)
	exited()
	// For every end, before the death closer, whose turn close would drop the cards without saying why.
	bc.endAsks(bridge)

	// Still registered means the process died on its own, so this closer owns the open turn.
	endedItself := bc.bridge.mgr.removeIfBridge(chatID, bridge)
	slog.Info("bridge exited", "chat_id", chatID, "session_id", bridge.SessionID(), "ended_itself", endedItself)
	if endedItself {
		// The only site that observes every death; readLoop reaps its own paths only.
		bridge.Stop()
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
		// A waiting_on_user claim is owed to the agent, which is gone.
		if bc.dischargeWaiting != nil {
			bc.dischargeWaiting(bc.lifecycle.shutdownCtx, chatID)
		}
	}

	// Flush staged writes, or a parked fs-handler and a phantom pending op replay to reconnecting clients.
	lastBridge := bc.bridge.mgr.count() == 0

	bc.mcpRegistry.dropPool(bridge)
	if lastBridge {
		bc.mcpRegistry.clearAll(bc.lifecycle.shutdownCtx)
	}
	// A run chat has no record and nothing calls cleanupChatState for one.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
	// Last, for every end (tab close, identity retire, crash): KAS's own dispose releases them on a
	// connection that has already closed, and no later bridge can see, read or stop them.
	bc.releaseTerminals(chatID, bridge)
}

// Only this loop knows the sender.
func (bc *bridgeCoordinator) recordPool(bridge ACPBridge, msg *marotte.RPCResponse) {
	if msg == nil || msg.ID != nil || msg.Method != methodV3MCPStatus {
		return
	}
	if pool, ok := translate.ReadMCPPool(msg); ok {
		bc.mcpRegistry.setPool(bridge, pool)
	}
}

// consumeFrame translates one frame, then advances the observed position (deferred, per
// frame): many paths consume a frame without touching a turn.
func (bc *bridgeCoordinator) consumeFrame(chatID marotte.ChatID, origin acpResponder, gen uint64, n marotte.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, origin, n.Msg)
}

// Effort lives on the chat record (marotte.Chat.Effort), never a global keyed by the last model.

// NoticeTarget names the tab a notification about chatID, or about its run, opens.
func (bc *bridgeCoordinator) NoticeTarget(ctx context.Context, chatID marotte.ChatID, runID string) notice.Target {
	if id := notice.AskRun(chatID, runID); id != "" {
		var label string
		if bc.runLabel != nil {
			label = bc.runLabel(id)
		}
		return notice.RunTarget(id, label)
	}
	return notice.ChatTarget(chatID, bc.chatName(ctx, chatID))
}

func (bc *bridgeCoordinator) chatName(ctx context.Context, chatID marotte.ChatID) string {
	if chatID == "" {
		return ""
	}
	if rec, ok := bc.chatStore.Get(ctx, chatID); ok {
		return rec.Name
	}
	return ""
}

// Notify shows n on both channels: the page's `notification` frame on chatID's stream, and
// the Web Push when push is configured.
func (bc *bridgeCoordinator) Notify(ctx context.Context, chatID marotte.ChatID, n *marotte.NotificationPayload) {
	bc.broadcast(ctx, marotte.NewEvent(marotte.EventNotification, chatID, *n))
	if bc.push == nil {
		return
	}
	if !bc.push.HasSubscribers() {
		bc.reportNoSubscribers(n.Kind, n.PushSubject)
		return
	}
	bc.noSubscribers.Store(false)
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, n)
	})
}

// retractPush drops any held ask push about subj: the ask was settled elsewhere.
func (bc *bridgeCoordinator) retractPush(subj marotte.PushSubject) {
	if bc.push == nil {
		return
	}
	bc.push.Retract(subj)
}

// reportNoSubscribers logs once per episode that a notification had no subscriber; a
// subscriber re-arms it. Both subject halves are logged: either may be empty.
func (bc *bridgeCoordinator) reportNoSubscribers(kind marotte.PushKind, subj marotte.PushSubject) {
	if !bc.noSubscribers.CompareAndSwap(false, true) {
		return
	}
	slog.Info("no push subscribers; notifications are being dropped until a browser subscribes",
		"chat_id", subj.ChatID, "subject", subj.Key, "kind", string(kind))
}

// SettleTurnOnResponse closes turnID's turn on its settling response once the folder has
// consumed everything queued before it, unless the wire's turn_end got there first. seq
// is the response's read-loop position; zero skips the wait.
func (bc *bridgeCoordinator) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptResponse, Resp: resp, Turn: turnID, Seq: seq})
}

// applyModelSwitch swaps the live bridge's model through session/set_config_option and
// re-applies the level. False with no bridge or a refused swap; never a restart.
func (bc *bridgeCoordinator) applyModelSwitch(ctx context.Context, chatID marotte.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitchOn(ctx, chatID, sb, model, effort)
}

// applyModelSwitchOn swaps the model on a bridge the caller already holds, then re-applies the level.
func (*bridgeCoordinator) applyModelSwitchOn(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, model, effort string,
) bool {
	if err := sb.bridge.SetModel(ctx, model); err != nil {
		slog.Info("model switch: the session refused the swap",
			"chat_id", chatID, "model", model, "error", err)
		return false
	}
	// Re-assert the level: KAS reconciles against the new model's tiers (2.19.1: a swap to
	// `auto` destroys it). Best-effort.
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

// repairEffort re-asserts the chat's effort level on an already-open bridge, catching a
// level KAS changed on its own. At the prompt, because a Call from Forward would block the
// drain it waits on. Best-effort.
func (bc *bridgeCoordinator) repairEffort(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) {
	rec, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	if bc.catalog.thinkingToggleable(rec.Model) && rec.Thinking != "" {
		if err := sb.bridge.EnsureThinking(ctx, rec.Thinking); err != nil {
			slog.Warn("thinking choice not re-applied on the open session",
				"chat_id", chatID, "thinking", rec.Thinking, "error", err)
		}
	}
	level := bc.sessionEffort(ctx, rec)
	if level == "" {
		return
	}
	if err := sb.bridge.EnsureEffort(ctx, level); err != nil {
		slog.Warn("reasoning effort not re-applied on the open session",
			"chat_id", chatID, "effort", level, "error", err)
	}
}

// healEffort wraps the config-option handler to re-assert the chosen level when a
// config_option_update reports another: the spawning turn never reaches repairEffort, and
// that is when KAS's first-prompt model pin moves the tier. Latched once per bridge: the
// repair triggers another update.
func (bc *bridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's own frame only: a step's session reports its own level.
		if !attr.ChatOwned() {
			return
		}
		sb := bc.bridgeFor(chatID)
		if sb == nil {
			return
		}
		rec, ok := bc.chatStore.Get(ctx, chatID)
		if !ok {
			return
		}
		// Hand the report over first: it is what lets EnsureEffort assert rather than compare equal against the ask.
		running := rec.EffortActive
		sb.bridge.ObserveEffort(running)
		sb.bridge.ObserveThinking(rec.ThinkingActive)
		// The capped level when thinking is off: re-asserting the uncapped one would re-enable
		// thinking (KAS enables it for xhigh and max).
		want := bc.sessionEffort(ctx, rec)
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
		// On inflight: Shutdown stops every bridge before waiting on this group, which is what unblocks a stuck Call.
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

// effortFor resolves the level a chat's next session starts at: the chat's choice, else
// the level remembered for its model (settings.KeyLastEffortByModel), else the catalog
// default; empty sends nothing. A fallback is never written to the record. Validated
// here because config.json is user-editable.
func (bc *bridgeCoordinator) effortFor(ctx context.Context, rec *marotte.Chat) string {
	if rec.Effort != "" {
		return rec.Effort
	}
	if level := bc.effortSeedFor(ctx, rec.Model); level != "" {
		return level
	}
	return bc.catalog.defaultEffortFor(rec.Model)
}

// sessionEffort is effortFor capped for thinking off, the level KAS reports there.
func (bc *bridgeCoordinator) sessionEffort(ctx context.Context, rec *marotte.Chat) string {
	level := bc.effortFor(ctx, rec)
	if bc.catalog.thinkingToggleable(rec.Model) && rec.ThinkingIsOff(bc.catalog.thinkingDefaultOff(rec.Model)) {
		return marotte.CapEffortForThinkingOff(level, rec.EffortLevels)
	}
	return level
}

// Per model, so one pick never retracts another model's level. The single seed read.
func (bc *bridgeCoordinator) effortSeedFor(ctx context.Context, model string) string {
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

// effortForSwitch resolves the level after a model switch: the target model's remembered
// level, else its catalog default, else "". Not effortFor: the stored choice was made
// under the model being left.
func (bc *bridgeCoordinator) effortForSwitch(ctx context.Context, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	return bc.catalog.defaultEffortFor(model)
}

// persistModelSwitch records a landed switch: the model_switched entry into the open turn
// (lanes sealed first) or after the newest close, none on an unstarted chat, then the header
// takes the pick, drops the old tier and resets usage. Detached context. Takes the payload
// because From, To and Effort are adjacent strings.
func (bc *bridgeCoordinator) persistModelSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModelSwitched, contextSize int) {
	ctx = durable.Context(ctx)
	bc.appendModelSwitched(ctx, chatID, "switch_model", sw)
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.Model = sw.To
		c.PendingModel = ""
		// The tier was chosen for the old model; resolution falls to the seed or the new default.
		c.Effort = ""
		c.Usage = marotte.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
	}
}

// ThinkingDefaultOff reports the catalog's default-thinking fact for model.
func (bc *bridgeCoordinator) ThinkingDefaultOff(model string) bool {
	return bc.catalog.thinkingDefaultOff(model)
}

// PersistEffortChange records a reader-asked tier change as a model_switched entry with
// From == To (marotte.EntryModelSwitched). Writes no header field. An empty model writes
// nothing (the renderer reads an empty To as `Context reset`), nor does an unstarted chat.
func (bc *bridgeCoordinator) PersistEffortChange(ctx context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel) {
	if model == "" {
		return
	}
	ctx = durable.Context(ctx)
	bc.appendModelSwitched(ctx, chatID, "set_effort", marotte.EntryModelSwitched{From: model, To: model, Effort: string(level)})
}

// appendModelSwitched writes one model_switched entry into the open turn (lanes sealed first) or
// after the newest close; op names the caller in its error lines.
func (bc *bridgeCoordinator) appendModelSwitched(ctx context.Context, chatID marotte.ChatID, op string, sw marotte.EntryModelSwitched) {
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.ModelSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error(op+": record the model_switched entry in the turn", "chat_id", chatID, "error", err)
		}
		return
	}
	if !bc.conversationStarted(ctx, chatID) {
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModelSwitched, "", sw); err != nil {
		slog.Error(op+": record the model_switched entry between turns", "chat_id", chatID, "error", err)
	}
}

// A between-turns append before the first prompt mints an event turn, which makes the empty chat
// render as a conversation; the header alone carries a switch made before then.
func (bc *bridgeCoordinator) conversationStarted(ctx context.Context, chatID marotte.ChatID) bool {
	c, ok := bc.chatStore.Get(ctx, chatID)
	return ok && c.TurnCount > 0
}

// PersistModeSwitch records a landed mode switch as a mode_switched entry, into the open
// turn (lanes sealed first) or after the newest close; an unstarted chat gets none. Writes no
// header field. Detached context.
func (bc *bridgeCoordinator) PersistModeSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModeSwitched) {
	ctx = durable.Context(ctx)
	if log, ok := bc.turns.foldTarget(chatID); ok {
		sealed, err := log.ModeSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error("set_mode: record the switch in the turn", "chat_id", chatID, "error", err)
		}
		return
	}
	if !bc.conversationStarted(ctx, chatID) {
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModeSwitched, "", sw); err != nil {
		slog.Error("set_mode: record the switch between turns", "chat_id", chatID, "error", err)
	}
}

// persistModelPick records a model choice on a chat with no live bridge: model takes the
// pick, pending_model and Effort clear, no entry, no usage reset.
func (bc *bridgeCoordinator) persistModelPick(ctx context.Context, chatID marotte.ChatID, model string) {
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

// AbandonInFlightTurn closes a turn its prompt call could not finish. It waits for no
// read-loop position: no bracket is coming. Only `interrupted` or `cancelled` are legal
// stops; anything else becomes `interrupted` with a Warn.
func (bc *bridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64) {
	if stop != marotte.StopReasonInterrupted && stop != marotte.StopReasonCancelled {
		slog.Warn("a prompt failure named a stop this close cannot conclude, so it concludes interrupted",
			"chat_id", chatID, "turn", turnID, "stop", stop)
		stop = marotte.StopReasonInterrupted
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptFailure, Stop: stop, Reason: reason, Kind: kind, Turn: turnID, Seq: seq})
}

// FinalizeLocalShellTurn appends a `!cmd` turn's output as its one text entry and closes it.
func (bc *bridgeCoordinator) FinalizeLocalShellTurn(ctx context.Context, chatID marotte.ChatID, turnID, output string) {
	if t, ok := bc.turns.turnByID(chatID, turnID); ok {
		sealed, err := t.Log.TextDelta(ctx, "", "", output)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Warn("a shell turn's output was not recorded", "chat_id", chatID, "turn", turnID, "error", err)
		}
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerLocalShell, Turn: turnID})
}

// WireTurnStart handles the engine's turn_start bracket: it binds a pending prompt turn,
// closes a bracketed own turn whose turn_end was lost, or opens a wire_turn_start turn.
// The bind is provisional; the first agentInitiated frame revises it (ReviseTurnBinding).
func (bc *bridgeCoordinator) WireTurnStart(ctx context.Context, chatID marotte.ChatID) {
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

// WireTurnEnd handles the engine's turn_end bracket. With no own turn it is a no-op,
// never a phantom turn. Live path only: replay is filtered upstream.
func (bc *bridgeCoordinator) WireTurnEnd(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerWireEnd, Stop: stop, Own: true})
}

// TurnFoldTarget returns the accumulator the chat's own frames fold into, opening a
// wire_turn_start turn when none is open; nil when refused. Never for a step frame.
func (bc *bridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn {
	if log, ok := bc.turns.foldTarget(chatID); ok {
		return log
	}
	t := bc.openWireTurn(ctx, chatID)
	if t == nil {
		return nil
	}
	return t.Log
}

// OwnTurn returns the chat's own open turn without opening one.
func (bc *bridgeCoordinator) OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.foldTarget(chatID)
}

// PromptTurn returns the chat's prompt-class turn awaiting or holding its bracket.
func (bc *bridgeCoordinator) PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.promptTurn(chatID)
}

// AppendBetweenTurns files a lane-less entry after the newest turn's close.
func (bc *bridgeCoordinator) AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) ([]*marotte.Entry, error) {
	return bc.chatStore.AppendBetweenTurns(ctx, chatID, e)
}

// ReviseTurnBinding acts on a frame proving the open turn is the agent's own
// (`agentInitiated` rides content frames only): it opens the agent turn as own via
// openLocked and drops the prompt's turn back to pending. Sealed entries stay put.
func (bc *bridgeCoordinator) ReviseTurnBinding(ctx context.Context, chatID marotte.ChatID) {
	var agent *activeTurn
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

// closeTurnOnBridgeDeath closes everything the bridge hosted: the chat's own and pending
// turns, every hosted run's open step turns (run lock taken after the chat's, never
// nested), then the unread steers. The second claim on a closed turn loses.
func (bc *bridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	// Detached once for all three steps.
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

// closeTurnsOnRetire is the death closer for the retire stops, inside a prompt's own
// openBridge: it closes own only when wire_turn_start, never pending, plus hosted runs;
// parked steers belong to the unstarted prompt.
func (bc *bridgeCoordinator) closeTurnsOnRetire(ctx context.Context, chatID marotte.ChatID) {
	if t, ok := bc.turns.ownTurn(chatID); ok && t.Source == marotte.TurnSourceWireTurnStart {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: t.ID})
	}
	bc.closeHostedRuns(ctx, chatID, marotte.StopReasonInterrupted)
}

func (bc *bridgeCoordinator) closeHostedRuns(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	if bc.runs == nil {
		return
	}
	if bc.endHostedSteers != nil {
		bc.endHostedSteers(ctx, chatID)
	}
	c := bc.concludeStop(chatID, stop, deathInterruptCause, "")
	byRun, err := bc.runs.closeHost(ctx, chatID, c)
	for runID, sealed := range byRun {
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), "", runID, sealed)
	}
	if err != nil {
		slog.Error("close the step turns a dead bridge hosted", "chat_id", chatID, "error", err)
	}
}

// claimShutdownCause names shutdown as the cause of every open turn on the chat, first-wins.
func (bc *bridgeCoordinator) claimShutdownCause(chatID marotte.ChatID) {
	for _, t := range bc.turns.openTurnIDs(chatID) {
		bc.turns.interrupt(chatID, t.ID, shutdownInterruptCause)
	}
}

// dropUnreadSteers records each agent row KAS queued but this process never received as an
// EMPTY-TEXT steer entry, the reconcile signal the next session/load reads, and reports the user rows' buffer gone.
func (bc *bridgeCoordinator) dropUnreadSteers(ctx context.Context, chatID marotte.ChatID) {
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

// recordSteer writes a minted steer entry into the open turn (lanes sealed first), else after the newest close.
func (bc *bridgeCoordinator) recordSteer(ctx context.Context, chatID marotte.ChatID, steerID string, steer *marotte.EntrySteer) {
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

// InterruptTurn records why kiro-cli abandoned a turn and trips its prompt call so the
// failure path finalizes it; the cause is first-wins on that turn. The bridge stays alive.
// No turn, no bridge or a claimed cause is not a failure.
func (bc *bridgeCoordinator) InterruptTurn(chatID marotte.ChatID, reason string) {
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

// ParentACPSession returns the running bridge's ACP session id for chatID, or "".
func (bc *bridgeCoordinator) ParentACPSession(chatID marotte.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
