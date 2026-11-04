package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// errRunNotNamed: an inspect reply that does not name the run it was asked about.
var errRunNotNamed = errors.New("the inspect reply does not name the run")

// RunRow is one run as the keep-state reconcile reads it from KAS.
type RunRow struct {
	WorkflowID      string
	ParentSessionID string
	Terminal        bool
}

// RunRows lists KAS's runs for this workspace with their launching session and whether each is terminal.
func (rs *Runs) RunRows(ctx context.Context) ([]RunRow, error) {
	runs, err := rs.listRaw(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RunRow, 0, len(runs))
	for i := range runs {
		if runs[i].WorkflowID == "" {
			continue
		}
		out = append(out, RunRow{
			WorkflowID:      runs[i].WorkflowID,
			ParentSessionID: runs[i].ParentSessionID,
			Terminal:        marotte.RunStatus(runs[i].Status).Terminal(),
		})
	}
	return out, nil
}

// InspectRunRow reads one run by inspect. "Workflow not found", a failed call and a reply that does not name the
// run are all errors: none of them shows the run's status or its launcher.
func (rs *Runs) InspectRunRow(ctx context.Context, workflowID string) (RunRow, error) {
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return RunRow{}, err
	}
	var res inspectRunState
	if err := json.Unmarshal(raw, &res); err != nil {
		return RunRow{}, err
	}
	if !res.names(workflowID) {
		return RunRow{}, errRunNotNamed
	}
	return RunRow{WorkflowID: workflowID, ParentSessionID: res.State.ParentSessionID, Terminal: res.State.Status.Terminal()}, nil
}

// SetRecordedRuns records the live runs the keep-state reconcile found only under runs/, workflow id to launching
// session, and marks runs/ read for RunsParentedBy.
func (rs *Runs) SetRecordedRuns(parents map[string]string) { rs.recorded.set(parents) }

// RecordLeaseHolders adds holders to a held lease's KeepChats, a union; a lease released meanwhile is left alone. The
// unattributed mark stays for reattributeRuns, which holds the facts on every chat before it clears the mark.
func (rs *Runs) RecordLeaseHolders(ctx context.Context, workflowID string, holders []marotte.ChatID) {
	if l, held := rs.lease(workflowID); held {
		rs.recordKeepChats(ctx, &l, holders)
	}
}

// runMarks is the in-memory side of the run keep marks: when each run's mark was last written, which throttles the
// progress refresh, and the parent session of each unattributed run whose lease ended. The zero value is usable.
type runMarks struct {
	wrote map[string]time.Time
	ended map[string]string
	mu    sync.Mutex
}

// due reports whether workflowID's mark was not written within chat.RunMarkRefresh of now.
func (m *runMarks) due(workflowID string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.wrote[workflowID]
	return !ok || now.Sub(at) >= chat.RunMarkRefresh
}

func (m *runMarks) written(workflowID string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wrote == nil {
		m.wrote = map[string]time.Time{}
	}
	m.wrote[workflowID] = at
}

func (m *runMarks) forget(workflowID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.wrote, workflowID)
}

// handOff keeps an unattributed run's parent session past its lease, for reattributeRuns.
func (m *runMarks) handOff(workflowID, parent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ended == nil {
		m.ended = map[string]string{}
	}
	m.ended[workflowID] = parent
}

func (m *runMarks) endedUnattributed() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.ended)
}

func (m *runMarks) attributed(workflowID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ended, workflowID)
}

// markRunAlive rewrites run:<workflowID> on every holder, a sign of life of the run: the mark keeps each holder until
// one retention period after it (chat.Store.PurgeIdle).
func (rs *Runs) markRunAlive(ctx context.Context, workflowID string, holders []marotte.ChatID) {
	key, ok := marotte.KeyRun(workflowID)
	if !ok {
		return
	}
	for _, h := range holders {
		rs.holdRunFact(ctx, h, key, workflowID)
	}
	rs.marks.written(workflowID, time.Now())
}

