package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	// locks answers the governance lock map a chat spawn composes from.
	locks     func() map[string]marotte.GovernanceLock
	bridge    *bridges
	chatStore bridgeChatRecords
	// catalog is the workspace mode and model vocabulary.
	catalog *Catalog
	// turns is the per-chat turn lifecycle (turn.go).
	turns          *turnRegistry
	broadcast      func(ctx context.Context, e marotte.ServerEvent)
	translateEvent func(chatID marotte.ChatID, msg *marotte.RPCResponse)
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
	// retireUtility resets the utility session at the chat bridges' identity boundary.
	retireUtility func()
	// replayProjection is the session/load replay lifecycle; nil in tests without a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the spawn path, to heal paused runs.
	onSessionRehydrated func(marotte.ChatID)
	// dischargeWaiting drops a retained waiting_on_user claim when its agent dies. Nil in tests.
	dischargeWaiting func(context.Context, marotte.ChatID)
	// clearDecisions drops a dead bridge's unanswered decisions: the idle window would read them as a person working a run.
	clearDecisions func(marotte.ChatID)
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
		// h implements replayProjector (load_projection.go).
		replayProjection: h.replay,
		agentEngine:      resolveAgentEngine(),
		acpArgs:          h.acpArgs,
		secretStorage:    func() bool { return h.secrets != nil },
		locks:            h.config.GovernanceLocks,
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
			// Its own goroutine: a bridge Call has no deadline and a closer must not wait on one.
			go func() {
				ctx, cancel := h.lifecycle.derivedContext()
				defer cancel()
				h.runs.clearStaleNotices(ctx, chatID)
			}()
		},
		dischargeWaiting:  h.DischargeWaiting,
		clearDecisions:    h.bus.ClearPendingPermsForChat,
		applyPendingModel: h.applyPendingModel,
		resolveEnds: func(ctx context.Context, chatID marotte.ChatID, f command.TurnFence) command.EndFacts {
			return h.dispatcher.ResolveAfterClose(ctx, chatID, f)
		},
		drainAfterClose: func(ctx context.Context, chatID marotte.ChatID, cl command.CloseFacts, ends command.EndFacts) {
			h.dispatcher.DrainAfterClose(ctx, chatID, cl, ends)
		},
		runs:             h.runs.log,
		takeAgentSteers:  h.bus.steers.TakeAgentRows,
		steerTurnEnded:   h.bus.steers.TurnEnded,
		steerTurnStarted: h.bus.steers.TurnStarted,
		steerTurnBound:   h.bus.steers.TurnBound,
		steerTurnRevised: h.bus.steers.TurnRevised,
		steerBridgeGone:  h.bus.steers.BridgeGone,
	}
}

// hasSecretStorage reports whether a bridge may declare `_meta.kiro.secretStorage`. Nil-safe.
func (bc *BridgeCoordinator) hasSecretStorage() bool {
	return bc.secretStorage != nil && bc.secretStorage()
}

// processLifetimeCtx returns the runtime's shutdown context, which bounds a kiro-cli
// subprocess; never the per-turn caller ctx.
func (bc *BridgeCoordinator) processLifetimeCtx() context.Context {
	return bc.lifecycle.shutdownCtx
}

// resolveAgentEngine returns v3 (KAS), ignoring KIRO_AGENT_ENGINE: marotte cannot talk to a legacy engine.
func resolveAgentEngine() string {
	return marotte.AgentEngineV3
}

// OpenBridge returns chatID's bridge, creating it; concurrent callers coalesce on bridgeSpawnKey.
//
//nolint:revive // unexported-return: sharedBridge is package-internal; callers within agent use the methods on it. Exporting would leak ACP wiring outside the runtime package.
func (bc *BridgeCoordinator) OpenBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	// Before the fast path, so an account switch retires the existing bridge.
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

	sfKey := bridgeSpawnKey(chatID, modelOverride)
	v, err, _ := bc.bridge.mgr.spawnSF.Do(sfKey, func() (any, error) {
		return bc.spawnBridge(ctx, chatID, modelOverride)
	})
	if err != nil {
		return nil, err
	}
	b, _ := v.(*sharedBridge)
	// A retire landed during the spawn: close and reopen rather than hand back a stale bridge.
	if reopen, stopped := bc.bridge.mgr.closeIfRetired(chatID, b); reopen {
		if stopped {
			bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
		}
		return bc.OpenBridge(ctx, chatID, modelOverride)
	}
	return b, nil
}

