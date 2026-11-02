package agent

// A run's lives: each grant of its lease is one, and an end releases only the life it read. The run's keep facts
// follow the lease, so a retry's new life keeps what the previous life's end would release.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"

	"github.com/cplieger/keylock"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// runEnd is why a run's lease ends. Every value but endRolledBack is evidence that the run ended, which alone
// releases its keep facts; there is no absence end.
type runEnd int

const (
	// endTerminal: a frame, list row or inspect reply says the run is terminal.
	endTerminal runEnd = iota + 1
	// endCancelled: KAS took the orphan cancel.
	endCancelled
	// endDeleted: the user's delete was taken or read back gone.
	endDeleted
	// endRolledBack: the call that minted the lease failed; its caller rolls back its own claims.
	endRolledBack
)

func (e runEnd) evidence() (chat.Evidence, bool) {
	switch e {
	case endTerminal:
		return chat.EvidenceRunTerminal, true
	case endCancelled:
		return chat.EvidenceRunCancelled, true
	case endDeleted:
		return chat.EvidenceRunDeleted, true
	case endRolledBack:
	}
	return 0, false
}

// lifeRead is the life a release ends, read where its evidence was read: the lease's grant token, whether a lease
// was held, and for a minting call the run_start count at its mint.
type lifeRead struct {
	token  string
	starts uint64
	held   bool
}

// lifeOfLease is the life of a lease read from the store.
func lifeOfLease(l *runlease.Lease) lifeRead { return lifeRead{token: l.Life, held: true} }

// lifeOf reads workflowID's current life.
func (rs *Runs) lifeOf(workflowID string) lifeRead {
	l, held := rs.lease(workflowID)
	return lifeRead{token: l.Life, held: held, starts: rs.lives.startCount(workflowID)}
}

func newLifeToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runLives serializes, per run, every change of its life: observeStart's holds and grant, a retry's holds and
// renewal, and every release of its lease and facts, so a release cannot interleave with a newer life's holds.
// starts counts observeStart calls; ended is endedThisBoot. The zero value is usable.
type runLives struct {
	locks  *keylock.Set[string]
	starts map[string]uint64
	ended  map[string]struct{}
	mu     sync.Mutex
}

// lock holds workflowID's life. It never waits on KAS: only local disk writes run under it.
func (v *runLives) lock(workflowID string) (unlock func()) {
	v.mu.Lock()
	if v.locks == nil {
		v.locks = keylock.New(cmp.Compare[string])
	}
	locks := v.locks
	v.mu.Unlock()
	unlock, _ = locks.Lock(context.Background(), workflowID)
	return unlock
}

func (v *runLives) bumpStart(workflowID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.starts == nil {
		v.starts = map[string]uint64{}
	}
	v.starts[workflowID]++
}

func (v *runLives) startCount(workflowID string) uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.starts[workflowID]
}

func (v *runLives) markEnded(workflowID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ended == nil {
		v.ended = map[string]struct{}{}
	}
	v.ended[workflowID] = struct{}{}
}

// clearEnded starts a new life of an ended run, reporting whether it was marked.
func (v *runLives) clearEnded(workflowID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, was := v.ended[workflowID]
	delete(v.ended, workflowID)
	return was
}

func (v *runLives) endedThisBoot(workflowID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.ended[workflowID]
	return ok
}

// releaseLease ends one life of a run's lease (runEnd names why). An evidence end marks the run ended, releases the
// lease, then the keep facts of every holder, in that order: a reconcile re-checking under a record lock sees the
// lease held or the run ended. endRolledBack releases only the lease, and only while no run_start arrived since its
// mint. ReleaseSuperseded means a newer life holds the run, which keeps its lease and facts; nothing else is done.
func (rs *Runs) releaseLease(ctx context.Context, workflowID string, end runEnd, life lifeRead) runlease.ReleaseOutcome {
	if workflowID == "" {
		return runlease.ReleaseAbsent
	}
	unlock := rs.lives.lock(workflowID)
	l, held, outcome := rs.endLife(ctx, workflowID, end, life)
	unlock()
	if outcome == runlease.ReleaseSuperseded {
		return outcome
	}
	rs.forgetAbsence(workflowID)
	rs.endAsks(ctx, workflowID)
	rs.quietEdge()
	if held && rs.coord != nil {
		rs.coord.releaseIfUnheld(marotte.ChatID(l.ChatID))
	}
	return outcome
}

