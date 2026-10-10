package command

// Driven over a REAL tabs.Store: a fake would let both halves agree while being wrong.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// flakyTabs embeds the real store rather than reimplementing one, so every case
// that is not about the failure exercises production behaviour. Its two knobs are
// what no real store offers: a TRANSIENT persist failure, and an observation point
// inside the coordinator's critical section.
type flakyTabs struct {
	*tabs.Store
	// beforeClose observes the state between the record removal and the tab close.
	beforeClose func()
	mu          sync.Mutex
	// failCloses counts down, so 1 is the transient failure the retry absorbs.
	failCloses int
	// failOpens reaches the state a create retry repairs: chat written, tab not.
	failOpens int
}

var errCloseFailed = errors.New("simulated tabs.json write failure")

func (f *flakyTabs) Close(ctx context.Context, id string) ([]marotte.TabSubject, uint64, error) {
	f.mu.Lock()
	hook, fail := f.beforeClose, f.failCloses > 0
	if fail {
		f.failCloses--
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, 0, errCloseFailed
	}
	return f.Store.Close(ctx, id)
}

func newFlakyMembership(t *testing.T, chats chatStore) (*Membership, *flakyTabs, *tabBus) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	flaky := &flakyTabs{Store: st}
	bus := &tabBus{}
	teardown := &recordingTeardown{}
	return newMembership(&membershipDeps{Chats: chats, Tabs: flaky, Bus: bus, Teardown: teardown}), flaky, bus
}

// recordingTeardown is the delete path's teardown seam: the escalation cases read
// back which grade ran and what chain travelled.
type recordingTeardown struct {
	// onByChain runs AT teardown time, so a test can prove the teardown ran before the tab close,
	// outside the operation lock.
	onByChain      func()
	deletedByChain map[marotte.ChatID][]string
	causes         map[marotte.ChatID]RunStopCause
	closed         []marotte.ChatID
	mu             sync.Mutex
}

func (r *recordingTeardown) DeleteChatStateByChain(_ context.Context, id marotte.ChatID, chain []string, cause RunStopCause) {
	if r.onByChain != nil {
		r.onByChain()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletedByChain == nil {
		r.deletedByChain = make(map[marotte.ChatID][]string)
	}
	r.deletedByChain[id] = slices.Clone(chain)
	if r.causes == nil {
		r.causes = make(map[marotte.ChatID]RunStopCause)
	}
	r.causes[id] = cause
}

func (r *recordingTeardown) CloseChatState(_ context.Context, id marotte.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, id)
}

func (*recordingTeardown) BeginChatTeardown(marotte.ChatID, bool) {}

func createChat(t *testing.T, mem *Membership, opID string) ChatOpened {
	t.Helper()
	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: opID,
		Init: func(c *marotte.Chat) { c.Name = marotte.DefaultChatName },
	})
	if err != nil {
		t.Fatalf("CreateChatAndOpen(op %q) = %v, want it to succeed", opID, err)
	}
	return opened
}

func tabIDsFor(st *tabs.Store, chatID marotte.ChatID) []string {
	open, _ := st.List()
	var out []string
	for _, tab := range open {
		if tab.Kind == marotte.TabKindChat && tab.Ref == string(chatID) {
			out = append(out, tab.ID)
		}
	}
	return out
}

// The create half of the gate: both stores hold the new chat afterwards.
func TestCreateChatAndOpen_WritesTheChatThenItsTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)

	opened := createChat(t, mem, "op-1")

	if _, ok := store.Get(t.Context(), marotte.ChatID(opened.Chat.ID)); !ok {
		t.Fatalf("the created chat %q is not in the chat store", opened.Chat.ID)
	}
	if got := tabIDsFor(st, marotte.ChatID(opened.Chat.ID)); len(got) != 1 || got[0] != opened.Subject.ID {
		t.Errorf("tabs for the new chat = %v, want exactly the returned subject %q", got, opened.Subject.ID)
	}
	if opened.Version != 1 {
		t.Errorf("version = %d, want 1: the first mutation of an empty collection", opened.Version)
	}
	frames := bus.frames(t)
	if len(frames) != 1 {
		t.Fatalf("bus saw %d tabs_changed frames, want exactly 1 for one committed mutation", len(frames))
	}
	if frames[0].Changed == nil || frames[0].Changed.ID != opened.Subject.ID {
		t.Errorf("frame's changed tab = %+v, want the subject %q", frames[0].Changed, opened.Subject.ID)
	}
	if frames[0].OpID != "op-1" {
		t.Errorf("frame carries op_id %q, want the op the caller sent", frames[0].OpID)
	}
	if !slices.Equal(frames[0].Order, []string{opened.Subject.ID}) {
		t.Errorf("frame's order = %v, want the one open tab", frames[0].Order)
	}
}