// bridgeSpawnKey is the singleflight key over (chatID, modelOverride); keyenc keeps it
// injective, since a collision hands a caller another chat's bridge.
func bridgeSpawnKey(chatID marotte.ChatID, modelOverride string) string {
	return keyenc.Join(string(chatID), modelOverride)
}

// spawnBridge creates chatID's bridge inside the singleflight, rolling it back out of the map on any start failure.
func (bc *BridgeCoordinator) spawnBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	sb, existed := bc.bridge.mgr.orInsert(chatID)
	if existed {
		return sb, nil
	}

	setupErr := func(err error) error {
		bc.bridge.mgr.removeIfSame(chatID, sb)
		// Ends an attached forward loop whose stream a pre-spawn Start failure never closes.
		sb.bridge.Stop()
		sb.setIdle()
		return err
	}

	rec, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
	}
	// A pick parked with no bridge lands here: this is the last header read before the session takes its model.
	if rec.PendingModel != "" {
		bc.persistModelPick(ctx, chatID, rec.PendingModel)
		if rec, exists = bc.chatStore.Get(ctx, chatID); !exists {
			return nil, setupErr(fmt.Errorf("chat %s not found", chatID))
		}
	}

	// No mcpServers parameter: KAS reads the config file marotte renders (internal/mcp/kasfile.go),
	// and an inline copy would win over the file.
	if bc.preBridgeSpawn != nil {
		bc.preBridgeSpawn(ctx)
	}

	model, effort, thinking := bc.sessionChoices(ctx, chatID, rec, modelOverride)

	if rec.ACPSessionID != "" {
		if bc.tryLoadSession(ctx, chatID, sb, rec.ACPSessionID, model, effort, thinking, rec.SupervisedMode) {
			return sb, nil
		}
	}

	// EnableHooks opts chat bridges into KAS's hook engine. Forward must drain NotifCh before
	// Start: the handshake already delivers notifications and requests.
	bc.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), Steering: bc.renderChatSteering(ctx), Model: model, Mode: rec.CurrentModeID, Effort: effort, Thinking: thinking, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: rec.SupervisedMode, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, TerminalTimeout: func(c context.Context) int { return terminalCommandTimeoutMs(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryPreference(ctx, bc.lifecycle.configDir), DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, bc.lifecycle.configDir)), Features: agentFeatures(ctx, bc.lifecycle.configDir, currentLocks(bc.locks)), ContentCollection: contentCollectionResolver(bc.lifecycle.configDir, bc.locks)}); err != nil {
		return nil, setupErr(err)
	}
	bc.persistNewSessionMetadata(ctx, chatID, sb.bridge)
	// The session door's half of the supervised fail-open (the assert inside Start is best-effort).
	bc.reportSupervisedNotApplied(ctx, chatID, rec.SupervisedMode, sb.bridge.SupervisedApplied())
	sb.setIdle()

	return sb, nil
}

// sessionChoices resolves the model, effort and thinking a chat's session opens with.
func (bc *BridgeCoordinator) sessionChoices(
	ctx context.Context, chatID marotte.ChatID, rec *marotte.Chat, modelOverride string,
) (model, effort, thinking string) {
	model = rec.Model
	if modelOverride != "" && modelOverride != modelAuto {
		model = modelOverride
	}
	// Silently withhold a model the account cannot run: it is inherited, not a fresh pick
	// (refused loudly in cmdSwitchModel). An empty advertised set allows.
	if !marotte.ModelServed(model, rec.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", model)
		model = ""
	}

	// A differing override is switch-by-restart: resolve effort against the target model.
	effort = bc.effortFor(ctx, rec)
	if model != "" && model != rec.Model {
		effort = bc.EffortForSwitch(ctx, model)
	}
	if target := cmp.Or(model, rec.Model); bc.catalog.ThinkingToggleable(target) {
		thinking = rec.Thinking
		if rec.ThinkingIsOff(bc.catalog.ThinkingDefaultOff(target)) {
			effort = marotte.CapEffortForThinkingOff(effort, rec.EffortLevels)
		}
	}
	return model, effort, thinking
}