// endLife is releaseLease's compare-and-delete, under the run's life lock.
func (rs *Runs) endLife(ctx context.Context, workflowID string, end runEnd, life lifeRead) (runlease.Lease, bool, runlease.ReleaseOutcome) {
	l, held := rs.lease(workflowID)
	ev, evidence := end.evidence()
	if !evidence {
		if !held {
			return l, false, runlease.ReleaseAbsent
		}
		if l.Life != life.token || rs.lives.startCount(workflowID) != life.starts {
			slog.Info("run lease kept: the run was seen starting during a failed launch", "workflow_id", workflowID)
			return l, true, runlease.ReleaseSuperseded
		}
		return l, true, rs.dropLease(ctx, workflowID, l.Life)
	}
	if held && (!life.held || l.Life != life.token) {
		return l, true, runlease.ReleaseSuperseded
	}
	if !held && rs.lives.endedThisBoot(workflowID) {
		// This life's first end released its facts.
		return l, false, runlease.ReleaseAbsent
	}
	rs.lives.markEnded(workflowID)
	outcome := runlease.ReleaseAbsent
	if held {
		outcome = rs.dropLease(ctx, workflowID, l.Life)
	}
	var holders *runlease.Lease
	if held {
		holders = &l
	}
	rs.releaseRunFacts(ctx, workflowID, ev, holders)
	return l, held, outcome
}

func (rs *Runs) dropLease(ctx context.Context, workflowID, life string) runlease.ReleaseOutcome {
	outcome, err := rs.leaseStore().Release(ctx, workflowID, life)
	if err != nil {
		slog.Warn("run lease not released on disk", "workflow_id", workflowID, "error", err)
	}
	return outcome
}

// releaseRunFacts releases run:<workflowID> on the lease's holders, or on every record holding it when no lease
// names them, and clears the unattributed mark. A failed release keeps that chat until a later boot reads the end.
func (rs *Runs) releaseRunFacts(ctx context.Context, workflowID string, ev chat.Evidence, l *runlease.Lease) {
	if rs.chats == nil {
		return
	}
	key, ok := marotte.KeyRun(workflowID)
	if !ok {
		return
	}
	rs.chats.ClearUnattributed(workflowID)
	if l == nil {
		if err := rs.chats.ReleaseAll(ctx, key, ev); err != nil {
			slog.Warn("run end: some chats keep the run's keep fact until a later boot reads the end",
				"workflow_id", workflowID, "error", err)
		}
		return
	}
	for _, h := range leaseHolders(l) {
		if err := rs.chats.Release(ctx, h, key, ev); err != nil {
			slog.Warn("keep fact not released; the chat is kept until it is",
				"chat_id", h, "key", key, "workflow_id", workflowID, "error", err)
		}
	}
}

// leaseHolders is every chat a lease names as holding its run's keep fact.
func leaseHolders(l *runlease.Lease) []marotte.ChatID {
	var out []marotte.ChatID
	for _, id := range append(slices.Clone(l.KeepChats), l.ChatID) {
		if ids.ValidChatID(id) && !slices.Contains(out, marotte.ChatID(id)) {
			out = append(out, marotte.ChatID(id))
		}
	}
	return out
}

// holdRunFact writes run:<workflowID> on holder for a writer that cannot refuse; a gone holder is logged, since its
// teardown owns the run.
func (rs *Runs) holdRunFact(ctx context.Context, holder marotte.ChatID, key, workflowID string) {
	_, err := rs.chats.HoldOrPend(ctx, holder, key, chat.KeepDurable)
	switch {
	case err == nil:
	case errors.Is(err, chat.ErrTombstoned), errors.Is(err, chat.ErrChatNotFound):
		slog.Info("run start: a chat that owns the run is gone; its teardown owns the run",
			"chat_id", holder, "workflow_id", workflowID)
	default:
		slog.Warn("run start: the run's keep fact could not be written", "chat_id", holder,
			"workflow_id", workflowID, "error", err)
	}
}

// chatIDs converts holders to the lease's plain ids.
func chatIDs(holders []marotte.ChatID) []string {
	out := make([]string, len(holders))
	for i, h := range holders {
		out[i] = string(h)
	}
	return out
}

// logMsgRunUnattributed is fixed text: an alert rule keys on it.
const logMsgRunUnattributed = "run start: the launching chat could not be named; purging nothing until it is"

