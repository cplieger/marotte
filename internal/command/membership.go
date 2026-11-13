package command

// The membership coordinator: every operation spanning the chat store and the open-tab
// set, under one operation lock. Two documents and no transaction, so ordering plus this
// lock is the whole correctness argument. Lock order is Membership.mu -> chat record lock
// -> tabs.Store writeMu, acyclic because neither store reaches the other. THE CHAT RECORD
// IS THE GATE both ways: written before its tab, removed before its tabs. Every tab
// mutation comes through here, pin and reorder included, because tabs.Store emits no
// events of its own and this lock is what keeps mutate-and-emit atomic and frames in
// version order; Prune is the one writer that does not hold it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// reasonTabsFull is the machine reason errTabsFull's 409 carries, so a client tells the tab limit from the other
// 409s an open can meet (a run being purged, the idempotency layer's request already in progress).
const reasonTabsFull = "tabs_full"

// The coordinator's own refusals.
var (
	errTabsFull = errors.New("too many tabs are open. Close a tab first")
	// errTabsUnavailable is the 503 for a build with no tab store wired; every
	// HTTP door adds the status.
	errTabsUnavailable = errors.New("the tab store is unavailable")
	// errOpenChatUnknown is the 404 for an open_tab, or a fresh create, naming a chat
	// that is gone — the delete-ordering gate's refusal.
	errOpenChatUnknown = errors.New("that chat no longer exists")
	// errChatUnread is the 503 for that gate when the record exists but could
	// not be read: the 404 would tell the client the chat is gone.
	errChatUnread = errors.New("could not read that conversation. Try again")
	// errChatPurging is the 409 for a tab of a chat whose retention teardown still runs.
	errChatPurging = errors.New("this chat is being deleted")
	// errSessionClaimed is the 409 for a resume of a session another chat's chain holds.
	errSessionClaimed = errors.New("this session already belongs to a chat")
	// errRunPurging is the 409 for an open_tab naming a run the retention purge is
	// deleting, and for a create that would bind the session that run belongs to.
	errRunPurging = errors.New("that workflow run is being deleted")
	// errRunLauncherUnread is the 503 for a new run tab whose launching chat
	// could not be read: Parent is immutable, so guessing top level is permanent.
	errRunLauncherUnread = errors.New("could not tell which chat launched that workflow run. Try again")
	// errRunUnknown is the 503 for a new run tab of a run no source names, so its launcher is unknown.
	errRunUnknown = errors.New("this run could not be found, so the chat that launched it is unknown")
	// errSessionParentsRun is the 409 for a resume of a session that launched a live or shown run.
	errSessionParentsRun = errors.New("this session launched a run that is still live or open; open that run's chat instead")
	// errSuppliedParentClosed is OpenTab's retry signal, never answered: the
	// caller's parent tab closed before mu, so the run owner is asked after all.
	errSuppliedParentClosed = errors.New("the supplied parent tab is not open")
	// errTabUnknown is the 404 for a pin or a reparent naming an id the set does
	// not hold.
	errTabUnknown = errors.New("that tab is not open")
	// errRunsUnavailable is the 503 for a run tab's ack with no run surface wired.
	errRunsUnavailable = errors.New("the workflow runs are unavailable")
	// errParentNotChat is the 409 for a reparent whose parent is not an open chat
	// tab: only a chat can hold sub-tabs.
	errParentNotChat = errors.New("the parent must be an open chat tab")
)

// TabSet is the open-tab set as this package uses it, declared at the consumer
// since internal/tabs exports no interface of its own. List is here beyond the
// mutations because the capacity reservation needs the count and every event
// needs the expanded order, which no mutation returns; Subtree is what a Close
// of an id will remove, asked before that close commits. Recent is read before
// and after each mutation, because no mutation reports whether the list changed.
type TabSet interface {
	Open(ctx context.Context, spec marotte.OpenTab) (subject marotte.TabSubject, created bool, version uint64, err error)
	Close(ctx context.Context, id string, unlisted []string, facts map[string]tabs.RecentFacts) ([]marotte.TabSubject, []tabs.OwedRelease, uint64, error)
	Forget(ctx context.Context, ref string) (uint64, error)
	ExpireRecent(ctx context.Context) (uint64, error)
	Recent() []marotte.RecentClose
	Reorder(ctx context.Context, ids []string) (uint64, error)
	SetPinned(ctx context.Context, id string, pinned bool) (uint64, error)
	SetSeen(ctx context.Context, id string, seen uint64) (uint64, error)
	Reparent(ctx context.Context, id, parent string) (uint64, error)
	List() ([]marotte.TabSubject, uint64)
	Subtree(id string) []marotte.TabSubject
	Owed() []tabs.OwedRelease
	Settle(ctx context.Context, entryIDs []string) error
	SetLaunchChats(ctx context.Context, id string, chats []string, resolved bool) error
	// Unread names the arrangement the store could not read at boot; empty when it read it.
	Unread() string
}

// membershipChats is the chat store as the coordinator uses it: the handlers' chatStore plus the
// keep-fact writes every tab open and close makes (chat.Store's Hold, Rollback and Release).
type membershipChats interface {
	chatStore
	Hold(ctx context.Context, id marotte.ChatID, key string, scope chat.KeepScope) (chat.Claim, error)
	Rollback(ctx context.Context, claim *chat.Claim) error
	Release(ctx context.Context, id marotte.ChatID, key string, ev chat.Evidence) error
	SwapTabForStop(ctx context.Context, id marotte.ChatID) (chat.Claim, bool)
	// SessionInTeardown names a session of a chat whose retention teardown still runs.
	SessionInTeardown(sessionID string) bool
	ReconcileKeep(ctx context.Context, id marotte.ChatID, fn func(r *chat.KeepRecord)) error
}

var _ membershipChats = (*chat.Store)(nil)

// runOwner is the run surface as the coordinator uses it: who owns a run, which runs a
// session parents, and a run's read bound. Declared here, at the consumer;
// *agent.Runs satisfies it.
type runOwner interface {
	// RunOwner may read the engine's run inventory: never call it under mu. An error means
	// the owners could not be read.
	RunOwner(ctx context.Context, workflowID string) (RunOwner, error)
	// RunsParentedBy answers the runs whose parent is sessionID: live is every leased or
	// non-terminal one, all adds the terminal ones. An error means some source could not be read.
	RunsParentedBy(ctx context.Context, sessionID string) (live, all []string, err error)
	// RunReadBound is the run's read position now, the clamp for an ack_tab.
	RunReadBound(ctx context.Context, workflowID string) (uint64, error)
}

// runVerdict is what RunOwner could establish about a run.
type runVerdict int

const (
	// RunOwned names at least one holder.
	RunOwned runVerdict = iota + 1
	// RunParentless is a run the engine answered for whose parent session no chat owns.
	RunParentless
	// RunUnknown is a run no lease, inventory row or inspect reply names, so its
	// launcher is unknown. Never read as parentless.
	RunUnknown
)

// RunOwner is the one attribution answer for a run: the chats whose session chain holds its
// parent session, plus the holders its lease records.
type RunOwner struct {
	Holders        []marotte.ChatID
	ParentSessions []string
	Verdict        runVerdict
}

// chatCloser is the tab-close teardown for a chat tab: cancel the turn, stop the
// chat's runs when stop is set (a person's close, carrying the close's stop hold),
// tear the bridge down, keep the record. Bound in RegisterDefaults.
type chatCloser func(ctx context.Context, chatID marotte.ChatID, stop *chat.Claim)

// chatDeleter is the delete grade of the same teardown, for a chat the close
// escalation has already erased; the captured session chain travels in.
type chatDeleter func(ctx context.Context, chatID marotte.ChatID, sessionChain []string, cause RunStopCause)

// A nil read means retention on — the fail-toward-keeping direction.
type retentionRead func(ctx context.Context) bool

// closeTeardownBudget bounds one teardown job, counted from its start; the session's own close outlives it
// (bridge.Session.Release).
const closeTeardownBudget = time.Minute

// retentionTeardownWait bounds RetentionClose's wait for the purged chat's teardown, so one stuck chat costs a purge
// pass this much and no more. A var for tests.
var retentionTeardownWait = closeTeardownBudget

