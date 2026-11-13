package command

import (
	"slices"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// recordingNotices is a notice ledger that records what the tab set told it.
type recordingNotices struct {
	seen   []noticeSeen
	closed []marotte.TabRef
	mu     sync.Mutex
}

type noticeSeen struct {
	kind   marotte.TabKind
	ref    string
	marker uint64
}

func (n *recordingNotices) Seen(kind marotte.TabKind, ref string, marker uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.seen = append(n.seen, noticeSeen{kind: kind, ref: ref, marker: marker})
}

func (n *recordingNotices) TabClosed(kind marotte.TabKind, ref string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = append(n.closed, marotte.TabRef{Kind: kind, Ref: ref})
}

func (n *recordingNotices) snapshot() (seen []noticeSeen, closed []marotte.TabRef) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.seen), slices.Clone(n.closed)
}

func newNoticeRig(t *testing.T) (*ackRig, *recordingNotices) {
	t.Helper()
	r := newAckRig(t)
	led := &recordingNotices{}
	r.mem = newMembership(&membershipDeps{
		Teardown: &recordingTeardown{}, Chats: withKeep(r.chats), Tabs: r.st, Bus: r.bus, Runs: r.runs, Notices: led,
	})
	return r, led
}

// An ack hands the ledger the marker as stored, clamped or not, so a notice the reader has seen is never pushed.
func TestAckTab_PassesTheStoredMarkerToTheNotices(t *testing.T) {
	t.Run("an advance", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		ref := r.subjectOf(t, id).Ref
		r.chats.bound = 4
		if _, _, err := r.mem.ackTab(t.Context(), id, 4, ""); err != nil {
			t.Fatalf("ackTab: %v", err)
		}
		if seen, _ := led.snapshot(); !slices.Equal(seen, []noticeSeen{{marotte.TabKindChat, ref, 4}}) {
			t.Errorf("Seen calls = %+v, want one at the stored marker 4", seen)
		}
	})
	t.Run("a clamped ack", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		ref := r.subjectOf(t, id).Ref
		r.chats.bound = 3
		if _, _, err := r.mem.ackTab(t.Context(), id, 9, ""); err != nil {
			t.Fatalf("ackTab: %v", err)
		}
		if seen, _ := led.snapshot(); !slices.Equal(seen, []noticeSeen{{marotte.TabKindChat, ref, 3}}) {
			t.Errorf("Seen calls = %+v, want one at the clamped marker 3", seen)
		}
	})
	t.Run("an ack not past the stored marker still reports it", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		r.chats.bound = 5
		for _, seen := range []uint64{5, 2} {
			if _, _, err := r.mem.ackTab(t.Context(), id, seen, ""); err != nil {
				t.Fatalf("ackTab(%d): %v", seen, err)
			}
		}
		seen, _ := led.snapshot()
		if len(seen) != 2 || seen[1].marker != 5 {
			t.Errorf("Seen calls = %+v, want the second at the stored marker 5", seen)
		}
	})
	t.Run("a stored marker of 0 reports nothing", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		r.chats.bound = 0
		if _, _, err := r.mem.ackTab(t.Context(), id, 4, ""); err != nil {
			t.Fatalf("ackTab: %v", err)
		}
		if seen, _ := led.snapshot(); len(seen) != 0 {
			t.Errorf("Seen calls = %+v, want none for a marker of 0", seen)
		}
	})
}

// A tab's subject goes to the ledger once no open tab shows it, through a person's close and a chat delete alike.
func TestCloseTab_TellsTheNoticesWhichTabsClosed(t *testing.T) {
	t.Run("closeTab", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		ref := r.subjectOf(t, id).Ref
		if _, _, err := r.mem.closeTab(t.Context(), id, "op-close", true, nil); err != nil {
			t.Fatalf("closeTab: %v", err)
		}
		if _, closed := led.snapshot(); !slices.Equal(closed, []marotte.TabRef{{Kind: marotte.TabKindChat, Ref: ref}}) {
			t.Errorf("TabClosed calls = %+v, want the chat's tab", closed)
		}
	})
	t.Run("closeTabsFor", func(t *testing.T) {
		r, led := newNoticeRig(t)
		id := r.chatTab(t)
		ref := r.subjectOf(t, id).Ref
		if err := r.mem.DeleteChatAndCloseTabs(t.Context(), marotte.ChatID(ref), "op-del", nil); err != nil {
			t.Fatalf("DeleteChatAndCloseTabs: %v", err)
		}
		if _, closed := led.snapshot(); !slices.Equal(closed, []marotte.TabRef{{Kind: marotte.TabKindChat, Ref: ref}}) {
			t.Errorf("TabClosed calls = %+v, want the deleted chat's tab", closed)
		}
	})
}

func (r *ackRig) subjectOf(t *testing.T, id string) marotte.TabSubject {
	t.Helper()
	open, _ := r.st.List()
	for _, s := range open {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("tab %s is not open", id)
	return marotte.TabSubject{}
}
