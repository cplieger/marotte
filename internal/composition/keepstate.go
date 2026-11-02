package composition

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/tabs"
)

const (
	keepStateRetry     = 5 * time.Minute
	keepStateListRetry = time.Minute
	// keepStateRemedy is the operator's way out of a run fact or a stall whose end never shows.
	keepStateRemedy = "DELETE /api/runs/<workflow_id>, or remove runs/<workflow_id>/ once KAS cannot read the run"
)

type keepChats interface {
	SessionOwners(ctx context.Context) chat.OwnerScan
	Reconciled(id marotte.ChatID) bool
	Boot() string
	ReleaseIfHeldAs(ctx context.Context, id marotte.ChatID, key string, want marotte.KeepFact, ev chat.Evidence) (bool, error)
}

type keepMembership interface {
	ReconcileTabs(ctx context.Context, id marotte.ChatID, fn func(r *chat.KeepRecord, open []marotte.TabSubject)) error
	SettleOwed(ctx context.Context) archive.PurgeResult
	SetLaunchChats(ctx context.Context, tabID string, chats []string, resolved bool) error
}

type keepTabs interface {
	List() ([]marotte.TabSubject, uint64)
	Owed() []tabs.OwedRelease
	Unread() string
}

type keepLeases interface {
	List() []runlease.Lease
	Get(workflowID string) (runlease.Lease, bool)
	Unread() string
}

type keepRuns interface {
	RunRows(ctx context.Context) ([]agent.RunRow, error)
	InspectRunRow(ctx context.Context, workflowID string) (agent.RunRow, error)
	RecordedRuns(ctx context.Context) ([]string, error)
	RunOwner(ctx context.Context, workflowID string) (command.RunOwner, error)
	EndedThisBoot(workflowID string) bool
	RecordLeaseHolders(ctx context.Context, workflowID string, holders []marotte.ChatID)
	SetRecordedRuns(parents map[string]string)
}

var (
	_ keepChats      = (*chat.Store)(nil)
	_ keepMembership = (*command.Membership)(nil)
	_ keepTabs       = (*tabs.Store)(nil)
	_ keepLeases     = (*runlease.Store)(nil)
	_ keepRuns       = (*agent.Runs)(nil)
)

// keepStateDeps is everything the keep-state reconcile reads and writes. A zero duration takes its default.
type keepStateDeps struct {
	chats      keepChats
	membership keepMembership
	tabs       keepTabs
	leases     keepLeases
	runs       keepRuns
	// runStates answers the workflow ids KAS keeps run state for (kirosession.Reaper.RunStates).
	runStates func() (map[string]struct{}, error)
	ready     <-chan struct{}
	retry     time.Duration
	listRetry time.Duration
}

