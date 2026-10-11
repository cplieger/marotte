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
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// The coordinator's own refusals.
var (
	errTabsFull = errors.New("too many tabs are open. Close a tab first")
	// errTabsUnavailable is the 503 for a build with no tab store wired; every
	// HTTP door adds the status.
	errTabsUnavailable = errors.New("the tab store is unavailable")
	// errOpenChatUnknown is the 404 for an open_tab, or a fresh create, naming a chat
	// that is gone — the delete-ordering gate's refusal.
	errOpenChatUnknown = errors.New("that chat no longer exists")
	// errRunPurging is the 409 for an open_tab naming a run the retention purge is
	// deleting, and for a create that would bind the session that run belongs to.
	errRunPurging = errors.New("that workflow run is being deleted")
	// errTabUnknown is the 404 for a pin or a reparent naming an id the set does
	// not hold.
	errTabUnknown = errors.New("that tab is not open")
	// errParentNotChat is the 409 for a reparent whose parent is not an open chat
	// tab: only a chat can hold sub-tabs.
	errParentNotChat = errors.New("the parent must be an open chat tab")
)

// TabSet is the open-tab set as this package uses it, declared at the consumer
// since internal/tabs exports no interface of its own. List is here beyond the
// mutations because the capacity reservation needs the count and every event
// needs the expanded order, which no mutation returns; Subtree is what a Close
// of an id will remove, asked before that close commits.
type TabSet interface {
	Open(ctx context.Context, spec marotte.OpenTab) (subject marotte.TabSubject, created bool, version uint64, err error)
	Close(ctx context.Context, id string) ([]marotte.TabSubject, uint64, error)
	Reorder(ctx context.Context, ids []string) (uint64, error)
	SetPinned(ctx context.Context, id string, pinned bool) (uint64, error)
	Reparent(ctx context.Context, id, parent string) (uint64, error)
	List() ([]marotte.TabSubject, uint64)
	Subtree(id string) []marotte.TabSubject
}

// runOwner is the run surface as the coordinator uses it: which chat's agent
// launched a run. Declared here, at the consumer; *agent.Runs satisfies it.
//
// `ok` is false for a run with no lease — one this server never put on the
// wire, or one whose lease was released when it ended — so a finished run's
// parent is unknown here and History supplies it instead.
type runOwner interface {
	RunChat(workflowID string) (chatID marotte.ChatID, ok bool)
}

// chatCloser is the tab-close teardown for a chat tab: cancel the turn, cancel
// the chat's runs, tear the bridge down, keep the record. Bound in
// RegisterDefaults.
type chatCloser func(ctx context.Context, chatID marotte.ChatID)

// chatDeleter is the delete grade of the same teardown, for a chat the close
// escalation has already erased; the captured session chain travels in.
type chatDeleter func(ctx context.Context, chatID marotte.ChatID, sessionChain []string)

// A nil read means retention on — the fail-toward-keeping direction.
type retentionRead func(ctx context.Context) bool

// closeTeardownBudget bounds the close escalation's post-commit work, which runs
// detached from the request so a client walking away cannot cancel roll-forward.
const closeTeardownBudget = time.Minute

// Membership owns every operation that spans the chat store and the tab set.
//
// Safe for concurrent use; the zero value is not usable, construct with
// newMembership. A nil TabSet means no tab store was wired: the chat half
// of every operation still runs and the tab half reports
// errTabsUnavailable.
type Membership struct {
	chats      chatStore
	tabs       TabSet
	bus        broadcaster
	teardown   chatTeardown
	closeChat  chatCloser
	deleteChat chatDeleter
	retention  retentionRead
	// runs resolves a run's launching chat when a client sends no parent. May
	// be nil, in which case no parent is filled and a run tab opens top level.
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
	// returned; OpenTab refuses them. purgingSessions counts those runs' parent
	// sessions; CreateChatAndOpen refuses to bind one. Both guarded by mu.
	purgingRuns     map[string]struct{}
	purgingSessions map[string]int
	// mu is THE operation lock, held across the reservation, the mint, both
	// durable writes and the event. Order: mu -> chat record lock -> tabs writeMu.
	mu sync.Mutex
}

// membershipDeps is Membership's constructor argument. Every field is required
// except Tabs, DeleteChat, Retention and SupervisedDefault, which default to
// the safe direction.
type membershipDeps struct {
	Chats      chatStore
	Tabs       TabSet
	Bus        broadcaster
	Teardown   chatTeardown
	CloseChat  chatCloser
	DeleteChat chatDeleter
	Retention  retentionRead
	// SupervisedDefault answers the workspace-wide Supervised default a new record is seeded from.
	// A function so the coordinator keeps no filesystem knowledge and cannot disagree with the
	// prompt path's reader; nil is false.
	SupervisedDefault func(context.Context) bool
	Runs              runOwner
	// Sessions answers the run purge's claim check. Nil spares every run with a
	// parent session (the fail-toward-keeping direction).
	Sessions sessionClaims
}

