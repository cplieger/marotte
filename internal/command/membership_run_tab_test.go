package command

// A run tab's Parent is immutable after open. A supplied parent whose tab is still open wins;
// otherwise the coordinator resolves the launcher from the run owner's answer.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

type fakeRunOwner struct {
	// chats is RunChat's answer per run; an absent run and a parentless one both answer "".
	chats map[string]marotte.ChatID
	// chatErr is RunChat's failure: the launcher could not be read.
	chatErr error
	// bounds is what RunReadBound answers per run, and boundErr its failure.
	bounds   map[string]uint64
	boundErr error
}

func (f *fakeRunOwner) RunReadBound(_ context.Context, workflowID string) (uint64, error) {
	return f.bounds[workflowID], f.boundErr
}

func (f *fakeRunOwner) RunChat(_ context.Context, workflowID string) (marotte.ChatID, error) {
	return f.chats[workflowID], f.chatErr
}

// slowRunOwner answers RunChat only once released: a finished run's engine read.
type slowRunOwner struct {
	fakeRunOwner
	asked   chan struct{}
	release chan struct{}
}

func (s *slowRunOwner) RunChat(ctx context.Context, workflowID string) (marotte.ChatID, error) {
	close(s.asked)
	<-s.release
	return s.fakeRunOwner.RunChat(ctx, workflowID)
}

func newRunTabMembership(t *testing.T, chats chatStore, runs runOwner) (*Membership, *tabs.Store) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	return newMembership(&membershipDeps{Chats: chats, Tabs: st, Bus: bus, Runs: runs}), st
}

func runTabParent(t *testing.T, st *tabs.Store, workflowID string) string {
	t.Helper()
	open, _ := st.List()
	for _, tab := range open {
		if tab.Kind == marotte.TabKindRun && tab.Ref == workflowID {
			return tab.Parent
		}
	}
	t.Fatalf("no run tab for %q in %v", workflowID, open)
	return ""
}

// TestOpenTab_FillsARunsParentFromTheRunOwner — a run tab opened by a door that holds
// no chat id — a notification click, a `/run/{id}` deep link on a fresh browser, a back
// press, another device's open — must still nest, because Parent is set once.
func TestOpenTab_FillsARunsParentFromTheRunOwner(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-launcher"}}
	mem, st := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-launcher")
	chatTab, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-launcher"}, "op-chat")
	if err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}

	opened, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
	if err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	if opened.Subject.Parent != chatTab.Subject.ID {
		t.Errorf("run tab parent = %q, want the launching chat's tab %q",
			opened.Subject.Parent, chatTab.Subject.ID)
	}
	if got := runTabParent(t, st, "wf_1"); got != chatTab.Subject.ID {
		t.Errorf("the STORED parent = %q, want %q; Parent is never reassigned, so this is the only chance",
			got, chatTab.Subject.ID)
	}
}

// TestOpenTab_AClientSuppliedParentWins is why the fill is a fallback rather
// than an override: a door that names the parent (the run bar, History) keeps
// it while that parent's tab is open.
func TestOpenTab_AClientSuppliedParentWins(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-lease"}}
	mem, st := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-lease")
	seedRecord(t, store, "c-explicit")
	if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-lease"}, "op-a"); err != nil {
		t.Fatalf("open c-lease: %v", err)
	}
	explicit, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-explicit"}, "op-b")
	if err != nil {
		t.Fatalf("open c-explicit: %v", err)
	}

	if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{
		Kind:   marotte.TabKindRun,
		Ref:    "wf_1",
		Parent: explicit.Subject.ID,
	}, "op-run"); err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	if got := runTabParent(t, st, "wf_1"); got != explicit.Subject.ID {
		t.Errorf("parent = %q, want the caller's %q rather than the run owner's", got, explicit.Subject.ID)
	}
}