// reconcileKeepState is the per-boot keep-state reconcile: it adds the keep facts every record read this boot is
// missing, releases a previous boot's facts on evidence of their end, and marks each record it reconciled, which is
// what lets the purge consider it. A round that cannot read a source reconciles nothing and is retried; the pass
// ends once every record is reconciled.
func reconcileKeepState(ctx context.Context, d *keepStateDeps) {
	retry, listRetry := cmpOr(d.retry, keepStateRetry), cmpOr(d.listRetry, keepStateListRetry)
	if !waitKeepReady(ctx, d.ready, retry) {
		return
	}
	for {
		wait, done := keepRound(ctx, d, retry, listRetry)
		if done {
			return
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func cmpOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func waitKeepReady(ctx context.Context, ready <-chan struct{}, warnEvery time.Duration) bool {
	tick := time.NewTicker(warnEvery)
	defer tick.Stop()
	for {
		select {
		case <-ready:
			return true
		case <-ctx.Done():
			return false
		case <-tick.C:
			slog.Warn("keep-state reconcile: the engine is not ready; purging nothing")
		}
	}
}

// keepCandidate is a run the round treats as live: a listed non-terminal row, a held lease, or a run record KAS
// inspects as non-terminal.
type keepCandidate struct {
	lease   *runlease.Lease
	holders []marotte.ChatID
	parents []string
}

// keepFact is one fact a round found on a record, with the value it read.
type keepFact struct {
	chatID marotte.ChatID
	key    string
	wf     string
	fact   marotte.KeepFact
}

// keepRoundState is one round's reads, shared by its steps.
type keepRoundState struct {
	listed     map[string]agent.RunRow
	candidates map[string]*keepCandidate
	// tabHolders is each open run tab's resolved holders, whether or not its write persisted.
	tabHolders map[string][]string
	owed       map[keepFactRef]struct{}
	added      map[string]int
	released   map[string]int
	boot       string
	unlisted   []keepFact
	stuckTabs  []keepFact
	stateless  int
	stuckRuns  int
	tabsUnread bool
}

type keepFactRef struct {
	chatID marotte.ChatID
	key    string
}

// keepRound runs one round and answers when to retry, or done when no record is left unreconciled.
func keepRound(ctx context.Context, d *keepStateDeps, retry, listRetry time.Duration) (time.Duration, bool) {
	if aside := d.leases.Unread(); aside != "" {
		slog.Warn("keep-state reconcile: the run lease file was unreadable; purging nothing", "aside", aside)
		return retry, false
	}
	rows, err := d.runs.RunRows(ctx)
	if err != nil {
		slog.Warn("keep-state reconcile: the run list could not be read", "error", err)
		return listRetry, false
	}
	st := &keepRoundState{
		listed: map[string]agent.RunRow{}, candidates: map[string]*keepCandidate{}, tabHolders: map[string][]string{},
		owed: map[keepFactRef]struct{}{}, boot: d.chats.Boot(), added: map[string]int{}, released: map[string]int{},
		tabsUnread: d.tabs.Unread() != "",
	}
	for _, r := range rows {
		st.listed[r.WorkflowID] = r
	}
	if !keepCandidates(ctx, d, st) {
		return retry, false
	}
	scan := d.chats.SessionOwners(ctx)
	if !scan.Listed {
		slog.Warn("keep-state reconcile: the chats directory could not be listed")
		return retry, false
	}
	if !keepAttribute(ctx, d, st, scan.Owners) || !keepResolveRunTabs(ctx, d, st) {
		return retry, false
	}
	d.membership.SettleOwed(ctx)
	for _, e := range d.tabs.Owed() {
		keepOwed(st, &e)
	}
	left := keepReconcileRead(ctx, d, st, &scan)
	keepSettleUnlisted(ctx, d, st)
	keepReport(st, left)
	return retry, left == 0
}

// keepReconcileRead reconciles every record the scan read and answers how many stay unreconciled, the scan's
// skipped ones included: those are reached only by a later round's scan. A record deleted since the scan is done.
func keepReconcileRead(ctx context.Context, d *keepStateDeps, st *keepRoundState, scan *chat.OwnerScan) (left int) {
	for _, id := range slices.Sorted(maps.Keys(scan.Read)) {
		if d.chats.Reconciled(id) {
			continue
		}
		err := d.membership.ReconcileTabs(ctx, id, func(r *chat.KeepRecord, open []marotte.TabSubject) {
			keepReconcileRecord(d, st, id, r, open)
		})
		if errors.Is(err, chat.ErrChatNotFound) || errors.Is(err, chat.ErrTombstoned) {
			continue
		}
		if err != nil {
			slog.Warn("keep-state reconcile: a chat could not be reconciled", "chat_id", id, "error", err)
			left++
		}
	}
	for _, id := range scan.Skipped {
		if !d.chats.Reconciled(id) {
			left++
		}
	}
	return left
}

// keepCandidates collects the round's live-run candidates: every non-terminal listed run, every held lease, and every run
// record under runs/ that KAS keeps state for and inspects as non-terminal. False stalls the round.
func keepCandidates(ctx context.Context, d *keepStateDeps, st *keepRoundState) bool {
	for wf, r := range st.listed {
		if !r.Terminal {
			st.candidates[wf] = &keepCandidate{parents: []string{r.ParentSessionID}}
		}
	}
	leases := d.leases.List()
	for i := range leases {
		l := &leases[i]
		c := &keepCandidate{lease: l, parents: []string{l.ParentSessionID}}
		if r, ok := st.listed[l.WorkflowID]; ok {
			c.parents = append(c.parents, r.ParentSessionID)
		}
		st.candidates[l.WorkflowID] = c
	}
	return keepRecordedCandidates(ctx, d, st)
}

// keepRecordedCandidates adds the run records under runs/ that neither the list nor a lease names: no KAS state needs
// no protection; otherwise inspect decides, and a run it cannot read stalls the round.
func keepRecordedCandidates(ctx context.Context, d *keepStateDeps, st *keepRoundState) bool {
	recorded, err := d.runs.RecordedRuns(ctx)
	if err != nil {
		slog.Warn("keep-state reconcile: the run records could not be listed; purging nothing", "error", err)
		return false
	}
	recorded = slices.DeleteFunc(recorded, func(wf string) bool {
		_, listed := st.listed[wf]
		_, leased := d.leases.Get(wf)
		return listed || leased
	})
	if len(recorded) == 0 {
		d.runs.SetRecordedRuns(nil)
		return true
	}
	states, err := d.runStates()
	if err != nil {
		slog.Warn("keep-state reconcile: the engine's run state could not be listed; purging nothing", "error", err)
		return false
	}
	parents := map[string]string{}
	for _, wf := range recorded {
		if _, kept := states[wf]; !kept {
			st.stateless++
			continue
		}
		r, err := d.runs.InspectRunRow(ctx, wf)
		if err != nil {
			slog.Warn("keep-state reconcile: a run KAS keeps state for could not be read; purging nothing until it is read or deleted",
				"workflow_id", wf, "remedy", keepStateRemedy, "error", err)
			return false
		}
		if !r.Terminal {
			st.candidates[wf] = &keepCandidate{parents: []string{r.ParentSessionID}}
			parents[wf] = r.ParentSessionID
		}
	}
	d.runs.SetRecordedRuns(parents)
	return true
}

// keepAttribute names each candidate's holders and records them on its lease. A live run that names no launcher
// stalls the round: any chat could own it.
func keepAttribute(ctx context.Context, d *keepStateDeps, st *keepRoundState, owners chat.SessionOwners) bool {
	for _, wf := range slices.Sorted(maps.Keys(st.candidates)) {
		c := st.candidates[wf]
		holders, known := agent.RunHolders(c.parents, c.lease, owners)
		if !known {
			slog.Warn("keep-state reconcile: a live run names no launching chat; purging nothing until it ends", "workflow_id", wf)
			return false
		}
		c.holders = holders
		if c.lease != nil && slices.ContainsFunc(holders, func(h marotte.ChatID) bool { return !slices.Contains(c.lease.KeepChats, string(h)) }) {
			d.runs.RecordLeaseHolders(ctx, wf, holders)
		}
	}
	return true
}

// keepResolveRunTabs resolves each open run tab's holders, grown and never shrunk. A run tab whose
// owner cannot be read, or an unresolved one whose run cannot be found, stalls the round.
func keepResolveRunTabs(ctx context.Context, d *keepStateDeps, st *keepRoundState) bool {
	open, _ := d.tabs.List()
	for i := range open {
		if open[i].Kind == marotte.TabKindRun && !keepResolveRunTab(ctx, d, st, &open[i]) {
			return false
		}
	}
	return true
}

func keepResolveRunTab(ctx context.Context, d *keepStateDeps, st *keepRoundState, t *marotte.TabSubject) bool {
	owner, err := d.runs.RunOwner(ctx, t.Ref)
	if err != nil {
		slog.Warn("keep-state reconcile: a run tab's launcher could not be read", "workflow_id", t.Ref, "error", err)
		return false
	}
	holders := slices.Clone(t.LaunchChats)
	switch owner.Verdict {
	case command.RunUnknown:
		if !t.LaunchResolved {
			slog.Warn("keep-state reconcile: an open run tab shows a run that cannot be found; purging nothing until it is found or the tab closes",
				"tab_id", t.ID, "workflow_id", t.Ref)
			return false
		}
	case command.RunOwned, command.RunParentless:
		for _, h := range owner.Holders {
			holders = append(holders, string(h))
		}
		if err := d.membership.SetLaunchChats(ctx, t.ID, holders, true); err != nil {
			slog.Warn("keep-state reconcile: a run tab's holders were not recorded", "tab_id", t.ID, "workflow_id", t.Ref, "error", err)
		}
	}
	st.tabHolders[t.ID] = holders
	return true
}

func keepOwed(st *keepRoundState, e *tabs.OwedRelease) {
	switch e.Kind {
	case marotte.TabKindChat:
		st.owed[keepFactRef{chatID: marotte.ChatID(e.Ref), key: marotte.KeyTab}] = struct{}{}
	case marotte.TabKindRun:
		key, ok := marotte.KeyRunTab(e.Ref)
		if !ok {
			return
		}
		for _, h := range e.Holders {
			st.owed[keepFactRef{chatID: marotte.ChatID(h), key: key}] = struct{}{}
		}
	}
}

// keepReconcileRecord reconciles one record, under Membership's mu and the record lock.
func keepReconcileRecord(d *keepStateDeps, st *keepRoundState, id marotte.ChatID, r *chat.KeepRecord, open []marotte.TabSubject) {
	facts := r.Facts()
	for _, key := range slices.Sorted(maps.Keys(facts)) {
		keepPreviousBootFact(d, st, id, r, key, facts[key], open)
	}
	for _, key := range keepAdds(d, st, id, r, open) {
		if _, held := r.Facts()[key]; !held {
			r.Add(key, chat.KeepDurable)
			kind, _ := marotte.KeepKind(key)
			st.added[kind]++
		}
	}
}

// keepAdds names the facts id's record must hold: a tab showing it (or, with the arrangement lost, any pre-upgrade
// record), a run tab it holds, and a live run it holds that no end overtook.
func keepAdds(d *keepStateDeps, st *keepRoundState, id marotte.ChatID, r *chat.KeepRecord, open []marotte.TabSubject) []string {
	var keys []string
	if keepShown(open, marotte.KeepKindTab, string(id), "") || (st.tabsUnread && r.FilledAt() == 0) {
		keys = append(keys, marotte.KeyTab)
	}
	for i := range open {
		t := &open[i]
		holders, resolved := st.tabHolders[t.ID]
		if !resolved {
			holders = t.LaunchChats
		}
		if key, ok := marotte.KeyRunTab(t.Ref); ok && t.Kind == marotte.TabKindRun && slices.Contains(holders, string(id)) {
			keys = append(keys, key)
		}
	}
	for _, wf := range slices.Sorted(maps.Keys(st.candidates)) {
		if key, ok := marotte.KeyRun(wf); ok && keepRunLive(d, st.candidates[wf], wf, id) {
			keys = append(keys, key)
		}
	}
	return keys
}

// keepRunLive re-checks, under the record lock, that the run still needs id's fact: every evidence end marks the run
// ended before it releases the lease and then the fact, so an end that overtook the round's reads is seen here.
func keepRunLive(d *keepStateDeps, c *keepCandidate, wf string, id marotte.ChatID) bool {
	if !slices.Contains(c.holders, id) || d.runs.EndedThisBoot(wf) {
		return false
	}
	if c.lease == nil {
		return true
	}
	_, held := d.leases.Get(wf)
	return held
}

// keepPreviousBootFact releases a fact an earlier boot wrote when a source shows its end, and records the ones no
// source can settle. This boot's facts belong to this boot's writers.
func keepPreviousBootFact(d *keepStateDeps, st *keepRoundState, id marotte.ChatID, r *chat.KeepRecord, key string, f marotte.KeepFact, open []marotte.TabSubject) {
	if f.Boot == st.boot {
		return
	}
	kind, wf := marotte.KeepKind(key)
	switch kind {
	case marotte.KeepKindTurn:
		if f.Process {
			r.Release(key, chat.EvidenceProcessGone)
			st.released[kind]++
		}
	case marotte.KeepKindRun:
		if _, held := d.leases.Get(wf); held {
			return
		}
		row, listed := st.listed[wf]
		switch {
		case !listed:
			st.unlisted = append(st.unlisted, keepFact{chatID: id, key: key, wf: wf, fact: f})
		case row.Terminal:
			r.Release(key, chat.EvidenceRunTerminal)
			st.released[kind]++
		}
	case marotte.KeepKindTab, marotte.KeepKindRunTab:
		if st.tabsUnread || keepShown(open, kind, string(id), wf) {
			return
		}
		if _, owed := st.owed[keepFactRef{chatID: id, key: key}]; !owed {
			st.stuckTabs = append(st.stuckTabs, keepFact{chatID: id, key: key, fact: f})
		}
	}
}

func keepShown(open []marotte.TabSubject, kind, chatID, wf string) bool {
	return slices.ContainsFunc(open, func(t marotte.TabSubject) bool {
		if kind == marotte.KeepKindTab {
			return t.Kind == marotte.TabKindChat && t.Ref == chatID
		}
		return t.Kind == marotte.TabKindRun && t.Ref == wf
	})
}

// keepSettleUnlisted settles the run facts no listed row decided: one inspect per unlisted run a previous boot's fact names. A terminal reply
// releases the fact; anything short of a status leaves it, reported as stuck.
func keepSettleUnlisted(ctx context.Context, d *keepStateDeps, st *keepRoundState) {
	byRun := map[string][]keepFact{}
	for _, f := range st.unlisted {
		byRun[f.wf] = append(byRun[f.wf], f)
	}
	for _, wf := range slices.Sorted(maps.Keys(byRun)) {
		row, err := d.runs.InspectRunRow(ctx, wf)
		for _, f := range byRun[wf] {
			keepSettleUnlistedFact(ctx, d, st, &f, row.Terminal, err)
		}
	}
	for _, f := range st.stuckTabs {
		slog.Warn("keep fact stuck: no open tab shows it and no close recorded its release; the chat stays kept",
			"chat_id", f.chatID, "key", f.key, "age", keepAge(f.fact.Since))
	}
}

func keepSettleUnlistedFact(ctx context.Context, d *keepStateDeps, st *keepRoundState, f *keepFact, terminal bool, inspectErr error) {
	if inspectErr != nil {
		slog.Warn("keep fact stuck: no source shows the run ended; the chat stays kept",
			"chat_id", f.chatID, "workflow_id", f.wf, "age", keepAge(f.fact.Since), "remedy", keepStateRemedy)
		st.stuckRuns++
		return
	}
	if !terminal {
		return
	}
	if _, held := d.leases.Get(f.wf); held {
		return
	}
	// The inspect ran outside the record lock: a life that started since wrote its own value, which stays.
	released, err := d.chats.ReleaseIfHeldAs(ctx, f.chatID, f.key, f.fact, chat.EvidenceRunTerminal)
	if err != nil {
		slog.Warn("keep fact not released; the chat is kept until it is", "chat_id", f.chatID, "key", f.key, "error", err)
		return
	}
	if released {
		st.released[marotte.KeepKindRun]++
	}
}

func keepAge(since int64) time.Duration {
	return time.Since(time.UnixMilli(since)).Round(time.Second)
}

func keepReport(st *keepRoundState, left int) {
	slog.Info("keep-state reconcile finished",
		"added_tab", st.added[marotte.KeepKindTab], "added_runtab", st.added[marotte.KeepKindRunTab],
		"added_run", st.added[marotte.KeepKindRun],
		"released_turn", st.released[marotte.KeepKindTurn], "released_run", st.released[marotte.KeepKindRun],
		"stuck_run_facts", st.stuckRuns, "stuck_tab_facts", len(st.stuckTabs),
		"runs_without_engine_state", st.stateless, "unreconciled", left)
}
