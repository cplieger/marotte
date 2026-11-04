package agent

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// The stop a person's tab close owes the runs its chat launched: cancel, retried with backoff, and a delete once the
// window is spent. Vars for tests.
var (
	runStopWindow     = 60 * time.Second
	runStopFirstRetry = 2 * time.Second
	runStopMaxRetry   = 15 * time.Second
)

// runStopJob is one run's stop, stepped on the runtime's lifetime so a shutdown abandons it between steps.
type runStopJob struct {
	deadline   time.Time
	hold       *stopHold
	chatID     marotte.ChatID
	workflowID string
	holders    []marotte.ChatID
	delay      time.Duration
	attempt    int
}

// stopHold is one close's stop hold, shared by its runs' stops; the last stop to end releases it.
type stopHold struct {
	claim chat.Claim
	mu    sync.Mutex
	left  int
}

// StopChatRuns stops every run chatID launched that KAS lists as not terminal, and every leased run chatID launched
// (the lease's ChatID, or its parent session in the chat's chain) that the list omits or could not show: a person
// closed the chat's last tab. A chat holding a run's mark only because an owner scan could not rule it out is not the
// run's launcher. An unreadable record has no chain, so it stops only the leases whose ChatID is chatID. hold, the
// close's stop hold, keeps the chat until every stop has ended. Each run's first cancel is sent before it returns,
// while the chat's session is still resident; the read-backs, retries and the delete fallback run later on the
// runtime's lifetime.
func (rs *Runs) StopChatRuns(ctx context.Context, chatID marotte.ChatID, hold *chat.Claim) {
	rec, found, err := rs.chats.Lookup(ctx, chatID)
	if err == nil && !found {
		rs.chats.EndStop(ctx, hold)
		return
	}
	var chain []string
	if err != nil {
		slog.Warn("close: the chat could not be read; stopping only the runs its leases name",
			"chat_id", chatID, "error", err)
	} else {
		chain = rec.SessionChain()
	}
	candidates := rs.stopCandidates(ctx, chatID, chain, rs.leasesOfChat(chatID, chain))
	if len(candidates) == 0 {
		rs.chats.EndStop(ctx, hold)
		return
	}
	h := &stopHold{claim: *hold, left: len(candidates)}
	deadline := time.Now().Add(runStopWindow)
	for wf, holders := range candidates {
		slog.Info("close: stopping a run the closed chat launched", "workflow_id", wf, "chat_id", chatID)
		j := &runStopJob{
			deadline: deadline, hold: h, chatID: chatID, workflowID: wf, holders: holders, delay: runStopFirstRetry,
		}
		rs.attemptCancel(ctx, j)
		rs.scheduleStop(j)
	}
}

// endStop ends j's stop; the close's last ending stop releases its stop hold.
func (rs *Runs) endStop(ctx context.Context, j *runStopJob) {
	j.hold.mu.Lock()
	j.hold.left--
	last := j.hold.left == 0
	j.hold.mu.Unlock()
	if last {
		rs.chats.EndStop(ctx, &j.hold.claim)
	}
}

// leasesOfChat names the held leases whose run chatID launched (the lease's ChatID, or its parent session in chain,
// the test deleteForSessions uses), each with the chats its keep mark is on.
func (rs *Runs) leasesOfChat(chatID marotte.ChatID, chain []string) map[string][]marotte.ChatID {
	out := map[string][]marotte.ChatID{}
	leases := rs.leaseStore().List()
	for i := range leases {
		l := &leases[i]
		if l.ChatID == string(chatID) || (l.ParentSessionID != "" && slices.Contains(chain, l.ParentSessionID)) {
			out[l.WorkflowID] = withHolder(leaseHolders(l), chatID)
		}
	}
	return out
}

// stopCandidates is leased less the runs KAS lists as terminal, plus the runs it lists as not terminal whose parent
// session chatID's chain owns, the test the purge teardown deletes by. A list that fails leaves the leased runs to
// stop. A leased run KAS lists as terminal gets no stop attempt, so its mark is not refreshed and ages out.
func (rs *Runs) stopCandidates(
	ctx context.Context, chatID marotte.ChatID, chain []string, leased map[string][]marotte.ChatID,
) map[string][]marotte.ChatID {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("close: the run list could not be read; stopping only the runs marotte leases",
			"chat_id", chatID, "error", err)
		return leased
	}
	out := map[string][]marotte.ChatID{}
	owners := chat.OwnersOfChain(chatID, chain)
	listed := map[string]bool{}
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" {
			continue
		}
		listed[r.WorkflowID] = true
		holders, isLeased := leased[r.WorkflowID]
		if marotte.RunStatus(r.Status).Terminal() || (!isLeased && len(owners.Of(r.ParentSessionID)) == 0) {
			continue
		}
		if !isLeased {
			holders = rs.runMarkHolders(r.WorkflowID, chatID)
		}
		out[r.WorkflowID] = holders
	}
	for wf, holders := range leased {
		if !listed[wf] {
			out[wf] = holders
		}
	}
	return out
}

