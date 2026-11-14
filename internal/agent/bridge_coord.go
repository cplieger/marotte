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

	"github.com/cplieger/go/keylock"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/logsafe"
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
	turns *turnRegistry
	// switchLocks is held per chat by every writer of pending_model (the switch's admission, the idle or turn-close
	// apply, the session open's fold): otherwise two appliers read one pick and apply it twice, or a landing falls
	// between an admission's read and its park. Keyed by chat id, so it needs no lifecycle; held across the bridge
	// call.
	switchLocks      *keylock.Set[marotte.ChatID]
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
	// settingsProfile is the profile a chat with none of its own is pinned to at its door (pinSecurityProfile).
	settingsProfile func(context.Context) (string, bool)
	// syncLive pushes the engine each live setting it has not confirmed.
	syncLive func(context.Context)
	// configHeld is the configDoc owner's verdict, which holds a marked session's reload (reloadIfMarked).
	configHeld func() bool
	// replayProjection is the session/load replay lifecycle; nil in tests without a load.
	replayProjection replayProjector
	// onSessionRehydrated fires after a successful session/load, on the door generation's life and context, to heal
	// paused runs.
	onSessionRehydrated func(context.Context, *chatLifecycle, marotte.ChatID)
	// reapSession removes one KAS session's on-disk state.
	reapSession func(sessionID string)
	// dischargeWaiting drops a retained waiting_on_user claim when its agent dies. Nil in tests.
	dischargeWaiting func(context.Context, marotte.ChatID)
	// endSession ends the searches and retires the unanswered asks an ended bridge carried: nothing can receive
	// their answers.
	endSession func(origin acpResponder)
	// observeLaneFrame reports each frame a session's forward consumed to the run-mutation registry, which settles
	// a host-session verb only once its lane read past the reply; forgetLane drops that position when the forward
	// ends. Nil in tests.
	observeLaneFrame func(origin acpResponder, seq uint64, msg *marotte.RPCResponse)
	forgetLane       func(origin acpResponder)
	// onTurnClosed fires from the winning closer so the terminal registry evicts the turn's output. Nil in tests.
	onTurnClosed func(marotte.ChatID, string)
	// applyPendingModel is the pending_model idle arm, dispatched by every closer on its own goroutine. Nil in tests.
	applyPendingModel func(context.Context, marotte.ChatID) `wiring:"optional"`
	// autoCompact owns every compaction of a chat: the policy's triggers and the reader's requests.
	autoCompact *autoCompactor `wiring:"optional"`
	// resolveEnds settles a close's steer ends in afterTurnClose.
	resolveEnds func(context.Context, marotte.ChatID, command.TurnFence) `wiring:"optional"`
	// launchEntry opens the turn of a run list entry the list's taker took (command.Dispatcher.LaunchEntry).
	launchEntry func(context.Context, marotte.ChatID, *command.EntryLaunch) (string, error) `wiring:"optional"`
	// sendEntrySteer routes and sends a row the run list offered as a steer (command.Dispatcher.SteerEntry).
	sendEntrySteer func(context.Context, marotte.ChatID, *command.EntrySteer) string `wiring:"optional"`
	// takeEntryFates takes the steer record's fates of the run list's offered rows (steerRecords.takeEntryFates).
	takeEntryFates func(marotte.ChatID) []entryFate `wiring:"optional"`
	// steerFacts reads the steer queue's pending ends and change sequence for the run list (steerRecords.runFacts).
	steerFacts func(chatID marotte.ChatID) (ends int, seq uint64) `wiring:"optional"`
	// settleNotices clears a run notice KAS's boundary left queued, once KAS is idle. Nil in tests.
	settleNotices func(context.Context, marotte.ChatID) `wiring:"optional"`
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
	steerBridgeGone  func(marotte.ChatID)                       `wiring:"optional"`
	steerKASSettled  func(marotte.ChatID)                       `wiring:"optional"`
	// chatHasLiveRun keeps pushTurnOutcome from claiming the work is over while a launched run continues. Nil means none.
	chatHasLiveRun func(marotte.ChatID) bool `wiring:"optional"`
	// chatHasTab reports an open tab on the chat; it answers true when no tab set is wired, so nothing is released.
	chatHasTab func(marotte.ChatID) bool
	// chatHasAsk reports a pending ask of the chat's own session; a launched run's asks are not the chat's.
	chatHasAsk func(marotte.ChatID) bool
	// finishes holds per chat the newest close's notice, parked while the chat has an open turn or a pending ask
	// (releaseFinish). In memory only: a restart forgets a parked finish.
	finishes map[marotte.ChatID]marotte.NotificationPayload
	// readBound is the read bound of the tab a notice names, 0 when unread (newReadBound). Nil reads 0.
	readBound func(ctx context.Context, kind marotte.TabKind, ref string) uint64 `wiring:"optional"`
	// runLabel is a run's display name, for a notification titled by it. Nil names none.
	runLabel func(workflowID string) string `wiring:"optional"`
	// unknownStops records stop reasons already warned about: one line per unmapped value.
	unknownStops sync.Map
	// finishMu guards finishes.
	finishMu sync.Mutex
	// noSubscribers latches the no-subscriber report once per episode (reportNoSubscribers).
	noSubscribers atomic.Bool
	// idleEndGrace is WireTurnActivity's wait, zero meaning defaultIdleEndGrace; a field so a test shortens it.
	idleEndGrace time.Duration
}