// A retry of an op whose FIRST attempt created the chat and then failed its tab
// write must finish that write, not answer with a chat that has no tab. The
// failure is injected because that is the only way to reach the state, which a
// real deployment reaches on a full disk.
func TestCreateChatAndOpen_ReplayFinishesAMissingTabWrite(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	flaky.openFails(1)

	_, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-retry", Init: func(c *marotte.Chat) { c.Name = "Half made" },
	})
	if err == nil {
		t.Fatal("the first attempt reported success, so the fixture did not inject a failure")
	}
	ids := storedChatIDs(t, store)
	if len(ids) != 1 {
		t.Fatalf("after a failed tab write the store holds %d chats (%v), want 1: the record leads and is kept", len(ids), ids)
	}
	if got := tabIDsFor(flaky.Store, ids[0]); len(got) != 0 {
		t.Fatalf("the first attempt left tabs %v, so there is no missing write to finish", got)
	}

	opened, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-retry", Init: func(c *marotte.Chat) { c.Name = "Half made" },
	})
	if err != nil {
		t.Fatalf("the retry = %v, want it to succeed", err)
	}

	if marotte.ChatID(opened.Chat.ID) != ids[0] {
		t.Errorf("the retry answered with chat %q, want the first attempt's %q", opened.Chat.ID, ids[0])
	}
	if !opened.Replay {
		t.Error("the retry did not report a replay, so the ledger did not resolve it")
	}
	if got := tabIDsFor(flaky.Store, ids[0]); len(got) != 1 {
		t.Errorf("tabs for the replayed chat = %v, want exactly 1: the replay owes the missing tab write", got)
	}
	if opened.Subject.ID == "" {
		t.Error("the replay answered with no subject, so the caller has nothing to show")
	}
	if got := storedChatIDs(t, store); len(got) != 1 {
		t.Errorf("store holds %d chats (%v), want 1: one gesture is one chat", len(got), got)
	}
	if frames := bus.frames(t); len(frames) != 1 {
		t.Errorf("bus saw %d frames, want 1: only the replay committed a mutation", len(frames))
	}
}

func TestCreateChatAndOpen_RefusesWhenTheRequiredChatIsGone(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedRecord(t, store, "c-required")
	mem, st, bus := newTabbedMembership(t, store)
	if _, err := store.Delete(t.Context(), "c-required"); err != nil {
		t.Fatalf("Delete(%q) = %v, want nil", "c-required", err)
	}

	_, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		ChatID:      "c-tangent",
		RequireChat: "c-required",
		Init:        func(c *marotte.Chat) { c.Name = "Must not exist" },
	})

	if statusOf(err) != http.StatusNotFound {
		t.Errorf("CreateChatAndOpen status = %d, want 404 (error %v)", statusOf(err), err)
	}
	if !errors.Is(err, errOpenChatUnknown) {
		t.Errorf("CreateChatAndOpen error = %v, want errOpenChatUnknown", err)
	}
	if got := storedChatIDs(t, store); len(got) != 0 {
		t.Errorf("CreateChatAndOpen left chat records %v, want none", got)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("CreateChatAndOpen left %d tabs, want none", len(open))
	}
	if frames := bus.frames(t); len(frames) != 0 {
		t.Errorf("CreateChatAndOpen emitted %d tab frames, want none", len(frames))
	}
}

// The capacity reservation runs before the mint. Reversed, the refusal lands after
// the record is written and the gesture leaves a chat nothing can ever open.
func TestCreateChatAndOpen_AtTheLimitLeavesTheChatStoreUnchanged(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := len(storedChatIDs(t, store))
	framesBefore := len(bus.frames(t))

	_, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID: "op-full", Init: func(c *marotte.Chat) { c.Name = "Never made" },
	})

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 at the limit (%s)", statusOf(err), errText(err))
	}
	if !errors.Is(err, errTabsFull) {
		t.Errorf("error = %v, want errTabsFull", err)
	}
	if got := storedChatIDs(t, store); len(got) != before {
		t.Errorf("store holds %d chats, want the %d it held before: a refused create must not leave an orphan", len(got), before)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
		t.Errorf("the set holds %d tabs, want %d unchanged", len(open), tabs.MaxOpenTabs)
	}
	if got := len(bus.frames(t)); got != framesBefore {
		t.Errorf("a refused create emitted %d new frames, want 0", got-framesBefore)
	}
}

// open_tab NEVER mints, so the chat store must be exactly as it was — the split
// this whole design rests on, so it is asserted rather than assumed.
func TestOpenTab_AtTheLimitIsRefusedAndTheChatStoreIsUntouched(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-seed")
	seedRecord(t, store, "c-waiting")
	fillTabs(t, mem, tabs.MaxOpenTabs)
	before := storedChatIDs(t, store)

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-waiting"}, "op-open")

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 at the limit (%s)", statusOf(err), errText(err))
	}
	if got := storedChatIDs(t, store); !slices.Equal(got, before) {
		t.Errorf("chat store went from %v to %v; open_tab must never write a chat", before, got)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs {
		t.Errorf("the set holds %d tabs, want %d: nothing was opened", len(open), tabs.MaxOpenTabs)
	}
	if got := tabIDsFor(st, marotte.ChatID(opened.Chat.ID)); len(got) != 1 {
		t.Errorf("the seeded chat's tab = %v, want it untouched", got)
	}
}

// Why `created` is on the response at all: a second open commits nothing, so it
// emits nothing, so a caller waiting for an event would wait forever.
func TestOpenTab_IsIdempotentAndSaysSo(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, bus := newTabbedMembership(t, store)
	seedRecord(t, store, "c-open")

	first, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-open"}, "op-a")
	if err != nil {
		t.Fatalf("first open = %v", err)
	}
	second, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-open"}, "op-b")
	if err != nil {
		t.Fatalf("second open = %v", err)
	}

	if !first.Created {
		t.Error("the first open reported created:false")
	}
	if second.Created {
		t.Error("the second open reported created:true; one (kind, ref) is one tab")
	}
	if second.Subject.ID != first.Subject.ID {
		t.Errorf("the second open returned tab %q, want the open one %q", second.Subject.ID, first.Subject.ID)
	}
	if second.Version != first.Version {
		t.Errorf("version moved from %d to %d; an idempotent open commits nothing", first.Version, second.Version)
	}
	if got := len(bus.frames(t)); got != 1 {
		t.Errorf("bus saw %d frames, want 1: the idempotent open emits nothing", got)
	}
}

// The delete ordering seen from the open side: the record is the gate, so this is
// a 404 rather than a tab pointing at nothing.
func TestOpenTab_ForAChatThatIsGoneIsRefused(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-never"}, "op-a")

	if statusOf(err) != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", statusOf(err), errText(err))
	}
	if !errors.Is(err, errOpenChatUnknown) {
		t.Errorf("error = %v, want errOpenChatUnknown", err)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the refused open left %d tabs behind, want none", len(open))
	}
}

