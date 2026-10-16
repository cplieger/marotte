package agent

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/signin"
)

// engineHooks binds the engine owner to the runtime.
func (rt *Runtime) engineHooks() engineHooks {
	return engineHooks{
		resolve: rt.resolveEngine,
		spawned: rt.signin.engineSpawned,
		options: func(gen uint64) []bridge.Option {
			return []bridge.Option{
				bridge.WithGeneration(gen),
				bridge.WithRouter(genRouter{r: rt.router, gen: gen}),
				bridge.WithExitHook(func(bridge.ExitInfo) { rt.engine.onExit(gen) }),
				bridge.WithDetachHandler(func(d bridge.DetachedSession) {
					if h, _ := rt.engine.current(); h != nil {
						rt.answerDetached(h, d)
					}
				}),
			}
		},
		attach:    rt.attachEngineLane,
		interrupt: rt.snapshotInterrupted,
		claim:     rt.claimInterrupt,
		resume:    rt.resumeInterrupted,
		busy:      rt.engineBusy,
		publish:   rt.publishEngineStatus,
		ready:     rt.onEngineReady,
		ended:     rt.onEngineEnded,
	}
}

// snapshotInterrupted is what an end of generation gen interrupts, from memory only: the runs the clock reads
// executing, the runs with an open step turn, the runs a driving write or a read-loop start left unsettled, less the
// runs with an unsettled stop; and the chats with an open turn. held is a restart's own snapshot, grown with the
// starts the read loop journalled after it.
func (rt *Runtime) snapshotInterrupted(gen uint64, held *interruptSet) interruptSet {
	set := interruptSet{runs: rt.interruptedRuns(gen, held)}
	if held != nil {
		set.chats = held.chats
		return set
	}
	for chatID := range rt.bridge.mgr.all() {
		if !isRunChat(chatID) && rt.coord.turns.live(chatID) {
			set.chats = append(set.chats, chatID)
		}
	}
	slices.Sort(set.chats)
	return set
}

// interruptedRuns is snapshotInterrupted's run half, sorted.
func (rt *Runtime) interruptedRuns(gen uint64, held *interruptSet) []string {
	drive, stop := rt.runs.mutations.snapshot(gen)
	runs := map[string]bool{}
	if held != nil {
		for _, id := range held.runs {
			runs[id] = true
		}
	} else {
		for _, id := range rt.executingRuns() {
			runs[id] = true
		}
	}
	for _, id := range slices.Concat(drive, rt.router.takeStarted(gen)) {
		runs[id] = true
	}
	for _, id := range stop {
		delete(runs, id)
	}
	return slices.Sorted(maps.Keys(runs))
}

// executingRuns is every leased run still executing: bounded, or with a step turn open.
func (rt *Runtime) executingRuns() []string {
	var out []string
	leases := rt.runs.leaseStore().List()
	for i := range leases {
		if l := &leases[i]; l.Bounded() || (rt.runs.log != nil && rt.runs.log.hasOpen(l.WorkflowID)) {
			out = append(out, l.WorkflowID)
		}
	}
	return out
}

// claimInterrupt names cause on each snapshotted chat's open turns, first-wins, so whichever closer wins records
// the turn interrupted by the restart.
func (rt *Runtime) claimInterrupt(set interruptSet, cause string) {
	for _, chatID := range set.chats {
		for _, t := range rt.coord.turns.openTurnIDs(chatID) {
			rt.coord.turns.interrupt(chatID, t.ID, marotte.InterruptCause(cause))
		}
	}
}

// resumeInterrupted drives each run an engine end interrupted once the next engine is ready, under the run's heal
// budget, through the one resume primitive: a chat-parented run by making its chat resident (the rehydrate path
// resumes every run of the chat's chain), a parentless one by loading its host session.
func (rt *Runtime) resumeInterrupted(ctx context.Context, set interruptSet) {
	chats := map[marotte.ChatID]bool{}
	for _, wf := range set.runs {
		l, leased := rt.runs.lease(wf)
		if !leased {
			continue
		}
		if attempt, ok := rt.runs.claimHeal(wf); !ok {
			slog.Warn("a run keeps pausing for a cause nobody chose; leaving it paused",
				"workflow_id", wf, "attempts", attempt)
			continue
		}
		if l.Origin == runlease.OriginAgent && l.ChatID != "" {
			chats[marotte.ChatID(l.ChatID)] = true
			continue
		}
		rt.lifecycle.goUnlessDraining(func() { rt.resumeParentless(ctx, wf) })
	}
	for chatID := range chats {
		rt.lifecycle.goUnlessDraining(func() {
			if _, err := rt.coord.openBridge(ctx, chatID, ""); err != nil {
				slog.Warn("an interrupted run's chat could not be reopened to resume it", "chat_id", chatID, "error", err)
			}
		})
	}
}

