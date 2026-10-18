package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	// preSessionOpen is installed by SetPreSessionOpen.
	preSessionOpen func(context.Context) `wiring:"optional"`
	// chatSteering renders the chat-only steering both chat session doors send; never a run host or an observer.
	chatSteering func(context.Context) []marotte.ClientSteeringDoc `wiring:"optional"`
	// reconcileSessions marks every chat for reload when a setting read only at a session door moved, so it
	// runs before this open checks the mark. It waits no later than its deadline.
	reconcileSessions func(context.Context, time.Time)
	// engine is the one agent engine every chat session opens on.
	engine *engineOwner
	// sessionOpts is what a chat's session door sends.
	sessionOpts func(ctx context.Context, rec *marotte.Chat, ask *sessionAsk, steering []marotte.ClientSteeringDoc) *marotte.SessionOpts
	// syncLive pushes the engine each live setting it has not confirmed.
	syncLive func(context.Context)
	// replayProjection is the session/load replay lifecycle; nil in tests without a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, off the session-open path, to heal paused runs.
	onSessionRehydrated func(marotte.ChatID)
	// reapSession removes one KAS session's on-disk state.
	reapSession func(sessionID string)
	// dischargeWaiting drops a retained waiting_on_user claim when its agent dies. Nil in tests.
	dischargeWaiting func(context.Context, marotte.ChatID)
	// endAsks retires the unanswered asks an ended bridge carried: nothing can receive their answers.
	endAsks func(origin acpResponder)
	// observeLaneFrame reports each frame a session's forward consumed to the run-mutation registry, which settles
	// a host-session verb only once its lane read past the reply; forgetLane drops that position when the forward
	// ends. Nil in tests.
	observeLaneFrame func(origin acpResponder, seq uint64, msg *marotte.RPCResponse)
	forgetLane       func(origin acpResponder)
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
	steerTurnOpened  func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnBound   func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerTurnRevised func(chatID marotte.ChatID, turnID string) `wiring:"optional"`
	steerBridgeGone  func(marotte.ChatID)                       `wiring:"optional"`
	// chatHasLiveRun keeps pushTurnOutcome from claiming the work is over while a launched run continues. Nil means none.
	chatHasLiveRun func(marotte.ChatID) bool `wiring:"optional"`
	// chatHasTab reports an open tab on the chat; it answers true when no tab set is wired, so nothing is released.
	chatHasTab func(marotte.ChatID) bool
	// runLabel is the name a run's tab shows, for a notification titled by it. Nil names none.
	runLabel func(workflowID string) string `wiring:"optional"`
	// unknownStops records stop reasons already warned about: one line per unmapped value.
	unknownStops sync.Map
	// noSubscribers latches the no-subscriber report once per episode (reportNoSubscribers).
	noSubscribers atomic.Bool
	// idleEndGrace is WireTurnActivity's wait, zero meaning defaultIdleEndGrace; a field so a test shortens it.
	idleEndGrace time.Duration
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
		push:        h.push,
		mcpRegistry: h.mcpRegistry,
		lifecycle:   h.lifecycle,
		engine:      h.engine,
		sessionOpts: h.chatSessionOpts,
		syncLive:    func(ctx context.Context) { h.engine.syncLive(ctx, h.liveSettings) },
		// h implements replayProjector (load_projection.go).
		replayProjection: h.replay,
		chatHasLiveRun: func(chatID marotte.ChatID) bool {
			return chatHoldsLiveRun(h.runs.leaseStore().List(), chatID)
		},
		chatHasTab: func(chatID marotte.ChatID) bool {
			if h.tabs == nil {
				return true
			}
			open, _ := h.tabs.List()
			return slices.ContainsFunc(open, func(t marotte.TabSubject) bool {
				return t.Kind == marotte.TabKindChat && marotte.ChatID(t.Ref) == chatID
			})
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
			// Its own goroutine: a bridge Call has no deadline and a closer must not wait on one. Gated, since a
			// closer need not run on inflight; Shutdown stops every bridge, so a refused clear has nothing to clear.
			h.lifecycle.goUnlessDraining(func() {
				ctx, cancel := h.lifecycle.derivedContext()
				defer cancel()
				h.runs.clearStaleNotices(ctx, chatID)
			})
		},
		dischargeWaiting: h.DischargeWaiting,
		endAsks:          h.bus.endAsksOf,
		observeLaneFrame: func(origin acpResponder, seq uint64, msg *marotte.RPCResponse) {
			h.runs.mutations.observeSessionFrame(laneRef{session: origin}, seq, msg, h.router.StepRun)
		},
		forgetLane:        func(origin acpResponder) { h.runs.mutations.forgetLane(laneRef{session: origin}) },
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
		steerTurnOpened:  h.bus.steers.turnOpened,
		steerTurnBound:   h.bus.steers.turnBound,
		steerTurnRevised: h.bus.steers.turnRevised,
		steerBridgeGone:  h.bus.steers.bridgeGone,
	}
}