// Why removed_ids is a LIST: a singular per-tab event would force either several
// frames sharing one version (the second reads as a duplicate) or several bumps
// for one mutation.
func TestCloseTab_AParentAndItsChildrenAreOneMutation(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	parent := createChat(t, mem, "op-parent")
	childA := openChild(t, mem, store, "c-child-a", parent.Subject.ID)
	childB := openChild(t, mem, store, "c-child-b", parent.Subject.ID)
	survivor := createChat(t, mem, "op-survivor")
	_, versionBefore := st.List()
	framesBefore := len(bus.frames(t))

	closed, version, err := mem.closeTab(t.Context(), parent.Subject.ID, "op-close")
	if err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	want := []string{parent.Subject.ID, childA.Subject.ID, childB.Subject.ID}
	got := subjectIDs(closed)
	slices.Sort(got)
	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)
	if !slices.Equal(got, wantSorted) {
		t.Errorf("closed = %v, want the parent and both children %v", got, wantSorted)
	}
	if version != versionBefore+1 {
		t.Errorf("version went %d -> %d, want exactly one bump for one mutation", versionBefore, version)
	}
	frames := bus.frames(t)
	if len(frames)-framesBefore != 1 {
		t.Fatalf("the close emitted %d frames, want exactly 1", len(frames)-framesBefore)
	}
	last := frames[len(frames)-1]
	removed := slices.Clone(last.RemovedIDs)
	slices.Sort(removed)
	if !slices.Equal(removed, wantSorted) {
		t.Errorf("removed_ids = %v, want every id that went %v", last.RemovedIDs, wantSorted)
	}
	if last.Version != version {
		t.Errorf("frame version = %d, want the version the mutation committed (%d)", last.Version, version)
	}
	if !slices.Equal(last.Order, []string{survivor.Subject.ID}) {
		t.Errorf("frame order = %v, want the one surviving tab %q", last.Order, survivor.Subject.ID)
	}
}

// Two devices can close the same tab, and an empty closed list already says
// nothing happened.
func TestCloseTab_AnIdThatIsNotOpenIsNotAnError(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	createChat(t, mem, "op-a")
	_, before := st.List()

	closed, version, err := mem.closeTab(t.Context(), "not-open", "op-close")
	if err != nil {
		t.Fatalf("closing an absent id = %v, want no error", err)
	}
	if len(closed) != 0 {
		t.Errorf("closed = %v, want nothing", subjectIDs(closed))
	}
	if version != before {
		t.Errorf("version went %d -> %d, want it unchanged", before, version)
	}
	if got := len(bus.frames(t)); got != 1 {
		t.Errorf("bus saw %d frames, want the 1 from the create only", got)
	}
}

// The × kills the work; the record survives, because a closed chat is a chat
// without a tab and reopening it loads everything back.
func TestCloseTab_AChatTabRunsTheTeardownAndKeepsTheRecord(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	var tornDown []marotte.ChatID
	mem := newMembership(&membershipDeps{
		Chats: store, Tabs: st, Bus: &tabBus{},
		CloseChat: func(_ context.Context, id marotte.ChatID) { tornDown = append(tornDown, id) },
	})
	opened := createChat(t, mem, "op-a")

	if _, _, err := mem.closeTab(t.Context(), opened.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	if !slices.Equal(tornDown, []marotte.ChatID{marotte.ChatID(opened.Chat.ID)}) {
		t.Errorf("teardown ran for %v, want exactly the closed chat %q", tornDown, opened.Chat.ID)
	}
	if _, ok := store.Get(t.Context(), marotte.ChatID(opened.Chat.ID)); !ok {
		t.Error("closing the tab deleted the chat record; only delete_chat may do that")
	}
}

// Closing a chat tab WAKES RETENTION, because the tab was the exemption. An exempt
// chat contributes no wake-up deadline, so a pass that saw only exempt chats backs
// off to an hour and a month-old chat can outlive its window by that much.
func TestCloseTab_AChatTabCloseWakesRetention(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{Chats: store, Tabs: st, Bus: &tabBus{}})
	wakes := 0
	mem.SetRetentionWake(func() { wakes++ })
	opened := createChat(t, mem, "op-a")

	if _, _, err := mem.closeTab(t.Context(), opened.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if wakes != 1 {
		t.Errorf("retention was woken %d times by a chat-tab close, want 1: the tab was "+
			"the exemption, and nothing else asks the purge to look again", wakes)
	}

	if _, _, err := mem.closeTab(t.Context(), "not-open", "op-close-2"); err != nil {
		t.Fatalf("CloseTab of an unopened id = %v", err)
	}
	if wakes != 1 {
		t.Errorf("retention was woken %d times, want 1: a close that committed nothing "+
			"cleared no exemption", wakes)
	}
}

// A run tab is the run purge's exemption the way a chat tab is the chat purge's, so
// closing one wakes retention too, and while it is open the run reads as shown.
func TestCloseTab_ARunTabCloseWakesRetention(t *testing.T) {
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{Chats: testsupport.NewInMemoryChatStore(), Tabs: st, Bus: &tabBus{}})
	wakes := 0
	mem.SetRetentionWake(func() { wakes++ })
	opened, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
	if err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}
	if _, ok := mem.AdmitRunPurge(t.Context(), "wf_1", ""); ok {
		t.Fatal("AdmitRunPurge(wf_1) = true with its tab open, want false")
	}

	if _, _, err := mem.closeTab(t.Context(), opened.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if wakes != 1 {
		t.Errorf("retention was woken %d times by a run-tab close, want 1", wakes)
	}
	done, ok := mem.AdmitRunPurge(t.Context(), "wf_1", "")
	if !ok {
		t.Fatal("AdmitRunPurge(wf_1) = false after its tab closed, want true")
	}
	done()
}

