package command

// KAS's steer buffer has two verbs, append and clear, and its read cursor does not
// rewind on a clear (kirodotdev/Kiro#11449), so deleting one steer clears and
// resends the user's kept rows as ONE combined probe whose fate is observed.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerRemoveBudget bounds an op's RPCs once its rows are marked. They run on a
// context the reader's disconnect does not cancel: a delete cut off between its
// clear and its resubmit would strand the rows it kept.
const steerRemoveBudget = 30 * time.Second

// ResendBridgeWait bounds the wait for a dead bridge's frames to fold before its
// unread steers are resent: a resend admitted earlier would reach the dying process.
// A var only so tests can shorten it.
var ResendBridgeWait = 10 * time.Second

var (
	errSteerRemoveNotWaiting = errors.New("that message is no longer waiting, because the agent has read it or the turn ended")
	errSteerRemoveNotUser    = errors.New("only a message you sent can be deleted")
	errSteerNotQueueable     = errors.New("only a message you sent in this chat can be moved to the queue")
	errSteerRemoveAgentRows  = errors.New("a workflow result is waiting beside it, so only discarding them all is possible")
	errSteerRemoveSettling   = errors.New("the agent is still reading or dropping the messages it held when its turn ended, so a message it holds cannot be deleted until it is done. Try again in a moment")
	errSteerRemoveConsumed   = errors.New("the agent read that message before it could be deleted")
	errSteerChatGone         = errors.New("this chat is closing")
	errSteerNoReply          = errors.New("the agent did not answer the clear, so nothing was changed")
)

func steerRefusal(reason string) error {
	switch reason {
	case SteerRefuseNotUser:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveNotUser)
	case SteerRefuseNotQueueable:
		return StatusErrorReason(http.StatusConflict, reason, errSteerNotQueueable)
	case SteerRefuseAgentRows:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveAgentRows)
	case SteerRefuseSettling:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveSettling)
	case SteerRefuseChatGone:
		return StatusErrorReason(http.StatusConflict, reason, errSteerChatGone)
	case SteerRefuseConsumed:
		return StatusErrorReason(http.StatusConflict, SteerRefuseNotWaiting, errSteerRemoveConsumed)
	case SteerRefuseNoReply:
		return StatusError(http.StatusBadGateway, errSteerNoReply)
	default:
		return StatusErrorReason(http.StatusNotFound, SteerRefuseNotWaiting, errSteerRemoveNotWaiting)
	}
}

// cmdSteerRemove deletes one dock row the user sent, or with Queue moves it to the chat's queue. Once
// the target is out of the dock the answer is success whatever became of the resubmit: the server
// keeps custody of the kept rows, and their frames say where they went.
func cmdSteerRemove(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SteerRemoveCommand
	if json.Unmarshal(cmd.Payload, &p) != nil || p.SteerID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if err := removeChatSteer(ctx, roles, cmd.ChatID, p.SteerID, p.Queue); err != nil {
		return nil, err
	}
	if p.Queue {
		return responseWith(map[string]any{"queued": p.SteerID}), nil
	}
	return removed(p.SteerID), nil
}

// removeChatSteer takes the row steerID out of the chat's dock under its steer lock: deleted, or with
// queue handed back to the chat's run list as a queued row.
func removeChatSteer(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, steerID string, queue bool) error {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	t := roles.steerTarget(chatID)
	begin := t.Queue.BeginRemove
	if queue {
		begin = t.Queue.BeginQueueInstead
	}
	return takeSteerOut(ctx, t, steerID, begin, func(opID string) { roles.queue.EndOp(chatID, opID) })
}

// RemoveSteerFrom deletes the row steerID from t's buffer: a row KAS cannot hold goes at once, else
// one clear and one combined resubmit of the user's kept rows. endOp ends the op under the steer lock
// the caller holds; it runs only when a clear was issued.
func RemoveSteerFrom(ctx context.Context, t SteerTarget, steerID string, endOp func(opID string)) error {
	return takeSteerOut(ctx, t, steerID, t.Queue.BeginRemove, endOp)
}