// startLife writes a run_start's keep facts, then grants its lease or records the holders on the held one. The facts
// go first, so no moment exists where the lease is held and a holder is not kept. The start count moves first, so a
// failed launch's rollback cannot drop a lease this start relies on.
func (rs *Runs) startLife(ctx context.Context, chatID marotte.ChatID, f *lifecycleFrame) {
	wf := f.WorkflowID
	rs.lives.bumpStart(wf)
	key, ok := marotte.KeyRun(wf)
	if !ok {
		slog.Warn("run start: the workflow id is not a plain name, so the run is not leased",
			"workflow_id", logsafe.Field(wf))
		return
	}
	unlock := rs.lives.lock(wf)
	defer unlock()
	var holders []marotte.ChatID
	unattributed := false
	if rs.chats != nil {
		holders, unattributed = rs.startHolders(ctx, chatID, f.ParentSessionID)
		for _, h := range holders {
			rs.holdRunFact(ctx, h, key, wf)
		}
		if unattributed {
			rs.chats.HoldUnattributed(wf)
			slog.Warn(logMsgRunUnattributed, "workflow_id", wf)
		}
	}
	l, held := rs.lease(wf)
	if !held {
		o := runStartLaunch(chatID)
		rs.grantKeeping(ctx, wf, f.WorkflowName, &o, nil, &leaseKeep{chats: chatIDs(holders), unattributed: unattributed})
		return
	}
	rs.recordKeepChats(ctx, &l, holders)
	if unattributed && !l.Unattributed {
		if err := rs.leaseStore().SetUnattributed(ctx, wf, true); err != nil {
			slog.Warn("run start: the unattributed mark was not recorded on disk", "workflow_id", wf, "error", err)
		}
	}
}

// startHolders names a run_start's holders by RunHolders over one fresh owner scan, plus the lane's chat for an
// agent's run and, on an incomplete scan, every chat the scan could not read: one of them may own the parent
// session. unattributed is a failed listing of the chats directory, which names no chat at all.
func (rs *Runs) startHolders(ctx context.Context, chatID marotte.ChatID, parent string) (holders []marotte.ChatID, unattributed bool) {
	scan := rs.chats.SessionOwners(ctx)
	holders, _ = RunHolders([]string{parent}, nil, scan.Owners)
	if o := runStartLaunch(chatID); o.origin == runlease.OriginAgent {
		holders = append(holders, chatID)
	}
	if !scan.Complete {
		holders = append(holders, scan.Skipped...)
	}
	slices.Sort(holders)
	return slices.Compact(holders), !scan.Listed
}

// recordKeepChats adds the holders a lease does not name yet to its KeepChats.
func (rs *Runs) recordKeepChats(ctx context.Context, l *runlease.Lease, holders []marotte.ChatID) {
	var add []string
	for _, h := range holders {
		if !slices.Contains(l.KeepChats, string(h)) {
			add = append(add, string(h))
		}
	}
	if len(add) == 0 {
		return
	}
	if err := rs.leaseStore().SetKeepChats(ctx, l.WorkflowID, add); err != nil {
		slog.Warn("run lease: its keep holders were not recorded on disk", "workflow_id", l.WorkflowID, "error", err)
	}
}

// ReattributeRuns retries every lease marked unattributed with a fresh owner scan; one that lists the chats
// directory holds the run's keep facts on its holders, records them and clears the mark. A purge side pass.
func (rs *Runs) ReattributeRuns(ctx context.Context) {
	if rs.chats == nil {
		return
	}
	leases := rs.leaseStore().List()
	for i := range leases {
		if leases[i].Unattributed {
			rs.reattribute(ctx, leases[i].WorkflowID)
		}
	}
}

func (rs *Runs) reattribute(ctx context.Context, wf string) {
	key, ok := marotte.KeyRun(wf)
	if !ok {
		return
	}
	unlock := rs.lives.lock(wf)
	defer unlock()
	l, held := rs.lease(wf)
	if !held || !l.Unattributed {
		return
	}
	scan := rs.chats.SessionOwners(ctx)
	if !scan.Listed {
		return
	}
	holders, _ := RunHolders([]string{l.ParentSessionID}, &l, scan.Owners)
	if !scan.Complete {
		holders = append(holders, scan.Skipped...)
	}
	for _, h := range holders {
		rs.holdRunFact(ctx, h, key, wf)
	}
	rs.recordKeepChats(ctx, &l, holders)
	if err := rs.leaseStore().SetUnattributed(ctx, wf, false); err != nil {
		slog.Warn("run lease: the cleared unattributed mark was not recorded on disk", "workflow_id", wf, "error", err)
	}
	rs.chats.ClearUnattributed(wf)
	slog.Info("run attributed: its launching chats are named", "workflow_id", wf, "holders", len(holders))
}

var (
	errRetryChatPurging = errors.New("the chat that launched this run is being deleted, so nothing was retried")
	errRetryChatGone    = errors.New("the chat that launched this run was deleted, so nothing was retried")
	errRetryChatUnread  = errors.New("the chat that launched this run could not be read, so nothing was retried. Try again")
)

