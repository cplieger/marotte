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
const ResendBridgeWait = 10 * time.Second

var (
	errSteerRemoveNotWaiting = errors.New("that message is no longer waiting — the agent has read it or the turn ended")
	errSteerRemoveNotUser    = errors.New("only a message you sent can be deleted")
	errSteerRemoveAgentRows  = errors.New("a workflow result is waiting beside it, so only discarding them all is possible")
	errSteerRemoveSettling   = errors.New("the turn just ended and that message is being resent — try again in a moment")
	errSteerRemoveConsumed   = errors.New("the agent read that message before it could be deleted")
	errSteerStarting         = errors.New("the agent is starting to read these messages — try again in a moment")
	errSteerChatGone         = errors.New("this chat is closing")
	errSteerNoReply          = errors.New("the agent did not answer the clear — nothing was changed")
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
	handedOff := false
	defer func() {
		if !handedOff {
			unlock()
		}
	}()
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
	defer func() { handedOff = endSteerOp(ctx, roles, cmd.ChatID, opID, unlock) }()
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

// endSteerOp ends an op that marked rows, still under the steer lock. A turn that
// closed under it is resolved with its rows still the op's, on a goroutine that takes
// over unlock and answers true, so the command replies without waiting out the
// resend's admission. It runs on a turn context like runSteerJobs', not the op's RPC
// budget, which the clear and the resend may already have spent.
func endSteerOp(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, opID string, unlock func()) bool {
	end := roles.queue.EndOp(chatID, opID)
	if end == nil {
		return false
	}
	roles.lifecycle.InflightAdd(1)
	go func() {
		defer roles.lifecycle.InflightDone()
		defer unlock()
		tctx, cancel := roles.lifecycle.TurnContext(ctx)
		defer cancel()
		resolveTurnEnd(tctx, roles, chatID, opID, end)
	}()
	return true
}

// runSteerJobs runs each job on its own goroutine under the chat's steer lock: the
// record hands them out from a fold, which must never wait on a command.
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
			if job.End == nil {
				flushSteers(ctx, roles, job.Chat)
				return
			}
			resolveTurnEnd(ctx, roles, job.Chat, job.Owner, job.End)
		}()
	}
}

func flushSteers(ctx context.Context, roles *promptRoles, chatID marotte.ChatID) {
	_ = issueSteers(ctx, roles, chatID, roles.jobs.PlanFlush(chatID), "", func(s SteerSend, queued bool, err error) {
		roles.queue.SteerSent(chatID, s, queued, err)
	})
}

// resolveTurnEnd resends a turn's unread rows together, in order, as the next
// prompt. A dead bridge is waited out first, or a resend would reach the dying
// process; the closing prompt still holds admission until its own reply.
func resolveTurnEnd(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, owner string, end *SteerTurnEnd) {
	rows, gone := roles.jobs.JobRows(chatID, owner, end.Lead)
	if gone || len(rows) == 0 {
		return
	}
	if end.BridgeDeath && end.Exit != nil {
		select {
		case <-end.Exit:
		case <-time.After(ResendBridgeWait):
			slog.Warn("steer resend: the dead bridge did not drain; the rows wait for the next prompt", "chat_id", chatID)
			roles.jobs.Unsent(chatID, owner)
			return
		case <-ctx.Done():
			roles.jobs.Unsent(chatID, owner)
			return
		}
	}
	if roles.admission.ReserveTurnForPrompt(ctx, chatID, AdmissionWait) != AdmissionAcquired {
		rerouteRows(ctx, roles, chatID, owner, rows)
		return
	}
	settleStrays(ctx, roles, chatID, owner, end, rows)
	resendRows(ctx, roles, chatID, owner, end)
}

// settleStrays: a row still in KAS when its turn closed was sent after the turn's
// own clear. Resending one KAS still holds would deliver it twice, so a dead buffer
// and a clear's reply are the only proofs it is gone.
func settleStrays(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, owner string, end *SteerTurnEnd, rows []SteerRow) {
	stray := false
	for _, r := range rows {
		stray = stray || r.InKAS
	}
	switch {
	case !stray:
	case end.BridgeDeath:
		roles.jobs.StraysCleared(chatID, owner, nil, true)
	case roles.jobs.AgentRowsWaiting(chatID):
		// A clear would drop an agent row with no re-wake; the next prompt's fresh
		// cursor reads the strays where they are.
		roles.jobs.Release(chatID, owner, true)
	default:
		if _, landed := clearSteerBuffer(ctx, roles, chatID); landed {
			roles.jobs.StraysCleared(chatID, owner, nil, true)
		} else {
			roles.jobs.Release(chatID, owner, true)
		}
	}
}

// rerouteRows: a prompt the user sent takes the rows as its steers, its fresh cursor
// reading the ones KAS holds; any other holder leaves them unsent.
func rerouteRows(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, owner string, rows []SteerRow) {
	source, held := roles.admission.AdmissionHolderSource(chatID)
	if !held || !source.PromptClass() {
		roles.jobs.Unsent(chatID, owner)
		return
	}
	roles.jobs.Release(chatID, owner, false)
	for _, r := range rows {
		if r.InKAS {
			continue
		}
		if refuse, _ := steerOne(ctx, roles, chatID, r.Key, r.Text, false); refuse != "" {
			slog.Info("steer resend: a row waits for the next prompt", "chat_id", chatID, "reason", refuse)
		}
	}
}

// resendRows keeps the rows in custody until their prompt has opened, so a refused
// open leaves them unsent rather than lost.
func resendRows(ctx context.Context, roles *promptRoles, chatID marotte.ChatID, owner string, end *SteerTurnEnd) {
	keys, text := roles.jobs.Resent(chatID, owner, end.Lead)
	if len(keys) == 0 {
		roles.admission.ReleaseTurnReservation(chatID)
		return
	}
	p := &marotte.PromptCommand{Text: text, MessageID: ids.NewMessageID()}
	if err := launchPrompt(ctx, roles, chatID, p, keys, false); err != nil {
		slog.Warn("steer resend: the prompt could not open; the rows wait unsent", "chat_id", chatID, keyError, err)
		roles.jobs.Unsent(chatID, owner)
		return
	}
	roles.jobs.Delivered(chatID, owner)
}