// TestOpenTab_AsksTheRunOwnerOutsideTheLock pins that a finished run's parent, an engine read,
// stalls no other tab mutation while it is answered.
func TestOpenTab_AsksTheRunOwnerOutsideTheLock(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &slowRunOwner{
		chats:   map[string]marotte.ChatID{"wf_1": "c-launcher"},
		asked:   make(chan struct{}),
		release: make(chan struct{}),
	}
	mem, st := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-launcher")
	seedRecord(t, store, "c-other")
	launcher, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-launcher"}, "op-a")
	if err != nil {
		t.Fatalf("open c-launcher: %v", err)
	}

	runOpened := make(chan error, 1)
	go func() {
		_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
		runOpened <- err
	}()
	<-runs.asked
	otherOpened := make(chan error, 1)
	go func() {
		_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-other"}, "op-b")
		otherOpened <- err
	}()
	select {
	case err := <-otherOpened:
		if err != nil {
			t.Fatalf("open c-other: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(runs.release)
		t.Fatal("a chat tab's open waited on the run owner's answer, so the read ran under the lock")
	}

	close(runs.release)
	if err := <-runOpened; err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}
	if got := runTabParent(t, st, "wf_1"); got != launcher.Subject.ID {
		t.Errorf("parent = %q, want the launching chat's tab %q", got, launcher.Subject.ID)
	}
}

// TestOpenTab_LeavesAParentlessRunTopLevel — a manual or scheduled run has a lease
// and no launching chat, so it belongs at the top of the strip.
func TestOpenTab_LeavesAParentlessRunTopLevel(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	for name, runs := range map[string]runOwner{
		"a parentless run names no chat": &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": ""}},
		"no source names the run":        &fakeRunOwner{},
		"no run owner is wired":          nil,
		"the launching chat was deleted": &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-gone"}},
	} {
		t.Run(name, func(t *testing.T) {
			mem, st := newRunTabMembership(t, store, runs)
			if _, err := mem.OpenTab(t.Context(),
				marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run"); err != nil {
				t.Fatalf("OpenTab(run) = %v", err)
			}
			if got := runTabParent(t, st, "wf_1"); got != "" {
				t.Errorf("parent = %q, want top level", got)
			}
		})
	}
}

// TestOpenTab_ReopensAClosedLaunchersTab — the launching chat still exists but its tab was closed,
// so the run opens under that chat's reopened tab rather than at the top of the strip.
func TestOpenTab_ReopensAClosedLaunchersTab(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	bus := &tabBus{}
	mem := newMembership(&membershipDeps{
		Chats: store, Tabs: st, Bus: bus,
		Runs: &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-launcher"}},
	})
	seedRecord(t, store, "c-launcher")

	opened, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
	if err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	open, _ := st.List()
	chatTabs := tabsForChat(open, "c-launcher")
	if len(chatTabs) != 1 {
		t.Fatalf("the launching chat has %d tabs, want its one reopened tab: %v", len(chatTabs), open)
	}
	chatTab := chatTabs[0]
	if opened.Subject.Parent != chatTab.ID {
		t.Errorf("run tab parent = %q, want the reopened chat tab %q", opened.Subject.Parent, chatTab.ID)
	}
	if chatTab.Parent != "" || !chatTab.Owns {
		t.Errorf("reopened chat tab = parent %q owns %v, want a top-level tab owning its chat",
			chatTab.Parent, chatTab.Owns)
	}
	if len(open) != 2 {
		t.Errorf("the set holds %d tabs, want the chat and its run", len(open))
	}
	frames := bus.frames(t)
	if len(frames) != 2 {
		t.Fatalf("emitted %d frames, want the chat's then the run's", len(frames))
	}
	if frames[0].Changed == nil || frames[0].Changed.ID != chatTab.ID || frames[0].OpID != "" {
		t.Errorf("first frame = %+v, want the chat tab with no op_id", frames[0])
	}
	if frames[1].Changed == nil || frames[1].Changed.ID != opened.Subject.ID || frames[1].OpID != "op-run" {
		t.Errorf("second frame = %+v, want the run tab carrying the open's op_id", frames[1])
	}
}

// TestOpenTab_RefusesWhenTheLauncherAndRunNeedTwoSlots — one slot left is not enough to reopen the
// launcher and nest the run, so neither opens.
func TestOpenTab_RefusesWhenTheLauncherAndRunNeedTwoSlots(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st := newRunTabMembership(t, store, &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-launcher"}})
	seedRecord(t, store, "c-launcher")
	for i := range tabs.MaxOpenTabs - 1 {
		ref := fmt.Sprintf("/workspace/f%d.go", i)
		if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindEditor, Ref: ref}, "op-"+ref); err != nil {
			t.Fatalf("open %s: %v", ref, err)
		}
	}

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")

	if code, _, _ := StatusOf(err); code != http.StatusConflict || !errors.Is(err, errTabsFull) {
		t.Errorf("OpenTab(run) = %v (status %d), want the 409 tab limit", err, code)
	}
	if open, _ := st.List(); len(open) != tabs.MaxOpenTabs-1 {
		t.Errorf("the set holds %d tabs, want %d: a refusal opens neither tab", len(open), tabs.MaxOpenTabs-1)
	}
}