// reportSupervisedNotApplied tells the user the session refused `autopilot: off`. The
// record keeps the request, so the next spawn re-asserts it.
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

// tryLoadSession attempts session/load on the stored session id, carrying the supervised gate (bridge.loadSession).
func (bc *BridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge,
	acpSessionID, model, effort, thinking string, supervised bool,
) bool {
	// Forward attaches before Start. On failure the old bridge is swapped out before it is
	// stopped, so the exit cleanup cannot evict the replacement. Open the projection first:
	// KAS replays inside Start.
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, acpSessionID)
	}
	// The load's read-loop position is only comparable within this attachment (replay_drain.go).
	gen := bc.goForward(chatID, sb.bridge)
	if err := sb.bridge.Start(ctx, &marotte.StartOpts{Lifetime: bc.processLifetimeCtx(), Steering: bc.renderChatSteering(ctx), SessionID: acpSessionID, Model: model, Effort: effort, Thinking: thinking, AgentEngine: bc.agentEngine, EnableHooks: true, ExtraArgs: bc.acpArgs, Supervised: supervised, SecretStorage: bc.hasSecretStorage(), Presets: securityPresets(ctx, bc.lifecycle.configDir), IgnoreFiles: func(c context.Context) []string { return spawnIgnoreFiles(c, bc.lifecycle.configDir) }, TerminalTimeout: func(c context.Context) int { return terminalCommandTimeoutMs(c, bc.lifecycle.configDir) }, ToolSearch: toolSearchEnabled(ctx, bc.lifecycle.configDir), Knowledge: knowledgeEnabled(ctx, bc.lifecycle.configDir), Memory: memoryPreference(ctx, bc.lifecycle.configDir), DisableAutoCompaction: sessionDisablesAutoCompaction(autoCompactionPolicy(ctx, bc.lifecycle.configDir)), Features: agentFeatures(ctx, bc.lifecycle.configDir, currentLocks(bc.locks)), ContentCollection: contentCollectionResolver(bc.lifecycle.configDir, bc.locks)}); err != nil {
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
		return false
	}
	// Record the read-loop position replay completion is measured against, with one settle
	// attempt (MarkReplayLoadedAt).
	if bc.replayProjection != nil {
		bc.replayProjection.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: sb.bridge.SessionLoadSeq()})
	}
	title := sb.bridge.SessionTitle()
	bc.catalog.SetModes(sb.bridge.Modes())
	bc.catalog.SetModels(sb.bridge.Models())
	var renameTo string
	if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		renameTo = applyLoadedSessionFacts(c, sb.bridge, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	reassertUserName(ctx, chatID, sb.bridge, renameTo)
	sb.setIdle()
	// Off the spawn path, and after the state flip so the resume's Call finds an idle bridge.
	if bc.onSessionRehydrated != nil {
		go bc.onSessionRehydrated(chatID)
	}
	return true
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
// load carried them: `session/load` routinely omits the model catalog.
func applyLoadedSessionFacts(c *marotte.Chat, facts acpSessionFacts, title string) (renameTo string) {
	if mode := facts.CurrentMode(); mode != "" {
		c.CurrentModeID = mode
	}
	// An absent catalog keeps the previous set (ApplyServedModels is false for empty).
	marotte.ApplyServedModels(c, facts.Catalog())
	dropUnservedModel(c)
	if v := facts.SummarizationThreshold(); v > 0 {
		c.Usage.SummarizationThresholdPct = v
	}
	return reconcileUserName(c, title, facts.SessionTitleSetByUser())
}

// dropUnservedModel clears a saved model the served catalogue no longer offers, with its
// effort. An empty catalogue decides nothing.
func dropUnservedModel(c *marotte.Chat) {
	if marotte.ModelServed(c.Model, c.ServedModelIDs) {
		return
	}
	slog.Info("clearing a saved model the served catalogue no longer offers",
		"chat_id", c.ID, "model", c.Model)
	c.Model = ""
	c.Effort = ""
}

// kasPlaceholderTitle is the title KAS gives every new session.
const kasPlaceholderTitle = "New Session"

// reconcileUserName applies the user naming rung at a session door. A user name is
// re-applied when KAS has not latched it; a session renamed elsewhere names a chat with no
// user name of its own, without the model-output shape rules.
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

// reassertUserName latches the chat's user name on the bridge's session.
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

func (bc *BridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID marotte.ChatID, bridge newSessionFacts) {
	newSessionID := bridge.SessionID()
	newModelID := bridge.ModelID()
	currentMode := bridge.CurrentMode()
	catalog := bridge.Catalog()
	title := bridge.SessionTitle()
	titleSetByUser := bridge.SessionTitleSetByUser()
	var renameTo string
	// A workspace fact, set outside the Mutate so the chat lock is not ordered against the catalog's.
	bc.catalog.SetModes(bridge.Modes())
	bc.catalog.SetModels(bridge.Models())
	// Read before the next line overwrites it with what landed.
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
		renameTo = reconcileUserName(c, title, titleSetByUser)
		return true
	}); err != nil {
		slog.Error("persist new session metadata",
			"chat_id", chatID,
			"acp_session", newSessionID,
			"model", newModelID,
			"error", err)
	}
	reassertUserName(ctx, chatID, bridge, renameTo)
	bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
}