// Membership owns every operation that spans the chat store and the tab set.
//
// Safe for concurrent use; the zero value is not usable, construct with
// newMembership. A nil TabSet means no tab store was wired: the chat half
// of every operation still runs and the tab half reports
// errTabsUnavailable.
type Membership struct {
	chats      membershipChats
	tabs       TabSet
	bus        broadcaster
	teardown   chatTeardown
	closeChat  chatCloser
	deleteChat chatDeleter
	retention  retentionRead
	// teardowns runs every committed close's teardown after the commit answered (teardownRunner).
	teardowns teardownQueue
	// impact reads what a close stops, for the check of a confirmation; nil leaves every close unchecked.
	impact closeImpactReader
	// notices hears each ack's stored marker and each tab no open tab shows any more; nil hears nothing.
	notices noticeLedger
	// runs answers a run's owners and a session's runs. Nil refuses every new run tab
	// and every create that claims a session, since neither can then be checked.
	runs runOwner
	// retentionWake asks the purge scheduler for a pass now; nil leaves its timer.
	retentionWake func()
	// supervisedDefault reads the workspace-wide Supervised default a fresh
	// record is seeded from. Nil reads as false — see supervisedDefaultValue.
	supervisedDefault func(context.Context) bool
	// ops is the create ledger, op_id -> chat id, so a retry resolves to the chat
	// its first attempt made. Here rather than in the handlers because resolving
	// an op and reserving a tab slot must happen in the same critical section.
	ops *createLedger
	// sessions is the fresh session-chain read AdmitRunPurge decides on. Nil spares
	// every run that names a parent session.
	sessions sessionClaims
	// purgingRuns holds the runs AdmitRunPurge admitted and whose delete has not
	// returned; OpenTab refuses them. purgingSessions counts the parent sessions of
	// those no chat claimed; CreateChatAndOpen refuses to bind one. Both guarded by mu.
	purgingRuns     map[string]struct{}
	purgingSessions map[string]int
	// recentWake tells RunRecentExpiry the list changed, so it re-reads the oldest expiry. Coalescing, buffer 1.
	recentWake chan struct{}
	// mu is THE operation lock, held across the reservation, the mint, both
	// durable writes and the event. Order: mu -> chat record lock -> tabs writeMu.
	mu sync.Mutex
}

// membershipDeps is Membership's constructor argument; each optional field's comment says what its nil does.
type membershipDeps struct {
	Chats membershipChats
	// Tabs nil answers every tab mutation 503 and opens chats with no tab.
	Tabs      TabSet
	Bus       broadcaster
	Teardown  chatTeardown
	CloseChat chatCloser
	// DeleteChat or Retention nil makes a tab close only close: no chat is deleted with it.
	DeleteChat chatDeleter
	Retention  retentionRead
	// SupervisedDefault answers the workspace-wide Supervised default a new record is seeded from.
	// A function so the coordinator keeps no filesystem knowledge and cannot disagree with the
	// prompt path's reader; nil is false.
	SupervisedDefault func(context.Context) bool
	// Runs nil refuses every new run tab and every create that claims a session's runs (503).
	Runs runOwner
	// Sessions answers the run purge's claim check. Nil spares every run with a
	// parent session (the fail-toward-keeping direction).
	Sessions sessionClaims
	// Lifecycle is the runtime lifetime the teardown runner runs on; nil runs each job after the commit's unlock.
	Lifecycle lifecycleAccess
	// Teardowns replaces the runner, for a test that records where each job is enqueued.
	Teardowns teardownQueue
	// Impact reads what a close stops; nil leaves every close unchecked.
	Impact closeImpactReader
	// Notices nil tells no ledger about acks and closes.
	Notices noticeLedger
}

// newMembership builds the coordinator.
func newMembership(deps *membershipDeps) *Membership {
	m := &Membership{
		chats:             deps.Chats,
		tabs:              deps.Tabs,
		bus:               deps.Bus,
		teardown:          deps.Teardown,
		closeChat:         deps.CloseChat,
		deleteChat:        deps.DeleteChat,
		retention:         deps.Retention,
		runs:              deps.Runs,
		sessions:          deps.Sessions,
		supervisedDefault: deps.SupervisedDefault,
		ops:               newCreateLedger(),
		purgingRuns:       map[string]struct{}{},
		purgingSessions:   map[string]int{},
		recentWake:        make(chan struct{}, 1),
		teardowns:         deps.Teardowns,
		impact:            deps.Impact,
		notices:           deps.Notices,
	}
	if m.teardowns == nil {
		m.teardowns = newTeardownRunner(deps.Lifecycle, m.runJob)
	}
	return m
}

// runJob runs one teardown grade. It never runs under mu.
func (m *Membership) runJob(ctx context.Context, job *teardownJob) {
	switch job.grade {
	case gradeClose:
		if m.closeChat != nil {
			m.closeChat(ctx, job.chatID, job.stop)
		}
	case gradeCloseDelete:
		m.deleteChat(ctx, job.chatID, job.chain, job.cause)
	case gradeDelete, gradeRetention:
		m.teardown.DeleteChatStateByChain(ctx, job.chatID, job.chain, job.cause)
	}
}

// enqueueAll hands each job to the runner. Caller holds mu, in the section of the commit the jobs tear down; it
// runs the returned funcs, in order, after unlocking.
func (m *Membership) enqueueAll(jobs []teardownJob) []func() {
	var after []func()
	for i := range jobs {
		if _, run := m.teardowns.enqueue(&jobs[i]); run != nil {
			after = append(after, run)
		}
	}
	return after
}

func runAll(fns []func()) {
	for _, fn := range fns {
		fn()
	}
}

// supervisedDefaultValue answers the Supervised default for a fresh record; an unwired reader is
// false rather than a panic.
func (m *Membership) supervisedDefaultValue(ctx context.Context) bool {
	if m.supervisedDefault == nil {
		return false
	}
	return m.supervisedDefault(ctx)
}

// ChatCreate is one create-and-open request: what to write on the new
// record, and where its tab goes.
type ChatCreate struct {
	// Init fills the new record's fields, called inside chat.Store.Mutate under
	// that chat's record lock; it must not reach either store.
	Init func(c *marotte.Chat)
	// OpID correlates every attempt of one create gesture, so a repeat resolves
	// to the chat the first attempt made instead of minting a second one.
	OpID string
	// ChatID is the id the envelope supplied, or empty to mint one. A supplied id
	// bypasses the ledger: Mutate's exists branch is already idempotent for it.
	ChatID marotte.ChatID
	// RequireChat is a fresh create's precondition, checked under the operation lock
	// so a delete cannot land between the check and the mint.
	RequireChat marotte.ChatID
	// ClaimSession is the session the new record binds when the client named it: the create is refused when
	// another chat's chain already holds it, so no session sits on two chains.
	ClaimSession string
	// ParentChat names the chat whose tab the new tab hangs under, empty for a
	// top-level tab. A chat id rather than a tab id, so it resolves inside the
	// operation lock; outside it, a parent tab closing would leave Parent naming
	// nothing.
	ParentChat marotte.ChatID
}

// ChatOpened is what a create answers with.
type ChatOpened struct {
	// Chat is read back from the store, since a replay resolves to a chat this
	// request did not write.
	Chat *marotte.Chat
	// Subject is the tab. Zero-valued only when no tab store is wired.
	Subject marotte.TabSubject
	Version uint64
	// Replay reports that this op_id had already created its chat.
	Replay bool
}

// TabOpened is what an open answers with.
type TabOpened struct {
	Subject marotte.TabSubject
	Version uint64
	// Created is false for an already-open (Kind, Ref), which mutates nothing and
	// emits no event, so a caller waiting on that event would wait forever.
	Created bool
}