// openBridge returns chatID's session once its generation is ready (sessionSettle), opening one when
// none is registered. The door runs under the runtime's lifetime, since a cancelled one would detach
// the chat's session; ctx bounds only this caller's wait. A failed generation answers its failure and
// is already gone, so the next call opens a fresh one. Before it returns it sends the engine each
// live setting it has not confirmed, waiting no later than ignorePushTimeout after the call.
func (bc *bridgeCoordinator) openBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, error) {
	syncBy := time.Now().Add(ignorePushTimeout)
	sb, err := bc.openBridgeBy(ctx, chatID, modelOverride, syncBy)
	if err != nil {
		return nil, err
	}
	syncCtx, cancel := context.WithDeadline(ctx, syncBy)
	bc.syncLive(syncCtx)
	cancel()
	return sb, nil
}

func (bc *bridgeCoordinator) openBridgeBy(ctx context.Context, chatID marotte.ChatID, modelOverride string, syncBy time.Time) (*sharedBridge, error) {
	bc.reconcileSessions(ctx, syncBy)
	for {
		sb, existed := bc.bridge.mgr.orInsert(chatID)
		if !existed {
			bc.lifecycle.inflight.Go(func() { bc.openGeneration(bc.lifecycle.shutdownCtx, chatID, sb, modelOverride) })
		}
		if err := sb.settled.wait(ctx); err != nil {
			return nil, err
		}
		reopen, err := bc.reloadIfMarked(ctx, chatID, sb)
		if err != nil {
			return nil, err
		}
		if !reopen {
			if existed {
				bc.repairEffort(ctx, chatID, sb)
			}
			return sb, nil
		}
	}
}

