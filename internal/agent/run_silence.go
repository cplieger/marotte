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

// silentFor is how long the run has sent no frame since its last frame or arm, zero for a run never armed.
func (rs *Runs) silentFor(workflowID string) time.Duration {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	last, armed := rs.bounds.lastFrame[workflowID]
	if !armed {
		return 0
	}
	return time.Since(last)
}

// silentSince is when the flagged run's silence began, zero when it is not flagged.
func (rs *Runs) silentSince(workflowID string) time.Time {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.bounds.silent[workflowID]
}

// flagSilent marks a chat-launched run silent and, once per silence, tells the user: a person decides whether the
// step is hung (Mark step failed, Cancel), since a long tool call looks the same.
func (rs *Runs) flagSilent(ctx context.Context, workflowID string) {
	rs.mu.Lock()
	_, flagged := rs.bounds.silent[workflowID]
	last, armed := rs.bounds.lastFrame[workflowID]
	if !flagged && armed {
		if rs.bounds.silent == nil {
			rs.bounds.silent = map[string]time.Time{}
		}
		rs.bounds.silent[workflowID] = last
	}
	rs.mu.Unlock()
	if flagged || !armed {
		return
	}
	l, held := rs.lease(workflowID)
	slog.Warn(logMsgRunSilent, "workflow_id", workflowID, "silent_since", last, "recipe", l.Recipe)
	rs.announceSilence(workflowID)
	if held && rs.coord != nil {
		n := notice.StepSilent(notice.RunTarget(workflowID, rs.leasedRunName(&l)))
		rs.coord.Notify(ctx, marotte.ChatID(l.ChatID), &n)
	}
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

// refillWorking is the idle expiry's refill for a run with an open step turn: a full window, cut short at the hour
// of silence so the flag or stop lands on time rather than up to a window late.
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