// CreateChatAndOpen writes a chat record, holds its tab fact and opens its tab as one operation, so
// the final slot cannot be consumed between mint and open and no delete lands between the writes.
// Returns errOpenChatUnknown (404) when a required chat is absent, errChatUnread (503) when it, the
// session owners or the session's runs could not be read, errSessionClaimed (409) when another chat
// holds ClaimSession, errSessionParentsRun (409) when ClaimSession launched a live or shown run,
// errTabsFull (409) at MaxOpenTabs, and errChatNotCreated (409) when the record is absent after a
// Mutate that reported no error. A failed tab write leaves the chat created with its hold rolled
// back; a retry with the same op_id finishes it.
func (m *Membership) CreateChatAndOpen(ctx context.Context, req *ChatCreate) (ChatOpened, error) {
	// Hoisted above every lock because the read is file I/O; resolved unconditionally (it is
	// cached) so no conditional resolve has to run inside the lock.
	supervisedDefault := m.supervisedDefaultValue(ctx)
	// Before mu: the answer is an engine read.
	runs := m.sessionRuns(ctx, req.ClaimSession)

	m.mu.Lock()
	defer m.mu.Unlock()

	prior, replay := m.priorChat(req)
	if req.RequireChat != "" && !replay {
		if err := m.requireRecord(ctx, req.RequireChat); err != nil {
			return ChatOpened{}, err
		}
	}

	// Reserve before anything mints. peek rather than resolve: a repeat whose tab
	// is already open needs no slot, and refusing it would strand the chat.
	if err := m.reserveSlot(marotte.TabKindChat, string(prior)); err != nil {
		return ChatOpened{}, err
	}

	chatID := prior
	if chatID == "" {
		chatID, _ = m.ops.resolve(req.OpID, marotte.NewChatID)
	}
	if err := m.refuseClaim(ctx, req.ClaimSession, chatID, &runs); err != nil {
		return ChatOpened{}, err
	}

	// The record leads.
	purging := false
	_, err := m.chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if exists {
			return false
		}
		// BEFORE Init, so Init stays the per-command override channel: a fork
		// inherits its parent's posture on top of this.
		c.SupervisedMode = supervisedDefault
		req.Init(c)
		// After Init, whatever it bound: a chat claiming a session whose run the
		// purge is deleting would claim a run that is about to be gone.
		purging = slices.ContainsFunc(c.SessionChain(), m.sessionPurging)
		return !purging
	})
	if refused := createRefusal(err, purging); refused != nil {
		return ChatOpened{}, refused
	}
	c, ok := m.chats.Get(ctx, chatID)
	if !ok {
		return ChatOpened{}, StatusError(http.StatusConflict, errChatNotCreated)
	}

	// The tab second, unconditional even on a replay, to finish a tab write the first
	// attempt did not — Open is idempotent by (Kind, Ref). Owns is STATED rather than
	// defaulted, because its zero value is a legal value: a chat tab owns the chat it
	// shows, so the client counts it and runs its local teardown on close.
	if m.tabs == nil {
		return ChatOpened{Chat: c, Replay: replay}, nil
	}
	claim, err := m.chats.Hold(ctx, chatID, marotte.KeyTab, chat.KeepDurable)
	if err != nil {
		return ChatOpened{}, holdTabErr(err)
	}
	opened, err := m.openTab(ctx, marotte.OpenTab{
		Kind:   marotte.TabKindChat,
		Ref:    string(chatID),
		Parent: m.tabForChat(req.ParentChat),
		Owns:   true,
	}, req.OpID)
	if err != nil {
		m.rollback(ctx, &claim)
		return ChatOpened{}, err
	}
	return ChatOpened{Chat: c, Subject: opened.Subject, Version: opened.Version, Replay: replay}, nil
}

// createRefusal maps the record write's outcome: an id or a bound session whose purge teardown is running, then a
// recently deleted id, are the client's to retry later, not server faults.
func createRefusal(err error, purging bool) error {
	switch {
	case errors.Is(err, chat.ErrInTeardown):
		return StatusError(http.StatusConflict, errChatPurging)
	case errors.Is(err, chat.ErrTombstoned):
		return StatusError(http.StatusConflict, err)
	case err != nil:
		return StatusError(http.StatusInternalServerError, err)
	case purging:
		return StatusError(http.StatusConflict, errRunPurging)
	}
	return nil
}

// resolvedChat reports the chat an op_id has already created, without minting
// one. Exists for fork_chat: a fork's record cannot be built until KAS answers
// session/fork, and that round trip must not happen under the operation lock
// (a bridge Call has no client-side timeout).
func (m *Membership) resolvedChat(opID string) (marotte.ChatID, bool) {
	return m.ops.peek(opID)
}

// OpenTab opens a tab for something that already exists; it never mints a chat.
// For a chat tab it holds the record's tab fact first, which refuses a record that
// is gone, unreadable or in teardown, with the hold and the open in one critical section.
// A new run tab holds the run-tab fact on each of its run's owners (openRunTabLocked).
func (m *Membership) OpenTab(ctx context.Context, spec marotte.OpenTab, opID string) (TabOpened, error) {
	if m.tabs == nil {
		return TabOpened{}, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	opened, err := m.openTabLocked(ctx, spec, opID, m.runLauncher(ctx, spec))
	if errors.Is(err, errSuppliedParentClosed) {
		spec.Parent = ""
		opened, err = m.openTabLocked(ctx, spec, opID, m.runLauncher(ctx, spec))
	}
	return opened, err
}

// launcherAnswer is the run owner's answer for a run tab, read before mu.
type launcherAnswer struct {
	err   error
	owner RunOwner
}

// openTabLocked is OpenTab's critical section.
func (m *Membership) openTabLocked(ctx context.Context, spec marotte.OpenTab, opID string, launcher launcherAnswer) (TabOpened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch spec.Kind {
	case marotte.TabKindChat:
		return m.openChatTabUnderMu(ctx, spec, opID)
	case marotte.TabKindRun:
		return m.openRunTabLocked(ctx, spec, opID, launcher)
	}
	spec.Launch = nil
	return m.openTab(ctx, spec, opID)
}

// openChatTabUnderMu holds the chat's tab fact, then opens its tab, rolling the hold back when the open fails.
// Caller holds mu.
func (m *Membership) openChatTabUnderMu(ctx context.Context, spec marotte.OpenTab, opID string) (TabOpened, error) {
	claim, err := m.chats.Hold(ctx, marotte.ChatID(spec.Ref), marotte.KeyTab, chat.KeepDurable)
	if err != nil {
		return TabOpened{}, holdTabErr(err)
	}
	opened, err := m.openTab(ctx, spec, opID)
	if err != nil {
		m.rollback(ctx, &claim)
	}
	return opened, err
}

// ReopenForMessage opens chatID's tab, owning and top level, when a message from an outside caller reaches a chat
// with none; a chat with a tab costs one lookup and writes nothing. It never refuses the message: at MaxOpenTabs the
// open is skipped at Info, a gone or tearing-down chat is left to the prompt's own refusal, and any other failure is
// logged at Warn.
func (m *Membership) ReopenForMessage(ctx context.Context, chatID marotte.ChatID) {
	if m.tabs == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tabForChat(chatID) != "" {
		return
	}
	_, err := m.openChatTabUnderMu(ctx, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(chatID), Owns: true}, "")
	switch {
	case err == nil, errors.Is(err, errOpenChatUnknown), errors.Is(err, errChatPurging):
	case errors.Is(err, errTabsFull):
		slog.Info("command: a messaged chat's tab was not reopened; 48 tabs are open", "chat_id", chatID)
	default:
		slog.Warn("command: a messaged chat's tab was not reopened", "chat_id", chatID, keyError, err)
	}
}

// openRunTabLocked opens a run tab. An open one only gains focus. A new one is refused unless its owners were
// read and named (refuseRunTab), then holds the run-tab fact on every holder before it opens; any later failure
// rolls those holds back. Parent is immutable, so a run whose launcher could not be read never opens top level.
// Caller holds mu.
func (m *Membership) openRunTabLocked(ctx context.Context, spec marotte.OpenTab, opID string, launcher launcherAnswer) (TabOpened, error) {
	if m.runTabOpen(spec.Ref) {
		return m.openTab(ctx, spec, opID)
	}
	if err := m.refuseRunTab(spec, &launcher); err != nil {
		return TabOpened{}, err
	}
	claims, holders, err := m.holdRunTab(ctx, spec.Ref, launcher.owner.Holders)
	if err != nil {
		return TabOpened{}, err
	}
	opened, err := m.openHeldRunTab(ctx, spec, opID, holders)
	if err != nil {
		for i := range claims {
			m.rollback(ctx, &claims[i])
		}
	}
	return opened, err
}

// openHeldRunTab settles the run tab's Parent (a supplied open parent, else the first holder's tab) and opens it with
// its holders recorded. Caller holds mu.
func (m *Membership) openHeldRunTab(ctx context.Context, spec marotte.OpenTab, opID string, holders []marotte.ChatID) (TabOpened, error) {
	if spec.Parent == "" && len(holders) > 0 {
		parent, err := m.launcherTab(ctx, holders[0])
		if err != nil {
			return TabOpened{}, err
		}
		spec.Parent = parent
	}
	spec.Launch = &marotte.RunLaunch{Chats: make([]string, 0, len(holders))}
	for _, h := range holders {
		spec.Launch.Chats = append(spec.Launch.Chats, string(h))
	}
	return m.openTab(ctx, spec, opID)
}

// refuseRunTab is a new run tab's refusals, in order: the parentless purge is deleting the run; its owners could not
// be read, or no source names it; the caller's parent closed; a launcher's teardown still runs, which only the
// run's parent session still names once the launcher's record is gone. Caller holds mu.
func (m *Membership) refuseRunTab(spec marotte.OpenTab, launcher *launcherAnswer) error {
	if _, purging := m.purgingRuns[spec.Ref]; purging {
		return StatusError(http.StatusConflict, errRunPurging)
	}
	if launcher.err != nil {
		return StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errRunLauncherUnread, launcher.err))
	}
	if launcher.owner.Verdict == RunUnknown {
		return StatusError(http.StatusServiceUnavailable, errRunUnknown)
	}
	// The store would promote a run under a closed parent, and Parent is immutable.
	if spec.Parent != "" && !m.tabOpen(spec.Parent) {
		return errSuppliedParentClosed
	}
	if slices.ContainsFunc(launcher.owner.ParentSessions, m.chats.SessionInTeardown) {
		return StatusError(http.StatusConflict, errRunPurging)
	}
	return nil
}