func takeSteerOut(
	ctx context.Context, t SteerTarget, steerID string,
	begin func(chatID marotte.ChatID, key, opID string) (needsClear bool, refuse string), endOp func(opID string),
) error {
	opID := ids.NewMessageID()
	needsClear, refuse := begin(t.Key, steerID, opID)
	if refuse != "" {
		return steerRefusal(refuse)
	}
	if !needsClear {
		return nil
	}
	rpcCtx, cancel := context.WithTimeout(durable.Context(ctx), steerRemoveBudget)
	defer cancel()
	defer endOp(opID)
	cleared, landed := clearSteerBuffer(rpcCtx, t)
	res := t.Queue.RemoveCleared(t.Key, opID, cleared, landed)
	if res.Reason != "" && res.Reason != SteerRefuseConsumed {
		return steerRefusal(res.Reason)
	}
	if res.Resend != nil {
		_ = issueSteers(rpcCtx, t, []SteerSend{*res.Resend}, "", func(s SteerSend, queued bool, err error) {
			t.Queue.OpSent(t.Key, opID, s, queued, err)
		})
	}
	if res.Reason == SteerRefuseConsumed {
		return steerRefusal(res.Reason)
	}
	return nil
}

func removed(key string) map[string]any {
	return responseWith(map[string]any{"deleted": key})
}

// runSteerJobs runs each flush on its own goroutine under the chat's steer lock:
// the record hands them out from a fold, which must never wait on a command.
func runSteerJobs(roles *promptRoles) func(SteerJob) {
	return func(job SteerJob) {
		roles.lifecycle.InflightAdd(1)
		go func() {
			defer roles.lifecycle.InflightDone()
			ctx, cancel := roles.lifecycle.TurnContext(context.Background())
			defer cancel()
			unlock, err := roles.queue.LockSteerOps(ctx, job.Chat)
			if err != nil {
				return
			}
			defer unlock()
			flushSteers(ctx, roles, job.Chat)
		}()
	}
}

func flushSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	SendPlanned(ctx, roles.steerTarget(chatID), roles.jobs.PlanFlush(chatID))
}

// resolveEnds settles every pending turn end whose turn opened at or before upTo,
// oldest first, leaving each end's rows unsent and unowned. A newer close's end is
// left for that close. The caller holds the steer lock; nothing is written to any
// store, so resolving cannot fail.
func resolveEnds(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, upTo uint64) {
	for _, e := range roles.jobs.Ends(chatID) {
		if e.End.TurnSeq > upTo {
			continue
		}
		rows, gone := roles.jobs.JobRows(chatID, e.Owner)
		if gone {
			return
		}
		switch {
		case len(rows) == 0:
		case e.End.BridgeDeath:
			// A dead buffer holds nothing; KAS's own re-injection is the post-load
			// clear's to decide.
			roles.jobs.StraysCleared(chatID, e.Owner, nil, true)
			if awaitForwarder(ctx, e.End.Exit) {
				roles.jobs.DeathDrained(chatID, e.Owner)
			} else {
				slog.Warn("steer resend: the dead bridge did not drain; the rows wait for the next prompt's clear",
					"chat_id", chatID)
			}
		default:
			settleStrays(ctx, roles, chatID, e.Owner, rows)
		}
		roles.jobs.EndUnsent(chatID, e.Owner)
	}
}

// awaitForwarder waits for a dead bridge's forward goroutine, bounded by
// ResendBridgeWait and ctx. The old forwarder's folds take the record mutex, never
// the steer lock, so the wait cannot deadlock on them.
func awaitForwarder(ctx context.Context, exit <-chan struct{}) bool {
	if exit == nil {
		return true
	}
	timer := time.NewTimer(ResendBridgeWait)
	defer timer.Stop()
	select {
	case <-exit:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// settleStrays handles a row still in KAS when its turn closed: resending one KAS still holds would
// deliver it twice, so only a landed clear proves it gone. An admission holder (whose fresh cursor
// may read it) or a waiting agent row (a clear drops it with no re-wake) releases the strays where
// they are.
func settleStrays(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, owner string, rows []SteerRow) {
	stray := false
	for _, r := range rows {
		stray = stray || r.InKAS
	}
	if !stray {
		return
	}
	if _, held := roles.admission.AdmissionHolderSource(chatID); held || roles.jobs.AgentRowsWaiting(chatID) {
		roles.jobs.Release(chatID, owner, true)
		return
	}
	if _, landed := clearSteerBuffer(ctx, roles.steerTarget(chatID)); landed {
		roles.jobs.StraysCleared(chatID, owner, nil, true)
	} else {
		roles.jobs.Release(chatID, owner, true)
	}
}

// resolveAfterClose is a close pipeline's first step: resolve the closed turn's
// ends and older ones.
func resolveAfterClose(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence) {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return
	}
	defer unlock()
	resolveEnds(ctx, roles, chatID, fence.Seq)
}