// An admitted purge holds the run until its delete returns: a run-tab open in that
// window is refused, and the same open succeeds once the purge reports done.
func TestOpenTab_ARunBeingPurgedIsRefusedUntilThePurgeIsDone(t *testing.T) {
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	mem := newMembership(&membershipDeps{Chats: testsupport.NewInMemoryChatStore(), Tabs: st, Bus: &tabBus{}})
	spec := marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}
	done, ok := mem.AdmitRunPurge(t.Context(), "wf_1", "")
	if !ok {
		t.Fatal("AdmitRunPurge(wf_1) = false with no tab open, want true")
	}

	_, err = mem.OpenTab(t.Context(), spec, "op-during")
	if !errors.Is(err, errRunPurging) {
		t.Errorf("OpenTab(run) during its purge = %v, want %v", err, errRunPurging)
	}
	if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_2"}, "op-other"); err != nil {
		t.Errorf("OpenTab(another run) during wf_1's purge = %v, want nil", err)
	}

	done()
	if _, err := mem.OpenTab(t.Context(), spec, "op-after"); err != nil {
		t.Errorf("OpenTab(run) after its purge reported done = %v, want nil", err)
	}
}

// resumeCreate is resume_session's create: a fresh chat bound to an existing session.
func resumeCreate(opID, sessionID string) ChatCreate {
	return ChatCreate{OpID: opID, Init: func(c *marotte.Chat) { c.RecordSession(sessionID) }}
}

type incompleteClaims struct{}

func (incompleteClaims) SessionClaimed(context.Context, string) (claimed, complete bool) {
	return false, false
}

func newClaimsMembership(t *testing.T, claims sessionClaims) *Membership {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	store := testsupport.NewInMemoryChatStore()
	if claims == nil {
		claims = store
	}
	return newMembership(&membershipDeps{Chats: store, Tabs: st, Bus: &tabBus{}, Sessions: claims})
}

// The purge's earlier claim snapshot cannot decide: a chat that adopted the run's
// launching session after it was taken still spares the run at admission.
func TestAdmitRunPurge_ARunWhoseSessionAChatClaimsIsSpared(t *testing.T) {
	mem := newClaimsMembership(t, nil)
	if _, err := mem.CreateChatAndOpen(t.Context(), resumeCreate("op-resume", "sess_run")); err != nil {
		t.Fatalf("Setup: CreateChatAndOpen(resume sess_run) = %v", err)
	}

	if _, ok := mem.AdmitRunPurge(t.Context(), "wf_1", "sess_run"); ok {
		t.Error("AdmitRunPurge(wf_1, sess_run) = true while a chat claims sess_run, want false")
	}
	done, ok := mem.AdmitRunPurge(t.Context(), "wf_2", "sess_other")
	if !ok {
		t.Fatal("AdmitRunPurge(wf_2, sess_other) = false with no chat claiming it, want true")
	}
	done()
}

// A claim that cannot be ruled out keeps the run: an unreadable chat file, or no
// claim reader wired at all.
func TestAdmitRunPurge_AnUnverifiableClaimSparesTheRun(t *testing.T) {
	for name, mem := range map[string]*Membership{
		"incomplete scan": newClaimsMembership(t, incompleteClaims{}),
		"no reader": newMembership(&membershipDeps{
			Chats: testsupport.NewInMemoryChatStore(), Bus: &tabBus{},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := mem.AdmitRunPurge(t.Context(), "wf_1", "sess_run"); ok {
				t.Error("AdmitRunPurge(wf_1, sess_run) = true, want false: the claim was not ruled out")
			}
		})
	}
}

// An admitted purge holds the run's launching session too: a create binding it is
// refused while the delete is in flight and lands once the purge reports done.
func TestCreateChatAndOpen_BindingAPurgingRunsSessionIsRefusedUntilDone(t *testing.T) {
	mem := newClaimsMembership(t, nil)
	done, ok := mem.AdmitRunPurge(t.Context(), "wf_1", "sess_run")
	if !ok {
		t.Fatal("AdmitRunPurge(wf_1, sess_run) = false with nothing claiming it, want true")
	}

	_, err := mem.CreateChatAndOpen(t.Context(), resumeCreate("op-during", "sess_run"))
	if !errors.Is(err, errRunPurging) {
		t.Errorf("CreateChatAndOpen(resume sess_run) during its run's purge = %v, want %v", err, errRunPurging)
	}
	if _, err := mem.CreateChatAndOpen(t.Context(), resumeCreate("op-other", "sess_other")); err != nil {
		t.Errorf("CreateChatAndOpen(resume sess_other) during wf_1's purge = %v, want nil", err)
	}

	done()
	opened, err := mem.CreateChatAndOpen(t.Context(), resumeCreate("op-during", "sess_run"))
	if err != nil {
		t.Fatalf("CreateChatAndOpen(resume sess_run) after the purge reported done = %v, want nil", err)
	}
	if got := opened.Chat.ACPSessionID; got != "sess_run" {
		t.Errorf("the retried resume bound %q, want sess_run", got)
	}
}