// reloadIfMarked closes sb's session once it is due its reload (bridgeManager.claimReload), so the next generation
// loads it with the current door settings, and reports whether the caller must open that generation. A refused close
// keeps the session and its mark, and the chat runs on it until a later open retries; an unconfirmed one fails this
// open, and the next door waits for the close to settle. A session another reload or a release is closing is
// waited out, then the caller asks again, so no open hands out a session being closed.
func (bc *bridgeCoordinator) reloadIfMarked(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) (reopen bool, err error) {
	claim := bc.bridge.mgr.claimReload(chatID, sb)
	session := claim.session
	switch {
	case claim.busy != nil:
		select {
		case <-claim.busy:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	case claim.stale:
		return true, nil
	case !claim.due:
		return false, nil
	case session == nil || !session.Open():
		bc.bridge.mgr.finishReload(chatID, sb, true)
		return true, nil
	}
	closeErr := session.Close(ctx)
	switch {
	case closeErr == nil:
		bc.bridge.mgr.finishReload(chatID, sb, true)
		slog.Info("chat session closed for its reload", "chat_id", chatID, "session_id", session.SessionID())
		return true, nil
	case session.Open():
		bc.bridge.mgr.finishReload(chatID, sb, false)
		slog.Warn("a chat's session refused its reload; it keeps its current settings until the next message",
			"chat_id", chatID, "error", closeErr)
		return false, nil
	}
	bc.bridge.mgr.finishReload(chatID, sb, true)
	if errors.Is(closeErr, marotte.ErrSessionClosing) {
		return false, closeErr
	}
	return false, fmt.Errorf("%w: %w", marotte.ErrSessionClosing, closeErr)
}

// releaseIfUnheld closes chatID's resident chat session once nothing holds it: no open tab, no live turn or
// admission, no live run it launched and no run step it hosts. The turn check follows the claim and an open waits
// on a claimed entry (reloadIfMarked), so a turn admitted after the check opens a fresh generation once the close
// lands. Run host sessions are releaseEndedHost's. The close runs on the lifetime, since the edges that call this
// hold the caller's goroutine.
func (bc *bridgeCoordinator) releaseIfUnheld(chatID marotte.ChatID) {
	if chatID == "" || isRunChat(chatID) || bc.chatHasTab == nil || bc.chatHasTab(chatID) ||
		(bc.chatHasLiveRun != nil && bc.chatHasLiveRun(chatID)) {
		return
	}
	sb, session, ok := bc.bridge.mgr.claimRelease(chatID)
	if !ok {
		return
	}
	if bc.turns.live(chatID) {
		bc.bridge.mgr.finishReload(chatID, sb, false)
		return
	}
	if !bc.lifecycle.goUnlessDraining(func() { bc.closeReleased(chatID, sb, session) }) {
		bc.bridge.mgr.finishReload(chatID, sb, false)
	}
}

func (bc *bridgeCoordinator) closeReleased(chatID marotte.ChatID, sb *sharedBridge, session engineSession) {
	ctx, cancel := bc.lifecycle.derivedContext()
	defer cancel()
	err := session.Close(ctx)
	bc.bridge.mgr.finishReload(chatID, sb, err == nil || !session.Open())
	if err != nil && session.Open() {
		slog.Warn("a chat's unused session refused its close; it stays until its next release check",
			"chat_id", chatID, "error", err)
		return
	}
	slog.Info("closed a chat session nothing holds", "chat_id", chatID, "session_id", session.SessionID())
}

// LoadSession answers once chatID's bridge generation is ready (openBridge).
func (bc *bridgeCoordinator) LoadSession(ctx context.Context, chatID marotte.ChatID) error {
	_, err := bc.openBridge(ctx, chatID, "")
	return err
}

func (bc *bridgeCoordinator) openGeneration(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, modelOverride string) {
	rec, exists := bc.chatStore.Get(ctx, chatID)
	if !exists {
		bc.failGeneration(chatID, sb, errChatUnreadable)
		return
	}
	// A pick parked with no bridge lands here: this is the last header read before the session takes its model.
	if rec.PendingModel != "" {
		bc.foldParkedPick(ctx, chatID)
		if rec, exists = bc.chatStore.Get(ctx, chatID); !exists {
			bc.failGeneration(chatID, sb, errChatUnreadable)
			return
		}
	}

	// No mcpServers parameter: KAS reads the config file marotte renders (internal/mcp/kasfile.go),
	// and an inline copy would win over the file.
	if bc.preSessionOpen != nil {
		bc.preSessionOpen(ctx)
	}

	ask := bc.sessionChoices(ctx, chatID, rec, modelOverride)
	held := bc.holdUnservedModel(ctx, chatID, ask.withheld)
	steering := bc.renderChatSteering(ctx)

	if rec.ACPSessionID != "" {
		so := bc.sessionOpts(ctx, rec, &ask, steering)
		so.SessionID = rec.ACPSessionID
		loaded, err := bc.tryLoadSession(ctx, chatID, sb, so, held)
		switch {
		case loaded:
			bc.settleResumed(ctx, chatID, sb, err)
			return
		case err != nil:
			bc.restoreUnservedModel(ctx, chatID, held)
			bc.failGeneration(chatID, sb, err)
			return
		}
		bc.replaceFailedLoad(ctx, rec, &ask)
	}

	// The forward attaches before the door writes: KAS's requests during session/new must be answered for it to
	// finish.
	session, err := bc.engine.openSession(ctx, bc.sessionOpts(ctx, rec, &ask, steering), func(s engineSession) {
		sb.attach(s)
		bc.goForward(chatID, s)
	})
	if err != nil {
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		return
	}
	if err := bc.persistNewSessionMetadata(ctx, chatID, session, ask.first); err != nil {
		sessionID := string(session.SessionID())
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		// A session no record holds is in no delete's chain, so this generation reaps it itself.
		bc.reapSession(sessionID)
		return
	}
	bc.commitUnservedModel(ctx, chatID, held, session.ModelID())
	// The session door's half of the supervised fail-open (the door's own assert is best-effort).
	bc.reportSupervisedNotApplied(ctx, chatID, rec.SupervisedMode, session.SupervisedApplied())
	sb.setIdle()
	bc.publishGeneration(ctx, chatID, sb)
}

// errChatUnreadable is a session open that could not read the record it opens; failGeneration decides
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

// failGeneration publishes a session open's failure, as ErrChatGone when the record is absent.
func (bc *bridgeCoordinator) failGeneration(chatID marotte.ChatID, sb *sharedBridge, err error) {
	if bc.chatStore.Presence(chatID) == chat.PresenceAbsent {
		err = fmt.Errorf("%w: %s", command.ErrChatGone, chatID)
	}
	bc.retireGeneration(chatID, sb, err)
}

// retireGeneration removes a failed generation and closes its session before its error publishes, so the next open
// starts fresh; no caller holds it, since openBridge admits none before readiness.
func (bc *bridgeCoordinator) retireGeneration(chatID marotte.ChatID, sb *sharedBridge, err error) {
	if bc.bridge.mgr.removeIfSame(chatID, sb) {
		if s := sb.current(); s != nil && s.Open() {
			slog.Info("a chat's session never reached its log; it was closed", "chat_id", chatID, "error", err)
			bc.bridge.mgr.closeSession(s)
		}
	}
	sb.settled.settle(err)
}

// sessionAsk is what a session door sends. effort is the level capped for thinking off.
// loadKeepsLevel marks a load sending none, whose replacement replaceFailedLoad resolves.
type sessionAsk struct {
	model, effort, thinking, withheld string
	first                             firstLevel
	loadKeepsLevel                    bool
}

// firstLevel is the uncapped preference a record holding no level for model takes once a session
// opens on it (onto). The session is sent sessionAsk.effort, this capped for thinking off.
type firstLevel struct{ model, level string }

// onto is the record's level once a session opened on running, the record holding current for
// prevModel: the first level when it was resolved for running and the record holds none for it,
// none when the session runs another model than the record's level was for.
func (f firstLevel) onto(current, prevModel, running string) string {
	switched := prevModel != "" && prevModel != running
	switch {
	case f.level != "" && f.model == running && (current == "" || switched):
		return f.level
	case switched:
		return ""
	}
	return current
}

// sessionChoices resolves what a door sends: the record's level capped for thinking off, or on a
// switch-by-restart resolveEffort's move onto the target. A record holding none opens a new
// session at levelRanOrFirst's level and loads a session sending none, so the session keeps its
// own level, which the record then adopts.
func (bc *bridgeCoordinator) sessionChoices(
	ctx context.Context, chatID marotte.ChatID, rec *marotte.Chat, modelOverride string,
) sessionAsk {
	ask := sessionAsk{model: rec.Model}
	if modelOverride != "" && modelOverride != marotte.ModelAuto {
		ask.model = modelOverride
	}
	// Withhold a model the account cannot run: it is inherited, not a fresh pick (refused in
	// cmdSwitchModel). An empty advertised set allows.
	if !marotte.ModelServed(ask.model, rec.ServedModelIDs) {
		slog.Warn("withholding a model this account does not serve; using the backend default",
			"chat_id", chatID, "model", ask.model)
		ask.withheld, ask.model = ask.model, ""
	}

	target := cmp.Or(ask.model, rec.Model)
	if bc.catalog.thinkingToggleable(target) {
		ask.thinking = rec.Thinking
	}
	switch {
	case ask.model != "" && ask.model != rec.Model:
		// A differing override is switch-by-restart: the target model's first session.
		in := bc.effortInputFor(ctx, rec, ask.model, "")
		d := resolveEffort(&in)
		ask.first = firstLevel{model: ask.model, level: d.pref}
		ask.effort = d.sent
	case rec.Effort != "":
		ask.effort = bc.capForThinking(rec, target, rec.Effort)
	case rec.ACPSessionID == "":
		var pref string
		pref, ask.effort = bc.levelRanOrFirst(ctx, rec)
		ask.first = firstLevel{model: rec.Model, level: pref}
	default:
		// The load sends none so KAS restores the session's own level.
		ask.loadKeepsLevel = true
	}
	return ask
}

// replaceFailedLoad turns ask, a failed load of rec's session, into the session/new replacing it:
// a load that sent no level opens at levelRanOrFirst's level, resolved only now so a load that
// succeeds never reads the seed.
func (bc *bridgeCoordinator) replaceFailedLoad(ctx context.Context, rec *marotte.Chat, ask *sessionAsk) {
	if !ask.loadKeepsLevel {
		return
	}
	var pref string
	pref, ask.effort = bc.levelRanOrFirst(ctx, rec)
	ask.loadKeepsLevel = false
	ask.first = firstLevel{model: rec.Model, level: pref}
}

// levelRanOrFirst is the level rec, holding none, takes for a new session on its model: the one
// its last session reported running, else resolveEffort's first level for the model. It returns
// the uncapped preference and the level sent, capped for thinking off.
func (bc *bridgeCoordinator) levelRanOrFirst(ctx context.Context, rec *marotte.Chat) (pref, sent string) {
	if marotte.EffortLevel(rec.EffortActive).Valid() {
		return rec.EffortActive, bc.capForThinking(rec, rec.Model, rec.EffortActive)
	}
	in := bc.effortInputFor(ctx, rec, rec.Model, "")
	d := resolveEffort(&in)
	return d.pref, d.sent
}

// unservedSelection is a model the session opens without, with the record's effort for it.
// onRecord is false for a withheld override the record never held; only a held one is written back.
type unservedSelection struct {
	model, effort string
	onRecord      bool
}

// holdUnservedModel takes the model off the record before the session opens so a racing
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

// restoreUnservedModel puts a held selection back after a failed session open, so the next open
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
			&marotte.EntryModelSwitched{From: held.model, To: to, Reason: marotte.ModelSwitchReasonUnavailable})
	}
	if !held.onRecord {
		return
	}
	effort := ""
	if to == held.model {
		effort = held.effort
	}
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		// Another value is a pick that landed during the session open.
		if !ex || (c.Model != "" && c.Model != held.model && c.Model != to) {
			return false
		}
		c.MoveToModel(to)
		c.Effort = effort
		return true
	}); err != nil {
		slog.Error("unserved model: persist the running model", "chat_id", chatID, "error", err)
	}
}

