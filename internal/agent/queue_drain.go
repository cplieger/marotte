package agent

// The follow-up rows' server half beyond the run list: the close confirmation's read of the rows a close stops, and
// the run list's part of a shutdown's record.

import (
	"context"

	"github.com/cplieger/marotte/internal/marotte"
)

// queuedPromptsPending is the ids of chatID's rows waiting on its run list, read from the cached header: the rows
// its page shows waiting, which a close stops. A steer kiro-cli was handed is left out: its dock row names it.
func (bc *bridgeCoordinator) queuedPromptsPending(ctx context.Context, chatID marotte.ChatID) []string {
	c, ok := bc.chatStore.Get(ctx, chatID)
	if !ok {
		return nil
	}
	var ids []string
	for i := range c.Run {
		e := &c.Run[i]
		queued := e.Kind == marotte.RunKindRow || e.Kind == marotte.RunKindRetry
		handed := e.State == marotte.RunOffered || e.State == marotte.RunInKAS
		if queued && !handed && e.Turn == "" && (!e.State.Live() || e.State == marotte.RunStarting) {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// cutSteers is the run list's part of a shutdown's record: every notify-door entry not yet in a turn (the next
// process drops it, marotte.SanitizeRun) and every steer kiro-cli was handed, KASHeld since kiro-cli's log may hold
// it. The user's other rows stay on the list, which the next process holds.
func (bc *bridgeCoordinator) cutSteers(chatID marotte.ChatID) []shutdownRow {
	lc, ok := bc.turns.lookup(chatID)
	if !ok {
		return nil
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	var out []shutdownRow
	for _, e := range lc.run.entries {
		if e.kind != marotte.RunKindRow || e.row == nil || e.turnID != "" ||
			(!e.inFlight() && e.origin != marotte.RunFromNotify) {
			continue
		}
		out = append(out, shutdownRow{
			Key: marotte.SteerIDFor(e.id), Text: e.row.Text, KASHeld: e.inFlight(), Caller: e.row.Caller,
		})
	}
	return out
}