// newBridgeCoordinator builds the coordinator once, from NewHub after all options apply.
func newBridgeCoordinator(h *Runtime) *bridgeCoordinator {
	turns := newTurnRegistry(h.chatStore)
	if h.lifecycle != nil {
		turns.lifeParent = h.lifecycle.shutdownCtx
	}
	if h.bridge != nil && h.bridge.mgr != nil {
		h.bridge.mgr.gates = turns.gates
	}
	bc := &bridgeCoordinator{
		bridge:         h.bridge,
		chatStore:      h.chatStore,
		catalog:        h.catalog,
		turns:          turns,
		switchLocks:    keylock.New(cmp.Compare[marotte.ChatID]),
		broadcast:      h.bus.Broadcast,
		translateEvent: h.translateACPEvent,
		// Read per call: agentTerms is built after the coordinator.
		releaseTerminals: func(chatID marotte.ChatID, origin acpResponder) {
			h.agentTerms.releaseForBridge(chatID, origin)
		},
		push:            h.push,
		mcpRegistry:     h.mcpRegistry,
		lifecycle:       h.lifecycle,
		engine:          h.engine,
		sessionOpts:     h.chatSessionOpts,
		settingsProfile: h.settingsProfile,
		syncLive:        func(ctx context.Context) { h.engine.syncLive(ctx, h.liveSettings) },
		configHeld:      h.configDoc.held,
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
		chatHasAsk: func(chatID marotte.ChatID) bool {
			return len(h.bus.pendingPerms.askRefs(chatID, command.ScopeChatSession)) > 0
		},
		runLabel:            h.runs.runName,
		onSessionRehydrated: h.runs.resumeInterruptedRuns,
		reapSession:         func(sessionID string) { h.reapSessions([]string{sessionID}) },
		onTurnClosed:        func(chatID marotte.ChatID, turnID string) { h.agentTerms.closeTurn(chatID, turnID) },
		dischargeWaiting:    h.DischargeWaiting,
		endSession:          h.bus.endSessionOf,
		observeLaneFrame: func(origin acpResponder, seq uint64, msg *marotte.RPCResponse) {
			h.runs.mutations.observeSessionFrame(laneRef{session: origin}, seq, msg, h.router.StepRun)
		},
		forgetLane:        func(origin acpResponder) { h.runs.mutations.forgetLane(laneRef{session: origin}) },
		applyPendingModel: h.applyPendingModel,
		resolveEnds: func(ctx context.Context, chatID marotte.ChatID, f command.TurnFence) {
			h.dispatcher.ResolveAfterClose(ctx, chatID, f)
		},
		launchEntry: func(ctx context.Context, chatID marotte.ChatID, l *command.EntryLaunch) (string, error) {
			return h.dispatcher.LaunchEntry(ctx, chatID, l)
		},
		sendEntrySteer: func(ctx context.Context, chatID marotte.ChatID, s *command.EntrySteer) string {
			return h.dispatcher.SteerEntry(ctx, chatID, s)
		},
		settleNotices:    h.runs.clearStaleNotices,
		runs:             h.runs.log,
		endHostedSteers:  h.runs.endHostedSteers,
		takeAgentSteers:  h.bus.steers.takeAgentRows,
		steerTurnEnded:   h.bus.steers.turnEnded,
		steerTurnStarted: h.bus.steers.turnStarted,
		steerTurnBound:   h.bus.steers.turnBound,
		steerBridgeGone:  h.bus.steers.bridgeGone,
		steerKASSettled:  h.bus.steers.kasSettled,
	}
	turns.runHost = bc
	turns.sendRole = bc.reassertBotRole
	turns.spawnRole = bc.spawnRole
	return bc
}

// openBridge returns chatID's session once its generation is ready (sessionSettle), opening one when
// none is registered, with the caller's open hold on it: no close of the session is granted until release runs.
// release is never nil (a no-op on an error), so the caller defers it before the error check. The door runs under
// the runtime's lifetime, since a cancelled one would detach the chat's session; ctx bounds only this caller's wait.
// A failed generation answers its failure and is already gone, so the next call opens a fresh one. Before it returns
// it sends the engine each live setting it has not confirmed, waiting no later than ignorePushTimeout after the call.
func (bc *bridgeCoordinator) openBridge(ctx context.Context, chatID marotte.ChatID, modelOverride string) (*sharedBridge, func(), error) {
	return bc.openBridgeAt(ctx, chatID, modelOverride, command.Reservation{Chat: chatID, Stamp: bc.turns.gates.stamp()})
}

// openBridgeAt is openBridge for the holder of res, a prompt's reservation: its intent began at res.Stamp, so a close
// committed since then answers marotte.ErrChatClosedDuringCall and a held one is waited out, and its own admission and
// unsent turn do not keep a marked session from reloading at this door (res.Token; 0 for a non-prompt door). The open
// hold also ends with res's release (releaseReservation).
func (bc *bridgeCoordinator) openBridgeAt(
	ctx context.Context, chatID marotte.ChatID, modelOverride string, res command.Reservation,
) (*sharedBridge, func(), error) {
	syncBy := time.Now().Add(ignorePushTimeout)
	sb, release, err := bc.openBridgeBy(ctx, chatID, modelOverride, syncBy, res)
	if err != nil {
		return nil, func() {}, err
	}
	if hook := bc.turns.openReleaseHook; hook != nil {
		held := release
		release = func() { hook(chatID, res.Token); held() }
	}
	syncCtx, cancel := context.WithDeadline(ctx, syncBy)
	bc.syncLive(syncCtx)
	cancel()
	return sb, release, nil
}

// openBridgeBy creates the chat's life before its door generation, which runs as the life's work on the life's
// context, so a close ends a door still waiting on kiro-cli.
func (bc *bridgeCoordinator) openBridgeBy(
	ctx context.Context, chatID marotte.ChatID, modelOverride string, syncBy time.Time, res command.Reservation,
) (*sharedBridge, func(), error) {
	bc.reconcileSessions(ctx, syncBy)
	for {
		sb, existed, err := bc.generationAt(ctx, chatID, modelOverride, res.Stamp)
		if err != nil {
			return nil, nil, err
		}
		if sb == nil {
			continue
		}
		err = sb.settled.wait(ctx)
		if err != nil {
			return nil, nil, err
		}
		release, reopen, err := bc.reloadIfMarked(ctx, chatID, sb, res.Token)
		if err != nil {
			return nil, nil, err
		}
		if !reopen {
			if existed {
				bc.repairEffort(ctx, chatID, sb)
			}
			return sb, release, nil
		}
	}
}