// reportSupervisedNotApplied tells the user the session refused `autopilot: off`. The
// record keeps the request, so the next session door re-asserts it.
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

// tryLoadSession loads so.SessionID for chatID. An error wraps ErrBridgeExited and keeps the chat's session for the
// next open: the engine ended under the load and its one retry, or no engine became ready before ctx ended. Every
// other failure (a KAS refusal, the replay budget, an unavailable engine, a session still resident or closing)
// returns loaded false with a nil error and detaches the chat, so the generation opens session/new.
func (bc *bridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, so *marotte.SessionOpts, held unservedSelection,
) (loaded bool, ready error) {
	// Open the projection first: KAS replays inside the door. The forward attaches before the door writes; a failed
	// load closed its session, and the generation still owns its entry (bridgeManager.removeIfBridge).
	acpSessionID := so.SessionID
	replayed := newSessionSettle()
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, acpSessionID, replayed)
	}
	session, gen, err := bc.loadOnEngine(ctx, chatID, sb, so, replayed)
	if err != nil {
		// A failed load must not keep a partial replay.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		if errors.Is(err, marotte.ErrBridgeExited) {
			return false, err
		}
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		bc.detachSession(ctx, chatID)
		return false, nil
	}
	bc.markReplayLoaded(chatID, gen, session, replayed)
	title := session.SessionTitle()
	bc.catalog.setModes(session.Modes())
	bc.catalog.SetModels(session.Models())
	var renameTo string
	var dropped unservedSelection
	if _, mErr := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		renameTo, dropped = applyLoadedSessionFacts(c, session, title)
		return true
	}); mErr != nil {
		slog.Error("refresh session metadata", "chat_id", chatID, "error", mErr)
	}
	if held.model == "" {
		held = dropped
	}
	bc.commitUnservedModel(ctx, chatID, held, session.ModelID())
	reassertUserName(ctx, chatID, session, renameTo)
	sb.setIdle()
	// The generation is ready only once its replay merged.
	return true, replayed.wait(bc.lifecycle.shutdownCtx)
}