// The scheduler is read per call, so no scheduler wired — the shape every other
// test in this package runs in — must be a no-op rather than a nil call.
func TestCloseTab_AChatTabCloseWithNoSchedulerWiredIsSafe(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-a")

	if _, _, err := mem.closeTab(t.Context(), opened.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
}

// The delete half of the gate. The observation happens inside the coordinator's
// critical section, via the store's close hook, because that is the only place the
// intermediate state exists.
func TestDeleteChatAndCloseTabs_TheRecordLeads(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, _ := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)

	var recordPresentAtClose bool
	var openErr error
	var openDone sync.WaitGroup
	flaky.setBeforeClose(func() {
		_, recordPresentAtClose = store.Get(context.Background(), chatID)
		openDone.Go(func() {
			_, openErr = mem.OpenTab(context.Background(),
				marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(chatID)}, "op-racer")
		})
	})

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}
	openDone.Wait()

	if recordPresentAtClose {
		t.Error("the chat record was still there when its tabs were closed; the record must lead on delete")
	}
	if statusOf(openErr) != http.StatusNotFound {
		t.Errorf("the racing open got status %d, want 404: once the record is gone every open is refused (%s)",
			statusOf(openErr), errText(openErr))
	}
	if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
		t.Errorf("tabs for the deleted chat = %v, want none", got)
	}
}

// The record is the delete's admission barrier: a bridge that opens after the teardown
// started would otherwise outlive the chat. The teardown is driven from the chain captured
// with the record and is attributed to the user's delete.
func TestDeleteChatAndCloseTabs_RemovesTheRecordBeforeTheTeardown(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _, td := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	if _, err := store.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-old")
		c.RecordSession("sess-current")
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the chain: %v", err)
	}
	recordAtTeardown := true
	td.onByChain = func() { _, recordAtTeardown = store.Get(context.Background(), chatID) }

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}

	td.mu.Lock()
	gotChain, ran := td.deletedByChain[chatID]
	cause := td.causes[chatID]
	td.mu.Unlock()
	if !ran {
		t.Fatalf("DeleteChatAndCloseTabs(%q) ran no by-chain teardown", chatID)
	}
	if recordAtTeardown {
		t.Error("the chat record still existed when the teardown ran; the record must go first")
	}
	if want := []string{"sess-old", "sess-current"}; !slices.Equal(gotChain, want) {
		t.Errorf("teardown chain = %v, want the record's %v", gotChain, want)
	}
	if cause != RunStopChatDeleted {
		t.Errorf("teardown cause = %v, want RunStopChatDeleted", cause)
	}
}

// A delete that cannot remove the record leaves the chat working: tearing its sessions
// down first would strand a record that points at nothing.
func TestDeleteChatAndCloseTabs_AFailedDeleteTearsNothingDown(t *testing.T) {
	store := &failingDeleteStore{InMemoryChatStore: testsupport.NewInMemoryChatStore()}
	mem, st, _, td := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); statusOf(err) != http.StatusInternalServerError {
		t.Fatalf("DeleteChatAndCloseTabs with a refused delete = status %d (%s), want 500", statusOf(err), errText(err))
	}

	td.mu.Lock()
	torn := len(td.deletedByChain) + len(td.closed)
	td.mu.Unlock()
	if torn != 0 {
		t.Errorf("teardowns after a refused delete = %d, want none", torn)
	}
	if got := tabIDsFor(st, chatID); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("tabs after a refused delete = %v, want the chat's tab %q kept", got, opened.Subject.ID)
	}
}

// A header that is present but undecodable holds the session chain the teardown needs, so
// removing it would strand the chat's runs and KAS sessions with nothing left to name them.
func TestDeleteChatAndCloseTabs_AnUndecodableRecordIsKept(t *testing.T) {
	dir := t.TempDir()
	store, err := chat.NewStore(dir)
	if err != nil {
		t.Fatalf("Setup: chat.NewStore: %v", err)
	}
	mem, st, _, td := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	if _, err := store.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-a")
		return true
	}); err != nil {
		t.Fatalf("Setup: seed the chain: %v", err)
	}
	header := filepath.Join(dir, string(chatID), "chat.json")
	if err := os.WriteFile(header, []byte("{not a chat"), 0o600); err != nil {
		t.Fatalf("Setup: corrupt the header: %v", err)
	}

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); statusOf(err) != http.StatusInternalServerError {
		t.Fatalf("DeleteChatAndCloseTabs(%q) with an undecodable header = status %d (%s), want 500", chatID, statusOf(err), errText(err))
	}

	if _, err := os.Stat(header); err != nil {
		t.Errorf("header after the refused delete: %v, want it kept", err)
	}
	td.mu.Lock()
	torn := len(td.deletedByChain) + len(td.closed)
	td.mu.Unlock()
	if torn != 0 {
		t.Errorf("teardowns after a refused delete = %d, want none", torn)
	}
	if got := tabIDsFor(st, chatID); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("tabs after a refused delete = %v, want the chat's tab %q kept", got, opened.Subject.ID)
	}
}

// Prune only runs at load, so a tab close that fails after the record is gone would
// otherwise leave a live tab for an unopenable chat until the process restarts.
func TestDeleteChatAndCloseTabs_RetriesAFailedCloseWithoutARestart(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	flaky.failCloseOnce()

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}

	if got := tabIDsFor(flaky.Store, chatID); len(got) != 0 {
		t.Errorf("tabs for the deleted chat = %v, want none: the retry owes this in the same pass", got)
	}
	if got := bus.removedIDs(t); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("removed ids on the wire = %v, want the closed tab %q", got, opened.Subject.ID)
	}
}