// TestOpenTab_RefusesANewRunTabWhoseLauncherCouldNotBeRead — Parent is immutable, so a run opened
// top level on a failed read could never nest once the read recovers.
func TestOpenTab_RefusesANewRunTabWhoseLauncherCouldNotBeRead(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chatErr: errors.New("the run inventory could not be read")}
	mem, st := newRunTabMembership(t, store, runs)

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")

	if code, _, _ := StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRunLauncherUnread) {
		t.Errorf("OpenTab(run) = %v (status %d), want the 503 unread launcher", err, code)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the set holds %v, want nothing opened", open)
	}
}

// unreadableChats is a chat store whose record for one id exists but cannot be read.
type unreadableChats struct {
	*testsupport.InMemoryChatStore
	unreadable marotte.ChatID
}

func (u *unreadableChats) Lookup(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool, error) {
	if id == u.unreadable {
		return nil, false, errors.New("chat.json: unexpected end of JSON input")
	}
	return u.InMemoryChatStore.Lookup(ctx, id)
}

// TestOpenTab_RefusesANewRunTabWhoseLauncherRecordCouldNotBeRead — a live run names its chat from
// the lease, but that chat's tab is closed and its record unreadable: it may still exist, so opening
// the run top level would be permanent.
func TestOpenTab_RefusesANewRunTabWhoseLauncherRecordCouldNotBeRead(t *testing.T) {
	store := &unreadableChats{InMemoryChatStore: testsupport.NewInMemoryChatStore(), unreadable: "c-launcher"}
	mem, st := newRunTabMembership(t, store, &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-launcher"}})

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")

	if code, _, _ := StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRunLauncherUnread) {
		t.Errorf("OpenTab(run) = %v (status %d), want the 503 unread launcher", err, code)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the set holds %v, want nothing opened", open)
	}
}

// closedSuppliedParent opens the launching chat's tab, as the page saw it, then closes it the way
// another device does, below the coordinator; the answer is the id the page still names.
func closedSuppliedParent(t *testing.T, mem *Membership, st *tabs.Store, store *testsupport.InMemoryChatStore) string {
	t.Helper()
	seedRecord(t, store, "c-launcher")
	seen, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-launcher"}, "op-chat")
	if err != nil {
		t.Fatalf("open the launching chat's tab: %v", err)
	}
	if _, _, err := st.Close(t.Context(), seen.Subject.ID); err != nil {
		t.Fatalf("close it elsewhere: %v", err)
	}
	return seen.Subject.ID
}