// loadOnEngine runs chatID's session/load and reports the forward attachment its lane position is comparable within
// (replay_drain.go). A load the engine's end failed is still KAS's to load (design 4.5.3), so it runs again on the next
// engine, once the first attempt's forward has folded what it replayed, into a fresh projection.
func (bc *bridgeCoordinator) loadOnEngine(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, so *marotte.SessionOpts, replayed *sessionSettle,
) (session engineSession, gen uint64, err error) {
	var forwardDone <-chan struct{}
	attach := func(s engineSession) {
		sb.attach(s)
		gen = bc.goForward(chatID, s)
		forwardDone = bc.turns.forwardExit(chatID)
	}
	session, engineGen, err := bc.engine.loadSessionAfter(ctx, 0, so, attach)
	if !errors.Is(err, marotte.ErrBridgeExited) || engineGen == 0 {
		return session, gen, err
	}
	slog.Info("the agent engine ended under a session load; loading again on the next engine",
		"chat_id", chatID, "acp_session", so.SessionID, "generation", engineGen)
	if forwardDone != nil {
		select {
		case <-forwardDone:
		case <-ctx.Done():
		}
	}
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, so.SessionID, replayed)
	}
	session, _, err = bc.engine.loadSessionAfter(ctx, engineGen, so, attach)
	return session, gen, err
}

// detachSession blanks chatID's session id after a load that failed other than by the engine's end, so the
// generation opens session/new. The session stays in the chain: blanking the id is what makes the reaper sweep its
// transcript.
func (bc *bridgeCoordinator) detachSession(ctx context.Context, chatID marotte.ChatID) {
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", err)
	}
}

// markReplayLoaded records the read-loop position replay completion is measured against, with one
// settle attempt (MarkReplayLoadedAt); the projection's swap settles replayed. With no projection
// nothing was replayed.
func (bc *bridgeCoordinator) markReplayLoaded(chatID marotte.ChatID, gen uint64, session engineSession, replayed *sessionSettle) {
	if bc.replayProjection == nil {
		replayed.settle(nil)
		return
	}
	bc.replayProjection.MarkReplayLoadedAt(chatID, drainPoint{gen: gen, seq: session.SessionLoadSeq()})
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

// persistNewSessionMetadata records the new session on the chat, pinning first (firstLevel.onto).
// An error is a record that did not take it, wrapping command.ErrChatGone when the record is absent.
func (bc *bridgeCoordinator) persistNewSessionMetadata(ctx context.Context, chatID marotte.ChatID, bridge newSessionFacts, first firstLevel) error {
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
		prevModel := c.Model
		if newModelID != "" {
			c.MoveToModel(string(newModelID))
		}
		c.Effort = first.onto(c.Effort, prevModel, c.Model)
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
// record keeps the actual mode, so the next session door asks for the current one.
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

// renderChatSteering returns the chat-only steering for a chat's session door, nil without a renderer.
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

// HasResidentSession reports whether a chat has a resident session; retention never purges such a chat (archive.WithLiveChats).
func (rt *Runtime) HasResidentSession(chatID marotte.ChatID) bool {
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

// closeBridge closes every turn the chat's session hosted with outcome, then removes and closes the session.
// The closer runs first, while the record still names the open turns.
func (bc *bridgeCoordinator) closeBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	bc.closeTurnOnBridgeDeath(ctx, chatID, outcome)
	bc.bridge.mgr.close(chatID)
}

type replayProjector interface {
	OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string, settled *sessionSettle)
	MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint)
	DiscardReplayProjection(marotte.ChatID)
	SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool)
}

// goForward starts a forward loop on the group Shutdown waits on: the loop's tail
// (closeTurnOnBridgeDeath) must land before shutdown completes. No deadlock: Shutdown
// stops the engine before the wait, which closes every lane and ends the range. The attachment is taken before the
// goroutine runs: a settle that captured the old generation reads a later attach as a gone forward
// and drops its close.
func (bc *bridgeCoordinator) goForward(chatID marotte.ChatID, bridge engineSession) uint64 {
	att := bc.turns.attachForward(chatID)
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, bridge, att) })
	return att.gen
}