// When the retry fails too, the removal is emitted anyway: the chat is gone either
// way. The frame is stamped one PAST the store's version because a client discards
// anything at or below its local one, so the unchanged version would be useless.
func TestDeleteChatAndCloseTabs_AnnouncesTheRemovalEvenWhenTheCloseKeepsFailing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, flaky, bus := newFlakyMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	_, versionBefore := flaky.List()
	flaky.openFails(0)
	flaky.setFailCloses(10)

	if err := mem.DeleteChatAndCloseTabs(t.Context(), chatID, "op-del"); err != nil {
		t.Fatalf("DeleteChatAndCloseTabs = %v", err)
	}

	frames := bus.frames(t)
	last := frames[len(frames)-1]
	if !slices.Equal(last.RemovedIDs, []string{opened.Subject.ID}) {
		t.Errorf("removed_ids = %v, want the tab whose close failed (%q)", last.RemovedIDs, opened.Subject.ID)
	}
	if last.Version != versionBefore+1 {
		t.Errorf("frame version = %d, want %d: a client ignores anything at or below its local version",
			last.Version, versionBefore+1)
	}
	if _, ok := store.Get(t.Context(), chatID); ok {
		t.Error("the chat record survived a delete; the record leads and is removed first")
	}
}

// Retention skips a chat with an open tab, so this path exists only for a tab
// opened between that check and the remove, and resolves it in the same pass.
func TestRetentionClose_ClosesWhatThePredicateRaced(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus, _ := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	if _, err := store.Delete(t.Context(), chatID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	mem.RetentionClose(t.Context(), chatID, nil)

	if got := tabIDsFor(st, chatID); len(got) != 0 {
		t.Errorf("tabs for the purged chat = %v, want none", got)
	}
	if got := bus.removedIDs(t); !slices.Contains(got, opened.Subject.ID) {
		t.Errorf("removed ids on the wire = %v, want the purged chat's tab %q", got, opened.Subject.ID)
	}
	if last := bus.frames(t); last[len(last)-1].OpID != "" {
		t.Errorf("the retention frame carries op_id %q, want none: no client asked for it", last[len(last)-1].OpID)
	}
}

// The purge was the ONE chat-removal path that ran no teardown, so a purged chat kept its
// bridge, terminals, pending perms, turn-registry entry and retained chat_status for the
// process's life. The grade is BY-CHAIN, not the record-reading one: the record is already
// gone by the time the hook fires, so a record-reading teardown would silently no-op.
func TestRetentionClose_RunsTheDeleteGradeTeardown(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _, td := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	if _, err := store.Delete(t.Context(), chatID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	chain := []string{"sess-old", "sess-current"}

	mem.RetentionClose(t.Context(), chatID, chain)

	td.mu.Lock()
	gotChain, ran := td.deletedByChain[chatID]
	cause := td.causes[chatID]
	closed := slices.Clone(td.closed)
	td.mu.Unlock()

	if !ran {
		t.Fatalf("no by-chain teardown for %q: the purge ran none, which is the gap this closes", chatID)
	}
	if cause != RunStopRetention {
		t.Errorf("teardown cause = %v, want RunStopRetention: a purge stops runs nobody asked to stop", cause)
	}
	if !slices.Equal(gotChain, chain) {
		t.Errorf("teardown chain = %v, want %v: the reap and the run cancel are driven from it", gotChain, chain)
	}
	if len(closed) != 0 {
		t.Errorf("the close grade ran for %v: a purge is a delete, so the KAS session is reaped too", closed)
	}
}

// The teardown runs OUTSIDE the operation lock, which is observable only as an ordering:
// at teardown time the doomed tab is still open.
func TestRetentionClose_TearsDownBeforeClosingTabs(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _, td := newTornDownMembership(t, store)
	opened := createChat(t, mem, "op-a")
	chatID := marotte.ChatID(opened.Chat.ID)
	if _, err := store.Delete(t.Context(), chatID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var openAtTeardown []string
	td.onByChain = func() { openAtTeardown = tabIDsFor(st, chatID) }

	mem.RetentionClose(t.Context(), chatID, []string{"sess-current"})

	if !slices.Contains(openAtTeardown, opened.Subject.ID) {
		t.Errorf("tabs open at teardown = %v, want the doomed tab %q: the teardown must precede the close",
			openAtTeardown, opened.Subject.ID)
	}
	if got := tabIDsFor(st, chatID); len(got) != 0 {
		t.Errorf("tabs after the pass = %v, want none", got)
	}
}

// Both directions, because a predicate that answered true for everything would make
// retention opt-out and still pass a one-sided test.
func TestHasOpenTab_IsRetentionsSecondPredicate(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)
	opened := createChat(t, mem, "op-a")
	closedChat := createChat(t, mem, "op-b")
	if _, _, err := mem.closeTab(t.Context(), closedChat.Subject.ID, "op-close"); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}

	cases := []struct {
		desc string
		chat marotte.ChatID
		want bool
	}{
		{desc: "a chat with a tab on the strip is in use", chat: marotte.ChatID(opened.Chat.ID), want: true},
		{desc: "a chat whose tab was closed is not", chat: marotte.ChatID(closedChat.Chat.ID), want: false},
		{desc: "a chat that was never open is not", chat: "c-stranger", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := mem.HasOpenTab(tc.chat); got != tc.want {
				t.Errorf("HasOpenTab(%q) = %v, want %v", tc.chat, got, tc.want)
			}
		})
	}
}

// The exact-set check is the whole precondition: a version precondition would refuse
// this drag, because a pin elsewhere bumps the version without changing the order.
func TestReorderTabs_AcceptedWhileAnUnrelatedPinBumpedTheVersion(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	c := createChat(t, mem, "op-c")

	dragged := []string{c.Subject.ID, a.Subject.ID, b.Subject.ID}
	pinnedVersion, err := mem.setPinned(t.Context(), b.Subject.ID, true, "op-pin")
	if err != nil {
		t.Fatalf("SetPinned = %v", err)
	}

	version, err := mem.reorderTabs(t.Context(), dragged, "op-drag")
	if err != nil {
		t.Fatalf("ReorderTabs after an unrelated pin = %v, want it ACCEPTED (%s)", err, errText(err))
	}

	if version != pinnedVersion+1 {
		t.Errorf("version went %d -> %d, want one bump", pinnedVersion, version)
	}
	open, _ := st.List()
	if got := subjectIDs(open); !slices.Equal(got, dragged) {
		t.Errorf("order = %v, want the arrangement the drag committed %v", got, dragged)
	}
}