// runMarkHolders names the chats a listed run's keep mark is on: its lease's holders when it has a lease, and chatID.
func (rs *Runs) runMarkHolders(workflowID string, chatID marotte.ChatID) []marotte.ChatID {
	if l, held := rs.lease(workflowID); held {
		return withHolder(leaseHolders(&l), chatID)
	}
	return []marotte.ChatID{chatID}
}

func withHolder(holders []marotte.ChatID, chatID marotte.ChatID) []marotte.ChatID {
	if slices.Contains(holders, chatID) {
		return holders
	}
	return append(slices.Clone(holders), chatID)
}

func (rs *Runs) attemptCancel(ctx context.Context, j *runStopJob) {
	j.attempt++
	if err := rs.cancelAs(ctx, j.workflowID, userStop(stopWhyTabClosed)); err != nil {
		slog.Warn("close: a run's cancel failed; retrying until the stop window ends",
			"workflow_id", j.workflowID, "chat_id", j.chatID, "attempt", j.attempt, "error", err)
	}
}

// scheduleStop runs the stop's next step after its backoff, or at the window's end when that comes first.
func (rs *Runs) scheduleStop(j *runStopJob) {
	wait := min(j.delay, max(time.Until(j.deadline), 0))
	j.delay = min(j.delay*2, runStopMaxRetry)
	rs.lifecycle.afterUnlessDraining(wait, func() { rs.stepStop(j) })
}

// stepStop reads the run back: terminal is the stop confirmed; otherwise another cancel, or the delete once the
// window is spent. A chat reopened in the window drops the stop, so the reopened chat drives the run again.
func (rs *Runs) stepStop(j *runStopJob) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if rs.coord != nil && rs.coord.chatHasTab != nil && rs.coord.chatHasTab(j.chatID) {
		slog.Info("close: the chat was reopened; its run's stop is dropped", "workflow_id", j.workflowID,
			"chat_id", j.chatID)
		rs.endStop(ctx, j)
		return
	}
	if rs.runReadTerminal(ctx, j.workflowID) {
		slog.Info("close: stopped a run the closed chat launched", "workflow_id", j.workflowID, "chat_id", j.chatID,
			"attempts", j.attempt)
		rs.releaseStoppedRun(ctx, j, chat.EvidenceRunCancelled)
		rs.endStop(ctx, j)
		return
	}
	if !time.Now().Before(j.deadline) {
		rs.deleteStoppedRun(ctx, j)
		return
	}
	rs.attemptCancel(ctx, j)
	rs.scheduleStop(j)
}

// deleteStoppedRun is the stop's last step: a run the window's cancels did not end is deleted, which ends it for
// good with its record. A delete that fails leaves the run's mark, rewritten here, to keep its chat by time.
func (rs *Runs) deleteStoppedRun(ctx context.Context, j *runStopJob) {
	defer rs.endStop(ctx, j)
	rs.markRunAlive(ctx, j.workflowID, j.holders)
	slog.Warn("close: a run's cancel did not stop it within the stop window; deleting it",
		"workflow_id", j.workflowID, "chat_id", j.chatID, "attempts", j.attempt, "window", runStopWindow)
	err := rs.deleteAs(ctx, j.workflowID, userStop(stopWhyTabClosed))
	if err != nil && !workflowNotFound(err) {
		slog.Error("close: a run the closed chat launched could not be stopped; its keep mark ages out on its own",
			"workflow_id", j.workflowID, "chat_id", j.chatID, "error", err)
		return
	}
	slog.Info("close: deleted a run the closed chat launched", "workflow_id", j.workflowID, "chat_id", j.chatID)
	rs.releaseStoppedRun(ctx, j, chat.EvidenceRunDeleted)
}

// runReadTerminal reports an inspect reply that names the run with a terminal status, or KAS's complete-search not
// found (workflowNotFound); anything else is not proof the run stopped.
func (rs *Runs) runReadTerminal(ctx context.Context, workflowID string) bool {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return workflowNotFound(err)
	}
	var res inspectRunState
	return unmarshalKeepingReadable(raw, &res) && res.names(workflowID) && res.State.Status.Terminal()
}

// releaseStoppedRun releases the stopped run's keep mark on every holder; a failed release keeps that chat until the
// mark ages out.
func (rs *Runs) releaseStoppedRun(ctx context.Context, j *runStopJob, ev chat.Evidence) {
	key, ok := marotte.KeyRun(j.workflowID)
	if !ok {
		return
	}
	for _, h := range j.holders {
		if err := rs.chats.Release(ctx, h, key, ev); err != nil {
			slog.Warn("close: a stopped run's keep mark was not released; it ages out on its own",
				"chat_id", h, "workflow_id", j.workflowID, "error", err)
		}
	}
}