// forwardAt is Forward on an attachment the caller already took: tryLoadSession orders its
// read-loop position against it.
func (bc *bridgeCoordinator) forwardAt(chatID marotte.ChatID, bridge engineSession, att forwardAttachment) {
	gen := att.gen
	ch := bridge.NotifCh()
	// The generation keeps a straggler from the previous bridge off a restarted counter.
	for n := range ch {
		bc.recordPool(n.Msg)
		bc.consumeFrame(chatID, bridge, gen, n)
		if bc.observeLaneFrame != nil {
			bc.observeLaneFrame(bridge, n.Seq, n.Msg)
		}
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
	att.exited()
	if bc.forgetLane != nil {
		bc.forgetLane(bridge)
	}
	// For every end, before the death closer, whose turn close would drop the cards without saying why.
	bc.endAsks(bridge)

	// Still registered means the session ended on its own (the engine ended, or its lane overflowed), so this closer
	// owns the open turn.
	endedItself := bc.bridge.mgr.removeIfBridge(chatID, bridge)
	cause := "closed"
	if endedItself {
		cause = "ended"
	}
	slog.Info("bridge exited", "chat_id", chatID, "session_id", bridge.SessionID(), "cause", cause)
	if endedItself {
		bc.closeTurnOnBridgeDeath(bc.lifecycle.shutdownCtx, chatID, marotte.TurnOutcomeInterrupted)
		// A waiting_on_user claim is owed to the agent, which is gone.
		if bc.dischargeWaiting != nil {
			bc.dischargeWaiting(bc.lifecycle.shutdownCtx, chatID)
		}
	}
	// A run chat has no record and nothing calls cleanupChatState for one.
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
	// Last, for every end (tab close, reload, engine end): KAS's own dispose releases them on a session that is
	// already closed, and no later session can see, read or stop them.
	bc.releaseTerminals(chatID, bridge)
}

// recordPool keeps the engine's MCP pool from any session's status copy.
func (bc *bridgeCoordinator) recordPool(msg *marotte.RPCResponse) {
	if msg == nil || msg.ID != nil || msg.Method != methodV3MCPStatus {
		return
	}
	if pool, ok := translate.ReadMCPPool(msg); ok {
		bc.mcpRegistry.setPool(pool)
	}
}

// consumeFrame translates one frame, then advances the observed position (deferred, per
// frame): many paths consume a frame without touching a turn.
func (bc *bridgeCoordinator) consumeFrame(chatID marotte.ChatID, origin acpResponder, gen uint64, n marotte.Notification) {
	bc.turns.beginFrame(chatID, gen)
	defer bc.turns.observe(chatID, gen, n.Seq)
	bc.translateEvent(chatID, origin, n.Msg)
}

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
	// Gated, since a caller need not run on inflight (the PR status poller outlives Shutdown). Once draining the send
	// still goes, unwaited: push.Close in Shutdown unblocks it and owns its writes.
	send := func() { bc.push.Send(ctx, n) }
	if !bc.lifecycle.goUnlessDraining(send) {
		go send()
	}
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
func (bc *bridgeCoordinator) applyModelSwitch(ctx context.Context, chatID marotte.ChatID, mv *effortDecision) bool {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return false
	}
	return bc.applyModelSwitchOn(ctx, chatID, sb, mv)
}