// generationAt returns chatID's entry for a door whose intent began at stamp, starting the new entry's door
// generation as the life's work. A nil entry with no error means a close's hold ended meanwhile: ask again.
func (bc *bridgeCoordinator) generationAt(
	ctx context.Context, chatID marotte.ChatID, modelOverride string, stamp uint64,
) (sb *sharedBridge, existed bool, err error) {
	lc, err := bc.turns.lifecycleAt(ctx, chatID, stamp)
	if err != nil {
		return nil, false, err
	}
	sb, existed, wait, err := bc.bridge.mgr.orInsertAt(chatID, stamp, lc)
	if err != nil {
		return nil, false, err
	}
	if wait != nil {
		select {
		case <-wait:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	if existed {
		return sb, true, nil
	}
	lifeCtx, done, ok := lc.beginWork(workDoor)
	if !ok {
		bc.retireGeneration(chatID, sb, marotte.ErrChatClosedDuringCall)
		return nil, false, marotte.ErrChatClosedDuringCall
	}
	bc.lifecycle.inflight.Go(func() {
		defer done()
		bc.openGeneration(lifeCtx, lc, chatID, sb, modelOverride, stamp)
	})
	return sb, false, nil
}

// reloadIfMarked is the door's close decision on sb, its open entry (chatLifecycle.openOrReload). It answers the
// caller's open hold on sb, or reopen when the caller must open the next generation. A close granted on sb is waited
// out, so no open hands out a session being closed. A dead session, or a marked one that no hold refuses but the
// opener's own (opener is its reservation token), closes here, so this open loads the next generation with the
// current door settings; a reload a hold refuses stays pending, or for a kept chat marked for its next door, and the
// door uses sb. A refused close keeps the session and its mark; a close that errs with the session ended fails this
// open with marotte.ErrSessionClosing. The close is a lease, so the chat's close refuses it or cuts it and this open
// fails with that error.
func (bc *bridgeCoordinator) reloadIfMarked(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, opener uint64,
) (release func(), reopen bool, err error) {
	lc, ok := bc.turns.closeOwner(chatID, sb)
	if !ok {
		return func() {}, false, nil
	}
	if bc.bridge.mgr.get(chatID) != sb {
		return nil, true, nil
	}
	a := lc.openOrReload(sb, opener, doorReads{dead: !sb.open(), configHeld: bc.configHeld(), kept: bc.keptResident(chatID)})
	switch a.verdict {
	case askClosing:
		return nil, true, awaitDone(ctx, a.done)
	case askPending:
		if a.startCloser {
			bc.startCloser(chatID, lc)
		}
		return a.release, false, nil
	case askEnded:
		return a.release, false, nil
	case askGranted:
	}
	g := a.grant
	exit := bc.turns.forwardExit(chatID)
	if g.why&closeDead != 0 {
		bc.endDoorClose(chatID, lc, g, true)
		bc.goCloseHeldSessionTurns(chatID, exit)
		return nil, true, nil
	}
	stillOpen := false
	closeErr := lc.useClosing(ctx, g, func(ctx context.Context, s engineSession) error {
		err := s.Close(ctx)
		stillOpen = s.Open()
		return err
	})
	switch {
	case errors.Is(closeErr, marotte.ErrChatClosedDuringCall):
		bc.endDoorClose(chatID, lc, g, !stillOpen)
		return nil, false, closeErr
	case closeErr == nil:
		bc.endDoorClose(chatID, lc, g, true)
		bc.closeHeldSessionTurns(ctx, chatID, exit)
		slog.Info("chat session closed for its reload", "chat_id", chatID, "session_id", sb.SessionID())
		return nil, true, nil
	case stillOpen:
		bc.endDoorClose(chatID, lc, g, false)
		slog.Warn("a chat's session refused its reload; it keeps its current settings until the next message",
			"chat_id", chatID, "error", closeErr)
		release, done := lc.holdOpenOn(sb, opener)
		if done != nil {
			return nil, true, awaitDone(ctx, done)
		}
		return release, false, nil
	}
	bc.endDoorClose(chatID, lc, g, true)
	bc.closeHeldSessionTurns(ctx, chatID, exit)
	if errors.Is(closeErr, marotte.ErrSessionClosing) {
		return nil, false, closeErr
	}
	return nil, false, fmt.Errorf("%w: %w", marotte.ErrSessionClosing, closeErr)
}

// awaitDone waits for a granted close's done, answering ctx's error when the caller stops waiting first.
func awaitDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// endDoorClose ends a door's own grant g: a closed session's entry leaves the registry before the grant ends, so an
// open woken by it registers the next generation. It starts the closer when the grant left a close owed.
func (bc *bridgeCoordinator) endDoorClose(chatID marotte.ChatID, lc *chatLifecycle, g *closeGrant, closed bool) {
	if closed {
		bc.bridge.mgr.removeIfSame(chatID, g.sb)
	}
	if lc.endClose(g, closed) {
		bc.startCloser(chatID, lc)
	}
}

// keptResident reports whether chatID's session stays resident whatever holds it: a run chat (releaseEndedHost's),
// an open tab (or no tab set wired), or a live run the chat launched. Neither a release nor the closer's reload closes
// such a chat's session: a reload waits for its next door, which closes and loads in one call.
func (bc *bridgeCoordinator) keptResident(chatID marotte.ChatID) bool {
	return isRunChat(chatID) || bc.chatHasTab == nil || bc.chatHasTab(chatID) ||
		(bc.chatHasLiveRun != nil && bc.chatHasLiveRun(chatID))
}

// releaseIfUnheld asks the owner to close chatID's resident chat session (closeRelease) unless it stays resident
// (keptResident). The close happens once no hold refuses it, run by the chat's closer, so this never waits: the
// forward's own edges call it. A dead session is its forward's end to retire.
func (bc *bridgeCoordinator) releaseIfUnheld(chatID marotte.ChatID) {
	if chatID == "" || bc.keptResident(chatID) {
		return
	}
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil || !sb.open() {
		return
	}
	lc, ok := bc.turns.closeOwner(chatID, sb)
	if !ok {
		return
	}
	if lc.askClose(sb, closeRelease, 0, false).startCloser {
		bc.startCloser(chatID, lc)
	}
}

// startCloser runs lc's closer on the lifetime, counted as the life's work; only the request answered startCloser
// calls it. A start the runtime refuses leaves nothing owed (chatLifecycle.closerRefused).
func (bc *bridgeCoordinator) startCloser(chatID marotte.ChatID, lc *chatLifecycle) {
	_, done, ok := lc.beginWork(workCloser)
	if ok && bc.lifecycle.goUnlessDraining(func() {
		defer done()
		bc.runCloser(chatID, lc)
	}) {
		return
	}
	done()
	lc.closerRefused()
}

// runCloser runs each close the owner grants lc until none is owed (chatLifecycle.nextClose).
func (bc *bridgeCoordinator) runCloser(chatID marotte.ChatID, lc *chatLifecycle) {
	for {
		g, why, ok := lc.nextClose()
		if !ok {
			return
		}
		if hook := bc.turns.closerHook; hook != nil {
			hook(chatID)
		}
		bc.closeGranted(chatID, lc, g, why)
	}
}

// closeGranted runs grant g for why after re-reading what the owner does not hold: the entry is still the chat's,
// the chat is not kept resident, and a reload is still marked and config.json reads. It never loads a session: the
// next open loads it on demand.
func (bc *bridgeCoordinator) closeGranted(chatID marotte.ChatID, lc *chatLifecycle, g *closeGrant, why closePurpose) {
	if cur := bc.bridge.mgr.get(chatID); cur != g.sb {
		lc.endClose(g, false)
		// The entry ended on its own and its chat registered another: the release is still owed, now on cur.
		if owner, ok := bc.turns.closeOwner(chatID, cur); ok && owner == lc && why&closeRelease != 0 {
			moved := closeRelease
			if lc.reloadMarked(cur) {
				moved |= closeReload
			}
			lc.askClose(cur, moved, 0, false)
		}
		return
	}
	if bc.keptResident(chatID) {
		why = 0
	}
	if why&closeReload != 0 && (!lc.reloadMarked(g.sb) || bc.configHeld()) {
		why &^= closeReload
	}
	if why&(closeReload|closeRelease) == 0 {
		lc.endClose(g, false)
		return
	}
	if lc.confirmClose(g) {
		bc.closeConfirmed(chatID, lc, g, why)
	}
}

// closeConfirmed closes g's session as one lease and ends g, logging what it did. A close the chat's close refused,
// cut or released meanwhile logs nothing.
func (bc *bridgeCoordinator) closeConfirmed(chatID marotte.ChatID, lc *chatLifecycle, g *closeGrant, why closePurpose) {
	ctx, cancel := bc.lifecycle.derivedContext()
	defer cancel()
	stillOpen, released := g.sb.open(), false
	exit := bc.turns.forwardExit(chatID)
	err := lc.useClosing(ctx, g, func(ctx context.Context, s engineSession) error {
		err := s.Close(ctx)
		stillOpen, released = s.Open(), s.Released()
		return err
	})
	closed := err == nil || !stillOpen
	if closed {
		bc.bridge.mgr.removeIfSame(chatID, g.sb)
	}
	quiet := released || errors.Is(err, marotte.ErrChatClosedDuringCall)
	if closed && !quiet {
		bc.goCloseHeldSessionTurns(chatID, exit)
	}
	lc.endClose(g, closed)
	switch {
	case quiet:
	case !closed:
		slog.Warn("a chat's session refused its close; it stays until its next release check or door",
			"chat_id", chatID, "error", err)
	case why&closeRelease != 0:
		slog.Info("closed a chat session nothing holds", "chat_id", chatID, "session_id", g.sb.SessionID())
	default:
		slog.Info("chat session closed for its reload", "chat_id", chatID, "session_id", g.sb.SessionID())
	}
}

// goCloseHeldSessionTurns is closeHeldSessionTurns on its own goroutine, so a closer never waits on the forward.
func (bc *bridgeCoordinator) goCloseHeldSessionTurns(chatID marotte.ChatID, exit <-chan struct{}) {
	bc.lifecycle.goUnlessDraining(func() {
		ctx, cancel := bc.lifecycle.derivedContext()
		defer cancel()
		bc.closeHeldSessionTurns(ctx, chatID, exit)
	})
}

// closeHeldSessionTurns closes, interrupted, the turns a session hosted once a holder's close (the release check, a
// reload) ended it and its forward exited: the granted close makes the forward's own closer stand down
// (removeIfBridge), so a turn KAS started during that close would otherwise stay open. A prompt still awaiting its
// send belongs to the next session and stays open.
func (bc *bridgeCoordinator) closeHeldSessionTurns(ctx context.Context, chatID marotte.ChatID, exit <-chan struct{}) {
	if !awaitForwardExit(ctx, exit) {
		return
	}
	ctx = durable.Context(ctx)
	for _, t := range bc.turns.sessionTurns(chatID) {
		bc.finalizeTurn(ctx, chatID, &turnClose{
			Closer: closerBridgeDeath, Stop: marotte.StopReasonInterrupted, Reason: deathInterruptCause, Turn: t.ID,
		})
	}
}

// LoadSession answers once chatID's bridge generation is ready (openBridge).
func (bc *bridgeCoordinator) LoadSession(ctx context.Context, chatID marotte.ChatID) error {
	_, release, err := bc.openBridge(ctx, chatID, "")
	release()
	return err
}

func (bc *bridgeCoordinator) openGeneration(
	ctx context.Context, lc *chatLifecycle, chatID marotte.ChatID, sb *sharedBridge, modelOverride string, stamp uint64,
) {
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

	bc.pinSecurityProfile(ctx, chatID, rec)

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
		loaded, err := bc.tryLoadSession(ctx, chatID, sb, rec, so, held, stamp)
		switch {
		case loaded:
			bc.settleResumed(ctx, lc, chatID, sb)
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
	so := bc.sessionOpts(ctx, rec, &ask, steering)
	session, err := bc.engine.openSession(ctx, so, func(s engineSession) error {
		if _, err := bc.goForward(ctx, chatID, s, stamp); err != nil {
			return err
		}
		sb.attach(s)
		return nil
	})
	if err = closeCut(ctx, err); err != nil {
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		return
	}
	if err := bc.holdBotDoor(ctx, rec, sb, session, false); err != nil {
		sessionID := string(session.SessionID())
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, err)
		bc.reapSession(sessionID)
		return
	}
	if err := bc.persistNewSessionMetadata(ctx, chatID, session, ask.first); err != nil {
		sessionID := string(session.SessionID())
		bc.restoreUnservedModel(ctx, chatID, held)
		bc.failGeneration(chatID, sb, closeCut(ctx, err))
		// A session no record holds is in no delete's chain, so this generation reaps it itself.
		bc.reapSession(sessionID)
		return
	}
	bc.commitUnservedModel(ctx, chatID, held, session.ModelID())
	// The session door's half of the supervised fail-open (the door's own assert is best-effort).
	bc.reportSupervisedNotApplied(ctx, chatID, rec.SupervisedMode, session.SupervisedApplied())
	sb.setReady()
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

// settleResumed publishes a generation that loaded its session. Only a published one resumes runs: a resume on a
// failed one would open the next one, which would fail and resume again.
func (bc *bridgeCoordinator) settleResumed(ctx context.Context, lc *chatLifecycle, chatID marotte.ChatID, sb *sharedBridge) {
	if bc.publishGeneration(ctx, chatID, sb) && bc.onSessionRehydrated != nil {
		bc.onSessionRehydrated(ctx, lc, chatID)
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
	if bc.bridge.mgr.removeIfSame(chatID, sb) && sb.open() {
		slog.Info("a chat's session never reached its log; it was closed", "chat_id", chatID, "error", err)
		sb.closeOwned(bc.bridge.mgr.closeSession)
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
// entry: a model_switched with an empty To names no model.
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

// tryLoadSession loads so.SessionID for chatID and reports loaded true only with a nil error, once the replay has
// merged. An error keeps the chat's session for the next open: it wraps ErrBridgeExited when the engine ended under
// the load and its one retry, under the replay, or before any engine became ready, wraps ErrChatClosedDuringCall
// when the chat's close refused the door's forward, and otherwise is the replay's merge failing. A load KAS refuses
// (the replay budget, an unavailable engine, a session still resident or closing included) and a load whose replay
// restored none of the chat's turns (errReplayRestoredNothing) both return loaded false with a nil error after
// retireSession, so the generation opens session/new.
func (bc *bridgeCoordinator) tryLoadSession(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, rec *marotte.Chat, so *marotte.SessionOpts,
	held unservedSelection, stamp uint64,
) (loaded bool, err error) {
	// Open the projection first: KAS replays inside the door. The forward attaches before the door writes; a failed
	// load closed its session, and the generation still owns its entry (bridgeManager.removeIfBridge).
	acpSessionID := so.SessionID
	replayed := newSessionSettle()
	if bc.replayProjection != nil {
		bc.replayProjection.OpenReplayProjection(ctx, chatID, acpSessionID, replayed)
	}
	session, gen, forwardDone, err := bc.loadOnEngine(ctx, chatID, sb, so, replayed, stamp)
	err = closeCut(ctx, err)
	if err != nil {
		// A failed load must not keep a partial replay.
		if bc.replayProjection != nil {
			bc.replayProjection.DiscardReplayProjection(chatID)
		}
		if errors.Is(err, marotte.ErrBridgeExited) || errors.Is(err, marotte.ErrChatClosedDuringCall) {
			return false, err
		}
		slog.Warn("session/load failed, starting new",
			"chat_id", chatID, "acp_session", acpSessionID, "error", err)
		bc.retireSession(ctx, chatID, marotte.ContextResetLoadFailed)
		return false, nil
	}
	bc.markReplayLoaded(chatID, gen, session, replayed)
	// The verdict comes before any fact is written from the session: a blank one must leave the record as the
	// session/new replacing it finds it, or commitUnservedModel records an unserved model twice.
	switch verdict := replayed.wait(bc.lifecycle.shutdownCtx); {
	case errors.Is(verdict, errReplayRestoredNothing):
		if closeErr := closeBlankLoad(ctx, session, forwardDone); closeErr != nil {
			return false, closeErr
		}
		bc.retireSession(ctx, chatID, marotte.ContextResetSessionMissing)
		return false, nil
	case verdict != nil:
		return false, verdict
	case !session.Open():
		return false, fmt.Errorf("%w: the session ended during its replay", marotte.ErrBridgeExited)
	}
	if err := bc.holdBotDoor(ctx, rec, sb, session, true); err != nil {
		return false, err
	}
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
	sb.setReady()
	return true, nil
}

// closeBlankLoad closes the blank session a lost record's load produced (KAS keeps its blank record) and waits for
// its forward to exit. It must run while the generation is still starting: the forward's exit then reads the close
// as the generation's own, not as the session ending under the prompt's open turn (bridgeManager.removeIfBridge).
func closeBlankLoad(
	ctx context.Context, session engineSession, forwardDone <-chan struct{},
) error {
	if err := session.Close(ctx); err != nil && session.Open() {
		return fmt.Errorf("close the blank session a lost record loaded: %w", err)
	}
	if forwardDone == nil {
		return nil
	}
	select {
	case <-forwardDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loadOnEngine runs chatID's session/load and reports the forward attachment its lane position is comparable within
// (replay_drain.go), and the channel that attachment's forward closes on exit. A load the engine's end failed is still
// KAS's to load (design 4.5.3), so it runs again on the next engine, once the first attempt's forward has folded what
// it replayed, into a fresh projection.
func (bc *bridgeCoordinator) loadOnEngine(
	ctx context.Context, chatID marotte.ChatID, sb *sharedBridge, so *marotte.SessionOpts, replayed *sessionSettle, stamp uint64,
) (session engineSession, gen uint64, forwardDone <-chan struct{}, err error) {
	attach := func(s engineSession) error {
		g, attachErr := bc.goForward(ctx, chatID, s, stamp)
		if attachErr != nil {
			return attachErr
		}
		gen = g
		sb.attach(s)
		forwardDone = bc.turns.forwardExit(chatID)
		return nil
	}
	session, engineGen, err := bc.engine.loadSessionAfter(ctx, 0, so, attach)
	if !errors.Is(err, marotte.ErrBridgeExited) || engineGen == 0 {
		return session, gen, forwardDone, err
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
	return session, gen, forwardDone, err
}

// retireSession blanks chatID's session id so its next open runs session/new, then records the context reset. It is
// the one place a chat leaves a session the agent no longer holds. RecordSession keeps the retired id in the chain,
// so the reaper keeps its transcript.
func (bc *bridgeCoordinator) retireSession(ctx context.Context, chatID marotte.ChatID, cause marotte.ContextResetCause) {
	var retired string
	if _, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex || c.ACPSessionID == "" {
			return false
		}
		retired = c.ACPSessionID
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("clear stale acp_session_id", "chat_id", chatID, "error", err)
		return
	}
	if retired != "" {
		bc.recordContextReset(ctx, chatID, retired, cause)
	}
}

// recordContextReset appends one context_reset with cause between turns when the chat still shows turns sessionID
// was sent: the agent holds nothing above it. A chat holding none of them lost nothing it shows.
func (bc *bridgeCoordinator) recordContextReset(ctx context.Context, chatID marotte.ChatID, sessionID string, cause marotte.ContextResetCause) {
	ctx = durable.Context(ctx)
	entries, err := bc.chatStore.All(ctx, chatID)
	if err != nil {
		slog.Error("context reset: read the chat's log", "chat_id", chatID, "error", err)
		return
	}
	if !holdsTurnsOf(entries, nil, sessionID) {
		return
	}
	if err := translate.AppendBetweenTurns(ctx, broadcastFunc(bc.broadcast), bc.chatStore, chatID,
		marotte.EntryKindContextReset, "", marotte.EntryContextReset{Cause: cause}); err != nil {
		slog.Error("context reset: record the entry between turns", "chat_id", chatID, "cause", cause, "error", err)
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
		c.AdoptMode(mode)
	}
	// An absent catalog keeps the previous set (ApplyServedModels is false for empty).
	marotte.ApplyServedModels(c, facts.Catalog())
	dropped = dropUnservedModel(c)
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
	var bot bool
	version, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if !ex {
			return false
		}
		requestedMode = doorMode(c)
		bot = c.IsStartedBot()
		c.RecordSession(string(newSessionID))
		prevModel := c.Model
		if newModelID != "" {
			c.MoveToModel(string(newModelID))
		}
		c.Effort = first.onto(c.Effort, prevModel, c.Model)
		c.AdoptMode(currentMode)
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
	// A started Bot's mode is the door's to report (holdBotRole).
	if !bot {
		bc.reportModeNotApplied(ctx, chatID, requestedMode, currentMode)
	}
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

// closeBridge removes the chat's entry and releases its session, waits for the chat's forward to drain what its lane
// had queued and exit, then closes every turn still open with outcome, and forgets a run chat. The release, then
// the exit, precede every turn close, so no frame queued on the lane lands after the turns were closed. It must not
// run on the forward of the chat it closes, which would wait for itself until ctx ends. Its callers: the teardown
// job (cleanupChatState), a run host's launch failure, a run's delete and a run host's end.
func (bc *bridgeCoordinator) closeBridge(ctx context.Context, chatID marotte.ChatID, outcome marotte.TurnOutcome) {
	exit := bc.turns.forwardExit(chatID)
	bc.bridge.mgr.close(chatID)
	if !awaitForwardExit(ctx, exit) {
		slog.Warn("a torn-down chat's bridge did not drain in time", "chat_id", chatID)
	}
	bc.closeTurnOnBridgeDeath(ctx, chatID, outcome)
	if isRunChat(chatID) {
		bc.turns.forget(chatID)
	}
}

// awaitForwardExit waits for a forward's exit channel (forwardExit), false when ctx ended first. A nil channel is a
// chat no forward attached to.
func awaitForwardExit(ctx context.Context, exit <-chan struct{}) bool {
	if exit == nil {
		return true
	}
	select {
	case <-exit:
		return true
	case <-ctx.Done():
		return false
	}
}

// refreshForRetry abandons the session that answered nothing, as one lease on the prompt's entry sb: release it (when
// sb still holds it and nobody else owns its end), wait for its forward, close the turns it hosted `interrupted`, and
// detach the record from it (detachRetried). A context check precedes each step, and a step skipped for a cut answers
// the cut, so a close that commits during it leaves the rest to its teardown. The detach is the line: a close
// committed before the closure's check keeps the id, and one after it is ordered after the refresh.
func (bc *bridgeCoordinator) refreshForRetry(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) error {
	return sb.use(ctx, func(ctx context.Context, s engineSession) error {
		cut := func() error { return fmt.Errorf("empty-turn refresh: %w", context.Cause(ctx)) }
		if ctx.Err() != nil {
			return cut()
		}
		exit := bc.turns.forwardExit(chatID)
		if bc.bridge.mgr.removeIfBridge(chatID, s) {
			s.Release()
			if !awaitForwardExit(ctx, exit) || ctx.Err() != nil {
				return cut()
			}
			bc.closeTurnOnBridgeDeath(ctx, chatID, marotte.TurnOutcomeInterrupted)
		}
		err := bc.detachRetried(ctx, chatID, string(s.SessionID()))
		if ctx.Err() != nil {
			return cut()
		}
		return err
	})
}

// detachRetried blanks the record's session id while it still names sessionID and ctx stands, then records the
// empty_retry context reset when the chat shows turns that session was sent (recordContextReset).
func (bc *bridgeCoordinator) detachRetried(ctx context.Context, chatID marotte.ChatID, sessionID string) error {
	retired := false
	_, err := bc.chatStore.Mutate(ctx, chatID, func(c *marotte.Chat, ex bool) bool {
		if ctx.Err() != nil || !ex || c.ACPSessionID != sessionID {
			return false
		}
		// Detach, don't forget: the abandoned session holds the chat's earlier transcript and must stay in the chain
		// or the reaper sweeps it.
		c.RecordSession("")
		retired = true
		return true
	})
	if err == nil && retired && ctx.Err() == nil {
		bc.recordContextReset(ctx, chatID, sessionID, marotte.ContextResetEmptyRetry)
	}
	return err
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
func (bc *bridgeCoordinator) goForward(ctx context.Context, chatID marotte.ChatID, bridge engineSession, stamp uint64) (uint64, error) {
	att, err := bc.turns.attachForward(ctx, chatID, stamp)
	if err != nil {
		return 0, err
	}
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, bridge, att) })
	return att.gen, nil
}

// goRunForward is goForward for a run chat's host session, which no close holds.
func (bc *bridgeCoordinator) goRunForward(chatID marotte.ChatID, bridge engineSession) {
	att := bc.turns.attachRunForward(chatID)
	bc.lifecycle.inflight.Go(func() { bc.forwardAt(chatID, bridge, att) })
}

// forwardAt is Forward on an attachment the caller already took: tryLoadSession orders its
// read-loop position against it.
func (bc *bridgeCoordinator) forwardAt(chatID marotte.ChatID, bridge engineSession, att forwardAttachment) {
	gen := att.gen
	bc.drainLane(chatID, bridge, gen)
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
	bc.endSession(bridge)

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
	// A run chat has no record and nothing calls cleanupChatState for one; a closer still owed its turn close
	// forgets it after (closeBridge).
	if isRunChat(chatID) {
		bc.turns.forgetIfIdle(chatID)
	}
	// Last, for every end (tab close, reload, engine end): KAS's own dispose releases them on a session that is
	// already closed, and no later session can see, read or stop them.
	bc.releaseTerminals(chatID, bridge)
	// A reload that reuses the session id may already serve it from a new bridge, whose view this must keep.
	if sid := bridge.SessionID(); sid != "" {
		if cur := bc.bridge.mgr.get(chatID); cur == nil || cur.SessionID() != sid {
			bc.mcpRegistry.dropSession(bc.lifecycle.shutdownCtx, sid)
		}
	}
	att.done()
}

// drainLane consumes bridge's frames until its lane closes. The generation keeps a straggler from the previous
// bridge off a restarted counter.
func (bc *bridgeCoordinator) drainLane(chatID marotte.ChatID, bridge engineSession, gen uint64) {
	for n := range bridge.NotifCh() {
		bc.consumeFrame(chatID, bridge, gen, n)
		if bc.observeLaneFrame != nil {
			bc.observeLaneFrame(bridge, n.Seq, n.Msg)
		}
		// Settle a replay projection here: this goroutine folds the frames, so only its position is comparable (replay_drain.go).
		if bc.replayProjection != nil {
			bc.replayProjection.SettleReplayProjection(chatID, drainPoint{gen: gen, seq: n.Seq}, false)
		}
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

// Notify hands n to the push service's notice ledger, which decides the page that shows it or the push. With no
// push service nothing is delivered.
func (bc *bridgeCoordinator) Notify(ctx context.Context, chatID marotte.ChatID, n *marotte.NotificationPayload) {
	if bc.push == nil {
		return
	}
	if bc.push.HasSubscribers() {
		bc.noSubscribers.Store(false)
	} else {
		bc.reportNoSubscribers(n.Kind, n.PushSubject)
	}
	// Gated, since a caller need not run on inflight (the PR status poller outlives Shutdown). Once draining the send
	// still goes, unwaited: push.Close in Shutdown unblocks it and owns its writes. The send outlives the caller's
	// ctx (a heal cancels its own on return); push.Close bounds it. Off the caller, since a run's read bound asks KAS.
	sendCtx := context.WithoutCancel(ctx)
	send := func() {
		deliver := bc.push.Raise(sendCtx, chatID, n, bc.noticePosition(sendCtx, n))
		deliver(sendCtx)
	}
	if !bc.lifecycle.goUnlessDraining(send) {
		go send()
	}
}

// settleNotice ends the notices about subj announcing ask: the ask was settled elsewhere.
func (bc *bridgeCoordinator) settleNotice(subj marotte.PushSubject, ask string) {
	if bc.push == nil {
		return
	}
	bc.push.Settled(subj, ask)
}

// reportNoSubscribers logs once per episode that a notification had no subscriber; a
// subscriber re-arms it. Both subject halves are logged: either may be empty.
func (bc *bridgeCoordinator) reportNoSubscribers(kind marotte.PushKind, subj marotte.PushSubject) {
	if !bc.noSubscribers.CompareAndSwap(false, true) {
		return
	}
	slog.Info("no push subscribers; pushes are dropped until a browser subscribes",
		"chat_id", subj.ChatID, "subject", subj.Key, "kind", string(kind))
}

// settleTurnOnResponse closes turnID's turn on its settling response once the folder has
// consumed everything queued before it, unless the wire's turn_end got there first. seq
// is the response's read-loop position; zero skips the wait.
func (bc *bridgeCoordinator) settleTurnOnResponse(ctx context.Context, chatID marotte.ChatID, turnID string, seq uint64, resp *marotte.RPCResponse) {
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptResponse, Resp: resp, Turn: turnID, Seq: seq})
}

// applyModelSwitch swaps the live bridge's model through session/set_config_option and
// re-applies the level, as one lease. marotte.ErrSessionClosed with no bridge; an error wrapping
// marotte.ErrChatClosedDuringCall when the chat's close refused or cut it; never a restart.
func (bc *bridgeCoordinator) applyModelSwitch(ctx context.Context, chatID marotte.ChatID, mv *effortDecision) error {
	sb := bc.bridge.mgr.get(chatID)
	if sb == nil {
		return marotte.ErrSessionClosed
	}
	return sb.use(ctx, func(ctx context.Context, s engineSession) error {
		return applyModelSwitchOn(ctx, chatID, s, mv)
	})
}

// applyModelSwitchOn swaps the model on a leased session, then asserts mv.sent, thinking first when the move turns
// it on. The error is the swap's refusal.
func applyModelSwitchOn(ctx context.Context, chatID marotte.ChatID, s engineSession, mv *effortDecision) error {
	if err := s.SetModel(ctx, mv.to); err != nil {
		slog.Info("model switch: the session refused the swap",
			"chat_id", chatID, "model", mv.to, "error", err)
		return err
	}
	// Thinking before the level: KAS caps a high tier while thinking is off. Best-effort.
	if mv.thinkingOn {
		if err := s.EnsureThinking(ctx, marotte.ThinkingOn); err != nil {
			slog.Warn("model switch: thinking not turned back on after the swap",
				"chat_id", chatID, "model", mv.to, "error", err)
		}
	}
	// Re-assert the level: KAS reconciles against the new model's tiers (2.19.1: a swap to
	// `auto` destroys it). Best-effort.
	if mv.sent != "" {
		if err := s.EnsureEffort(ctx, mv.sent); err != nil {
			slog.Warn("model switch: reasoning effort not re-applied after the swap",
				"chat_id", chatID, "model", mv.to, "effort", mv.sent, "error", err)
		}
	}
	slog.Info("model switch: fast path succeeded (session/set_config_option)",
		"chat_id", chatID, "model", mv.to, "effort", mv.sent)
	return nil
}

// repairEffort re-asserts the chat's effort level on an already-open bridge, catching a
// level KAS changed on its own. At the prompt, because a Call from Forward would block the
// drain it waits on. Best-effort.
func (bc *bridgeCoordinator) repairEffort(ctx context.Context, chatID marotte.ChatID, sb *sharedBridge) {
	rec, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return
	}
	thinking := ""
	if bc.catalog.thinkingToggleable(rec.Model) {
		thinking = rec.Thinking
	}
	level := bc.sessionEffort(rec)
	_ = sb.use(ctx, func(ctx context.Context, s engineSession) error {
		if thinking != "" {
			if err := s.EnsureThinking(ctx, thinking); err != nil {
				slog.Warn("thinking choice not re-applied on the open session",
					"chat_id", chatID, "thinking", thinking, "error", err)
			}
		}
		if level == "" {
			return nil
		}
		if err := s.EnsureEffort(ctx, level); err != nil {
			slog.Warn("reasoning effort not re-applied on the open session",
				"chat_id", chatID, "effort", level, "error", err)
		}
		return nil
	})
}

// healWorkflowLaunch hands the workflowLaunch value a chat's own config_option_update offers to its session, which
// asserts the setting's value: the option first appears when a chat moves into a mode that offers it.
func (bc *bridgeCoordinator) healWorkflowLaunch(next sessionUpdateHandler) sessionUpdateHandler {
	return func(ctx context.Context, chatID marotte.ChatID, raw json.RawMessage, attr translate.FrameAttribution) {
		next(ctx, chatID, raw, attr)
		if !attr.ChatOwned() {
			return
		}
		reported, offered := marotte.WorkflowLaunchReported(raw)
		if !offered {
			return
		}
		sb := bc.bridgeFor(chatID)
		if sb == nil {
			return
		}
		bc.lifecycle.inflight.Go(func() {
			hctx, cancel := bc.lifecycle.derivedContext()
			defer cancel()
			err := sb.use(hctx, func(ctx context.Context, s engineSession) error { return s.HealWorkflowLaunch(ctx, reported) })
			switch {
			case errors.Is(err, marotte.ErrChatClosedDuringCall):
				slog.Debug("workflow use heal stood down: the chat closed", "chat_id", chatID)
			case err != nil:
				slog.Warn("workflow use not re-applied after the session reported another value",
					"chat_id", chatID, "reported", logsafe.Field(reported), "error", err)
			}
		})
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
		sb.observeEffort(running)
		sb.observeThinking(rec.ThinkingActive)
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
			err := sb.use(hctx, func(ctx context.Context, s engineSession) error { return s.EnsureEffort(ctx, want) })
			switch {
			case errors.Is(err, marotte.ErrChatClosedDuringCall):
				slog.Debug("reasoning effort heal stood down: the chat closed", "chat_id", chatID)
			case err != nil:
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
		c.Usage = marotte.Usage{ContextSize: contextSize, ContextCompacted: c.Usage.ContextCompacted}
		c.ResetSpend()
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
// nothing (a model_switched with an empty To names no model), nor does an unstarted chat.
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

// foldParkedPick lands the pair parked on a chat whose session is about to open, under the chat's switch lock
// (switchLocks); a ctx end leaves the pick parked for the next open.
func (bc *bridgeCoordinator) foldParkedPick(ctx context.Context, chatID marotte.ChatID) {
	unlock, err := bc.switchLocks.Lock(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
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

// abandonInFlightTurn closes a turn its prompt call could not finish. It waits for no
// read-loop position: no bracket is coming. Only `interrupted` or `cancelled` are legal
// stops; anything else becomes `interrupted` with a Warn.
func (bc *bridgeCoordinator) abandonInFlightTurn(ctx context.Context, chatID marotte.ChatID, turnID string, stop marotte.StopReason, reason string, kind marotte.FailureKind, seq uint64) {
	if stop != marotte.StopReasonInterrupted && stop != marotte.StopReasonCancelled {
		slog.Warn("a prompt failure named a stop this close cannot conclude, so it concludes interrupted",
			"chat_id", chatID, "turn", turnID, "stop", stop)
		stop = marotte.StopReasonInterrupted
	}
	bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerPromptFailure, Stop: stop, Reason: reason, Kind: kind, Turn: turnID, Seq: seq})
}

// WireTurnStart handles the engine's turn_start bracket: it binds the sent prompt turn whose id the bracket's tag
// echoes, closes the own turn the bracket supersedes (bindPending), or opens a wire_turn_start turn.
func (bc *bridgeCoordinator) WireTurnStart(ctx context.Context, chatID marotte.ChatID, frame translate.TurnStartFrame) {
	bound, lost := bc.turns.bindPending(ctx, chatID, frame.Tag)
	for lost != nil {
		bc.finalizeTurn(ctx, chatID, &turnClose{Closer: closerBracketLost, Turn: lost.ID, Claimed: lost})
		bound, lost = bc.turns.bindPending(ctx, chatID, frame.Tag)
	}
	if bound != "" {
		if bc.steerTurnBound != nil {
			bc.steerTurnBound(chatID, bound)
		}
		return
	}
	bc.openWireTurn(ctx, chatID, frame.KASTurnID, true)
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
	t := bc.openWireTurn(ctx, chatID, "", false)
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
		if string(sb.SessionID()) == sessionID {
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
	return string(sb.SessionID())
}