// newMembership builds the coordinator.
func newMembership(deps *membershipDeps) *Membership {
	return &Membership{
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

// CreateChatAndOpen writes a chat record and opens its tab as one operation, so the final slot
// cannot be consumed between mint and open and no delete lands between the two writes. Returns
// errOpenChatUnknown (404) when a required chat is absent, errTabsFull (409) at MaxOpenTabs, and
// errChatNotCreated (409) when the record is absent after a Mutate that reported no error. A failed
// tab write leaves the chat created; a retry with the same op_id finishes it.
func (m *Membership) CreateChatAndOpen(ctx context.Context, req ChatCreate) (ChatOpened, error) {
	// Hoisted above every lock because the read is file I/O; resolved unconditionally (it is
	// cached) so no conditional resolve has to run inside the lock.
	supervisedDefault := m.supervisedDefaultValue(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()

	prior, replay := m.priorChat(req)
	if req.RequireChat != "" && !replay {
		if _, ok := m.chats.Get(ctx, req.RequireChat); !ok {
			return ChatOpened{}, StatusError(http.StatusNotFound, errOpenChatUnknown)
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
	if err != nil {
		return ChatOpened{}, StatusError(http.StatusInternalServerError, err)
	}
	if purging {
		return ChatOpened{}, StatusError(http.StatusConflict, errRunPurging)
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
	opened, err := m.openTab(ctx, marotte.OpenTab{
		Kind:   marotte.TabKindChat,
		Ref:    string(chatID),
		Parent: m.tabForChat(req.ParentChat),
		Owns:   true,
	}, req.OpID)
	if err != nil {
		return ChatOpened{}, err
	}
	return ChatOpened{Chat: c, Subject: opened.Subject, Version: opened.Version, Replay: replay}, nil
}

// resolvedChat reports the chat an op_id has already created, without minting
// one. Exists for fork_chat: a fork's record cannot be built until KAS answers
// session/fork, and that round trip must not happen under the operation lock
// (a bridge Call has no client-side timeout).
func (m *Membership) resolvedChat(opID string) (marotte.ChatID, bool) {
	return m.ops.peek(opID)
}

// OpenTab opens a tab for something that already exists; it never mints a chat.
// For a chat tab it gates on the record existing — the other half of the delete
// ordering — with the check and the open in one critical section.
func (m *Membership) OpenTab(ctx context.Context, spec marotte.OpenTab, opID string) (TabOpened, error) {
	if m.tabs == nil {
		return TabOpened{}, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if spec.Kind == marotte.TabKindChat {
		if _, ok := m.chats.Get(ctx, marotte.ChatID(spec.Ref)); !ok {
			return TabOpened{}, StatusError(http.StatusNotFound, errOpenChatUnknown)
		}
	}
	if _, purging := m.purgingRuns[spec.Ref]; purging && spec.Kind == marotte.TabKindRun {
		return TabOpened{}, StatusError(http.StatusConflict, errRunPurging)
	}
	spec.Parent = m.fillRunParent(spec)
	return m.openTab(ctx, spec, opID)
}

// fillRunParent answers a run tab's Parent, resolving the launching chat's tab
// when the caller supplied none — one rule for every door, so no door has to
// hold the run's launch history. A CLIENT-supplied parent is never overwritten:
// History knows the parent of a finished run whose lease is gone, and Parent is
// immutable after open, so this is the only chance to get it right.
//
// Caller holds mu.
func (m *Membership) fillRunParent(spec marotte.OpenTab) string {
	if spec.Kind != marotte.TabKindRun || spec.Parent != "" || m.runs == nil {
		return spec.Parent
	}
	chatID, ok := m.runs.RunChat(spec.Ref)
	if !ok || chatID == "" {
		// A parentless run (manual, scheduled), or one this server never
		// launched: top level.
		return ""
	}
	return m.tabForChat(chatID)
}

// closeTab closes a tab and its descendants, then tears down what an owned tab
// showed — and, with retention off, deletes each chat the close left tabless. An id
// that is not open closes nothing and is not an error: two devices can close one.
//
// Escalation, ordered, under the operation lock: decide the doomed set; then
// tabs.Close, the COMMIT POINT; then chats.Delete each doomed record, which answers
// the session chain the teardown runs from. Past the commit there is no rollback,
// only roll-forward on a detached context.
func (m *Membership) closeTab(ctx context.Context, id, opID string) (closed []marotte.TabSubject, version uint64, err error) {
	if m.tabs == nil {
		return nil, 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	doomed := m.doomedChats(ctx, id)
	closed, version, err = m.tabs.Close(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, 0, StatusError(http.StatusInternalServerError, err)
	}
	if len(closed) == 0 {
		m.mu.Unlock()
		return closed, version, nil
	}
	// Committed. Everything from here rolls forward under its own bound;
	// the request context governed the operation only up to this point.
	rollCtx, done := context.WithTimeout(context.WithoutCancel(ctx), closeTeardownBudget)
	defer done()
	m.emit(rollCtx, &marotte.TabsChangedPayload{
		RemovedIDs: subjectIDs(closed),
		Order:      m.order(),
		Version:    version,
		OpID:       opID,
	})
	deleted := m.deleteDoomedRecords(rollCtx, doomed)
	m.mu.Unlock()

	// After the lock: the teardown's session/cancel has no client-side timeout, so one wedged
	// process would block every tab mutation. One grade per chat: delete grade for a record this
	// close took (from the chain its delete answered), close grade for every other chat tab, including a
	// doomed chat whose delete failed.
	if m.tearDownClosed(rollCtx, closed, deleted) {
		// The closed tab may have been what held an expired chat or finished run outside the age
		// test, so wake the purge now rather than after an idle back-off of up to an hour. After
		// the lock, because the wake reads a field it guards.
		m.wakeRetention()
	}
	return closed, version, nil
}

// tearDownClosed runs one teardown grade per chat the close took and reports whether
// a chat or run tab went, which is what owes retention a wake. Called after mu is
// released.
func (m *Membership) tearDownClosed(ctx context.Context, closed []marotte.TabSubject, deleted map[marotte.ChatID][]string) (retentionTabClosed bool) {
	dispatched := make(map[marotte.ChatID]bool, len(deleted))
	for _, t := range closed {
		if t.Kind == marotte.TabKindRun {
			retentionTabClosed = true
		}
		if t.Kind != marotte.TabKindChat {
			continue
		}
		retentionTabClosed = true
		chatID := marotte.ChatID(t.Ref)
		if chain, isDoomed := deleted[chatID]; isDoomed {
			if !dispatched[chatID] {
				dispatched[chatID] = true
				m.deleteChat(ctx, chatID, chain)
			}
			continue
		}
		if m.closeChat != nil {
			m.closeChat(ctx, chatID)
		}
	}
	return retentionTabClosed
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
	version, err := m.tabs.Reorder(ctx, ids)
	if err != nil {
		return 0, tabStatus(err)
	}
	// An order identical to the one already held commits nothing and must
	// emit nothing.
	if version != before {
		m.emit(ctx, &marotte.TabsChangedPayload{Order: m.order(), Version: version, OpID: opID})
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
	version, err := m.tabs.SetPinned(ctx, id, pinned)
	if err != nil {
		return 0, StatusError(http.StatusInternalServerError, err)
	}
	if version != before {
		if changed, ok := m.subject(id); ok {
			m.emit(ctx, &marotte.TabsChangedPayload{Changed: &changed, Version: version, OpID: opID})
		}
	}
	return version, nil
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
		m.emit(ctx, &marotte.TabsChangedPayload{Changed: &changed, Order: m.order(), Version: version, OpID: opID})
	}
	return changed, version, nil
}

// DeleteChatAndCloseTabs is the delete path: remove the record and close its tabs in one
// critical section, then tear the chat's work down from the session chain the removed
// record held. The record is the admission barrier: once it is gone every later open is
// refused and a bridge opening on it settles ErrChatGone, and the teardown after it closes
// any bridge that opened before. A failed delete tears nothing down, so the chat keeps
// working. The teardown runs after the lock: its KAS calls have no client-side timeout,
// so one wedged process would block every tab mutation.
func (m *Membership) DeleteChatAndCloseTabs(ctx context.Context, chatID marotte.ChatID, opID string) error {
	chain, err := m.deleteRecordAndCloseTabs(ctx, chatID, opID)
	if err != nil {
		return StatusError(http.StatusInternalServerError, err)
	}
	m.teardown.DeleteChatStateByChain(ctx, chatID, chain, RunStopChatDeleted)
	return nil
}

func (m *Membership) deleteRecordAndCloseTabs(ctx context.Context, chatID marotte.ChatID, opID string) (sessionChain []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionChain, err = m.chats.Delete(ctx, chatID)
	if err != nil {
		return nil, err
	}
	slog.Info("chat deleted", "chat_id", chatID)
	m.closeTabsFor(ctx, chatID, opID)
	return sessionChain, nil
}

// RetentionClose tears down the work of a chat the retention purge already removed, then closes its
// tabs. The teardown runs outside the operation lock, as in DeleteChatAndCloseTabs. Tabs to close
// here mean one was opened between HasOpenTab and the remove.
func (m *Membership) RetentionClose(ctx context.Context, chatID marotte.ChatID, sessionChain []string) {
	m.teardown.DeleteChatStateByChain(ctx, chatID, sessionChain, RunStopRetention)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeTabsFor(ctx, chatID, "")
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

// HasOpenTab reports whether any tab shows this chat, retention's second predicate. Takes no
// operation lock: it is a read, and the lock orders writes against their events.
func (m *Membership) HasOpenTab(chatID marotte.ChatID) bool {
	if m.tabs == nil {
		return false
	}
	open, _ := m.tabs.List()
	return len(tabsForChat(open, chatID)) > 0
}

// AdmitRunPurge answers whether the run purge may delete workflowID, launched from parentSessionID:
// false while a tab shows the run, while any chat's chain holds that session, or when the chats
// cannot all be read. On true both are marked purging until the caller runs done, so OpenTab and
// CreateChatAndOpen refuse them. Checks and marks share mu, so nothing commits between the decision
// and the delete.
func (m *Membership) AdmitRunPurge(ctx context.Context, workflowID, parentSessionID string) (done func(), ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runTabOpen(workflowID) || !m.sessionUnclaimed(ctx, parentSessionID) {
		return nil, false
	}
	m.purgingRuns[workflowID] = struct{}{}
	if parentSessionID != "" {
		m.purgingSessions[parentSessionID]++
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.purgingRuns, workflowID)
		if parentSessionID == "" {
			return
		}
		m.purgingSessions[parentSessionID]--
		if m.purgingSessions[parentSessionID] <= 0 {
			delete(m.purgingSessions, parentSessionID)
		}
	}, true
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
	claimed, complete := m.sessions.SessionClaimed(ctx, sessionID)
	return !claimed && complete
}

// Caller holds mu.
func (m *Membership) sessionPurging(sessionID string) bool {
	return m.purgingSessions[sessionID] > 0
}

// openTab opens and, when something was committed, emits. Caller holds mu.
func (m *Membership) openTab(ctx context.Context, spec marotte.OpenTab, opID string) (TabOpened, error) {
	subject, created, version, err := m.tabs.Open(ctx, spec)
	if err != nil {
		return TabOpened{}, tabStatus(err)
	}
	if created {
		m.emit(ctx, &marotte.TabsChangedPayload{
			Changed: &subject,
			Order:   m.order(),
			Version: version,
			OpID:    opID,
		})
	}
	return TabOpened{Subject: subject, Version: version, Created: created}, nil
}

// closeTabsFor closes every tab showing chatID. A close that fails after the record is
// already gone is retried once, and if that fails too the removal is emitted ANYWAY: the
// chat being gone is worse to leave unstated than a tab set this process failed to write.
// That frame is stamped one past the current version, so it costs the next real mutation
// being read as a duplicate.
//
// Caller holds mu.
func (m *Membership) closeTabsFor(ctx context.Context, chatID marotte.ChatID, opID string) {
	if m.tabs == nil {
		return
	}
	open, _ := m.tabs.List()
	for _, doomed := range tabsForChat(open, chatID) {
		closed, version, err := m.tabs.Close(ctx, doomed.ID)
		if err != nil {
			slog.Warn("tab close failed after its chat was removed, retrying",
				"chat_id", chatID, "tab", doomed.ID, keyError, err)
			closed, version, err = m.tabs.Close(ctx, doomed.ID)
		}
		if err != nil {
			slog.Error("tab close still failing after its chat was removed; announcing the removal anyway",
				"chat_id", chatID, "tab", doomed.ID, keyError, err)
			m.emit(ctx, &marotte.TabsChangedPayload{
				RemovedIDs: []string{doomed.ID},
				Version:    m.version() + 1,
				OpID:       opID,
			})
			continue
		}
		if len(closed) == 0 {
			continue // already gone; another close won the race
		}
		m.emit(ctx, &marotte.TabsChangedPayload{
			RemovedIDs: subjectIDs(closed),
			Order:      m.order(),
			Version:    version,
			OpID:       opID,
		})
	}
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
		return StatusError(http.StatusConflict, errTabsFull)
	}
	return nil
}

// priorChat returns the chat this create already resolves to, without
// minting: the envelope's id when supplied, else whatever the ledger
// already holds for the op.
func (m *Membership) priorChat(req ChatCreate) (id marotte.ChatID, replay bool) {
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
		return StatusError(http.StatusConflict, errTabsFull)
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