// TestOpenTab_RecoversTheLauncherWhenTheSuppliedParentClosed — the store would promote a run under
// a closed parent to top level for good, so the open asks the run owner as if no parent were named.
func TestOpenTab_RecoversTheLauncherWhenTheSuppliedParentClosed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st := newRunTabMembership(t, store, &fakeRunOwner{chats: map[string]marotte.ChatID{"wf_1": "c-launcher"}})
	stale := closedSuppliedParent(t, mem, st, store)

	opened, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1", Parent: stale}, "op-run")
	if err != nil {
		t.Fatalf("OpenTab(run) = %v", err)
	}

	open, _ := st.List()
	chatTabs := tabsForChat(open, "c-launcher")
	if len(chatTabs) != 1 {
		t.Fatalf("the launching chat has tabs %v, want its one reopened tab", chatTabs)
	}
	if opened.Subject.Parent != chatTabs[0].ID {
		t.Errorf("run tab parent = %q, want the reopened launcher tab %q", opened.Subject.Parent, chatTabs[0].ID)
	}
	if len(open) != 2 {
		t.Errorf("the set holds %v, want the chat and its run", open)
	}
}

// TestOpenTab_RefusesAClosedSuppliedParentWhoseLauncherCouldNotBeRead — the recovery reads the run
// owner, and a failed read refuses rather than opening the run top level.
func TestOpenTab_RefusesAClosedSuppliedParentWhoseLauncherCouldNotBeRead(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st := newRunTabMembership(t, store, &fakeRunOwner{chatErr: errors.New("the run inventory could not be read")})
	stale := closedSuppliedParent(t, mem, st, store)

	_, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1", Parent: stale}, "op-run")

	if code, _, _ := StatusOf(err); code != http.StatusServiceUnavailable || !errors.Is(err, errRunLauncherUnread) {
		t.Errorf("OpenTab(run) = %v (status %d), want the 503 unread launcher", err, code)
	}
	if open, _ := st.List(); len(open) != 0 {
		t.Errorf("the set holds %v, want nothing opened", open)
	}
}

// TestOpenTab_FocusesAnOpenRunTabWhenTheLauncherCouldNotBeRead — an open tab's parent is already
// settled, so a failed read must not refuse a back press or a second click.
func TestOpenTab_FocusesAnOpenRunTabWhenTheLauncherCouldNotBeRead(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{}
	mem, _ := newRunTabMembership(t, store, runs)
	first, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-a")
	if err != nil {
		t.Fatalf("first OpenTab(run) = %v", err)
	}
	runs.chatErr = errors.New("the run inventory could not be read")

	again, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-b")
	if err != nil {
		t.Fatalf("OpenTab(open run) = %v, want the open tab", err)
	}
	if again.Created || again.Subject.ID != first.Subject.ID {
		t.Errorf("OpenTab(open run) = created %v id %q, want the existing %q", again.Created, again.Subject.ID, first.Subject.ID)
	}
}

// TestOpenTab_FillsNoParentForAnyOtherKind pins the fill to run tabs. A chat's
// nesting is the tangent's own two-field opt-in and an editor's is nobody's, so a
// fill reaching them would invent a hierarchy no door asked for.
func TestOpenTab_FillsNoParentForAnyOtherKind(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	runs := &fakeRunOwner{chats: map[string]marotte.ChatID{"/workspace/a.go": "c-launcher", "c-other": "c-launcher"}}
	mem, st := newRunTabMembership(t, store, runs)
	seedRecord(t, store, "c-launcher")
	seedRecord(t, store, "c-other")
	if _, err := mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: "c-launcher"}, "op-a"); err != nil {
		t.Fatalf("open c-launcher: %v", err)
	}

	for _, spec := range []marotte.OpenTab{
		{Kind: marotte.TabKindEditor, Ref: "/workspace/a.go"},
		{Kind: marotte.TabKindChat, Ref: "c-other"},
	} {
		opened, err := mem.OpenTab(t.Context(), spec, "op-"+spec.Ref)
		if err != nil {
			t.Fatalf("OpenTab(%s) = %v", spec.Kind, err)
		}
		if opened.Subject.Parent != "" {
			t.Errorf("%s tab parent = %q, want top level", spec.Kind, opened.Subject.Parent)
		}
	}
	if open, _ := st.List(); len(open) != 3 {
		t.Errorf("the set holds %d tabs, want 3", len(open))
	}
}