// All four refusals share one sentinel and one status, because the client's answer to
// every one of them is identical: re-list, never re-send.
func TestReorderTabs_RefusalShapes(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, _ := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	c := createChat(t, mem, "op-c")
	want := []string{a.Subject.ID, b.Subject.ID, c.Subject.ID}
	_, versionBefore := st.List()

	cases := []struct {
		desc  string
		order []string
	}{
		{desc: "a short list, which is what a client that dropped a tab would send", order: []string{a.Subject.ID, b.Subject.ID}},
		{desc: "a long list naming a tab that is not open", order: []string{a.Subject.ID, b.Subject.ID, c.Subject.ID, "ghost"}},
		{desc: "an id that is not open", order: []string{a.Subject.ID, b.Subject.ID, "ghost"}},
		{desc: "the same id twice", order: []string{a.Subject.ID, b.Subject.ID, b.Subject.ID}},
		{desc: "an empty order against a non-empty set", order: nil},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := mem.reorderTabs(t.Context(), tc.order, "op-drag")

			if statusOf(err) != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (%s)", statusOf(err), errText(err))
			}
			if !errors.Is(err, tabs.ErrOrderMismatch) {
				t.Errorf("error = %v, want tabs.ErrOrderMismatch", err)
			}
			open, version := st.List()
			if got := subjectIDs(open); !slices.Equal(got, want) {
				t.Errorf("a refused reorder changed the order to %v, want %v unchanged", got, want)
			}
			if version != versionBefore {
				t.Errorf("a refused reorder moved the version to %d, want %d", version, versionBefore)
			}
		})
	}
}

// A tab dragged back where it started is not news: no version bump, no frame.
func TestReorderTabs_AnIdenticalOrderCommitsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	_, before := st.List()
	framesBefore := len(bus.frames(t))

	version, err := mem.reorderTabs(t.Context(), []string{a.Subject.ID, b.Subject.ID}, "op-drag")
	if err != nil {
		t.Fatalf("ReorderTabs = %v", err)
	}

	if version != before {
		t.Errorf("version went %d -> %d for an unchanged order", before, version)
	}
	if got := len(bus.frames(t)) - framesBefore; got != 0 {
		t.Errorf("an unchanged order emitted %d frames, want 0", got)
	}
}

// An unopen id is refused here but not on close, because answering success would
// claim a tab that does not exist is pinned.
func TestSetPinned_IdempotentInBothDirections(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")

	first, err := mem.setPinned(t.Context(), a.Subject.ID, true, "op-pin")
	if err != nil {
		t.Fatalf("SetPinned = %v", err)
	}
	again, err := mem.setPinned(t.Context(), a.Subject.ID, true, "op-pin-again")
	if err != nil {
		t.Fatalf("repeat SetPinned = %v", err)
	}
	if again != first {
		t.Errorf("version went %d -> %d for a repeat pin, want it unchanged", first, again)
	}

	_, err = mem.setPinned(t.Context(), "not-open", true, "op-pin-ghost")
	if statusOf(err) != http.StatusNotFound {
		t.Errorf("status = %d for a pin on an absent tab, want 404 (%s)", statusOf(err), errText(err))
	}

	frames := bus.frames(t)
	pins := 0
	for _, f := range frames {
		if f.Changed != nil && f.Changed.ID == a.Subject.ID && f.Changed.Pinned {
			pins++
		}
	}
	if pins != 1 {
		t.Errorf("saw %d pinned frames for %q, want exactly 1", pins, a.Subject.ID)
	}
	open, _ := st.List()
	if i := indexOfTab(open, a.Subject.ID); i < 0 || !open[i].Pinned {
		t.Errorf("the tab is not pinned in the store: %+v", open)
	}
}

// A reparent is the one mutation that reassigns Parent, and it lives here for the
// same reason a pin does: the emit must be serialized behind the commit. The
// frame carries Order beside Changed because the row moved; a repeat carries
// nothing, because nothing committed.
func TestReparent_EmitsOneFrameWithOrderAndIsSilentOnARepeat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	b := createChat(t, mem, "op-b")
	spec, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindSpec, Ref: ".kiro/specs/x"}, "op-spec")
	if err != nil {
		t.Fatalf("Setup: OpenTab(spec) = %v", err)
	}
	framesBefore := len(bus.frames(t))

	moved, version, err := mem.reparent(t.Context(), spec.Subject.ID, a.Subject.ID, "op-move")
	if err != nil {
		t.Fatalf("Reparent = %v", err)
	}
	if moved.ID != spec.Subject.ID || moved.Parent != a.Subject.ID {
		t.Errorf("Reparent returned %+v, want %q under %q", moved, spec.Subject.ID, a.Subject.ID)
	}
	frames := bus.frames(t)
	if got := len(frames) - framesBefore; got != 1 {
		t.Fatalf("a reparent emitted %d frames, want exactly 1", got)
	}
	frame := frames[len(frames)-1]
	if frame.Changed == nil || frame.Changed.ID != spec.Subject.ID || frame.Changed.Parent != a.Subject.ID {
		t.Errorf("frame.Changed = %+v, want the spec tab under %q", frame.Changed, a.Subject.ID)
	}
	wantOrder := []string{a.Subject.ID, spec.Subject.ID, b.Subject.ID}
	if !slices.Equal(frame.Order, wantOrder) {
		t.Errorf("frame.Order = %v, want %v: the row moved behind its new parent", frame.Order, wantOrder)
	}
	if frame.Version != version || frame.OpID != "op-move" {
		t.Errorf("frame = (v%d, op %q), want (v%d, op %q)", frame.Version, frame.OpID, version, "op-move")
	}

	again, repeatVersion, err := mem.reparent(t.Context(), spec.Subject.ID, a.Subject.ID, "op-move-again")
	if err != nil {
		t.Fatalf("repeat Reparent = %v", err)
	}
	if repeatVersion != version || again.Parent != a.Subject.ID {
		t.Errorf("repeat Reparent = (v%d, parent %q), want (v%d, %q) unchanged", repeatVersion, again.Parent, version, a.Subject.ID)
	}
	if got := len(bus.frames(t)) - framesBefore; got != 1 {
		t.Errorf("a repeat reparent emitted %d more frames, want 0", got-1)
	}
	open, _ := st.List()
	if got := subjectIDs(open); !slices.Equal(got, wantOrder) {
		t.Errorf("store order = %v, want %v", got, wantOrder)
	}
}

