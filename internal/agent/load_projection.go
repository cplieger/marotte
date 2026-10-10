package agent

// Replay-projection lifecycle. Replay frames reach notifCh before session/load returns, so
// replay_drain.go owns the completion condition; bridge.replayBudget bounds the RPC. A load
// that never returned discards the projection.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
)

// replayStore is the chat store as the resume merge reaches it: the provenance snapshotted at
// open, and the locked swap. The provenance is a store read because the log is reachable only
// inside a Reconcile callback.
type replayStore interface {
	NewestRevert(ctx context.Context, chatID marotte.ChatID) (string, bool)
	Reconcile(ctx context.Context, chatID marotte.ChatID, swap func(l *chat.EntryLog, h chat.EntryHeader) (bool, error)) (version string, changed bool, err error)
}

// replay owns the session/load projection: the in-flight rebuild of a chat's history from KAS's
// replay and the swap that merges it.
type replay struct {
	chats replayStore
	// lifetime supplies the context the swap runs under.
	lifetime *lifetime
	// projections are the rebuilds in flight, keyed by chat.
	projections map[marotte.ChatID]*loadProjection
	// onProjection receives a settled projection without projMu held: the swap writes the chat store,
	// which could deadlock against a replay frame. Its error settles the load's gate.
	onProjection func(chatID marotte.ChatID, lp *loadProjection) error
	// underLifecycle runs the swap under the chat's lifecycle mutex, which the rewind holds across its
	// truncate. Nil runs it unserialized (bare projection tests).
	underLifecycle func(ctx context.Context, chatID marotte.ChatID, fn func() error) error
	// broadcast publishes the replacement announcement.
	broadcast func(context.Context, marotte.ServerEvent)
	// workDir is the root projected diff paths are relative to; a value because projection tests carry no lifetime.
	workDir string
	projMu  sync.Mutex
}

// loadProjection is one in-flight session/load transcript. Guarded by Runtime.projMu.
type loadProjection struct {
	proj *translate.EntryProjection
	// settled is the loading bridge's gate, settled by the swap's outcome.
	settled *sessionSettle
	// sessionID is the session the replay came from: turn pairing's scope.
	sessionID string
	// snapshot is the newest turn_revert id at open, or empty; the swap refuses a log reverted since.
	snapshot string
	// frames counts ingested replay frames: many frames and zero turns is a decoding bug.
	frames int
	// drain is the completion condition shared with the step route. Last for govet fieldalignment.
	drain replayDrain
}

// The three settle triggers, for the settle log: the post-load attempt, a consumed frame's position, and the exit seal.
const (
	settleOnLoad  = "load"
	settleOnFrame = "frame"
	settleOnExit  = "exit"
)

// OpenReplayProjection starts a projection before a session/load, snapshotting the newest
// turn_revert; its swap settles settled. An open one is discarded: only a re-load reaches this twice.
func (rp *replay) OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string, settled *sessionSettle) {
	snapshot, _ := rp.chats.NewestRevert(ctx, chatID)
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if rp.projections == nil {
		rp.projections = make(map[marotte.ChatID]*loadProjection)
	}
	if old, dup := rp.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
		if old.settled != settled {
			old.settled.settle(errReplaySuperseded)
		}
	}
	rp.projections[chatID] = &loadProjection{
		proj:      translate.NewEntryProjection(newMessageID, rp.workDir),
		sessionID: sessionID,
		snapshot:  snapshot,
		settled:   settled,
	}
}

// MarkReplayLoadedAt records the session/load response's read-loop position and attempts one
// settle, from the spawn goroutine: a replay already drained has no later frame to notice it.
func (rp *replay) MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint) {
	lp := rp.claimSettled(chatID, at.gen, false, func(d *replayDrain) { d.markLoadedAt(at) })
	rp.adopt(chatID, lp, settleOnLoad)
}

// DiscardReplayProjection drops a chat's projection unsettled after a failed load; the fresh
// session the spawn falls back to settles its gate.
func (rp *replay) DiscardReplayProjection(chatID marotte.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if _, open := rp.projections[chatID]; open {
		delete(rp.projections, chatID)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

func (rp *replay) ingestReplayFrame(chatID marotte.ChatID, kind marotte.ACPUpdateKind, raw json.RawMessage) bool {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return false
	}
	lp.proj.Ingest(kind, raw)
	lp.frames++
	return true
}

// SettleReplayProjection folds one drain observation into the chat's projection and completes it
// once everything preceding the load result is folded. `force` is the exit seal, bypassing the
// position. No-op with no projection open.
func (rp *replay) SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool) {
	lp := rp.claimSettled(chatID, at.gen, force, func(d *replayDrain) { d.noteConsumed(at) })
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	rp.adopt(chatID, lp, trigger)
}

// claimSettled applies one observation and, when the replay is complete, hands the projection to
// exactly one caller: three triggers on two goroutines reach it.
func (rp *replay) claimSettled(chatID marotte.ChatID, gen uint64, force bool, note func(*replayDrain)) *loadProjection {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	lp := rp.projections[chatID]
	if lp == nil {
		return nil
	}
	note(&lp.drain)
	if !lp.drain.complete(gen, force) {
		return nil
	}
	delete(rp.projections, chatID)
	return lp
}

// nil means nothing was claimed.
func (rp *replay) adopt(chatID marotte.ChatID, lp *loadProjection, trigger string) {
	if lp == nil {
		return
	}
	slog.Info("replay projection settled",
		"chat_id", chatID, "frames", lp.frames, "trigger", trigger)
	var err error
	if rp.onProjection != nil {
		err = rp.onProjection(chatID, lp)
	}
	lp.settled.settle(err)
}

// swapProjectedTranscript merges a settled replay through swapMerged's gates under the lifecycle
// mutex, never holding projMu. A rewrite is announced as subject_changed with its minted version.
func (rp *replay) swapProjectedTranscript(chatID marotte.ChatID, lp *loadProjection) error {
	ctx := durable.Context(rp.lifetime.shutdownCtx)
	projected := lp.proj.Turns()
	var version string
	var changed bool
	swap := func() error {
		var err error
		version, changed, err = rp.chats.Reconcile(ctx, chatID, func(l *chat.EntryLog, h chat.EntryHeader) (bool, error) {
			// The whole file, reverted material included: the swap ends in a rewrite, and the surviving view
			// would compact the rewind's window away.
			entries, reverted, readErr := l.AllWithReverted()
			if readErr != nil {
				return false, readErr
			}
			return swapMerged(ctx, &swapRequest{
				Log:       l,
				Header:    h,
				SessionID: lp.sessionID,
				Record:    recordTurnsOf(entries, reverted),
				Projected: projected,
				Snapshot:  lp.snapshot,
			})
		})
		return err
	}
	var err error
	if rp.underLifecycle != nil {
		err = rp.underLifecycle(ctx, chatID, swap)
	} else {
		err = swap()
	}
	if err != nil {
		slog.Error("replay projection: swap failed", "chat_id", chatID, "error", err)
		return err
	}
	slog.Info("replay projection: merged",
		"chat_id", chatID, "projected_turns", len(projected), "rewritten", changed)
	if changed {
		frame := marotte.NewEvent(marotte.EventSubjectChanged, chatID, marotte.SubjectChangedPayload{})
		frame.Subject = marotte.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
		rp.broadcast(ctx, frame)
	}
	return nil
}