// applyModelSwitchOn swaps the model on a bridge the caller already holds, then asserts mv.sent,
// thinking first when the move turns it on.
func (*bridgeCoordinator) applyModelSwitchOn(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, mv *effortDecision,
) bool {
	if err := sb.bridge.SetModel(ctx, mv.to); err != nil {
		slog.Info("model switch: the session refused the swap",
			"chat_id", chatID, "model", mv.to, "error", err)
		return false
	}
	// Thinking before the level: KAS caps a high tier while thinking is off. Best-effort.
	if mv.thinkingOn {
		if err := sb.bridge.EnsureThinking(ctx, marotte.ThinkingOn); err != nil {
			slog.Warn("model switch: thinking not turned back on after the swap",
				"chat_id", chatID, "model", mv.to, "error", err)
		}
	}
	// Re-assert the level: KAS reconciles against the new model's tiers (2.19.1: a swap to
	// `auto` destroys it). Best-effort.
	if mv.sent != "" {
		if err := sb.bridge.EnsureEffort(ctx, mv.sent); err != nil {
			slog.Warn("model switch: reasoning effort not re-applied after the swap",
				"chat_id", chatID, "model", mv.to, "effort", mv.sent, "error", err)
		}
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", mv.to, "effort", mv.sent)
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
	level := bc.sessionEffort(rec)
	if level == "" {
		return
	}
	if err := sb.bridge.EnsureEffort(ctx, level); err != nil {
		slog.Warn("reasoning effort not re-applied on the open session",
			"chat_id", chatID, "effort", level, "error", err)
	}
}

// healEffort wraps the config-option handler to re-assert the chosen level when a
// config_option_update reports another: the turn that opened the session never reaches repairEffort, and
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
		// A swap the session already runs while the record still holds the old model's level is
		// a switch in flight, which asserts its own level (applyModelSwitchOn); a heal would race
		// it with the old one and spend the latch. A pick merely parked behind a turn still heals.
		want := bc.sessionEffort(rec)
		switching := rec.PendingModel != "" && rec.PendingModel == rec.Model
		if switching || want == "" || running == "" || want == running {
			return
		}
		if !sb.claimEffortHeal() {
			slog.Debug("reasoning effort still diverges after one repair; leaving it to the next prompt",
				"chat_id", chatID, "want", want, "running", running)
			return
		}
		slog.Info("re-asserting the chat's reasoning effort: the session reported a different level",
			"chat_id", chatID, "want", want, "running", running)
		// On inflight: Shutdown stops the engine before waiting on this group, which is what unblocks a stuck Call.
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

// sessionEffort is the level an open session is held to: the record's, capped for thinking off.
// Never the seed, which another chat's pick moves.
func (bc *bridgeCoordinator) sessionEffort(rec *marotte.Chat) string {
	return bc.capForThinking(rec, rec.Model, rec.Effort)
}

// capForThinking caps level as KAS does while model's thinking is off: asserting xhigh or max
// there would re-enable thinking.
func (bc *bridgeCoordinator) capForThinking(rec *marotte.Chat, model, level string) string {
	if off, levels := bc.thinkingOffFor(rec, model); off {
		return marotte.CapEffortForThinkingOff(level, levels)
	}
	return level
}

// thinkingOffFor reports whether model's session runs with thinking off for rec, with the tiers
// KAS caps xhigh and max against there. rec's reported thinking and tiers are rec.Model's only,
// so another model reads the chat's choice, its default and its catalog cap.
func (bc *bridgeCoordinator) thinkingOffFor(rec *marotte.Chat, model string) (bool, []marotte.SessionEffortLevel) {
	if !bc.catalog.thinkingToggleable(model) {
		return false, nil
	}
	defaultOff := bc.catalog.thinkingDefaultOff(model)
	switch {
	case model != rec.Model:
		if !rec.ThinkingChosenOff(defaultOff) {
			return false, nil
		}
	case !rec.ThinkingIsOff(defaultOff):
		return false, nil
	case len(rec.EffortLevels) != 0:
		return true, rec.EffortLevels
	}
	return true, bc.catalog.thinkingOffCap(model)
}

// Per model, so one pick never retracts another model's level. Validated because config.json
// is user-editable.
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

// persistModelSwitch records a landed switch: the model_switched entry into the open turn
// (lanes sealed first) or after the newest close, none on an unstarted chat, then the header
// lands the move (landOn) and resets usage. Detached context.
func (bc *bridgeCoordinator) persistModelSwitch(ctx context.Context, chatID marotte.ChatID, mv *effortDecision, contextSize int) {
	ctx = durable.Context(ctx)
	bc.appendModelSwitched(ctx, chatID, "switch_model", &marotte.EntryModelSwitched{
		From: mv.from, To: mv.to, Effort: mv.sent, Unoffered: mv.unoffered,
	})
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		mv.landOn(c)
		c.Usage = marotte.Usage{ContextSize: contextSize}
		return true
	}); err != nil {
		slog.Error("switch_model: persist model", "chat_id", chatID, "error", err)
		return
	}
	bc.rememberPick(ctx, mv)
}

// ThinkingDefaultOff reports the catalog's default-thinking fact for model.
func (bc *bridgeCoordinator) ThinkingDefaultOff(model string) bool {
	return bc.catalog.thinkingDefaultOff(model)
}

// EffortSeedable answers from the catalog's tiers for model; an unknown model keeps any tier,
// as resolveEffort does.
func (bc *bridgeCoordinator) EffortSeedable(model string, level marotte.EffortLevel) bool {
	tiers := bc.catalog.tiersOf(model)
	return tiers.known != tiersListed || slices.Contains(tiers.levels, string(level))
}

// PersistEffortChange records a reader-asked tier change as a model_switched entry with
// From == To (marotte.EntryModelSwitched). Writes no header field. An empty model writes
// nothing (the renderer reads an empty To as `Context reset`), nor does an unstarted chat.
func (bc *bridgeCoordinator) PersistEffortChange(ctx context.Context, chatID marotte.ChatID, model string, level marotte.EffortLevel) {
	if model == "" {
		return
	}
	ctx = durable.Context(ctx)
	bc.appendModelSwitched(ctx, chatID, "set_effort", &marotte.EntryModelSwitched{From: model, To: model, Effort: string(level)})
}

