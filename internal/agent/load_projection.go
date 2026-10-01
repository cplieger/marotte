package agent

// Replay-projection lifecycle. Replay frames are PUSHED to the bridge's buffered
// notifCh before session/load returns, so settling at Start's return would race a
// partial transcript; replay_drain.go owns the completion condition instead, and the
// step route reads the same type. bridge.replayBudget bounds the RPC above, so the
// condition needs no timeout of its own. A load that never returned leaves the drain
// unloaded and the projection is DISCARDED.

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

// replayStore is the chat store as the resume's merge reaches it: the provenance the
// projection snapshots at open, and the locked swap over log and header. The
// provenance is a STORE read because the log is reachable only inside a Reconcile
// callback, which is the swap's own path and announces a rewrite.
//
// TWO methods, not three: the record read this held for the revision snapshot has no
// caller left once the provenance comes off the log.
type replayStore interface {
	NewestRevert(ctx context.Context, chatID marotte.ChatID) (string, bool)
	Reconcile(ctx context.Context, chatID marotte.ChatID, swap func(l *chat.EntryLog, h chat.EntryHeader) (bool, error)) (version string, changed bool, err error)
}

// replay owns the session/load transcript projection: the in-flight rebuild of a
// chat's history from KAS's own replay, and the swap that merges it into the record.
type replay struct {
	chats replayStore
	// lifetime supplies the context the swap runs under.
	lifetime *lifetime
	// projections are the rebuilds in flight, keyed by chat.
	projections map[marotte.ChatID]*loadProjection
	// onProjection receives a settled projection, called WITHOUT projMu held: the swap
	// writes the chat store, and holding the lock across that would let a store
	// mutation and a replay frame deadlock against each other.
	onProjection func(chatID marotte.ChatID, lp *loadProjection)
	// underLifecycle runs the swap under the chat's lifecycle mutex, the same one the
	// rewind holds across its truncate, so the merge's gates and its rewrite see one
	// log. Nil runs the swap unserialized (the projection tests build a bare replay).
	underLifecycle func(ctx context.Context, chatID marotte.ChatID, fn func() error) error
	// broadcast publishes the replacement announcement.
	broadcast func(context.Context, marotte.ServerEvent)
	// workDir is the workspace root a projected diff path is made relative to. A VALUE
	// rather than a read through lifetime, because the projection tests build a bare
	// replay carrying no lifetime and still open a projection.
	workDir string
	projMu  sync.Mutex
}

// loadProjection is one in-flight session/load's accumulating transcript.
// Guarded by Runtime.projMu; the fields are not independently safe.
type loadProjection struct {
	proj *translate.EntryProjection
	// sessionID is the session the replay came from: turn pairing's scope.
	sessionID string
	// snapshot is the newest turn_revert's entry id when the projection opened, empty
	// when the log held none; the swap refuses a log a revert landed in since.
	snapshot string
	// frames counts replay frames ingested: many frames projecting zero turns is a
	// decoding bug.
	frames int
	// drain is the completion condition, shared with the step route. Last so every
	// pointer field stays ahead of it (govet fieldalignment).
	drain replayDrain
}

// The three triggers a settle can run from, for the settle log: the reader's own
// post-load attempt, the position a consumed frame reached, and the bridge-exit seal.
const (
	settleOnLoad  = "load"
	settleOnFrame = "frame"
	settleOnExit  = "exit"
)

// OpenReplayProjection starts a projection for a chat about to session/load its
// session, snapshotting the log's newest turn_revert. A projection already open for
// that chat is discarded: the only way to reach this twice is a re-load (model-switch
// fallback), whose replay supersedes.
func (rp *replay) OpenReplayProjection(ctx context.Context, chatID marotte.ChatID, sessionID string) {
	snapshot, _ := rp.chats.NewestRevert(ctx, chatID)
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if rp.projections == nil {
		rp.projections = make(map[marotte.ChatID]*loadProjection)
	}
	if _, dup := rp.projections[chatID]; dup {
		slog.Debug("replay projection: superseding an open one", "chat_id", chatID)
	}
	rp.projections[chatID] = &loadProjection{
		proj:      translate.NewEntryProjection(newMessageID, rp.workDir),
		sessionID: sessionID,
		snapshot:  snapshot,
	}
}