// renamableSession is a session the door can rename.
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

// renderChatSteering returns the chat-only steering for a chat spawn, nil without a renderer.
func (bc *BridgeCoordinator) renderChatSteering(ctx context.Context) []marotte.ClientSteeringDoc {
	if bc.chatSteering == nil {
		return nil
	}
	return bc.chatSteering(ctx)
}

// Bridge returns the bridge for chatID, or nil.
//
//nolint:revive // unexported-return: see OpenBridge above.
func (bc *BridgeCoordinator) Bridge(chatID marotte.ChatID) *sharedBridge {
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

// CloseBridge closes every turn the bridge hosted with outcome, then stops and removes it.
// The closer runs first, while the record still names the open turns.
func (bc *BridgeCoordinator) CloseBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	bc.closeTurnOnBridgeDeath(ctx, chatID, outcome)
	bc.bridge.mgr.close(chatID)
}

// RetireBridges closes idle chat bridges, marks busy ones for their next open and resets
// the utility session. A bridge hosting a live run counts as busy (retireChatBridges).
func (bc *BridgeCoordinator) RetireBridges(reason string) {
	victims, marked := bc.bridge.mgr.retireChatBridges()
	for _, chatID := range victims {
		bc.closeTurnsOnRetire(bc.lifecycle.shutdownCtx, chatID)
	}
	bc.retireUtility()
	slog.Info("identity changed; retiring live chat bridges",
		"reason", reason, "closed", len(victims), "marked", marked)
}

// replayProjector is the replay-projection lifecycle the coordinator drives.
type replayProjector interface {
	OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string)
	MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint)
	DiscardReplayProjection(marotte.ChatID)
	SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool)
}

// goForward starts a forward loop on the group Shutdown waits on: the loop's tail
// (closeTurnOnBridgeDeath) must land before shutdown completes. No deadlock: Shutdown
// stops every bridge before the wait, which ends the range. The attachment is taken before the
// goroutine runs: a settle that captured the old generation reads a later attach as a gone forward
// and drops its close.
func (bc *BridgeCoordinator) goForward(chatID marotte.ChatID, bridge ACPBridge) uint64 {
	gen := bc.turns.attachForward(chatID)
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, bridge, gen) })
	return gen
}

