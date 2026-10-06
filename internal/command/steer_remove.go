package command

// KAS's steer buffer has two verbs, append and clear, and its read cursor does not
// rewind on a clear (kirodotdev/Kiro#11449), so deleting one steer clears and
// resends the kept rows as ONE combined probe whose fate is observed.

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
	errSteerRemoveAgentRows  = errors.New("a workflow result is waiting beside it, so only discarding them all is possible")
	errSteerRemoveSettling   = errors.New("the turn ended and that message is being resent. Try again in a moment")
	errSteerRemoveConsumed   = errors.New("the agent read that message before it could be deleted")
	errSteerStarting         = errors.New("the agent is starting to read these messages. Try again in a moment")
	errSteerChatGone         = errors.New("this chat is closing")
	errSteerNoReply          = errors.New("the agent did not answer the clear, so nothing was changed")
)

func steerRefusal(reason string) error {
	switch reason {
	case SteerRefuseNotUser:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveNotUser)
	case SteerRefuseAgentRows:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveAgentRows)
	case SteerRefuseSettling:
		return StatusErrorReason(http.StatusConflict, reason, errSteerRemoveSettling)
	case SteerRefuseStarting:
		return StatusErrorReason(http.StatusConflict, reason, errSteerStarting)
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

// CmdSteerRemove deletes one dock row the user sent. Once the target is deleted the
// answer is success whatever became of the resubmit: the server keeps custody of the
// kept rows, and their frames say where they went.
func CmdSteerRemove(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand) (any, error) {
	if err := requireChatID(cmd); err != nil {
		return nil, err
	}
	var p marotte.SteerRemoveCommand
	if json.Unmarshal(cmd.Payload, &p) != nil || p.SteerID == "" {
		return nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	unlock, err := roles.queue.LockSteerOps(ctx, cmd.ChatID)
	if err != nil {
		return nil, StatusError(http.StatusServiceUnavailable, err)
	}
	defer unlock()
	opID := ids.NewMessageID()
	needsClear, refuse := roles.queue.BeginRemove(cmd.ChatID, p.SteerID, opID)
	if refuse != "" {
		return nil, steerRefusal(refuse)
	}
	if !needsClear {
		return removed(p.SteerID), nil
	}
	rpcCtx, cancel := context.WithTimeout(durable.Context(ctx), steerRemoveBudget)
	defer cancel()
	defer endSteerOp(ctx, roles, cmd.ChatID, opID)
	cleared, landed := clearSteerBuffer(rpcCtx, roles, cmd.ChatID)
	res := roles.queue.RemoveCleared(cmd.ChatID, opID, cleared, landed)
	if res.Reason != "" && res.Reason != SteerRefuseConsumed {
		return nil, steerRefusal(res.Reason)
	}
	if res.Resend != nil {
		_ = issueSteers(rpcCtx, roles, cmd.ChatID, []SteerSend{*res.Resend}, "", func(s SteerSend, queued bool, err error) {
			roles.queue.OpSent(cmd.ChatID, opID, s, queued, err)
		})
	}
	if res.Reason == SteerRefuseConsumed {
		return nil, steerRefusal(res.Reason)
	}
	return removed(p.SteerID), nil
}

func removed(key string) map[string]any {
	return responseWith(map[string]any{"deleted": key})
}

// endSteerOp ends an op that marked rows, still under the steer lock. A stale bind
// that ended the op's channel settled its rows, and they are routed here before
// the lock is released; a close's rows wait for that close's pipeline. The route
// runs on a turn context, not the op's RPC budget, which the clear may have spent.
func endSteerOp(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, opID string) {
	if roles.queue.EndOp(chatID, opID) {
		tctx, cancel := roles.lifecycle.TurnContext(ctx)
		defer cancel()
		routeIntoStartedPrompt(tctx, roles, chatID)
	}
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
			routeIntoStartedPrompt(ctx, roles, job.Chat)
		}()
	}
}

func flushSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	_ = issueSteers(ctx, roles, chatID, roles.jobs.PlanFlush(chatID), "", func(s SteerSend, queued bool, err error) {
		roles.queue.SteerSent(chatID, s, queued, err)
	})
}

// EndFacts is what resolving a chat's pending ends learned about bridge deaths:
// Death when a death end was resolved, Undrained when its forwarder never drained.
type EndFacts struct {
	Death     bool
	Undrained bool
}

// ResolveEnds settles every pending turn end whose turn opened at or before upTo,
// oldest first, leaving each end's rows unsent and unowned. A newer close's end is
// left for that close. The caller holds the steer lock; nothing is written to any
// store, so resolving cannot fail.
func ResolveEnds(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, upTo uint64) EndFacts {
	var facts EndFacts
	for _, e := range roles.jobs.Ends(chatID) {
		if e.End.TurnSeq > upTo {
			continue
		}
		rows, gone := roles.jobs.JobRows(chatID, e.Owner)
		if gone {
			return facts
		}
		switch {
		case len(rows) == 0:
		case e.End.BridgeDeath:
			facts.Death = true
			if !awaitForwarder(ctx, e.End.Exit) {
				facts.Undrained = true
				slog.Warn("steer resend: the dead bridge did not drain; the rows wait for the next prompt's clear",
					"chat_id", chatID)
			}
			// A dead buffer holds nothing; KAS's own re-injection is the post-load
			// clear's to decide.
			roles.jobs.StraysCleared(chatID, e.Owner, nil, true)
		default:
			settleStrays(ctx, roles, chatID, e.Owner, rows)
		}
		roles.jobs.EndUnsent(chatID, e.Owner)
	}
	return facts
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
	if _, landed := clearSteerBuffer(ctx, roles, chatID); landed {
		roles.jobs.StraysCleared(chatID, owner, nil, true)
	} else {
		roles.jobs.Release(chatID, owner, true)
	}
}

// routeIntoStartedPrompt sends the chat's unread rows into the prompt turn that
// holds admission, once that turn may be past its own delivery point. A prompt
// that has not started is left alone: its own deliverParkedSteers takes them. The
// caller holds the steer lock.
func routeIntoStartedPrompt(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	turnID, ok := roles.admission.PromptHolder(chatID)
	if !ok || roles.jobs.NeedsPostLoadClear(chatID) {
		return
	}
	deliverUnread(ctx, roles, chatID, turnID)
}

// resolveAfterClose is a close pipeline's first step: resolve the closed turn's
// ends and older ones, then route what that left unsent into a started prompt.
func resolveAfterClose(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, fence TurnFence) EndFacts {
	unlock, err := roles.queue.LockSteerOps(ctx, chatID)
	if err != nil {
		return EndFacts{}
	}
	defer unlock()
	facts := ResolveEnds(ctx, roles, chatID, fence.Seq)
	routeIntoStartedPrompt(ctx, roles, chatID)
	return facts
}
