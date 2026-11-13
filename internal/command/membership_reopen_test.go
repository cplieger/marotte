package command

// ReopenForMessage: a message an outside caller sends into a chat with no open tab opens that tab, and the frame
// announcing it drops the chat from Recently closed.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

const infoReopenSkipped = "command: a messaged chat's tab was not reopened; 48 tabs are open"

var grelotCaller = &marotte.Caller{Label: "grelot"}

// chatTabOf is the open chat tab for ref, if any.
func chatTabOf(st *tabs.Store, ref marotte.ChatID) (marotte.TabSubject, bool) {
	open, _ := st.List()
	for _, s := range open {
		if s.Kind == marotte.TabKindChat && s.Ref == string(ref) {
			return s, true
		}
	}
	return marotte.TabSubject{}, false
}

// logsAt counts the captured records at level whose message is msg.
func logsAt(logs *capture.Recorder, level slog.Level, msg string) int {
	n := 0
	for _, r := range logs.Records() {
		if r.Level == level && r.Message == msg {
			n++
		}
	}
	return n
}

// warnings are the captured Warn and Error messages.
func warnings(logs *capture.Recorder) []string {
	var out []string
	for _, r := range logs.Records() {
		if r.Level >= slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

func TestReopenForMessage_OpensAClosedChatsTabAndTheFrameDropsItFromRecent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-a", "Alpha")
	seedNamed(t, store, "c-b", "Beta")
	closeListed(t, mem, "c-b")
	closeListed(t, mem, "c-a")
	mark := len(bus.frames(t))

	mem.ReopenForMessage(t.Context(), "c-a")

	tab, ok := chatTabOf(st, "c-a")
	if !ok {
		t.Fatal("no chat tab for c-a after the message, want its tab reopened")
	}
	if !tab.Owns || tab.Parent != "" {
		t.Errorf("reopened tab = owns %v, parent %q; want an owning top-level tab", tab.Owns, tab.Parent)
	}
	frames := bus.frames(t)[mark:]
	if len(frames) != 1 {
		t.Fatalf("the reopen emitted %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f.Changed == nil || f.Changed.Ref != "c-a" || f.Changed.ID != tab.ID {
		t.Errorf("frame changed = %+v, want the reopened tab %s", f.Changed, tab.ID)
	}
	if f.OpID != "" {
		t.Errorf("frame op_id = %q, want none, so no device activates the tab", f.OpID)
	}
	if refs, carried := frameRecentRefs(&f); !carried || !slices.Equal(refs, []string{"c-b"}) {
		t.Errorf("frame recent = %v (carried %v), want [c-b]", refs, carried)
	}
	if refs := refsOf(st.Recent()); !slices.Equal(refs, []string{"c-b"}) {
		t.Errorf("Recent() = %v, want [c-b]", refs)
	}
	c, _ := store.Get(t.Context(), "c-a")
	if _, held := c.Keep[marotte.KeyTab]; !held {
		t.Error("the reopened chat holds no tab fact, want the open's hold")
	}
}

// A chat closed past the 6-hour window, never listed, is reopened all the same: the window is a display window.
func TestReopenForMessage_OpensAChatThatWasNeverListed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-old", "Old work")
	mark := len(bus.frames(t))

	mem.ReopenForMessage(t.Context(), "c-old")

	if _, ok := chatTabOf(st, "c-old"); !ok {
		t.Fatal("no chat tab for c-old after the message, want its tab opened")
	}
	frames := bus.frames(t)[mark:]
	if len(frames) != 1 {
		t.Fatalf("the reopen emitted %d frames, want 1", len(frames))
	}
	if frames[0].Recent != nil {
		t.Errorf("frame recent = %+v, want none carried: the list did not change", frames[0].Recent.Items)
	}
}

func TestReopenForMessage_AChatWithAnOpenTabWritesNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	seedNamed(t, store, "c-a", "Alpha")
	openChatTab(t, mem, "c-a", "")
	before, _ := store.Get(t.Context(), "c-a")
	_, version := st.List()
	mark := len(bus.frames(t))

	mem.ReopenForMessage(t.Context(), "c-a")

	if _, after := st.List(); after != version {
		t.Errorf("tab version = %d, want %d unchanged", after, version)
	}
	if n := len(bus.frames(t)) - mark; n != 0 {
		t.Errorf("emitted %d frames, want 0", n)
	}
	after, _ := store.Get(t.Context(), "c-a")
	if after.Keep[marotte.KeyTab] != before.Keep[marotte.KeyTab] {
		t.Errorf("tab fact = %+v, want %+v untouched", after.Keep[marotte.KeyTab], before.Keep[marotte.KeyTab])
	}
}