// forwardAt is Forward on an attachment the caller already took: tryLoadSession orders its
// read-loop position against it.
func (bc *BridgeCoordinator) forwardAt(chatID marotte.ChatID, bridge ACPBridge, gen uint64) {
	exited := bc.turns.exitFor(chatID, gen)
	ch := bridge.NotifCh()
	// The generation keeps a straggler from the previous bridge off a restarted counter.
	for n := range ch {
		bc.recordPool(bridge, n.Msg)
		bc.consumeFrame(chatID, gen, n)
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
		if bc.clearDecisions != nil {
			bc.clearDecisions(chatID)
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
}

// recordPool keeps bridge's MCP pool from its own status frames; only this loop knows the sender.
func (bc *BridgeCoordinator) recordPool(bridge ACPBridge, msg *marotte.RPCResponse) {
	if msg == nil || msg.ID != nil || msg.Method != methodV3MCPStatus {
		return
	}
	if pool, ok := translate.ReadMCPPool(msg); ok {
		bc.mcpRegistry.setPool(bridge, pool)
	}
}

// consumeFrame translates one frame, then advances the observed position (deferred, per
// frame): many paths consume a frame without touching a turn.
func (bc *BridgeCoordinator) consumeFrame(chatID marotte.ChatID, gen uint64, n marotte.Notification) {
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, n.Msg)
}

// Effort lives on the chat record (marotte.Chat.Effort), never a global keyed by the last model.

// NotifyPush sends a push about one chat, if push is configured.
func (bc *BridgeCoordinator) NotifyPush(ctx context.Context, body string, kind marotte.PushKind, chatID marotte.ChatID) {
	bc.NotifyPushSubject(ctx, body, kind, marotte.ChatSubject(chatID))
}

// NotifyPushSubject sends a push about any subject (chat, pull request, workflow run), if
// push is configured.
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
	var chatName string
	if subj.ChatID != "" {
		if rec, ok := bc.chatStore.Get(ctx, subj.ChatID); ok {
			chatName = rec.Name
		}
	}
	bc.lifecycle.inflight.Go(func() {
		bc.push.Send(ctx, push.DefaultTitle, body, kind, subj, chatName)
	})
}

// RetractPush drops any held push about subj, where a chat's ask is settled elsewhere.
// A run ask has none: its subject carries the run's outcome push.
func (bc *BridgeCoordinator) RetractPush(subj marotte.PushSubject) {
	if bc.push == nil {
		return
	}
	bc.push.Retract(subj)
}

// reportNoSubscribers logs once per episode that a notification had no subscriber; a
// subscriber re-arms it. Both subject halves are logged: either may be empty.
func (bc *BridgeCoordinator) reportNoSubscribers(kind marotte.PushKind, subj marotte.PushSubject) {
	if !bc.noSubscribers.CompareAndSwap(false, true) {
		return
	}
	slog.Info("no push subscribers; notifications are being dropped until a browser subscribes",
		"chat_id", subj.ChatID, "subject", subj.Key, "kind", string(kind))
}

// SettleTurnOnResponse closes turnID's turn on its settling response once the folder has
// consumed everything queued before it, unless the wire's turn_end got there first. seq
// is the response's read-loop position; zero skips the wait.
func (bc *BridgeCoordinator) SettleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptResponse, Resp: resp, Turn: turnID, Seq: seq})
}

// defaultAgentFinishedBody is the body when the agent never declared what it was doing.
const defaultAgentFinishedBody = "Agent finished"

// agentFinishedBodyFrom picks the body from a chat's self-declared description.
func agentFinishedBodyFrom(description string) string {
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return defaultAgentFinishedBody
}

// ApplyModelSwitch swaps the live bridge's model through session/set_config_option and
// re-applies the level. False with no bridge or a refused swap; never a restart.
func (bc *BridgeCoordinator) ApplyModelSwitch(ctx context.Context, chatID marotte.ChatID, model, effort string) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitch(ctx, chatID, sb, model, effort)
}