// markLeaseAlive is markRunAlive on the holders a held lease names; a run with no lease writes nothing.
func (rs *Runs) markLeaseAlive(ctx context.Context, workflowID string) {
	if l, held := rs.lease(workflowID); held {
		rs.markRunAlive(ctx, workflowID, leaseHolders(&l))
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
		slog.Info("run keep mark: a chat that owns the run is gone; its teardown owns the run",
			"chat_id", holder, "workflow_id", workflowID)
	default:
		slog.Warn("run keep mark: the mark could not be written", "chat_id", holder,
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

// startRunMarks writes a run_start's keep marks, then grants its lease or records the holders on the held one. The
// marks go first, so no moment exists where the lease is held and a holder is not kept.
func (rs *Runs) startRunMarks(ctx context.Context, chatID marotte.ChatID, f *lifecycleFrame) {
	wf := f.WorkflowID
	if _, ok := marotte.KeyRun(wf); !ok {
		slog.Warn("run start: the workflow id is not a plain name, so the run is not leased",
			"workflow_id", logsafe.Field(wf))
		return
	}
	holders, unattributed := rs.startHolders(ctx, chatID, f.ParentSessionID)
	rs.markRunAlive(ctx, wf, holders)
	if unattributed {
		rs.chats.HoldUnattributed(wf)
		slog.Warn(logMsgRunUnattributed, "workflow_id", wf)
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

// reattributeRuns retries every unattributed run, leased or ended, with a fresh owner scan; one that lists the chats
// directory writes the run's keep marks on its holders and clears the mark. A purge side pass.
func (rs *Runs) reattributeRuns(ctx context.Context) {
	leases := rs.leaseStore().List()
	for i := range leases {
		if leases[i].Unattributed {
			rs.reattribute(ctx, leases[i].WorkflowID)
		}
	}
	for wf, parent := range rs.marks.endedUnattributed() {
		if rs.attribute(ctx, wf, parent, nil) {
			rs.marks.attributed(wf)
		}
	}
}

func (rs *Runs) reattribute(ctx context.Context, wf string) {
	l, held := rs.lease(wf)
	if !held || !l.Unattributed || !rs.attribute(ctx, wf, l.ParentSessionID, &l) {
		return
	}
	if err := rs.leaseStore().SetUnattributed(ctx, wf, false); err != nil {
		slog.Warn("run lease: the cleared unattributed mark was not recorded on disk", "workflow_id", wf, "error", err)
	}
}

// attribute names an unattributed run's holders from one fresh owner scan, writes its marks on them and clears the
// store's mark; false when the chats directory still cannot be listed.
func (rs *Runs) attribute(ctx context.Context, wf, parent string, l *runlease.Lease) bool {
	scan := rs.chats.SessionOwners(ctx)
	if !scan.Listed {
		return false
	}
	holders, _ := RunHolders([]string{parent}, l, scan.Owners)
	if !scan.Complete {
		holders = append(holders, scan.Skipped...)
	}
	rs.markRunAlive(ctx, wf, holders)
	if l != nil {
		rs.recordKeepChats(ctx, l, holders)
	}
	rs.chats.ClearUnattributed(wf)
	slog.Info("run attributed: its launching chats are named", "workflow_id", wf, "holders", len(holders))
	return true
}

var (
	errRetryChatPurging = errors.New("the chat that launched this run is being deleted, so nothing was retried")
	errRetryChatUnread  = errors.New("the chat that launched this run could not be read, so nothing was retried. Try again")
	errRetryChatClosed  = errors.New("the chat that launched this run is closed, so nothing was retried. " +
		"Open the chat to retry the run")
)

// retryHolders names a retry's mark holders by RunHolders over one fresh owner scan, and its launchers: the lease's
// ChatID and the chats owning the parent session, never a chat the lease keeps only in doubt. It refuses a run whose
// launching session is in teardown, and one whose scan could not read every chat and found no launcher.
func (rs *Runs) retryHolders(
	ctx context.Context, workflowID string, o *runOrigin,
) (holders, launchers []marotte.ChatID, err error) {
	if o.parent.session != "" && rs.chats.SessionInTeardown(o.parent.session) {
		slog.Info("retry refused: the run's launching chat is being deleted", "workflow_id", workflowID)
		return nil, nil, command.StatusError(http.StatusConflict, errRetryChatPurging)
	}
	scan := rs.chats.SessionOwners(ctx)
	var lp *runlease.Lease
	if l, held := rs.lease(workflowID); held {
		lp = &l
	}
	holders, _ = RunHolders([]string{o.parent.session}, lp, scan.Owners)
	var launch *runlease.Lease
	if lp != nil {
		launch = &runlease.Lease{ChatID: lp.ChatID, Origin: lp.Origin}
	}
	launchers, _ = RunHolders([]string{o.parent.session}, launch, scan.Owners)
	if len(launchers) == 0 && !scan.Complete {
		return nil, nil, command.StatusError(http.StatusServiceUnavailable, errRetryChatUnread)
	}
	return holders, launchers, nil
}

// refuseClosedLauncher refuses the retry of a chat-launched run none of whose launchers has an open tab, so a run a
// person's close stopped cannot be revived from its run tab or History.
func (rs *Runs) refuseClosedLauncher(workflowID string, launchers []marotte.ChatID) error {
	if len(launchers) == 0 || rs.coord == nil || rs.coord.chatHasTab == nil {
		return nil
	}
	if slices.ContainsFunc(launchers, rs.coord.chatHasTab) {
		return nil
	}
	slog.Info("retry refused: no tab shows the chat that launched the run", "workflow_id", workflowID)
	return command.StatusError(http.StatusConflict, errRetryChatClosed)
}
