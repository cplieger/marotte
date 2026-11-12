package agent

// A step whose turn stays open and sends no frame (runSilenceBound). marotte has no KAS signal to relay, so it only
// shows what it observes: the run read's `silent_since`, a run_progress `silence` frame and one push per silence.

import (
	"context"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/translate"
)

const logMsgRunSilent = "a workflow step's turn has sent nothing for an hour; flagging the run and leaving it running"

// noteFrame stamps an armed run's latest frame, reporting whether it cleared the run's silence flag. A run never
// armed has no entry and gets none, so frames of a run marotte does not bound leave nothing behind.
func (rs *Runs) noteFrame(workflowID string) (cleared bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, armed := rs.bounds.lastFrame[workflowID]; !armed {
		return false
	}
	rs.bounds.lastFrame[workflowID] = time.Now()
	if _, flagged := rs.bounds.silent[workflowID]; !flagged {
		return false
	}
	delete(rs.bounds.silent, workflowID)
	return true
}

// framed stamps every `_kiro/workflow/*` frame as the run's activity, a watch's idle poll included.
func (rs *Runs) framed(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		if id := workflowIDOfFrame(msg); id != "" && rs.noteFrame(id) {
			rs.announceSilence(id)
		}
		next(ctx, chatID, msg)
	}
}

var _ runAskListener = (*Runs)(nil)

// askOpened files a step's ask on its turn, after stamping it as the run's activity: actOnSilence reads the tree
// before the clock, so every stamp must precede the change it announces.
func (rs *Runs) askOpened(workflowID, nodeID string, askID int64) {
	rs.askSeen(workflowID)
	if rs.log != nil {
		rs.log.askOpened(workflowID, nodeID, askID)
	}
}

// askSettled restarts the run's silence when one of its asks settles, then closes it on its step: the wait was not
// silence.
func (rs *Runs) askSettled(workflowID, nodeID string, askID int64) {
	rs.askSeen(workflowID)
	if rs.log != nil {
		rs.log.askClosed(workflowID, nodeID, askID, time.Now())
	}
}

func (rs *Runs) askSeen(workflowID string) {
	if rs.noteFrame(workflowID) {
		rs.announceSilence(workflowID)
	}
}

// containerNode reports whether a KAS node of this type only orchestrates children (sequence, repeat, parallel).
func containerNode(nodeType string) bool {
	switch nodeType {
	case "sequence", "repeat", "parallel":
		return true
	}
	return false
}

// silenceCandidate reports whether a step of the run counts toward its silence at now (countsTowardSilence). It is the
// one place that decides, reading the whole tree under one hold of mu.
func (r *runLog) silenceCandidate(workflowID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[workflowID]
	if !ok {
		return false
	}
	for _, t := range rec.open {
		if t.countsTowardSilence(now) {
			return true
		}
	}
	return false
}

// countsTowardSilence is whether the open turn is a step that may be hung: not a container (its children count), not
// a watch (its own idleTimeoutSec bounds it), not parked by KAS (node_paused; a person or a heal resumes it), with
// no ask open and none settled less than an idle window ago (a person is its bound, and an answer is not silence).
// An untyped turn is a step's.
func (t *runTurn) countsTowardSilence(now time.Time) bool {
	if containerNode(t.nodeType) || t.nodeType == "watch" || t.parked || len(t.asks) > 0 {
		return false
	}
	return now.Sub(t.askClosed) >= runIdleWindow
}

// askOpened files an open ask on each of the run's open turns of node nodeID.
func (r *runLog) askOpened(workflowID, nodeID string, askID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.nodeTurnsLocked(workflowID, nodeID) {
		if t.asks == nil {
			t.asks = map[int64]struct{}{}
		}
		t.asks[askID] = struct{}{}
	}
}

// askClosed settles an ask on the run's open turns of node nodeID at at.
func (r *runLog) askClosed(workflowID, nodeID string, askID int64, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.nodeTurnsLocked(workflowID, nodeID) {
		if _, open := t.asks[askID]; open {
			delete(t.asks, askID)
			t.askClosed = at
		}
	}
}