// resumeParentless makes a parentless run's host resident and resumes it, re-reading once after orphanResumeDelay
// while KAS still lists it running.
func (rt *Runtime) resumeParentless(ctx context.Context, workflowID string) {
	if _, err := rt.runs.ensureResident(ctx, workflowID); err != nil {
		slog.Warn("an interrupted run's launching session could not be loaded, so it stays paused",
			"workflow_id", workflowID, "error", err)
		return
	}
	rt.runs.resumeIfInterrupted(ctx, runChatID(workflowID), workflowID)
	if st, ok := rt.runs.inspect(ctx, workflowID); ok && st.State.Status == marotte.RunStatusRunning {
		rt.lifecycle.afterUnlessDraining(orphanResumeDelay, func() {
			hctx, cancel := rt.lifecycle.derivedContext()
			defer cancel()
			rt.runs.resumeIfInterrupted(hctx, runChatID(workflowID), workflowID)
		})
	}
}

// engineBusy is what a restart now would interrupt: the chats with an open turn and the runs the snapshot would
// hold, by name.
func (rt *Runtime) engineBusy() engineBusy {
	ctx := rt.lifecycle.shutdownCtx
	var b engineBusy
	all := rt.bridge.mgr.all()
	b.sessions = len(all)
	for chatID := range all {
		if !isRunChat(chatID) && rt.coord.turns.live(chatID) {
			b.turns = append(b.turns, marotte.EngineBusyTurn{ChatID: chatID, Name: rt.coord.chatName(ctx, chatID)})
		}
	}
	slices.SortFunc(b.turns, func(x, y marotte.EngineBusyTurn) int {
		return compareChatIDs(x.ChatID, y.ChatID)
	})
	_, gen := rt.engine.current()
	drive, _ := rt.runs.mutations.snapshot(gen)
	leases := rt.runs.leaseStore().List()
	for i := range leases {
		if l := &leases[i]; l.Bounded() || (rt.runs.log != nil && rt.runs.log.hasOpen(l.WorkflowID)) || slices.Contains(drive, l.WorkflowID) {
			b.runs = append(b.runs, marotte.EngineBusyRun{WorkflowID: l.WorkflowID, Name: rt.runs.tabLabel(l.WorkflowID)})
		}
	}
	b.unsettled = !rt.runs.mutations.empty()
	return b
}

func compareChatIDs(x, y marotte.ChatID) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func (rt *Runtime) publishEngineStatus(st *marotte.EngineStatus) {
	rt.bus.Broadcast(rt.lifecycle.shutdownCtx, marotte.NewEvent(marotte.EventEngineStatus, "", *st))
}

// onEngineReady runs after each generation is ready. The first one makes the boot observer pass: the governance
// profile and the slash menu's seed arrive at its door, and the administrator rules are read on it.
func (rt *Runtime) onEngineReady(uint64) {
	rt.bootPass.Do(func() {
		rt.lifecycle.goUnlessDraining(func() {
			ctx, cancel := context.WithTimeout(rt.lifecycle.shutdownCtx, governanceWarmTimeout)
			defer cancel()
			if err := rt.config.warmGovernance(ctx); err != nil {
				slog.Warn("governance: the boot observer pass failed; the next session door reads it", "error", err)
			}
		})
	})
	rt.standing.refetchSoon()
	// Off the supervisor: it must be back on the engine's end while the init's observer pass runs.
	rt.lifecycle.goUnlessDraining(func() { rt.EnsureCodeIntelligence(rt.lifecycle.shutdownCtx) })
}

// onEngineEnded runs once generation gen ended and was reaped: its unsettled mutations go (their writes meet a dead
// engine), and every MCP server it ran is reported gone.
func (rt *Runtime) onEngineEnded(gen uint64) {
	rt.runs.mutations.dropGeneration(gen)
	rt.mcpRegistry.clearAll(rt.lifecycle.shutdownCtx)
}

// closeSession ends a session the registry let go of, off the caller's path and bounded by the session's own close
// budget; Shutdown waits for it.
func (rt *Runtime) closeSession(s engineSession) {
	if !rt.lifecycle.goUnlessDraining(func() {
		if err := s.Close(rt.lifecycle.shutdownCtx); err != nil {
			slog.Warn("a session did not close", "session_id", s.SessionID(), "error", err)
		}
	}) {
		slog.Debug("a session close was skipped: the runtime is draining and the engine stops", "session_id", s.SessionID())
	}
}

// onSignInChange applies a committed sign-in edge: the engine fingerprint holds the pending account, and the first
// sign-in reloads resident sessions at their next idle open, so their model lists follow the account.
func (rt *Runtime) onSignInChange(c signin.Change) {
	rt.engine.reconcile(rt.lifecycle.shutdownCtx)
	if c.To == signin.StatusSignedIn && c.From != signin.StatusSignedIn {
		rt.markSessionsForReload("sign-in")
	}
}
