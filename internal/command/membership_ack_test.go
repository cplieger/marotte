package command

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
	"github.com/cplieger/marotte/internal/testsupport"
)

// boundChats is the in-memory chat store with a settable read bound; block, when set, holds
// ReadBound until it is closed, and entered, when set, receives once ReadBound is reached.
type boundChats struct {
	*testsupport.InMemoryChatStore
	err     error
	block   chan struct{}
	entered chan struct{}
	bound   uint64
}

func (b *boundChats) ReadBound(ctx context.Context, _ marotte.ChatID) (uint64, error) {
	if b.entered != nil {
		b.entered <- struct{}{}
	}
	if b.block != nil {
		select {
		case <-b.block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return b.bound, b.err
}

// failingSeen is the real tab store with SetSeen refused.
type failingSeen struct {
	*tabs.Store
}

func (failingSeen) SetSeen(context.Context, string, uint64) (uint64, error) {
	return 0, errors.New("disk full")
}

type ackRig struct {
	mem   *Membership
	st    *tabs.Store
	bus   *tabBus
	chats *boundChats
	runs  *fakeRunOwner
}

func newAckRig(t *testing.T) *ackRig {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("open tab store: %v", err)
	}
	r := &ackRig{
		st:    st,
		bus:   &tabBus{},
		chats: &boundChats{InMemoryChatStore: testsupport.NewInMemoryChatStore()},
		runs:  &fakeRunOwner{bounds: map[string]uint64{}},
	}
	r.mem = newMembership(&membershipDeps{Chats: r.chats, Tabs: st, Bus: r.bus, Runs: r.runs})
	return r
}

func (r *ackRig) chatTab(t *testing.T) string {
	t.Helper()
	return createChat(t, r.mem, "op-chat").Subject.ID
}

func (r *ackRig) seen(t *testing.T, id string) uint64 {
	t.Helper()
	open, _ := r.st.List()
	for _, s := range open {
		if s.ID == id {
			return s.Seen
		}
	}
	t.Fatalf("tab %s is not open", id)
	return 0
}

func TestAckTab_AdvanceEmitsOneFrameCarryingSeen(t *testing.T) {
	r := newAckRig(t)
	id := r.chatTab(t)
	r.chats.bound = 4
	framesBefore := len(r.bus.frames(t))

	_, stored, err := r.mem.ackTab(t.Context(), id, 4, "op-ack")
	if err != nil || stored != 4 {
		t.Fatalf("ackTab(4) = (seen %d, %v), want 4", stored, err)
	}
	frames := r.bus.frames(t)[framesBefore:]
	if len(frames) != 1 || frames[0].Changed == nil || frames[0].Changed.Seen != 4 || frames[0].OpID != "op-ack" {
		t.Fatalf("frames after the ack = %+v, want one tabs_changed whose subject has seen 4 and op op-ack", frames)
	}
}

func TestAckTab_NotPastTheStoredValueIsANoOp(t *testing.T) {
	r := newAckRig(t)
	id := r.chatTab(t)
	r.chats.bound = 9
	if _, _, err := r.mem.ackTab(t.Context(), id, 5, ""); err != nil {
		t.Fatalf("Setup: ackTab(5): %v", err)
	}
	_, version := r.st.List()
	framesBefore := len(r.bus.frames(t))

	for _, seen := range []uint64{5, 3} {
		v, stored, err := r.mem.ackTab(t.Context(), id, seen, "")
		if err != nil {
			t.Fatalf("ackTab(%d): %v", seen, err)
		}
		if v != version || stored != 5 {
			t.Errorf("ackTab(%d) after 5 = (v%d, seen %d), want (v%d, seen 5)", seen, v, stored, version)
		}
	}
	if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
		t.Errorf("acks not past the stored value emitted %d frames, want 0", n)
	}
}

func TestAckTab_IsClampedToTheBound(t *testing.T) {
	t.Run("a chat ack past its read position stores the position", func(t *testing.T) {
		r := newAckRig(t)
		id := r.chatTab(t)
		r.chats.bound = 2
		if _, stored, err := r.mem.ackTab(t.Context(), id, 7, ""); err != nil || stored != 2 {
			t.Errorf("ackTab(7) with bound 2 = (seen %d, %v), want 2", stored, err)
		}
	})
	t.Run("a bound of 0 writes and emits nothing", func(t *testing.T) {
		r := newAckRig(t)
		id := r.chatTab(t)
		framesBefore := len(r.bus.frames(t))
		if _, stored, err := r.mem.ackTab(t.Context(), id, 3, ""); err != nil || stored != 0 {
			t.Errorf("ackTab(3) with bound 0 = (seen %d, %v), want 0", stored, err)
		}
		if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
			t.Errorf("an ack clamped to 0 emitted %d frames, want 0", n)
		}
	})
	t.Run("a run reads the run bound, not the chat's", func(t *testing.T) {
		r := newAckRig(t)
		opened, err := r.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
		if err != nil {
			t.Fatalf("Setup: OpenTab(run): %v", err)
		}
		r.chats.bound = 9
		r.runs.bounds["wf_1"] = 3
		if _, stored, err := r.mem.ackTab(t.Context(), opened.Subject.ID, 5, ""); err != nil || stored != 3 {
			t.Errorf("run ackTab(5) with run bound 3 = (seen %d, %v), want 3", stored, err)
		}
	})
	t.Run("a run at position 0 stays unseen", func(t *testing.T) {
		r := newAckRig(t)
		opened, err := r.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
		if err != nil {
			t.Fatalf("Setup: OpenTab(run): %v", err)
		}
		if _, stored, err := r.mem.ackTab(t.Context(), opened.Subject.ID, 1, ""); err != nil || stored != 0 {
			t.Errorf("run ackTab(1) at position 0 = (seen %d, %v), want 0", stored, err)
		}
	})
}

