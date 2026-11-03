package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
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
	chatID     marotte.ChatID
	workflowID string
	holders    []marotte.ChatID
	delay      time.Duration
	attempt    int
}

// StopChatRuns stops every run chatID launched that is still running or paused: a person closed the chat's last tab.
// Before it returns, every such run's keep mark is rewritten, so the chat stays kept while the stop is in flight, and
// its first cancel is sent while the chat's session is still resident; the read-backs, retries and the delete
// fallback run later on the runtime's lifetime.
func (rs *Runs) StopChatRuns(ctx context.Context, chatID marotte.ChatID) {
	rec, ok := rs.chats.Get(ctx, chatID)
	if !ok {
		return
	}
	chain := rec.SessionChain()
	leased := rs.leasesOfChat(chatID, chain)
	for wf, holders := range leased {
		rs.markRunAlive(ctx, wf, holders)
	}
	deadline := time.Now().Add(runStopWindow)
	for wf, holders := range rs.stopCandidates(ctx, chatID, chain, leased) {
		slog.Info("close: stopping a run the closed chat launched", "workflow_id", wf, "chat_id", chatID)
		j := &runStopJob{deadline: deadline, chatID: chatID, workflowID: wf, holders: holders, delay: runStopFirstRetry}
		rs.attemptCancel(ctx, j)
		rs.scheduleStop(j)
	}
}

// leasesOfChat names the held leases whose run chatID launched, each with the chats its keep mark is on.
func (rs *Runs) leasesOfChat(chatID marotte.ChatID, chain []string) map[string][]marotte.ChatID {
	out := map[string][]marotte.ChatID{}
	leases := rs.leaseStore().List()
	for i := range leases {
		l := &leases[i]
		holders := leaseHolders(l)
		if slices.Contains(holders, chatID) || (l.ParentSessionID != "" && slices.Contains(chain, l.ParentSessionID)) {
			out[l.WorkflowID] = withHolder(holders, chatID)
		}
	}
	return out
}

// stopCandidates adds to leased the runs KAS lists as running or paused whose parent session chatID's chain owns, the
// test the purge teardown deletes by. A list that fails leaves the leased runs alone to stop.
func (rs *Runs) stopCandidates(
	ctx context.Context, chatID marotte.ChatID, chain []string, leased map[string][]marotte.ChatID,
) map[string][]marotte.ChatID {
	out := leased
	runs, err := rs.listRaw(ctx)
	if err != nil {
		slog.Warn("close: the run list could not be read; stopping only the runs marotte leases",
			"chat_id", chatID, "error", err)
		return out
	}
	owners := chat.OwnersOfChain(chatID, chain)
	for i := range runs {
		r := &runs[i]
		if _, known := out[r.WorkflowID]; known || r.WorkflowID == "" || len(owners.Of(r.ParentSessionID)) == 0 {
			continue
		}
		switch marotte.RunStatus(r.Status) {
		case marotte.RunStatusRunning, marotte.RunStatusPaused:
			out[r.WorkflowID] = []marotte.ChatID{chatID}
			rs.markRunAlive(ctx, r.WorkflowID, out[r.WorkflowID])
		}
	}
	return out
}

func withHolder(holders []marotte.ChatID, chatID marotte.ChatID) []marotte.ChatID {
	if slices.Contains(holders, chatID) {
		return holders
	}
	return append(slices.Clone(holders), chatID)
}

// attemptCancel sends one cancel of the stop's run, itself a sign of life of the run.
func (rs *Runs) attemptCancel(ctx context.Context, j *runStopJob) {
	j.attempt++
	rs.markRunAlive(ctx, j.workflowID, j.holders)
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
// window is spent.
func (rs *Runs) stepStop(j *runStopJob) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	if rs.runReadTerminal(ctx, j.workflowID) {
		slog.Info("close: stopped a run the closed chat launched", "workflow_id", j.workflowID, "chat_id", j.chatID,
			"attempts", j.attempt)
		rs.releaseStoppedRun(ctx, j, chat.EvidenceRunCancelled)
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
// good with its record. A delete that fails leaves the run's mark to keep its chat by time.
func (rs *Runs) deleteStoppedRun(ctx context.Context, j *runStopJob) {
	rs.markRunAlive(ctx, j.workflowID, j.holders)
	slog.Warn("close: a run's cancel did not stop it within the stop window; deleting it",
		"workflow_id", j.workflowID, "chat_id", j.chatID, "attempts", j.attempt, "window", runStopWindow)
	if err := rs.deleteAs(ctx, j.workflowID, userStop(stopWhyTabClosed)); err != nil {
		slog.Error("close: a run the closed chat launched could not be stopped; its keep mark ages out on its own",
			"workflow_id", j.workflowID, "chat_id", j.chatID, "error", err)
		return
	}
	slog.Info("close: deleted a run the closed chat launched", "workflow_id", j.workflowID, "chat_id", j.chatID)
	rs.releaseStoppedRun(ctx, j, chat.EvidenceRunDeleted)
}

// runReadTerminal reports an inspect reply that names the run with a terminal status; anything else is not proof
// the run stopped.
func (rs *Runs) runReadTerminal(ctx context.Context, workflowID string) bool {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return false
	}
	var res inspectRunState
	return json.Unmarshal(raw, &res) == nil && res.names(workflowID) && res.State.Status.Terminal()
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