func TestReopenForMessage_At48TabsOpensNothingAndLogsInfo(t *testing.T) {
	logs := capture.Default(t)
	store := testsupport.NewInMemoryChatStore()
	mem, st, bus := newTabbedMembership(t, store)
	for i := range tabs.MaxOpenTabs {
		id := marotte.ChatID(fmt.Sprintf("c-%02d", i))
		seedNamed(t, store, id, "Open")
		openChatTab(t, mem, id, "")
	}
	seedNamed(t, store, "c-x", "Messaged")
	_, version := st.List()
	mark := len(bus.frames(t))

	mem.ReopenForMessage(t.Context(), "c-x")

	if _, ok := chatTabOf(st, "c-x"); ok {
		t.Error("c-x has a tab at 48 open, want the reopen skipped")
	}
	if _, after := st.List(); after != version {
		t.Errorf("tab version = %d, want %d unchanged", after, version)
	}
	if n := len(bus.frames(t)) - mark; n != 0 {
		t.Errorf("emitted %d frames, want 0", n)
	}
	if n := logsAt(logs, slog.LevelInfo, infoReopenSkipped); n != 1 {
		t.Errorf("Info %q logged %d times, want 1", infoReopenSkipped, n)
	}
	if w := warnings(logs); len(w) != 0 {
		t.Errorf("warnings = %v, want none for the 48-tab skip", w)
	}
	c, _ := store.Get(t.Context(), "c-x")
	if _, held := c.Keep[marotte.KeyTab]; held {
		t.Error("c-x holds a tab fact after the skipped open, want it rolled back")
	}
}

// A chat that is gone, or whose teardown runs, is the prompt's to answer: the reopen opens nothing and says nothing.
func TestReopenForMessage_AGoneOrTearingDownChatOpensNothingQuietly(t *testing.T) {
	cases := map[string]struct {
		hold error
		seed bool
	}{
		"gone":        {},
		"in teardown": {seed: true, hold: chat.ErrInTeardown},
		"tombstoned":  {seed: true, hold: chat.ErrTombstoned},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			logs := capture.Default(t)
			store := testsupport.NewInMemoryChatStore()
			if tc.seed {
				seedNamed(t, store, "c-x", "Doomed")
			}
			st, err := tabs.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("Setup: open tab store: %v", err)
			}
			bus := &tabBus{}
			chats := &refusingHold{keepingStore: &keepingStore{chatStore: store}, err: tc.hold}
			mem := newMembership(&membershipDeps{Teardown: &recordingTeardown{}, Chats: chats, Tabs: st, Bus: bus, Runs: &fakeRunOwner{}})

			mem.ReopenForMessage(t.Context(), "c-x")

			if open, _ := st.List(); len(open) != 0 {
				t.Errorf("tabs = %+v, want none", open)
			}
			if n := len(bus.frames(t)); n != 0 {
				t.Errorf("emitted %d frames, want 0", n)
			}
			if w := warnings(logs); len(w) != 0 {
				t.Errorf("warnings = %v, want none", w)
			}
		})
	}
}

// Any other failure is logged at Warn with the chat id; the message is still the prompt's to deliver.
func TestReopenForMessage_AnyOtherFailureWarnsWithTheChatID(t *testing.T) {
	logs := capture.Default(t)
	store := testsupport.NewInMemoryChatStore()
	seedNamed(t, store, "c-x", "Messaged")
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("Setup: open tab store: %v", err)
	}
	failing := &failingOpen{Store: st}
	mem := newMembership(&membershipDeps{Teardown: &recordingTeardown{}, Chats: withKeep(store), Tabs: failing, Bus: &tabBus{}, Runs: &fakeRunOwner{}})

	mem.ReopenForMessage(t.Context(), "c-x")

	var found bool
	for _, r := range logs.Records() {
		if r.Level != slog.LevelWarn {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Value.String() == "c-x" {
				found = true
			}
			return true
		})
	}
	if !found {
		t.Errorf("logs = %v, want a Warn naming c-x", logs.Messages())
	}
	c, _ := store.Get(t.Context(), "c-x")
	if _, held := c.Keep[marotte.KeyTab]; held {
		t.Error("c-x holds a tab fact after the failed open, want it rolled back")
	}
}

// refusingHold refuses every Hold with err when err is set.
type refusingHold struct {
	*keepingStore
	err error
}

func (r *refusingHold) Hold(ctx context.Context, id marotte.ChatID, key string, scope chat.KeepScope) (chat.Claim, error) {
	if r.err != nil {
		return chat.Claim{}, r.err
	}
	return r.keepingStore.Hold(ctx, id, key, scope)
}

var errOpenWrite = errors.New("simulated tabs.json write failure")

// failingOpen is the real tab store with every Open refused as a failed write.
type failingOpen struct {
	*tabs.Store
}

func (*failingOpen) Open(context.Context, marotte.OpenTab) (marotte.TabSubject, bool, uint64, error) {
	return marotte.TabSubject{}, false, 0, errOpenWrite
}