// holdRunTab holds workflowID's run-tab fact on each owner and answers the claims with the owners still present. A
// record deleted since the owners were read drops out; one tombstoned or in teardown refuses with 409, an unreadable
// one with 503, and either refusal rolls back the claims already taken. Caller holds mu.
func (m *Membership) holdRunTab(ctx context.Context, workflowID string, owners []marotte.ChatID) ([]chat.Claim, []marotte.ChatID, error) {
	if len(owners) == 0 {
		return nil, nil, nil
	}
	key, ok := marotte.KeyRunTab(workflowID)
	if !ok {
		return nil, nil, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	claims := make([]chat.Claim, 0, len(owners))
	held := make([]marotte.ChatID, 0, len(owners))
	for _, h := range owners {
		claim, err := m.chats.Hold(ctx, h, key, chat.KeepDurable)
		switch {
		case err == nil:
			claims = append(claims, claim)
			held = append(held, h)
			continue
		case errors.Is(err, chat.ErrChatNotFound):
			continue
		case errors.Is(err, chat.ErrTombstoned):
			err = StatusError(http.StatusConflict, errRunPurging)
		default:
			err = StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errRunLauncherUnread, err))
		}
		for i := range claims {
			m.rollback(ctx, &claims[i])
		}
		return nil, nil, err
	}
	return claims, held, nil
}

// runLauncher asks the run owner who owns a run tab's run: one rule for every door (a
// notification, a deep link, a run card), so no door has to hold the run's launch history.
// Called before mu, because the answer may be an engine read.
func (m *Membership) runLauncher(ctx context.Context, spec marotte.OpenTab) launcherAnswer {
	if spec.Kind != marotte.TabKindRun {
		return launcherAnswer{}
	}
	if m.runs == nil {
		return launcherAnswer{err: errRunsUnavailable}
	}
	owner, err := m.runs.RunOwner(ctx, spec.Ref)
	return launcherAnswer{owner: owner, err: err}
}

// launcherTab is the tab a new run tab nests under: the launching chat's open
// tab, else that chat's tab reopened with its tab fact held, so a run whose chat
// still exists never opens top level. Both slots are counted before either opens.
// "" only when no record exists; holdTabErr maps every other refusal. Only a
// failure of its own open rolls the hold back. Caller holds mu.
func (m *Membership) launcherTab(ctx context.Context, launcher marotte.ChatID) (string, error) {
	if id := m.tabForChat(launcher); id != "" {
		return id, nil
	}
	claim, err := m.chats.Hold(ctx, launcher, marotte.KeyTab, chat.KeepDurable)
	if errors.Is(err, chat.ErrChatNotFound) {
		return "", nil
	}
	if err != nil {
		return "", holdTabErr(err)
	}
	if open, _ := m.tabs.List(); len(open)+2 > tabs.MaxOpenTabs {
		m.rollback(ctx, &claim)
		return "", StatusErrorReason(http.StatusConflict, reasonTabsFull, errTabsFull)
	}
	// No op_id: the client settles its pending open on the first frame naming
	// the op, and that frame must be the run's.
	opened, err := m.openTab(ctx, marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(launcher), Owns: true}, "")
	if err != nil {
		m.rollback(ctx, &claim)
		return "", err
	}
	return opened.Subject.ID, nil
}