// appendModelSwitched writes one model_switched entry into the open turn (lanes sealed first) or
// after the newest close; op names the caller in its error lines.
func (bc *bridgeCoordinator) appendModelSwitched(ctx context.Context, chatID marotte.ChatID, op string, sw *marotte.EntryModelSwitched) {
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
		marotte.EntryKindModelSwitched, "", *sw); err != nil {
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

// foldParkedPick lands the pair parked on a chat whose session is about to open, under the chat's
// switch lock (chatLifecycle.switchMu).
func (bc *bridgeCoordinator) foldParkedPick(ctx context.Context, chatID marotte.ChatID) {
	lc := bc.turns.lifecycleFor(chatID)
	lc.switchMu.Lock()
	defer lc.switchMu.Unlock()
	if rec, ok := bc.chatStore.Get(ctx, chatID); ok && rec.PendingModel != "" {
		bc.persistModelPick(ctx, chatID, rec)
	}
}

// persistModelPick lands rec's pending pair on a chat with no live bridge: the move lands on the
// header, with no entry and no usage reset. The caller holds the chat's switch lock and read rec
// under it, so the pair it resolves is the one parked.
func (bc *bridgeCoordinator) persistModelPick(ctx context.Context, chatID marotte.ChatID, rec *marotte.Chat) {
	in := bc.effortInputFor(ctx, rec, rec.PendingModel, "")
	mv := resolveEffort(&in)
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		mv.landOn(c)
		return true
	}); err != nil {
		slog.Error("switch_model: persist pick", "chat_id", chatID, "error", err)
		return
	}
	bc.rememberPick(ctx, &mv)
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

// WireTurnStart handles the engine's turn_start bracket: it binds a pending prompt turn already sent, closes the
// own turn the bracket supersedes (bindPending), or opens a wire_turn_start turn.
// The bind is provisional; the first agentInitiated frame revises it (ReviseTurnBinding).
func (bc *bridgeCoordinator) WireTurnStart(ctx context.Context, chatID marotte.ChatID) {
	bound, lost := bc.turns.bindPending(ctx, chatID)
	for lost != nil {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBracketLost, Turn: lost.ID, Claimed: lost})
		bound, lost = bc.turns.bindPending(ctx, chatID)
	}
	if bound != "" {
		if bc.steerTurnBound != nil {
			bc.steerTurnBound(chatID, bound)
		}
		return
	}
	bc.openWireTurn(ctx, chatID, true)
}

// WireTurnEnd handles the engine's turn_end bracket. It is a no-op, never a phantom turn, when no own turn is open
// or the open one is a prompt its turn_start has not bound (claimBound). Live path only: replay is filtered upstream.
func (bc *bridgeCoordinator) WireTurnEnd(ctx context.Context, chatID marotte.ChatID, stop marotte.StopReason) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerWireEnd, Stop: stop, Own: true})
}

// TurnFoldTarget returns the accumulator the chat's own frames fold into, opening a
// wire_turn_start turn when none is open; nil when refused. Never for a step frame.
func (bc *bridgeCoordinator) TurnFoldTarget(ctx context.Context, chatID marotte.ChatID) *turnlog.Turn {
	if log, ok := bc.turns.foldTarget(chatID); ok {
		return log
	}
	t := bc.openWireTurn(ctx, chatID, false)
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

// closeTurnOnBridgeDeath closes everything the session hosted: the chat's own and pending
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
		slog.Error("close the step turns an ended session hosted", "chat_id", chatID, "error", err)
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

// recordSteer writes a minted steer entry into the open turn, else after the newest close.
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

// InterruptTurn records why kiro-cli abandoned the named turn and trips that turn's own prompt call so the failure
// path finalizes it; the cause is first-wins on that turn. The bridge stays alive. A turn that closed, or one with
// no prompt call, is not a failure: no other turn is ever reached.
func (bc *bridgeCoordinator) InterruptTurn(chatID marotte.ChatID, turnID, reason string) {
	cancel, ok := bc.turns.interruptTurn(chatID, turnID, marotte.InterruptCause(reason))
	if !ok {
		slog.Debug("interrupt turn: the turn closed or another cause claimed it",
			"chat_id", chatID, "turn", turnID, "reason", reason)
		return
	}
	tripPromptCall(chatID, turnID, cancel)
}

// tripPromptCall cancels an interrupted turn's own prompt call; nil is a turn with none, which only records its cause.
func tripPromptCall(chatID marotte.ChatID, turnID string, cancel context.CancelCauseFunc) {
	if cancel == nil {
		slog.Debug("interrupt turn: the turn has no prompt call", "chat_id", chatID, "turn", turnID)
		return
	}
	// No cause, so the turn concludes `interrupted`.
	cancel(nil)
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

// ChatForACPSession answers the chat whose resident session sessionID is.
func (bc *bridgeCoordinator) ChatForACPSession(sessionID string) (marotte.ChatID, bool) {
	if sessionID == "" {
		return "", false
	}
	for chatID, sb := range bc.bridge.mgr.all() {
		if string(sb.bridge.SessionID()) == sessionID {
			return chatID, true
		}
	}
	return "", false
}

// ParentACPSession returns the running bridge's ACP session id for chatID, or "".
func (bc *bridgeCoordinator) ParentACPSession(chatID marotte.ChatID) string {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return ""
	}
	return string(sb.bridge.SessionID())
}
