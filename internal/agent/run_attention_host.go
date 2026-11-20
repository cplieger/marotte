package agent

import (
	"context"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

var _ attentionHost = (*Runs)(nil)

// attentionLease is the lease's and the run log's part of a leased run's health; false for a run with no lease.
func (rs *Runs) attentionLease(workflowID string) (attnLease, bool) {
	l, held := rs.lease(workflowID)
	if !held {
		return attnLease{}, false
	}
	out := attnLease{name: rs.leasedRunName(&l), stoppedAt: l.AutoResumeStoppedAt, healTry: rs.healsSpent(workflowID) + 1}
	if chat := marotte.ChatID(l.ChatID); chat != "" && !marotte.IsRunHostChat(chat) {
		out.chat = chat
	}
	if rs.log != nil {
		out.steps = rs.log.attentionSteps(workflowID)
	}
	return out, true
}

func (rs *Runs) attentionHeld(workflowID string) bool {
	_, held := rs.lease(workflowID)
	return held
}

// attentionCell is the run's cell's held read, false for a run with no cell.
func (rs *Runs) attentionCell(workflowID string) (attentionInput, bool) {
	c := rs.asks.get(workflowID)
	if c == nil || c.transient {
		return attentionInput{}, false
	}
	return rs.cellInput(c), true
}

func (rs *Runs) cellInput(c *askCell) attentionInput {
	epoch := rs.asks.epochNow()
	c.mu.Lock()
	defer c.mu.Unlock()
	in := attentionInput{workflowID: c.workflowID, reply: c.reply, readFailed: c.readFailed, gone: c.gone, seq: rs.asks.attnSeq.Add(1)}
	in.reachable = c.reply.OK && !c.readFailed
	if c.reply.OK {
		in.asks = workflow.OpenAsks(c.reply, c.runLogLocked(c.basis, epoch))
	}
	return in
}

// attend hands the ledger the cell's newest read once a read or publish completed. Called with c.mu released.
func (rs *Runs) attend(c *askCell) {
	if rs.attention == nil || c.transient {
		return
	}
	in := rs.cellInput(c)
	rs.attention.observe(&in)
}

func (rs *Runs) attentionEntry(chat marotte.ChatID, entryID string) attnEntry {
	if rs.coord == nil {
		return attnEntryGone
	}
	lc, ok := rs.coord.turns.lookup(chat)
	if !ok {
		return attnEntryGone
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.run.attentionEntry(entryID)
}

func (rs *Runs) attentionNotify(ctx context.Context, chat marotte.ChatID, text string) (command.NotifyReceipt, error) {
	if rs.notifyChat == nil {
		return command.NotifyReceipt{}, errLaunchUnwired
	}
	return rs.notifyChat(ctx, chat, text)
}

func (rs *Runs) attentionGo(work func()) bool {
	return rs.lifecycle != nil && rs.lifecycle.goUnlessDraining(work)
}

func (rs *Runs) attentionAfter(d time.Duration, fn func()) *time.Timer {
	if rs.lifecycle == nil {
		return nil
	}
	return rs.lifecycle.afterUnlessDraining(d, fn)
}
