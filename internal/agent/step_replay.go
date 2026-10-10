package agent

// The step-replay registry: one step's `session/load` replay, keyed by ACP session id (a step has no chat). The
// reader takes and drops the projection; no chat store is touched. Completion is replay_drain.go's.

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// stepReplays holds the step replays in flight, keyed by ACP session id. Its zero
// value is usable.
type stepReplays struct {
	replays map[string]*stepReplay
	mu      sync.Mutex
}

// stepReplay is one step's accumulating transcript, guarded by stepReplays.mu.
type stepReplay struct {
	proj *translate.EntryProjection
	// settled is the barrier the reader waits on; closed exactly once.
	settled chan struct{}
	// frames counts replay frames ingested; zero turns from many is a decoding bug.
	frames int
	// drain is the completion condition; last so pointer fields stay ahead (fieldalignment).
	drain replayDrain
}

// open starts a replay for a session about to load and reports whether it is the first reader. A second is
// refused: superseding would strand the first on a barrier nothing closes. The caller builds the projection.
func (sr *stepReplays) open(sessionID string, proj *translate.EntryProjection) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.replays == nil {
		sr.replays = make(map[string]*stepReplay)
	}
	if _, dup := sr.replays[sessionID]; dup {
		return false
	}
	sr.replays[sessionID] = &stepReplay{
		proj:    proj,
		settled: make(chan struct{}),
	}
	return true
}

// False (no reader) is ordinary, checked before a foreign-frame warning.
func (sr *stepReplays) ingest(sessionID string, kind marotte.ACPUpdateKind, raw json.RawMessage) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return false
	}
	rep.proj.Ingest(kind, raw)
	rep.frames++
	return true
}

// markLoadedAt records the `session/load` response's position and attempts one settle, from the reader's
// goroutine: forward may already have drained every frame.
func (sr *stepReplays) markLoadedAt(sessionID string, at drainPoint) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return
	}
	rep.drain.markLoadedAt(at)
	sr.settleLocked(sessionID, rep, at.gen, false, settleOnLoad)
}

// A closed channel is stateless.
var closedBarrier = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// barrier returns a channel closed once sessionID's replay drained, or already closed with no replay open.
func (sr *stepReplays) barrier(sessionID string) <-chan struct{} {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if rep := sr.replays[sessionID]; rep != nil {
		return rep.settled
	}
	return closedBarrier
}

// settleConsumed folds one observation into every open replay and closes each caught-up barrier. No session id:
// the position is the bridge's read loop's. `force` seals at exit.
func (sr *stepReplays) settleConsumed(at drainPoint, force bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	trigger := settleOnFrame
	if force {
		trigger = settleOnExit
	}
	for id, rep := range sr.replays {
		rep.drain.noteConsumed(at)
		sr.settleLocked(id, rep, at.gen, force, trigger)
	}
}

// settleLocked closes one replay's barrier when its drain is complete, logging the
// settle exactly once. Caller holds sr.mu.
func (sr *stepReplays) settleLocked(sessionID string, rep *stepReplay, gen uint64, force bool, trigger string) {
	if !rep.drain.complete(gen, force) {
		return
	}
	if sr.closeLocked(rep) {
		slog.Debug("step replay settled",
			"session_id", sessionID, "frames", rep.frames, "trigger", trigger)
	}
}

// take removes sessionID's replay and returns its projection, on success and timeout alike, so nothing leaks.
func (sr *stepReplays) take(sessionID string) []translate.ProjectedTurn {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	rep := sr.replays[sessionID]
	if rep == nil {
		return nil
	}
	delete(sr.replays, sessionID)
	// The timeout path's barrier has nothing else to close it.
	sr.closeLocked(rep)
	return rep.proj.Turns()
}

// closeLocked closes a barrier at most once, reporting whether this call did (three paths reach it). Caller holds sr.mu.
func (*stepReplays) closeLocked(rep *stepReplay) bool {
	select {
	case <-rep.settled:
		return false
	default:
		close(rep.settled)
		return true
	}
}