// The three refusals a reparent can answer, each with its own status: 404 for a
// tab that is not open (a statement about a tab, like a pin), 409 for a parent
// that is not an open chat tab, 409 for a parent inside the tab's own subtree.
// None of them commits or emits.
func TestReparent_RefusesAnAbsentTabANonChatParentAndACycle(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, bus := newTabbedMembership(t, store)
	a := createChat(t, mem, "op-a")
	editor, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: "/workspace/a.go"}, "op-editor")
	if err != nil {
		t.Fatalf("Setup: OpenTab(editor) = %v", err)
	}
	tangent, err := mem.CreateChatAndOpen(t.Context(), ChatCreate{
		OpID:       "op-tangent",
		ParentChat: marotte.ChatID(a.Chat.ID),
		Init:       func(c *marotte.Chat) { c.Name = marotte.DefaultChatName },
	})
	if err != nil {
		t.Fatalf("Setup: CreateChatAndOpen(tangent) = %v", err)
	}
	_, before := mem.tabs.List()
	framesBefore := len(bus.frames(t))

	cases := []struct {
		desc       string
		id, parent string
		want       int
	}{
		{desc: "an absent tab", id: "ghost", parent: a.Subject.ID, want: http.StatusNotFound},
		{desc: "a parent that is not open", id: editor.Subject.ID, parent: "ghost", want: http.StatusConflict},
		{desc: "a parent that is an editor", id: a.Subject.ID, parent: editor.Subject.ID, want: http.StatusConflict},
		{desc: "a chat under its own tangent", id: a.Subject.ID, parent: tangent.Subject.ID, want: http.StatusConflict},
		{desc: "a chat under itself", id: a.Subject.ID, parent: a.Subject.ID, want: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, _, err := mem.reparent(t.Context(), tc.id, tc.parent, "op-"+tc.desc)

			if statusOf(err) != tc.want {
				t.Errorf("Reparent(%q, %q) status = %d, want %d (%s)", tc.id, tc.parent, statusOf(err), tc.want, errText(err))
			}
		})
	}
	if _, after := mem.tabs.List(); after != before {
		t.Errorf("version went %d -> %d across refusals, want it unchanged", before, after)
	}
	if got := len(bus.frames(t)) - framesBefore; got != 0 {
		t.Errorf("refused reparents emitted %d frames, want 0", got)
	}
}

// openFails is a method rather than a field write at the call site so the mutex is
// not somebody else's business.
func (f *flakyTabs) openFails(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOpens = n
}

func (f *flakyTabs) failCloseOnce() { f.setFailCloses(1) }

func (f *flakyTabs) setFailCloses(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCloses = n
}

func (f *flakyTabs) setBeforeClose(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforeClose = fn
}

// Open fails the first failOpens calls.
func (f *flakyTabs) Open(ctx context.Context, spec marotte.OpenTab) (marotte.TabSubject, bool, uint64, error) {
	f.mu.Lock()
	fail := f.failOpens > 0
	if fail {
		f.failOpens--
	}
	f.mu.Unlock()
	if fail {
		return marotte.TabSubject{}, false, 0, errCloseFailed
	}
	return f.Store.Open(ctx, spec)
}

// seedRecord puts a chat in the store without a tab, which is what every chat the
// user has closed looks like. Distinct from rewind_test.go's seedChat, which seeds a
// transcript a rewind can revert to.
func seedRecord(t *testing.T, store *testsupport.InMemoryChatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = string(id)
		return true
	}); err != nil {
		t.Fatalf("seed %q: %v", id, err)
	}
}

func openChild(t *testing.T, mem *Membership, store *testsupport.InMemoryChatStore, id marotte.ChatID, parentTab string) TabOpened {
	t.Helper()
	seedRecord(t, store, id)
	opened, err := mem.OpenTab(t.Context(),
		marotte.OpenTab{Kind: marotte.TabKindChat, Ref: string(id), Parent: parentTab}, "op-child")
	if err != nil {
		t.Fatalf("open child %q: %v", id, err)
	}
	return opened
}

// fillTabs opens n EDITOR tabs, which need no chat record, so a capacity test is
// about capacity.
func fillTabs(t *testing.T, mem *Membership, n int) {
	t.Helper()
	open, _ := mem.tabs.List()
	for i := len(open); i < n; i++ {
		if _, err := mem.OpenTab(t.Context(),
			marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: fmt.Sprintf("/workspace/f%d.go", i)}, ""); err != nil {
			t.Fatalf("fill tab %d: %v", i, err)
		}
	}
}

func storedChatIDs(t *testing.T, store *testsupport.InMemoryChatStore) []marotte.ChatID {
	t.Helper()
	var out []marotte.ChatID
	for _, h := range store.List(t.Context()) {
		out = append(out, marotte.ChatID(h.ID))
	}
	slices.Sort(out)
	return out
}