// retryStart is what beginRetryLife took: its claims, the life it granted, and whether it minted the lease.
type retryStart struct {
	claims []chat.Claim
	life   lifeRead
	minted bool
	// wasEnded is the run's ended mark before this life cleared it.
	wasEnded bool
}

// beginRetryLife starts a retry's new life before anything is sent: it refuses a run whose launching chat is being
// deleted or is gone, holds run:<workflowID> on every holder RunHolders names, then grants the lease or renews the
// held one under a new life token, so an end the previous life still owes cannot release this one.
func (rs *Runs) beginRetryLife(ctx context.Context, workflowID string, o *runOrigin) (retryStart, error) {
	key, ok := marotte.KeyRun(workflowID)
	if !ok {
		return retryStart{}, command.StatusError(http.StatusBadRequest, errors.New("invalid workflow id"))
	}
	holders, err := rs.retryHolders(ctx, workflowID, o)
	if err != nil {
		return retryStart{}, err
	}
	unlock := rs.lives.lock(workflowID)
	defer unlock()
	wasEnded := rs.lives.clearEnded(workflowID)
	start := retryStart{wasEnded: wasEnded}
	for _, h := range holders {
		claim, err := rs.chats.Hold(ctx, h, key, chat.KeepDurable)
		if err != nil {
			rs.rollbackClaims(ctx, start.claims)
			if wasEnded {
				rs.lives.markEnded(workflowID)
			}
			return retryStart{}, retryHoldErr(workflowID, err)
		}
		start.claims = append(start.claims, claim)
	}
	if _, held := rs.lease(workflowID); held {
		start.life = lifeRead{token: newLifeToken(), held: true, starts: rs.lives.startCount(workflowID)}
		if err := rs.leaseStore().Renew(ctx, workflowID, start.life.token, chatIDs(holders)); err != nil {
			slog.Warn("retry: the run lease's new life was not recorded on disk", "workflow_id", workflowID, "error", err)
		}
		return start, nil
	}
	start.life = rs.grantRetryLease(ctx, workflowID, o, holders)
	start.minted = true
	return start, nil
}

// retryHolders names a retry's holders by RunHolders over one fresh owner scan, refusing a run whose launching
// session is in teardown, and one whose scan could not read every chat and found no holder.
func (rs *Runs) retryHolders(ctx context.Context, workflowID string, o *runOrigin) ([]marotte.ChatID, error) {
	if rs.chats == nil {
		return nil, nil
	}
	if o.parent.session != "" && rs.chats.SessionInTeardown(o.parent.session) {
		slog.Info("retry refused: the run's launching chat is being deleted", "workflow_id", workflowID)
		return nil, command.StatusError(http.StatusConflict, errRetryChatPurging)
	}
	scan := rs.chats.SessionOwners(ctx)
	var lp *runlease.Lease
	if l, held := rs.lease(workflowID); held {
		lp = &l
	}
	holders, _ := RunHolders([]string{o.parent.session}, lp, scan.Owners)
	if len(holders) == 0 && !scan.Complete {
		return nil, command.StatusError(http.StatusServiceUnavailable, errRetryChatUnread)
	}
	return holders, nil
}

func retryHoldErr(workflowID string, err error) error {
	switch {
	case errors.Is(err, chat.ErrInTeardown):
		return command.StatusError(http.StatusConflict, errRetryChatPurging)
	case errors.Is(err, chat.ErrTombstoned), errors.Is(err, chat.ErrChatNotFound):
		return command.StatusError(http.StatusConflict, errRetryChatGone)
	}
	slog.Warn("retry refused: the launching chat's record could not be written", "workflow_id", workflowID, "error", err)
	return command.StatusError(http.StatusServiceUnavailable, errRetryChatUnread)
}

// abandonRetry undoes a failed retry: the lease it minted when release is set, then its own claims once no lease
// holds the run. A lease still held keeps its facts, so nothing is rolled back under it.
func (rs *Runs) abandonRetry(ctx context.Context, workflowID string, start *retryStart, release bool) {
	if start.minted && release {
		rs.releaseLease(ctx, workflowID, endRolledBack, start.life)
	}
	if _, held := rs.lease(workflowID); held {
		return
	}
	if start.wasEnded {
		rs.lives.markEnded(workflowID)
	}
	rs.rollbackClaims(ctx, start.claims)
}

func (rs *Runs) rollbackClaims(ctx context.Context, claims []chat.Claim) {
	for i := range claims {
		if err := rs.chats.Rollback(ctx, &claims[i]); err != nil {
			slog.Warn("keep fact not rolled back; the chat is kept until it is released",
				"chat_id", claims[i].ChatID, "key", claims[i].Key, "error", err)
		}
	}
}
