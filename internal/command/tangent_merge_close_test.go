package command

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// closingParent is the merge host's admission with a close of the parent modelled as the gate does it: while a
// hold stands, AwaitNoClose parks and the parent's reserve is refused.
type closingParent struct {
	*mergeHost
	idle chan struct{}
	// afterClear runs once, when an AwaitNoClose finds no hold: a close committing right after it.
	afterClear func()
	// parked receives one value each time an AwaitNoClose parks on a hold.
	parked chan struct{}
	mu     sync.Mutex
	held   bool
}

func newClosingParent(h *mergeHost) *closingParent {
	return &closingParent{mergeHost: h, parked: make(chan struct{}, 8)}
}

func (c *closingParent) hold() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held, c.idle = true, make(chan struct{})
}

func (c *closingParent) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = false
	close(c.idle)
}

func (c *closingParent) holding() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

func (c *closingParent) AwaitNoClose(ctx context.Context, chatID marotte.ChatID) (bool, error) {
	if chatID != "c-parent" {
		return false, nil
	}
	c.mu.Lock()
	if !c.held {
		hook := c.afterClear
		c.afterClear = nil
		c.mu.Unlock()
		if hook != nil {
			hook()
		}
		return false, nil
	}
	idle := c.idle
	c.mu.Unlock()
	c.parked <- struct{}{}
	select {
	case <-idle:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

func (c *closingParent) TryReserveTurn(chatID marotte.ChatID, source marotte.TurnOpenSource, stamp uint64) (Reservation, bool) {
	if chatID == "c-parent" && c.holding() {
		return Reservation{}, false
	}
	return c.mergeHost.TryReserveTurn(chatID, source, stamp)
}

// closingQueue refuses an append while the parent's close is held, as AppendIfLive's gate read does.
type closingQueue struct {
	*fakeQueue
	gate    *closingParent
	appends int
}

func (q *closingQueue) AppendIfLive(ctx context.Context, chatID marotte.ChatID, fn func(c *marotte.Chat) error) (bool, error) {
	q.appends++
	if q.gate.holding() {
		return false, nil
	}
	return q.fakeQueue.AppendIfLive(ctx, chatID, fn)
}

func awaitMergeSettled(t *testing.T, f *mergeFixture) marotte.TangentMerge {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		rec, ok := f.merges.record(t.Context(), tangentOp1)
		if ok && rec.State != marotte.TangentMergeRunning {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the merge never settled: %+v", rec)
		}
	}
}

func awaitParked(t *testing.T, c *closingParent) {
	t.Helper()
	select {
	case <-c.parked:
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery never waited on the parent's close")
	}
}

// The findings are the parent record's across its lives: a close of the parent is waited out and the delivery
// lands in its next life; a close after the delivery's turn opened ends the merge as a delivery failure; a parent
// deleted meanwhile settles as gone.
func TestTangentMerge_AParentClosedDuringTheSummaryGetsTheFindingsAfterItsTeardown(t *testing.T) {
	findings := &fakeTangents{parent: "c-parent", reply: "The cache halves the load."}
	setup := func(t *testing.T) (*mergeFixture, *closingParent, *closingQueue) {
		t.Helper()
		f := newMergeFixture(t)
		c := newClosingParent(f.host)
		q := &closingQueue{fakeQueue: f.queue, gate: c}
		f.roles.admission, f.roles.followups = c, q
		return f, c, q
	}

	t.Run("closed before the delivery", func(t *testing.T) {
		f, c, q := setup(t)
		c.hold()
		if _, err := f.merge(t, findings); err != nil {
			t.Fatalf("CmdMergeTangent = %v, want accepted", err)
		}
		awaitParked(t, c)
		if n := f.host.promptCount(); n != 1 {
			t.Errorf("prompts opened while the parent's close is held = %d, want the summary alone", n)
		}
		c.release()

		rec := awaitMergeSettled(t, f)
		if rec.State != marotte.TangentMergeSucceeded {
			t.Errorf("merge = %+v, want succeeded after the close", rec)
		}
		if n := f.host.promptCount(); n != 2 {
			t.Errorf("prompts opened = %d, want the summary then the findings in the parent's next life", n)
		}
		if q.appends != 0 {
			t.Errorf("appends = %d, want none: the parent was idle after its close", q.appends)
		}
	})

	t.Run("a close between the wait and the reserve", func(t *testing.T) {
		f, c, q := setup(t)
		c.afterClear = func() {
			c.hold()
			go func() {
				select {
				case <-c.parked:
				case <-time.After(5 * time.Second):
				}
				c.release()
			}()
		}
		if _, err := f.merge(t, findings); err != nil {
			t.Fatalf("CmdMergeTangent = %v, want accepted", err)
		}

		rec := awaitMergeSettled(t, f)
		if rec.State != marotte.TangentMergeSucceeded {
			t.Errorf("merge = %+v, want succeeded in the next life", rec)
		}
		if q.appends != 0 || len(q.chat.QueuedPrompts) != 0 {
			t.Errorf("appends = %d, queued rows = %+v, want nothing queued into the closing life", q.appends, q.chat.QueuedPrompts)
		}
		if n := f.host.promptCount(); n != 2 {
			t.Errorf("prompts opened = %d, want the summary then the findings", n)
		}
	})

	t.Run("closed during the delivery's turn", func(t *testing.T) {
		f, _, q := setup(t)
		f.host.unbound["c-parent"] = ""
		if _, err := f.merge(t, findings); err != nil {
			t.Fatalf("CmdMergeTangent = %v, want accepted", err)
		}

		rec := awaitMergeSettled(t, f)
		if rec.State != marotte.TangentMergeFailed || rec.Message != deliveryFailure("") {
			t.Errorf("merge = %+v, want failed with the delivery failure, the findings kept in the tangent", rec)
		}
		if len(q.chat.QueuedPrompts) != 0 {
			t.Errorf("queued rows = %+v, want no findings row in the parent", q.chat.QueuedPrompts)
		}
	})

	t.Run("deleted during the wait", func(t *testing.T) {
		f, c, _ := setup(t)
		c.hold()
		if _, err := f.merge(t, findings); err != nil {
			t.Fatalf("CmdMergeTangent = %v, want accepted", err)
		}
		awaitParked(t, c)
		f.host.openErr = chat.ErrTombstoned
		c.release()

		rec := awaitMergeSettled(t, f)
		if rec.State != marotte.TangentMergeFailed || rec.Message != mergeParentGone {
			t.Errorf("merge = %+v, want failed as the parent gone", rec)
		}
		if n := f.host.promptCount(); n != 1 {
			t.Errorf("prompts opened = %d, want the summary alone", n)
		}
		for _, e := range f.host.mergeFrames() {
			if p, ok := e.Payload.(marotte.ErrorPayload); ok && strings.HasPrefix(p.Message, deliveryFailure("")) {
				t.Errorf("merge frame %+v reads as a delivery failure, want the parent gone", p)
			}
		}
	})
}