// closeTab closes a tab and its descendants and, with retention off, deletes each chat
// the close left tabless. An id that is not open closes nothing and is not an error.
//
// Under mu it holds each closed chat's life gate, refuses a stale impact with 409
// close_impact_changed (holds released, nothing committed), then commits with tabs.Close (the doomed chats unlisted),
// swaps a person's close's tab facts for stop holds, deletes the doomed records and enqueues
// each chat's teardown; it answers without waiting for the teardowns (teardownRunner.enqueue
// owns the draining exception). Past the commit there is no rollback, only roll-forward.
func (m *Membership) closeTab(ctx context.Context, id, opID string, byPerson bool, impact *marotte.CloseImpact) (closed []marotte.TabSubject, version uint64, err error) {
	if m.tabs == nil {
		return nil, 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	// Held before the impact read and tabs.Close and committed before the unlock, so no device sees the tab gone
	// while a chat it closed still admits work, and no work begins between the check and the commit.
	chats := m.subtreeChats(id)
	holds := m.holdAll(chats)
	if check, ok := m.checkImpact(ctx, impact, chats, byPerson); !ok {
		releaseAll(holds)
		m.mu.Unlock()
		return nil, 0, check.refusal(errCloseImpactChanged, "tab", id)
	}
	doomed := m.doomedChats(ctx, id)
	before := m.tabs.Recent()
	closed, owed, version, err := m.tabs.Close(ctx, id, chatRefs(doomed), m.recentFacts(ctx, chats))
	if err != nil {
		releaseAll(holds)
		m.mu.Unlock()
		return nil, 0, StatusError(http.StatusInternalServerError, err)
	}
	if len(closed) == 0 {
		releaseAll(holds)
		m.mu.Unlock()
		return closed, version, nil
	}
	commitAll(holds)
	// Committed. Everything from here rolls forward under its own bound;
	// the request context governed the operation only up to this point.
	rollCtx, done := context.WithTimeout(context.WithoutCancel(ctx), closeTeardownBudget)
	defer done()
	m.emit(rollCtx, &marotte.TabsChangedPayload{
		RemovedIDs: subjectIDs(closed),
		Order:      m.order(),
		Recent:     m.recentChange(before),
		Version:    version,
		OpID:       opID,
	})
	m.noticeClosed(closed)
	stops := m.releaseForClosed(rollCtx, owed, "", byPerson && m.closeChat != nil)
	deleted := m.deleteDoomedRecords(rollCtx, doomed)
	jobs, retentionTabClosed := jobsFor(closed, deleted, holds, stops, byPerson)
	after := m.enqueueAll(jobs)
	m.mu.Unlock()
	runAll(after)
	if retentionTabClosed {
		// The closed tab may have been what held an expired chat or finished run outside the age
		// test, so wake the purge now rather than after an idle back-off of up to an hour. After
		// the lock, because the wake reads a field it guards.
		m.wakeRetention()
	}
	return closed, version, nil
}

// jobsFor builds one teardown job per chat the close took, each carrying that chat's committed hold: the delete
// grade for a record this close deleted (from the chain its delete answered), the close grade for every other chat
// tab, a doomed chat whose delete failed included. A person's close hands each close grade the chat's stop hold
// (stops). It also reports whether a chat or run tab went, which owes retention a wake.
func jobsFor(
	closed []marotte.TabSubject, deleted map[marotte.ChatID][]string, holds map[marotte.ChatID]ChatHold,
	stops map[marotte.ChatID]chat.Claim, byPerson bool,
) (jobs []teardownJob, retentionTabClosed bool) {
	queued := make(map[marotte.ChatID]bool, len(holds))
	for _, t := range closed {
		if t.Kind == marotte.TabKindRun {
			retentionTabClosed = true
		}
		if t.Kind != marotte.TabKindChat {
			continue
		}
		retentionTabClosed = true
		chatID := marotte.ChatID(t.Ref)
		if queued[chatID] {
			continue
		}
		queued[chatID] = true
		job := teardownJob{chatID: chatID, hold: holds[chatID], grade: gradeClose}
		if chain, isDoomed := deleted[chatID]; isDoomed {
			job.grade, job.chain, job.cause = gradeCloseDelete, chain, RunStopTabClosed
		} else if byPerson {
			stop := stops[chatID]
			job.stop = &stop
		}
		jobs = append(jobs, job)
	}
	return jobs, retentionTabClosed
}

// subtreeChats is every chat a Close of id will take, each once. Caller holds mu.
func (m *Membership) subtreeChats(id string) []marotte.ChatID {
	var chats []marotte.ChatID
	seen := map[marotte.ChatID]bool{}
	for _, t := range m.tabs.Subtree(id) {
		chatID := marotte.ChatID(t.Ref)
		if t.Kind != marotte.TabKindChat || seen[chatID] {
			continue
		}
		seen[chatID] = true
		chats = append(chats, chatID)
	}
	return chats
}

// holdAll holds each chat's life gate. Caller holds mu.
func (m *Membership) holdAll(chats []marotte.ChatID) map[marotte.ChatID]ChatHold {
	holds := make(map[marotte.ChatID]ChatHold, len(chats))
	for _, chatID := range chats {
		if _, ok := holds[chatID]; !ok {
			holds[chatID] = m.teardown.HoldChat(chatID)
		}
	}
	return holds
}

func commitAll(holds map[marotte.ChatID]ChatHold) {
	for _, h := range holds {
		h.Commit()
	}
}

func releaseAll(holds map[marotte.ChatID]ChatHold) {
	for _, h := range holds {
		h.Release()
	}
}

// doomedChats decides what a retention-off close of id will delete: every chat whose
// open tabs all lie inside the closing subtree and whose record exists. A recordless chat
// is skipped — no chat_deleted for an id no device knows.
//
// Caller holds mu.
func (m *Membership) doomedChats(ctx context.Context, id string) []marotte.ChatID {
	if m.deleteChat == nil || m.retention == nil {
		// Escalation unwired: a close can only close. Retention defaults
		// on — the fail-toward-keeping direction.
		return nil
	}
	refs := m.tablessChatRefs(m.tabs.Subtree(id))
	if len(refs) == 0 || m.retention(ctx) {
		return nil
	}
	doomed := make([]marotte.ChatID, 0, len(refs))
	for _, ref := range refs {
		chatID := marotte.ChatID(ref)
		if _, ok := m.chats.Get(ctx, chatID); ok {
			doomed = append(doomed, chatID)
		}
	}
	return doomed
}

// Caller holds mu.
func (m *Membership) tablessChatRefs(subtree []marotte.TabSubject) []string {
	if len(subtree) == 0 {
		return nil
	}
	inSubtree := make(map[string]bool, len(subtree))
	for _, t := range subtree {
		inSubtree[t.ID] = true
	}
	open, _ := m.tabs.List()
	remaining := make(map[string]bool)
	for _, t := range open {
		if t.Kind == marotte.TabKindChat && !inSubtree[t.ID] {
			remaining[t.Ref] = true
		}
	}
	var refs []string
	seen := make(map[string]bool)
	for _, t := range subtree {
		if t.Kind != marotte.TabKindChat || seen[t.Ref] || remaining[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		refs = append(refs, t.Ref)
	}
	return refs
}

// deleteDoomedRecords removes each doomed chat's record — tombstone and
// chat_deleted happen inside Delete — and returns the session chains of
// the ones that actually went. A failed delete logs ERROR and is left out
// of the result, demoting that chat to the close-grade teardown.
//
// Caller holds mu; ctx is the detached roll-forward context.
func (m *Membership) deleteDoomedRecords(ctx context.Context, doomed []marotte.ChatID) map[marotte.ChatID][]string {
	if len(doomed) == 0 {
		return nil
	}
	deleted := make(map[marotte.ChatID][]string, len(doomed))
	for _, chatID := range doomed {
		chain, err := m.chats.Delete(ctx, chatID)
		if err != nil {
			slog.Error("close: retention-off record delete failed after the tab close committed; the record survives with close-grade teardown",
				"chat_id", chatID, keyError, err)
			continue
		}
		slog.Info("chat deleted on close (retention off)", "chat_id", chatID)
		deleted[chatID] = chain
	}
	return deleted
}

// reorderTabs replaces the order. ids must name every open tab exactly
// once; tabs.ErrOrderMismatch maps to 409.
//
// No base-version precondition: the exact-set check is the precondition,
// and a version one would discard a valid drag whenever an unrelated pin
// landed first.
func (m *Membership) reorderTabs(ctx context.Context, ids []string, opID string) (uint64, error) {
	if m.tabs == nil {
		return 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.version()
	recent := m.tabs.Recent()
	version, err := m.tabs.Reorder(ctx, ids)
	if err != nil {
		return 0, tabStatus(err)
	}
	// An order identical to the one already held commits nothing and must
	// emit nothing.
	if version != before {
		m.emit(ctx, &marotte.TabsChangedPayload{Order: m.order(), Recent: m.recentChange(recent), Version: version, OpID: opID})
	}
	return version, nil
}

// setPinned pins or unpins one tab. Idempotent in both directions.
//
// An id that is not open is errTabUnknown (404), unlike a close: a pin is
// a statement about a tab, so success would tell the caller its tab is
// pinned when it is not.
func (m *Membership) setPinned(ctx context.Context, id string, pinned bool, opID string) (uint64, error) {
	if m.tabs == nil {
		return 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	open, before := m.tabs.List()
	if indexOfTab(open, id) < 0 {
		return 0, StatusError(http.StatusNotFound, errTabUnknown)
	}
	recent := m.tabs.Recent()
	version, err := m.tabs.SetPinned(ctx, id, pinned)
	if err != nil {
		return 0, StatusError(http.StatusInternalServerError, err)
	}
	if version != before {
		if changed, ok := m.subject(id); ok {
			m.emit(ctx, &marotte.TabsChangedPayload{Changed: &changed, Recent: m.recentChange(recent), Version: version, OpID: opID})
		}
	}
	return version, nil
}

// ackTab advances a chat or run tab's seen marker to seen, clamped to the position the tab's
// state names now, and answers the version and the marker as stored. The marker never moves
// backward (tabs.Store.SetSeen), so a lagging window's older ack is a no-op. An id that is not
// open, or closed during the bound read, is a no-op too, unlike a pin: an ack racing a close on
// another device is normal.
//
// The bound read may open a chat log or ask KAS, so it runs between two sections of mu, never
// under it.
func (m *Membership) ackTab(ctx context.Context, id string, seen uint64, opID string) (version, stored uint64, err error) {
	if m.tabs == nil {
		return 0, 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	sub, open := m.subject(id)
	current := m.version()
	m.mu.Unlock()
	if !open {
		return current, 0, nil
	}
	var bound uint64
	switch sub.Kind {
	case marotte.TabKindChat:
		bound, err = m.chats.ReadBound(ctx, marotte.ChatID(sub.Ref))
	case marotte.TabKindRun:
		if m.runs == nil {
			return 0, 0, StatusError(http.StatusServiceUnavailable, errRunsUnavailable)
		}
		bound, err = m.runs.RunReadBound(ctx, sub.Ref)
	default:
		return 0, 0, StatusError(http.StatusBadRequest, ErrInvalidPayload)
	}
	if err != nil {
		slog.Warn("tab ack: read bound not read", "tab", id, "error", err)
		return 0, 0, StatusError(http.StatusInternalServerError, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.version()
	recent := m.tabs.Recent()
	version, err = m.tabs.SetSeen(ctx, id, min(seen, bound))
	if err != nil {
		slog.Warn("tab ack not persisted", "tab", id, "error", err)
		return 0, 0, StatusError(http.StatusInternalServerError, err)
	}
	changed, ok := m.subject(id)
	if version != before && ok {
		m.emit(ctx, &marotte.TabsChangedPayload{Changed: &changed, Recent: m.recentChange(recent), Version: version, OpID: opID})
	}
	if ok && changed.Seen > 0 && m.notices != nil {
		m.notices.Seen(changed.Kind, changed.Ref, changed.Seen)
	}
	return version, changed.Seen, nil
}

// reparent hangs one open tab under an open chat tab and returns the subject
// as it now reads. Idempotent when the parent is unchanged: nothing commits,
// nothing is emitted.
//
// errTabUnknown (404) for an id that is not open, like a pin; 409 when parent
// is not an open TabKindChat tab or sits inside the tab's own subtree. The
// frame carries Order beside Changed because the row moved.
func (m *Membership) reparent(ctx context.Context, id, parent, opID string) (marotte.TabSubject, uint64, error) {
	if m.tabs == nil {
		return marotte.TabSubject{}, 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	open, before := m.tabs.List()
	if indexOfTab(open, id) < 0 {
		return marotte.TabSubject{}, 0, StatusError(http.StatusNotFound, errTabUnknown)
	}
	if p := indexOfTab(open, parent); p < 0 || open[p].Kind != marotte.TabKindChat {
		return marotte.TabSubject{}, 0, StatusError(http.StatusConflict, errParentNotChat)
	}
	recent := m.tabs.Recent()
	version, err := m.tabs.Reparent(ctx, id, parent)
	if err != nil {
		if errors.Is(err, tabs.ErrCycle) {
			return marotte.TabSubject{}, 0, StatusError(http.StatusConflict, err)
		}
		return marotte.TabSubject{}, 0, StatusError(http.StatusInternalServerError, err)
	}
	changed, ok := m.subject(id)
	if !ok {
		return marotte.TabSubject{}, 0, StatusError(http.StatusInternalServerError, errTabUnknown)
	}
	if version != before {
		m.emit(ctx, &marotte.TabsChangedPayload{
			Changed: &changed, Order: m.order(), Recent: m.recentChange(recent), Version: version, OpID: opID,
		})
	}
	return changed, version, nil
}

// DeleteChatAndCloseTabs is the delete path: remove the record and close its tabs in one
// critical section, and answer; the chat's work is torn down in the background from the session chain the removed
// record held, and every chat whose tab sat under one of its tabs is closed as closing that tab would. The record is
// the admission barrier: once it is gone every later open is refused and a bridge opening on it settles
// ErrChatGone, and the teardown closes any bridge that opened before. A failed delete tears nothing down, so the
// chat keeps working.
func (m *Membership) DeleteChatAndCloseTabs(ctx context.Context, chatID marotte.ChatID, opID string, impact *marotte.CloseImpact) error {
	children, after, err := m.deleteRecordAndCloseTabs(ctx, chatID, opID, impact)
	if errors.Is(err, errDeleteImpactChanged) {
		return err
	}
	if err != nil {
		return StatusError(http.StatusInternalServerError, err)
	}
	runAll(after)
	if children {
		m.wakeRetention()
	}
	return nil
}

func (m *Membership) deleteRecordAndCloseTabs(ctx context.Context, chatID marotte.ChatID, opID string, impact *marotte.CloseImpact) (children bool, after []func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.holdDescendants(chatID)
	own := m.teardown.HoldChat(chatID)
	// Only the descendants are compared: the confirm already says the chat itself goes with whatever it is doing. A
	// descendant's close is not a person's, so its runs go on and are not compared.
	if check, ok := m.checkImpact(ctx, impact, slices.Sorted(maps.Keys(held)), false); !ok {
		own.Release()
		releaseAll(held)
		return false, nil, check.refusal(errDeleteImpactChanged, "chat_id", chatID)
	}
	sessionChain, err := m.chats.Delete(ctx, chatID)
	if err != nil {
		own.Release()
		releaseAll(held)
		return false, nil, err
	}
	own.Commit()
	slog.Info("chat deleted", "chat_id", chatID)
	closed := m.closeTabsFor(ctx, chatID, opID, held)
	jobs := make([]teardownJob, 0, 1+len(closed))
	jobs = append(jobs, teardownJob{chatID: chatID, hold: own, chain: sessionChain, cause: RunStopChatDeleted, grade: gradeDelete})
	jobs = append(jobs, closed...)
	return len(closed) > 0, m.enqueueAll(jobs), nil
}

// RetentionClose tears down the work of a chat the retention purge already removed, then closes its tabs. It waits
// for that teardown at most retentionTeardownWait, so one stuck chat never stops the pass; the job then finishes on
// its own. A tab fact kept the chat, so a tab closed here is one no keep fact recorded.
func (m *Membership) RetentionClose(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
	m.mu.Lock()
	hold := m.teardown.HoldChat(chatID)
	hold.Commit()
	done, run := m.teardowns.enqueue(&teardownJob{
		chatID: chatID, hold: hold, chain: sessionChain, cause: RunStopRetention, grade: gradeRetention,
	})
	m.mu.Unlock()
	if run != nil {
		run()
	}
	wait := time.NewTimer(retentionTeardownWait)
	select {
	case <-done:
	case <-ctx.Done():
	case <-wait.C:
		slog.Debug("a purged chat's teardown is still running; the purge goes on", "chat_id", chatID)
	}
	wait.Stop()
	m.mu.Lock()
	jobs := m.closeTabsFor(ctx, chatID, "", m.holdDescendants(chatID))
	after := m.enqueueAll(jobs)
	m.mu.Unlock()
	runAll(after)
	if len(jobs) > 0 {
		m.wakeRetention()
	}
}

// SetRetentionWake registers the callback that asks the retention purge for a pass now; a setter
// because the purge scheduler is built after the coordinator. Closing a tab owes it: an exempt chat
// contributes no deadline, so without a wake an all-exempt pass backs off to the one-hour ceiling.
func (m *Membership) SetRetentionWake(wake func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retentionWake = wake
}

// WakeRetention asks the purge to reconsider, if a scheduler is wired. A run that
// just finished owes it as much as a closed tab: with retention off, a finished run
// no tab shows is purgeable from that moment.
func (m *Membership) WakeRetention() { m.wakeRetention() }

func (m *Membership) wakeRetention() {
	m.mu.Lock()
	wake := m.retentionWake
	m.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// ArrangementUnreadError is AdmitRunPurge's refusal while the tab arrangement could not be read at boot:
// whether a tab shows the run is then unknown. Aside is the tab store's Unread.
type ArrangementUnreadError struct{ Aside string }

func (e *ArrangementUnreadError) Error() string {
	return "the tab arrangement could not be read (" + e.Aside + "), so no run purge is admitted"
}

// errRunPurgeKept is AdmitRunPurge's refusal of a run something still keeps.
var errRunPurgeKept = errors.New("the run is shown, claimed or in a chat's teardown")

// AdmitRunPurge answers whether the run purge may delete workflowID, launched from parentSessionID,
// refusing with *ArrangementUnreadError while the tab arrangement is unread, and otherwise while a tab
// shows the run or its session is in a chat's retention teardown. Unless owned (the purge ages out a
// chat's runs too), it also refuses while any chat's chain holds that session or the chats cannot all
// be read. On nil the run, and an unowned run's session, are marked purging until the caller runs
// done, so OpenTab and CreateChatAndOpen refuse them. Checks and marks share mu, so nothing commits
// between the decision and the delete.
func (m *Membership) AdmitRunPurge(ctx context.Context, workflowID, parentSessionID string, owned bool) (done func(), refused error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tabs != nil {
		if aside := m.tabs.Unread(); aside != "" {
			return nil, &ArrangementUnreadError{Aside: aside}
		}
	}
	if m.runTabOpen(workflowID) || (!owned && !m.sessionUnclaimed(ctx, parentSessionID)) ||
		(parentSessionID != "" && m.chats.SessionInTeardown(parentSessionID)) {
		return nil, errRunPurgeKept
	}
	m.purgingRuns[workflowID] = struct{}{}
	// A claimed session is already bound, so a create binding it cannot make the run look claimed; marking it would
	// only refuse a living chat's fork or resume.
	markSession := !owned && parentSessionID != ""
	if markSession {
		m.purgingSessions[parentSessionID]++
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.purgingRuns, workflowID)
		if !markSession {
			return
		}
		m.purgingSessions[parentSessionID]--
		if m.purgingSessions[parentSessionID] <= 0 {
			delete(m.purgingSessions, parentSessionID)
		}
	}, nil
}

// requireRecord is a fresh create's precondition: 404 when the record is gone, 503 when it could
// not be read. Caller holds mu.
func (m *Membership) requireRecord(ctx context.Context, chatID marotte.ChatID) error {
	_, found, err := m.chats.Lookup(ctx, chatID)
	if err != nil {
		return StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errChatUnread, err))
	}
	if !found {
		return StatusError(http.StatusNotFound, errOpenChatUnknown)
	}
	return nil
}

// holdTabErr maps a chat-tab hold's refusal: a chat in teardown first, then a chat that is gone,
// then an unreadable record, which a 404 would tell the client is gone.
func holdTabErr(err error) error {
	switch {
	case errors.Is(err, chat.ErrInTeardown):
		return StatusError(http.StatusConflict, errChatPurging)
	case errors.Is(err, chat.ErrChatNotFound), errors.Is(err, chat.ErrTombstoned):
		return StatusError(http.StatusNotFound, errOpenChatUnknown)
	}
	return StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errChatUnread, err))
}

// rollback undoes one failed open's hold. A failed rollback leaves the fact, which keeps the chat.
func (m *Membership) rollback(ctx context.Context, claim *chat.Claim) {
	if err := m.chats.Rollback(ctx, claim); err != nil {
		slog.Warn("keep fact not rolled back after a failed open; the chat is kept until it is",
			"chat_id", claim.ChatID, "key", claim.Key, keyError, err)
	}
}

// refuseClaim is a claimed session's two refusals: another chat owns it, or it launched a live or shown run.
// Caller holds mu.
func (m *Membership) refuseClaim(ctx context.Context, sessionID string, chatID marotte.ChatID, runs *parentedRuns) error {
	if err := m.refuseClaimedSession(ctx, sessionID, chatID); err != nil {
		return err
	}
	return m.refuseRunParent(sessionID, runs)
}

// refuseClaimedSession refuses binding sessionID when another chat's chain holds it, or when no owner
// was found on an owner scan that could not read every record. Caller holds mu, so two resumes of
// one session serialise.
func (m *Membership) refuseClaimedSession(ctx context.Context, sessionID string, chatID marotte.ChatID) error {
	if sessionID == "" {
		return nil
	}
	if m.sessions == nil {
		return StatusError(http.StatusServiceUnavailable, errChatUnread)
	}
	scan := m.sessions.SessionOwners(ctx)
	owners := scan.Owners.Of(sessionID)
	if slices.ContainsFunc(owners, func(o marotte.ChatID) bool { return o != chatID }) {
		slog.Info("refused binding a session another chat holds", "acp_session", sessionID, "chat_id", chatID)
		return StatusError(http.StatusConflict, errSessionClaimed)
	}
	if len(owners) == 0 && !scan.Complete {
		slog.Warn("could not read every chat, so a session was not bound", "acp_session", sessionID, "chat_id", chatID)
		return StatusError(http.StatusServiceUnavailable, errChatUnread)
	}
	return nil
}

// parentedRuns is the run owner's answer for a claimed session, read before mu.
type parentedRuns struct {
	err       error
	live, all []string
}

func (m *Membership) sessionRuns(ctx context.Context, sessionID string) parentedRuns {
	if sessionID == "" {
		return parentedRuns{}
	}
	if m.runs == nil {
		return parentedRuns{err: errRunsUnavailable}
	}
	live, all, err := m.runs.RunsParentedBy(ctx, sessionID)
	return parentedRuns{live: live, all: all, err: err}
}

// refuseRunParent refuses binding a session that launched a live run or one an open run tab shows: the new chat
// would own that run, and its purge would delete it. Caller holds mu.
func (m *Membership) refuseRunParent(sessionID string, runs *parentedRuns) error {
	if sessionID == "" {
		return nil
	}
	if runs.err != nil {
		slog.Warn("could not read the runs a session launched, so it was not bound", "acp_session", sessionID, keyError, runs.err)
		return StatusError(http.StatusServiceUnavailable, fmt.Errorf("%w: %w", errChatUnread, runs.err))
	}
	if len(runs.live) > 0 || slices.ContainsFunc(runs.all, m.runTabOpen) {
		slog.Info("refused binding a session that launched a live or shown run", "acp_session", sessionID)
		return StatusError(http.StatusConflict, errSessionParentsRun)
	}
	return nil
}

// releaseForClosed releases the keep facts a committed close owes, skipping except (a record
// already gone), and settles each entry whose releases all returned nil. With stopping (a
// person's close whose teardown stops runs) each chat's tab fact is swapped for its stop hold
// instead (chat.Store.SwapTabForStop), and the holds are answered by chat for the teardown,
// whose stop ends them. An entry left owed is retried by SettleOwed. Caller holds mu.
func (m *Membership) releaseForClosed(
	ctx context.Context, owed []tabs.OwedRelease, except marotte.ChatID, stopping bool,
) map[marotte.ChatID]chat.Claim {
	var settled []string
	var holds map[marotte.ChatID]chat.Claim
	for i := range owed {
		e := &owed[i]
		if stopping && e.Kind == marotte.TabKindChat {
			if holds == nil {
				holds = make(map[marotte.ChatID]chat.Claim)
			}
			hold, swapped := m.chats.SwapTabForStop(ctx, marotte.ChatID(e.Ref))
			holds[marotte.ChatID(e.Ref)] = hold
			if swapped {
				settled = append(settled, e.ID)
			}
			continue
		}
		if m.releaseOwed(ctx, e, except) {
			settled = append(settled, e.ID)
		}
	}
	m.settle(ctx, settled)
	return holds
}

// releaseOwed releases one owed entry's facts and reports whether every release returned nil.
func (m *Membership) releaseOwed(ctx context.Context, e *tabs.OwedRelease, except marotte.ChatID) bool {
	ok := true
	for _, rel := range owedFacts(e) {
		if rel.chat == except {
			continue
		}
		if err := m.chats.Release(ctx, rel.chat, rel.key, chat.EvidenceTabClosed); err != nil {
			slog.Warn("keep fact not released after its tab closed; retried every purge pass",
				"chat_id", rel.chat, "key", rel.key, keyError, err)
			ok = false
		}
	}
	return ok
}

func (m *Membership) settle(ctx context.Context, entryIDs []string) {
	if err := m.tabs.Settle(ctx, entryIDs); err != nil {
		slog.Warn("owed keep-fact releases not settled; the next pass releases them again", keyError, err)
	}
}

// owedFact is one (chat, key) release an owed entry names.
type owedFact struct {
	chat marotte.ChatID
	key  string
}

func owedFacts(e *tabs.OwedRelease) []owedFact {
	if e.Kind == marotte.TabKindChat {
		return []owedFact{{chat: marotte.ChatID(e.Ref), key: marotte.KeyTab}}
	}
	key, ok := marotte.KeyRunTab(e.Ref)
	if !ok {
		return nil
	}
	out := make([]owedFact, 0, len(e.Holders))
	for _, h := range e.Holders {
		out = append(out, owedFact{chat: marotte.ChatID(h), key: key})
	}
	return out
}

// SettleOwed retries every owed release, a purge side pass and a boot step. A fact is released only
// when no open tab shows its subject now and its Since is not after the close, so a tab reopened
// since keeps its new fact; such an entry is settled, since that tab's own close owes the release.
func (m *Membership) SettleOwed(ctx context.Context) archive.PurgeResult {
	if m.tabs == nil {
		return archive.PurgeResult{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var settled []string
	for _, e := range m.tabs.Owed() {
		if m.settleOwedEntry(ctx, &e) {
			settled = append(settled, e.ID)
		}
	}
	m.settle(ctx, settled)
	return archive.PurgeResult{}
}

// ReconcileTabs runs the keep-state reconcile's fn over chatID's record under mu and the record lock, with the open
// tabs read under the same mu, so no tab open or close interleaves with the reconcile's tab and run-tab adds.
func (m *Membership) ReconcileTabs(ctx context.Context, chatID marotte.ChatID, fn func(r *chat.KeepRecord, open []marotte.TabSubject)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var open []marotte.TabSubject
	if m.tabs != nil {
		open, _ = m.tabs.List()
	}
	return m.chats.ReconcileKeep(ctx, chatID, func(r *chat.KeepRecord) { fn(r, open) })
}

// SetLaunchChats grows run tab tabID's holders and resolves it (tabs.Store.SetLaunchChats), under mu like every tab
// write. It emits nothing: the client renders neither field.
func (m *Membership) SetLaunchChats(ctx context.Context, tabID string, chats []string, resolved bool) error {
	if m.tabs == nil {
		return errTabsUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tabs.SetLaunchChats(ctx, tabID, chats, resolved)
}

// settleOwedEntry releases e's facts by SettleOwed's rule and reports whether e is settled. Caller holds mu, which
// every tab and run-tab hold also holds, so the Since read and the release see the same fact.
func (m *Membership) settleOwedEntry(ctx context.Context, e *tabs.OwedRelease) bool {
	open, _ := m.tabs.List()
	if indexOfSubject(open, e.Kind, e.Ref) >= 0 {
		return true
	}
	ok := true
	for _, rel := range owedFacts(e) {
		c, found, err := m.chats.Lookup(ctx, rel.chat)
		if err != nil {
			slog.Warn("owed keep-fact release not retried: the record could not be read",
				"chat_id", rel.chat, "key", rel.key, keyError, err)
			ok = false
			continue
		}
		if !found {
			continue
		}
		if f, held := c.Keep[rel.key]; !held || f.Since > e.ClosedAt {
			continue
		}
		if err := m.chats.Release(ctx, rel.chat, rel.key, chat.EvidenceTabClosed); err != nil {
			slog.Warn("keep fact not released after its tab closed; retried every purge pass",
				"chat_id", rel.chat, "key", rel.key, keyError, err)
			ok = false
		}
	}
	return ok
}

// tabOpen reports whether the set holds id. Caller holds mu.
func (m *Membership) tabOpen(id string) bool {
	open, _ := m.tabs.List()
	return slices.ContainsFunc(open, func(t marotte.TabSubject) bool { return t.ID == id })
}

// Caller holds mu.
func (m *Membership) runTabOpen(workflowID string) bool {
	if m.tabs == nil {
		return false
	}
	open, _ := m.tabs.List()
	return slices.ContainsFunc(open, func(t marotte.TabSubject) bool {
		return t.Kind == marotte.TabKindRun && t.Ref == workflowID
	})
}

// sessionUnclaimed reports whether no chat's session chain holds sessionID, true
// only when every chat was read. An empty id names no session. Caller holds mu.
func (m *Membership) sessionUnclaimed(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return true
	}
	if m.sessions == nil {
		return false
	}
	scan := m.sessions.SessionOwners(ctx)
	return len(scan.Owners.Of(sessionID)) == 0 && scan.Complete
}

// Caller holds mu.
func (m *Membership) sessionPurging(sessionID string) bool {
	return m.purgingSessions[sessionID] > 0
}

// openTab opens and, when something was committed, emits. Caller holds mu.
func (m *Membership) openTab(ctx context.Context, spec marotte.OpenTab, opID string) (TabOpened, error) {
	recent := m.tabs.Recent()
	subject, created, version, err := m.tabs.Open(ctx, spec)
	if err != nil {
		return TabOpened{}, tabStatus(err)
	}
	if created {
		slog.Info("tab opened", "kind", spec.Kind, "ref", spec.Ref)
		m.emit(ctx, &marotte.TabsChangedPayload{
			Changed: &subject,
			Order:   m.order(),
			Recent:  m.recentChange(recent),
			Version: version,
			OpID:    opID,
		})
	}
	return TabOpened{Subject: subject, Version: version, Created: created}, nil
}

// closeTabsFor closes every tab showing chatID. A close that fails after the record is gone is retried once, and if
// that fails too the removal is emitted ANYWAY, stamped one past the current version: the chat being gone is worse
// to leave unstated than the next real mutation being read as a duplicate. Each closed tab's descendant chats close
// as closing that tab would: their holds in held are committed and a close job returned for each, which the caller
// enqueues in the same section; a hold no close committed is released uncommitted. chatID's Recently closed entry
// is forgotten first and chatID is never listed; the cascade's chats are listed only with retention on. Caller holds mu.
func (m *Membership) closeTabsFor(ctx context.Context, chatID marotte.ChatID, opID string, held map[marotte.ChatID]ChatHold) []teardownJob {
	defer releaseAll(held)
	if m.tabs == nil {
		return nil
	}
	var jobs []teardownJob
	m.forgetRecent(ctx, chatID, opID)
	open, _ := m.tabs.List()
	removing := tabsForChat(open, chatID)
	// Retention off keeps no closed chat to list again, so the cascade then lists none of the chats it closes.
	listUnder := m.retention == nil || m.retention(ctx)
	for _, doomed := range removing {
		under := m.subtreeChats(doomed.ID)
		unlisted := []string{string(chatID)}
		if !listUnder {
			unlisted = append(unlisted, chatRefs(under)...)
		}
		recent := m.tabs.Recent()
		closed, owed, version, ok := m.closeRemovedTab(ctx, chatID, doomed.ID, opID, unlisted, m.recentFacts(ctx, under))
		if !ok {
			continue
		}
		if len(closed) == 0 {
			continue // already gone; another close won the race
		}
		for _, child := range under {
			if h, ok := held[child]; ok {
				h.Commit()
				delete(held, child)
				jobs = append(jobs, teardownJob{chatID: child, hold: h, grade: gradeClose})
			}
		}
		m.emit(ctx, &marotte.TabsChangedPayload{
			RemovedIDs: subjectIDs(closed),
			Order:      m.order(),
			Recent:     m.recentChange(recent),
			Version:    version,
			OpID:       opID,
		})
		m.noticeClosed(closed)
		m.releaseForClosed(ctx, owed, chatID, false)
	}
	return jobs
}

// closeRemovedTab closes tabID for closeTabsFor, retrying once; when both tries fail it emits the removal anyway
// and reports false. Caller holds mu.
func (m *Membership) closeRemovedTab(ctx context.Context, chatID marotte.ChatID, tabID, opID string, unlisted []string,
	facts map[string]tabs.RecentFacts,
) (closed []marotte.TabSubject, owed []tabs.OwedRelease, version uint64, ok bool) {
	closed, owed, version, err := m.tabs.Close(ctx, tabID, unlisted, facts)
	if err != nil {
		slog.Warn("tab close failed after its chat was removed, retrying",
			"chat_id", chatID, "tab", tabID, keyError, err)
		closed, owed, version, err = m.tabs.Close(ctx, tabID, unlisted, facts)
	}
	if err != nil {
		slog.Error("tab close still failing after its chat was removed; announcing the removal anyway",
			"chat_id", chatID, "tab", tabID, keyError, err)
		m.emit(ctx, &marotte.TabsChangedPayload{
			RemovedIDs: []string{tabID},
			Version:    m.version() + 1,
			OpID:       opID,
		})
		return nil, nil, 0, false
	}
	return closed, owed, version, true
}

// noticeClosed tells the notice ledger each closed subject no open tab still shows. Caller holds mu.
func (m *Membership) noticeClosed(closed []marotte.TabSubject) {
	if m.notices == nil {
		return
	}
	open, _ := m.tabs.List()
	for _, t := range closed {
		if indexOfSubject(open, t.Kind, t.Ref) < 0 {
			m.notices.TabClosed(t.Kind, t.Ref)
		}
	}
}

// holdDescendants holds every chat whose tab sits under a tab showing chatID, chatID itself excluded, once per
// chat. Caller holds mu, and passes the holds to closeTabsFor in the same section.
func (m *Membership) holdDescendants(chatID marotte.ChatID) map[marotte.ChatID]ChatHold {
	if m.tabs == nil {
		return nil
	}
	var under []marotte.ChatID
	open, _ := m.tabs.List()
	for _, t := range tabsForChat(open, chatID) {
		for _, child := range m.subtreeChats(t.ID) {
			if child != chatID {
				under = append(under, child)
			}
		}
	}
	return m.holdAll(under)
}

// reserveSlot refuses when opening a tab for (kind, ref) would have to mint one
// and the set is already at MaxOpenTabs. A subject already open needs no slot,
// which lets a retry finish its own tab write at the limit.
//
// Caller holds mu.
func (m *Membership) reserveSlot(kind marotte.TabKind, ref string) error {
	if m.tabs == nil {
		return nil
	}
	open, _ := m.tabs.List()
	for _, t := range open {
		if t.Kind == kind && t.Ref == ref {
			return nil
		}
	}
	if len(open) >= tabs.MaxOpenTabs {
		return StatusErrorReason(http.StatusConflict, reasonTabsFull, errTabsFull)
	}
	return nil
}

// priorChat returns the chat this create already resolves to, without
// minting: the envelope's id when supplied, else whatever the ledger
// already holds for the op.
func (m *Membership) priorChat(req *ChatCreate) (id marotte.ChatID, replay bool) {
	if req.ChatID != "" {
		return req.ChatID, false
	}
	return m.ops.peek(req.OpID)
}

// tabForChat returns the id of the tab showing chatID, or "" when none is
// open. Caller holds mu.
func (m *Membership) tabForChat(chatID marotte.ChatID) string {
	if chatID == "" {
		return ""
	}
	open, _ := m.tabs.List()
	if found := tabsForChat(open, chatID); len(found) > 0 {
		return found[0].ID
	}
	return ""
}

// order is the expanded order every event carries: every open tab
// including children. Caller holds mu.
func (m *Membership) order() []string {
	open, _ := m.tabs.List()
	return subjectIDs(open)
}

// version is the collection version as the store currently holds it.
// Caller holds mu.
func (m *Membership) version() uint64 {
	_, v := m.tabs.List()
	return v
}

// subject reads one tab back after a mutation that returned only a
// version. Caller holds mu.
func (m *Membership) subject(id string) (marotte.TabSubject, bool) {
	open, _ := m.tabs.List()
	if i := indexOfTab(open, id); i >= 0 {
		return open[i], true
	}
	return marotte.TabSubject{}, false
}

// Workspace-global: the arrangement is not per chat.
func (m *Membership) emit(ctx context.Context, p *marotte.TabsChangedPayload) {
	if m.bus == nil {
		return
	}
	m.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventTabsChanged, "", *p))
}

func tabStatus(err error) error {
	switch {
	case errors.Is(err, tabs.ErrTooMany):
		return StatusErrorReason(http.StatusConflict, reasonTabsFull, errTabsFull)
	case errors.Is(err, tabs.ErrOrderMismatch):
		// 409 rather than 400: the caller's view of the set is not the
		// server's, a conflict to re-list from rather than a malformed body.
		return StatusError(http.StatusConflict, err)
	case errors.Is(err, tabs.ErrBadKind), errors.Is(err, tabs.ErrBadRef):
		return StatusError(http.StatusBadRequest, err)
	}
	return StatusError(http.StatusInternalServerError, err)
}

func tabsForChat(open []marotte.TabSubject, chatID marotte.ChatID) []marotte.TabSubject {
	var out []marotte.TabSubject
	for _, t := range open {
		if t.Kind == marotte.TabKindChat && t.Ref == string(chatID) {
			out = append(out, t)
		}
	}
	return out
}

func subjectIDs(subjects []marotte.TabSubject) []string {
	out := make([]string, 0, len(subjects))
	for _, t := range subjects {
		out = append(out, t.ID)
	}
	return out
}

func indexOfTab(open []marotte.TabSubject, id string) int {
	return slices.IndexFunc(open, func(t marotte.TabSubject) bool { return t.ID == id })
}

func indexOfSubject(open []marotte.TabSubject, kind marotte.TabKind, ref string) int {
	return slices.IndexFunc(open, func(t marotte.TabSubject) bool { return t.Kind == kind && t.Ref == ref })
}