// applyModelSwitch swaps the model on a bridge the caller already holds, then re-applies the level.
func (bc *BridgeCoordinator) applyModelSwitch(
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
func (bc *BridgeCoordinator) repairEffort(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) {
	rec, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	if bc.catalog.ThinkingToggleable(rec.Model) && rec.Thinking != "" {
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
func (bc *BridgeCoordinator) healEffort(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		// The chat's own frame only: a step's session reports its own level.
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
func (bc *BridgeCoordinator) effortFor(ctx context.Context, rec *marotte.Chat) string {
	if rec.Effort != "" {
		return rec.Effort
	}
	if level := bc.effortSeedFor(ctx, rec.Model); level != "" {
		return level
	}
	return bc.catalog.DefaultEffortFor(rec.Model)
}

// sessionEffort is effortFor capped for thinking off, the level KAS reports there.
func (bc *BridgeCoordinator) sessionEffort(ctx context.Context, rec *marotte.Chat) string {
	level := bc.effortFor(ctx, rec)
	if bc.catalog.ThinkingToggleable(rec.Model) && rec.ThinkingIsOff(bc.catalog.ThinkingDefaultOff(rec.Model)) {
		return marotte.CapEffortForThinkingOff(level, rec.EffortLevels)
	}
	return level
}

// effortSeedFor returns the KeyLastEffortByModel entry for exactly `model`, else "".
// Per model, so one pick never retracts another model's level. The single seed read.
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

// EffortForSwitch resolves the level after a model switch: the target model's remembered
// level, else its catalog default, else "". Not effortFor: the stored choice was made
// under the model being left.
func (bc *BridgeCoordinator) EffortForSwitch(ctx context.Context, model string) string {
	if level := bc.effortSeedFor(ctx, model); level != "" {
		return level
	}
	return bc.catalog.DefaultEffortFor(model)
}

// PersistModelSwitch records a landed switch: the model_switched entry into the open turn
// (lanes sealed first) or after the newest close, none on an unstarted chat, then the header
// takes the pick, drops the old tier and resets usage. Detached context. Takes the payload
// because From, To and Effort are adjacent strings.
func (bc *BridgeCoordinator) PersistModelSwitch(ctx context.Context, chatID marotte.ChatID, sw marotte.EntryModelSwitched, contextSize int) {
	ctx = durable.Context(ctx)
	switch log, ok := bc.turns.foldTarget(chatID); {
	case ok:
		sealed, err := log.ModelSwitched(ctx, sw)
		translate.PublishSealed(ctx, broadcastFunc(bc.broadcast), chatID, "", sealed)
		if err != nil {
			slog.Error("switch_model: record the switch in the turn", "chat_id", chatID, "error", err)
		}
	case bc.conversationStarted(ctx, chatID):
		if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
			marotte.EntryKindModelSwitched, "", sw); err != nil {
			slog.Error("switch_model: record the switch between turns", "chat_id", chatID, "error", err)
		}
	}
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
func (bc *BridgeCoordinator) ThinkingDefaultOff(model string) bool {
	return bc.catalog.ThinkingDefaultOff(model)
}

// PersistEffortChange records a reader-asked tier change as a model_switched entry with
// From == To (marotte.EntryModelSwitched). Writes no header field. An empty model writes
// nothing (the renderer reads an empty To as `Context reset`), nor does an unstarted chat.
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
	if !bc.conversationStarted(ctx, chatID) {
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindModelSwitched, "", sw); err != nil {
		slog.Error("set_effort: record the change between turns", "chat_id", chatID, "error", err)
	}
}

// A between-turns append before the first prompt mints an event turn, which makes the empty chat
// render as a conversation; the header alone carries a switch made before then.
func (bc *BridgeCoordinator) conversationStarted(ctx context.Context, chatID marotte.ChatID) bool {
	c, ok := bc.chatStore.Get(ctx, chatID)
	return ok && c.TurnCount > 0
}

// PersistModeSwitch records a landed mode switch as a mode_switched entry, into the open
// turn (lanes sealed first) or after the newest close; an unstarted chat gets none. Writes no
// header field. Detached context.
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

// AbandonInFlightTurn closes a turn its prompt call could not finish. It waits for no
// read-loop position: no bracket is coming. Only `interrupted` or `cancelled` are legal
// stops; anything else becomes `interrupted` with a Warn.
func (bc *BridgeCoordinator) AbandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64) {
	if stop != marotte.StopReasonInterrupted && stop != marotte.StopReasonCancelled {
		slog.Warn("a prompt failure named a stop this close cannot conclude, so it concludes interrupted",
			"chat_id", chatID, "turn", turnID, "stop", stop)
		stop = marotte.StopReasonInterrupted
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptFailure, Stop: stop, Reason: reason, Kind: kind, Turn: turnID, Seq: seq})
}

// FinalizeLocalShellTurn appends a `!cmd` turn's output as its one text entry and closes it.
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

// WireTurnStart handles the engine's turn_start bracket: it binds a pending prompt turn,
// closes a bracketed own turn whose turn_end was lost, or opens a wire_turn_start turn.
// The bind is provisional; the first agentInitiated frame revises it (ReviseTurnBinding).
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

// WireTurnEnd handles the engine's turn_end bracket. With no own turn it is a no-op,
// never a phantom turn. Live path only: replay is filtered upstream.
func (bc *BridgeCoordinator) WireTurnEnd(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerWireEnd, Stop: stop, Own: true})
}