func TestAckTab_FailuresAnswerAndEmitNothing(t *testing.T) {
	t.Run("a failing bound read is a 500 and stores nothing", func(t *testing.T) {
		logs := captureLogs(t)
		r := newAckRig(t)
		id := r.chatTab(t)
		r.chats.bound, r.chats.err = 5, errors.New("log unreadable")
		framesBefore := len(r.bus.frames(t))

		_, _, err := r.mem.ackTab(t.Context(), id, 5, "")
		if statusOf(err) != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500 (%s)", statusOf(err), errText(err))
		}
		if got := r.seen(t, id); got != 0 {
			t.Errorf("seen = %d after a failed bound read, want 0", got)
		}
		if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
			t.Errorf("a failed ack emitted %d frames, want 0", n)
		}
		if !strings.Contains(logs.String(), "tab ack: read bound not read") {
			t.Errorf("logs = %q, want the bound read's Warn", logs.String())
		}
	})
	t.Run("a failed write is a 500 and emits nothing", func(t *testing.T) {
		logs := captureLogs(t)
		r := newAckRig(t)
		id := r.chatTab(t)
		r.chats.bound = 5
		mem := newMembership(&membershipDeps{Chats: r.chats, Tabs: failingSeen{r.st}, Bus: r.bus, Runs: r.runs})
		framesBefore := len(r.bus.frames(t))

		_, _, err := mem.ackTab(t.Context(), id, 5, "")
		if statusOf(err) != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500 (%s)", statusOf(err), errText(err))
		}
		if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
			t.Errorf("a failed write emitted %d frames, want 0", n)
		}
		if !strings.Contains(logs.String(), "tab ack not persisted") {
			t.Errorf("logs = %q, want the write's Warn", logs.String())
		}
	})
	t.Run("a failing run bound read is a 500 and stores nothing", func(t *testing.T) {
		logs := captureLogs(t)
		r := newAckRig(t)
		opened, err := r.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
		if err != nil {
			t.Fatalf("Setup: OpenTab(run): %v", err)
		}
		r.runs.bounds["wf_1"], r.runs.boundErr = 4, errors.New("kas unreachable")
		framesBefore := len(r.bus.frames(t))

		_, _, err = r.mem.ackTab(t.Context(), opened.Subject.ID, 4, "")
		if statusOf(err) != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500 (%s)", statusOf(err), errText(err))
		}
		if got := r.seen(t, opened.Subject.ID); got != 0 {
			t.Errorf("seen = %d after a failed run bound read, want 0", got)
		}
		if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
			t.Errorf("a failed ack emitted %d frames, want 0", n)
		}
		if !strings.Contains(logs.String(), "tab ack: read bound not read") {
			t.Errorf("logs = %q, want the bound read's Warn", logs.String())
		}
	})
	t.Run("a run tab with no run surface is a 503", func(t *testing.T) {
		r := newAckRig(t)
		opened, err := r.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindRun, Ref: "wf_1"}, "op-run")
		if err != nil {
			t.Fatalf("Setup: OpenTab(run): %v", err)
		}
		mem := newMembership(&membershipDeps{Chats: r.chats, Tabs: r.st, Bus: r.bus})
		if _, _, err := mem.ackTab(t.Context(), opened.Subject.ID, 1, ""); statusOf(err) != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 (%s)", statusOf(err), errText(err))
		}
	})
	t.Run("no tab store is a 503", func(t *testing.T) {
		mem := newTestMembership(t, testsupport.NewInMemoryChatStore())
		if _, _, err := mem.ackTab(t.Context(), "t1", 1, ""); statusOf(err) != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 (%s)", statusOf(err), errText(err))
		}
	})
	t.Run("a tab that is not open is a no-op answering the version", func(t *testing.T) {
		r := newAckRig(t)
		r.chatTab(t)
		_, version := r.st.List()
		v, stored, err := r.mem.ackTab(t.Context(), "ghost", 3, "")
		if err != nil || v != version || stored != 0 {
			t.Errorf("ackTab(ghost) = (v%d, seen %d, %v), want (v%d, 0, nil): an ack racing a close is normal", v, stored, err, version)
		}
	})
	t.Run("a tab of another kind is a 400", func(t *testing.T) {
		r := newAckRig(t)
		opened, err := r.mem.OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindSpec, Ref: ".kiro/specs/x"}, "op-spec")
		if err != nil {
			t.Fatalf("Setup: OpenTab(spec): %v", err)
		}
		if _, _, err := r.mem.ackTab(t.Context(), opened.Subject.ID, 1, ""); statusOf(err) != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (%s)", statusOf(err), errText(err))
		}
	})
}