func (r *runLog) nodeTurnsLocked(workflowID, nodeID string) []*runTurn {
	rec, ok := r.runs[workflowID]
	if !ok || nodeID == "" {
		return nil
	}
	var out []*runTurn
	for _, t := range rec.open {
		if t.nodeID == nodeID {
			out = append(out, t)
		}
	}
	return out
}

// nodeIDOf is the step's node id, the last segment of its path; "" when the opening frame carried none.
func nodeIDOf(step *translate.RunStep) string {
	if len(step.Path) == 0 {
		return ""
	}
	return step.Path[len(step.Path)-1]
}

// silentSince is when the flagged run's silence began, zero when it is not flagged.
func (rs *Runs) silentSince(workflowID string) time.Time {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bounds.silent[workflowID]
}

// actOnSilence acts on a run with a step that counts toward its silence (silenceCandidate) and no frame for
// runSilenceBound, true when the run is to stop as stalled. A chat-launched run is flagged instead and, once per
// silence, the user told: a person decides whether the step is hung (Mark step failed, Cancel), since a long tool call
// looks the same. A paused run is not silent, whose stale timer may still fire. The tree is read before the clock,
// which is re-read under mu with the lease: a frame landing after the tree was read stamps the clock first.
func (rs *Runs) actOnSilence(ctx context.Context, workflowID string) (stop bool) {
	if rs.log == nil || !rs.log.silenceCandidate(workflowID, time.Now()) {
		return false
	}
	parented := rs.chatResumes(workflowID)
	store := rs.leaseStore()
	rs.mu.Lock()
	last, armed := rs.bounds.lastFrame[workflowID]
	if l, held := store.Get(workflowID); !held || !l.Bounded() || !armed || time.Since(last) < runSilenceBound {
		rs.mu.Unlock()
		return false
	}
	if !parented {
		rs.mu.Unlock()
		return true
	}
	_, flagged := rs.bounds.silent[workflowID]
	if !flagged {
		if rs.bounds.silent == nil {
			rs.bounds.silent = map[string]time.Time{}
		}
		rs.bounds.silent[workflowID] = last
	}
	rs.mu.Unlock()
	if flagged {
		return false
	}
	l, held := rs.lease(workflowID)
	slog.Warn(logMsgRunSilent, "workflow_id", workflowID, "silent_since", last, "recipe", l.Recipe)
	rs.announceSilence(workflowID)
	if held && rs.coord != nil {
		n := notice.StepSilent(notice.RunTarget(workflowID, rs.leasedRunName(&l)))
		rs.coord.Notify(ctx, marotte.ChatID(l.ChatID), &n)
	}
	return false
}

// announceSilence sends the run's chat a run-level `silence` frame, on which the run view reads the run again.
func (rs *Runs) announceSilence(workflowID string) {
	l, held := rs.lease(workflowID)
	if !held || rs.bus == nil {
		return
	}
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	rs.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunProgress, marotte.ChatID(l.ChatID),
		marotte.RunProgressPayload{WorkflowID: workflowID, Kind: marotte.RunProgressSilence}))
}

// refillWorking is the idle expiry's refill for a run with open work: a full window, cut short at the hour of silence
// so the flag or stop lands on time rather than up to a window late.
func (rs *Runs) refillWorking(ctx context.Context, workflowID string) bool {
	return rs.stampDeadline(ctx, workflowID, func(l runlease.Lease, now time.Time) (time.Time, bool) {
		if !l.Bounded() {
			return time.Time{}, false
		}
		next := runlease.NextDeadline(now, runBounds(&l))
		_, flagged := rs.bounds.silent[workflowID]
		if last, armed := rs.bounds.lastFrame[workflowID]; armed && !flagged {
			if due := last.Add(runSilenceBound); due.After(now) && due.Before(next) {
				next = due
			}
		}
		return next, true
	})
}