// TurnFoldTarget returns the accumulator the chat's own frames fold into, opening a
// wire_turn_start turn when none is open; nil when refused. Never for a step frame.
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

// OwnTurn returns the chat's own open turn without opening one.
func (bc *BridgeCoordinator) OwnTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.foldTarget(chatID)
}

// PromptTurn returns the chat's prompt-class turn awaiting or holding its bracket.
func (bc *BridgeCoordinator) PromptTurn(chatID marotte.ChatID) (*turnlog.Turn, bool) {
	return bc.turns.promptTurn(chatID)
}

// AppendBetweenTurns files a lane-less entry after the newest turn's close.
func (bc *BridgeCoordinator) AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) (*marotte.Entry, error) {
	return bc.chatStore.AppendBetweenTurns(ctx, chatID, e)
}

// ReviseTurnBinding acts on a frame proving the open turn is the agent's own
// (`agentInitiated` rides content frames only): it opens the agent turn as own via
// openLocked and drops the prompt's turn back to pending. Sealed entries stay put.
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

// closeTurnOnBridgeDeath closes everything the bridge hosted: the chat's own and pending
// turns, every hosted run's open step turns (run lock taken after the chat's, never
// nested), then the unread steers. The second claim on a closed turn loses.
func (bc *BridgeCoordinator) closeTurnOnBridgeDeath(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
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
// OpenBridge: it closes own only when wire_turn_start, never pending, plus hosted runs;
// parked steers belong to the unstarted prompt.
func (bc *BridgeCoordinator) closeTurnsOnRetire(ctx context.Context, chatID marotte.ChatID) {
	if t, ok := bc.turns.ownTurn(chatID); ok && t.Source == marotte.TurnSourceWireTurnStart {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: t.ID})
	}
	bc.closeHostedRuns(ctx, chatID, marotte.StopReasonInterrupted)
}

// closeHostedRuns closes every open step turn of the runs this chat's bridge hosted and announces their sealed entries.
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

// claimShutdownCause names shutdown as the cause of every open turn on the chat, first-wins.
func (bc *BridgeCoordinator) claimShutdownCause(chatID marotte.ChatID) {
	for _, t := range bc.turns.openTurnIDs(chatID) {
		bc.turns.interrupt(chatID, t.ID, shutdownInterruptCause)
	}
}

// dropUnreadSteers records each agent row KAS queued but this process never received as an
// EMPTY-TEXT steer entry, the reconcile signal the next session/load reads, and reports the user rows' buffer gone.
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

// recordSteer writes a minted steer entry into the open turn (lanes sealed first), else after the newest close.
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

// InterruptTurn records why kiro-cli abandoned a turn and trips its prompt call so the
// failure path finalizes it; the cause is first-wins on that turn. The bridge stays alive.
// No turn, no bridge or a claimed cause is not a failure.
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

// BridgeRespond answers an ACP request on the chat's bridge; a no-op when it has none.
func (rt *Runtime) BridgeRespond(ctx context.Context, chatID marotte.ChatID, requestID int64, result any, err error) error {
	sb := rt.bridge.mgr.get(chatID)
	if sb == nil {
		return nil
	}
	return sb.bridge.Respond(ctx, requestID, result, err)
}

// ParentACPSession returns the running bridge's ACP session id for chatID, or "".
func (bc *BridgeCoordinator) ParentACPSession(chatID marotte.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