// The bound read may open a chat log, so a slow one must not block every other tab mutation.
func TestAckTab_ReadsTheBoundWithoutTheCoordinatorLock(t *testing.T) {
	r := newAckRig(t)
	id := r.chatTab(t)
	r.chats.bound = 2
	r.chats.block = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := r.mem.ackTab(t.Context(), id, 2, "")
		done <- err
	}()

	pinned := make(chan error, 1)
	go func() {
		_, err := r.mem.setPinned(t.Context(), id, true, "")
		pinned <- err
	}()
	select {
	case err := <-pinned:
		if err != nil {
			t.Errorf("setPinned during an ack's bound read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("setPinned waited on an ack's bound read: the coordinator lock is held across it")
	}
	close(r.chats.block)
	if err := <-done; err != nil {
		t.Errorf("ackTab: %v", err)
	}
}

// The bound read runs outside the coordinator lock, so the tab can close under it; the ack then
// answers like one for a tab that is not open.
func TestAckTab_ATabClosedDuringTheBoundReadIsANoOp(t *testing.T) {
	r := newAckRig(t)
	id := r.chatTab(t)
	r.chats.bound = 2
	r.chats.block = make(chan struct{})
	r.chats.entered = make(chan struct{}, 1)
	type answer struct {
		err             error
		version, stored uint64
	}
	done := make(chan answer, 1)
	go func() {
		v, stored, err := r.mem.ackTab(t.Context(), id, 2, "")
		done <- answer{version: v, stored: stored, err: err}
	}()
	<-r.chats.entered
	if _, _, err := r.mem.closeTab(t.Context(), id, "op-close"); err != nil {
		t.Fatalf("Setup: closeTab during the bound read: %v", err)
	}
	_, version := r.st.List()
	framesBefore := len(r.bus.frames(t))
	close(r.chats.block)

	got := <-done
	if got.err != nil || got.version != version || got.stored != 0 {
		t.Errorf("ackTab across a close = (v%d, seen %d, %v), want (v%d, 0, nil)", got.version, got.stored, got.err, version)
	}
	if n := len(r.bus.frames(t)) - framesBefore; n != 0 {
		t.Errorf("an ack for a tab closed under it emitted %d frames, want 0", n)
	}
}

func TestCmdAckTab_RefusesMalformedJSON(t *testing.T) {
	r := newAckRig(t)
	cmd := &marotte.ClientCommand{Type: marotte.CmdAckTab, Payload: []byte(`{"id":`)}
	if _, err := cmdAckTab(t.Context(), r.mem, cmd); statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", statusOf(err), errText(err))
	}
}

func TestCmdAckTab_ValidatesThePayload(t *testing.T) {
	r := newAckRig(t)
	id := r.chatTab(t)
	r.chats.bound = 3
	cases := []struct {
		desc    string
		payload marotte.AckTabCommand
		want    int
	}{
		{desc: "a valid ack", payload: marotte.AckTabCommand{ID: id, Seen: 3, OpID: "op-1"}, want: http.StatusOK},
		{desc: "an empty id", payload: marotte.AckTabCommand{Seen: 3}, want: http.StatusBadRequest},
		{desc: "an id outside the identifier rule", payload: marotte.AckTabCommand{ID: "a/b", Seen: 3}, want: http.StatusBadRequest},
		{desc: "seen 0", payload: marotte.AckTabCommand{ID: id}, want: http.StatusBadRequest},
		{desc: "an op_id outside the identifier rule", payload: marotte.AckTabCommand{ID: id, Seen: 3, OpID: "op/../x"}, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			body, err := cmdAckTab(t.Context(), r.mem, tabCmd(t, marotte.CmdAckTab, tc.payload))
			if statusOf(err) != tc.want {
				t.Fatalf("status = %d, want %d (%s)", statusOf(err), tc.want, errText(err))
			}
			if tc.want != http.StatusOK {
				return
			}
			if got, ok := bodyField(t, body, "seen").(uint64); !ok || got != 3 {
				t.Errorf("seen = %v, want uint64 3", bodyField(t, body, "seen"))
			}
		})
	}
}