// MarkReplayLoadedAt records the read-loop position the session/load response arrived
// at, and ATTEMPTS one settle. Called from the spawn goroutine.
//
// The attempt is the point: a replay Forward has already drained is complete HERE and
// no later frame is coming to notice it.
func (rp *replay) MarkReplayLoadedAt(chatID marotte.ChatID, at drainPoint) {
	lp := rp.claimSettled(chatID, at.gen, false, func(d *replayDrain) { d.markLoadedAt(at) })
	rp.adopt(chatID, lp, settleOnLoad)
}

// DiscardReplayProjection drops a chat's projection unsettled, when the load failed,
// so a half-built transcript cannot be adopted later.
func (rp *replay) DiscardReplayProjection(chatID marotte.ChatID) {
	rp.projMu.Lock()
	defer rp.projMu.Unlock()
	if _, open := rp.projections[chatID]; open {
		delete(rp.projections, chatID)
		slog.Debug("replay projection: discarded", "chat_id", chatID)
	}
}

// ingestReplayFrame folds one replay-tagged frame into the chat's open projection.
// Reports whether a projection consumed it, so the caller can drop a replay that
// arrived with no load in flight.
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

// SettleReplayProjection folds one drain observation into the chat's projection and
// completes it once the consumer has folded everything that preceded the load result.
// `at` is the frame's own position and the attachment that consumed it; `force` is the
// bridge-exit seal, which bypasses the position because no frame can advance it again.
//
// No-op when no projection is open, so callers may call it per frame.
func (rp *replay) SettleReplayProjection(chatID marotte.ChatID, at drainPoint, force bool) {
	lp := rp.claimSettled(chatID, at.gen, force, func(d *replayDrain) { d.noteConsumed(at) })
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	rp.adopt(chatID, lp, trigger)
}

// claimSettled applies one observation to the chat's drain and, when that leaves the
// replay complete, takes the projection out of `projections` — returning it to exactly
// one caller and nil to every other. The claim is what makes the settle run once: three
// triggers reach it, on two goroutines.
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

// adopt swaps a claimed projection into the record. Nil is the ordinary answer — a
// settle attempt that claimed nothing.
func (rp *replay) adopt(chatID marotte.ChatID, lp *loadProjection, trigger string) {
	if lp == nil {
		return
	}
	slog.Info("replay projection settled",
		"chat_id", chatID, "frames", lp.frames, "trigger", trigger)
	if rp.onProjection != nil {
		rp.onProjection(chatID, lp)
	}
}

// swapProjectedTranscript merges a settled replay into the chat's log through
// SwapMerged's two gates, under the lifecycle mutex so the gates and the rewrite see
// one log. Runs on the Forward goroutine OR on the spawn goroutine, so it must not
// hold projMu.
//
// It ANNOUNCES a rewrite as a subject_changed for the chat, stamped with the version
// the rewrite minted: the fetch instruction the client already honours, and the only
// frame that can tell a rewritten transcript from an appended one.
func (rp *replay) swapProjectedTranscript(chatID marotte.ChatID, lp *loadProjection) {
	ctx := durable.Context(rp.lifetime.shutdownCtx)
	projected := lp.proj.Turns()
	var version string
	var changed bool
	swap := func() error {
		var err error
		version, changed, err = rp.chats.Reconcile(ctx, chatID, func(l *chat.EntryLog, h chat.EntryHeader) (bool, error) {
			// The WHOLE file, reverted material included, because the swap ends in a
			// rewrite: the surviving view as input would make that rewrite a physical
			// compaction and the rewind's own record would name a window the log no
			// longer holds. The one caller of AllWithReverted.
			entries, reverted, readErr := l.AllWithReverted()
			if readErr != nil {
				return false, readErr
			}
			return SwapMerged(ctx, &Swap{
				Log:       l,
				Header:    h,
				SessionID: lp.sessionID,
				Record:    RecordTurnsOf(entries, reverted),
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
		return
	}
	slog.Info("replay projection: merged",
		"chat_id", chatID, "projected_turns", len(projected), "rewritten", changed)
	if changed {
		frame := marotte.NewEvent(marotte.EventSubjectChanged, chatID, marotte.SubjectChangedPayload{})
		frame.Subject = marotte.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
		rp.broadcast(ctx, frame)
	}
}
