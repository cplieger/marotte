package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/subject"
)

func pendingVersion(t *testing.T, v *subject.Versions) string {
	t.Helper()
	cur, _ := v.Current(subject.KindPending, "")
	return cur
}

// mustMove asserts that step moved the `pending` counter exactly once past before.
func mustMove(t *testing.T, v *subject.Versions, name string, step func()) {
	t.Helper()
	before := pendingVersion(t, v)
	step()
	after := pendingVersion(t, v)
	if after == before {
		t.Errorf("%s did not move the pending counter (still %q)", name, before)
	}
}

func mustHold(t *testing.T, v *subject.Versions, name string, step func()) {
	t.Helper()
	before := pendingVersion(t, v)
	step()
	if after := pendingVersion(t, v); after != before {
		t.Errorf("%s moved the pending counter %q -> %q; it changed no item", name, before, after)
	}
}

func TestPendingPermsTracker_EveryMutationMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	tr := newPendingPermsTracker()
	tr.versions = v
	perm := func(chat marotte.ChatID, id int64, run string) marotte.ServerEvent {
		return marotte.NewEvent(marotte.EventPermissionNeeded, chat, marotte.PermissionNeededPayload{
			RequestID: id, RunID: run,
			Options: []marotte.PermissionOption{{OptionID: "allow"}},
		})
	}
	mustMove(t, v, "Add", func() { tr.add(1, perm("c1", 1, ""), nil) })
	mustMove(t, v, "Add second", func() { tr.add(2, perm("c1", 2, "wf-1"), nil) })
	mustMove(t, v, "Add third", func() { tr.add(3, perm("c2", 3, ""), nil) })
	mustMove(t, v, "TakeIfPresent", func() {
		if _, ok := tr.takeIfPresent("c1", 1, marotte.DecisionKindPermission); !ok {
			t.Fatal("TakeIfPresent(c1, 1) = false, want the tracked request")
		}
	})
	mustHold(t, v, "TakeIfPresent on a missing request", func() { tr.takeIfPresent("c1", 1, marotte.DecisionKindPermission) })
	mustHold(t, v, "TakePermissionOption with an off-list option", func() {
		if _, pending, offered := tr.takePermissionOption("c1", 2, "nope"); !pending || offered {
			t.Fatalf("TakePermissionOption(off-list) = (pending %v, offered %v), want (true, false)", pending, offered)
		}
	})
	mustMove(t, v, "TakePermissionOption", func() {
		if _, _, offered := tr.takePermissionOption("c1", 2, "allow"); !offered {
			t.Fatal("TakePermissionOption(allow) = not offered, want the claim")
		}
	})
	tr.add(4, perm("c2", 4, "wf-2"), nil)
	mustMove(t, v, "ClearForRun", func() { tr.clearForRun("wf-2") })
	mustHold(t, v, "ClearForRun with nothing to drop", func() { tr.clearForRun("wf-2") })
	tr.add(6, marotte.NewEvent(marotte.EventElicitationNeeded, "c2", marotte.ElicitationNeededPayload{}), nil)
	mustMove(t, v, "ClearInputs", func() { tr.clearInputs("c2", command.ScopeChatAndRuns) })
	mustHold(t, v, "ClearInputs with nothing to drop", func() { tr.clearInputs("c2", command.ScopeChatAndRuns) })
	origin := newFakeBridge()
	tr.add(5, perm("c3", 5, ""), origin)
	mustMove(t, v, "EndOrigin", func() { tr.endOrigin(origin) })
	mustHold(t, v, "EndOrigin with nothing to retire", func() { tr.endOrigin(origin) })
}

func TestRunAsks_EveryPublishMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	rs := &Runs{}
	rs.asks.versions = v
	if err := rs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf-1", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	var c *askCell
	mustMove(t, v, "a leased run's first trigger", func() { c = rs.cellFor("wf-1") })
	mustHold(t, v, "a second trigger", func() { rs.cellFor("wf-1") })
	asks := []marotte.RunAsk{{AskID: "a1", NodePath: []string{"root", "n1"}}}
	publish := func() {
		c.mu.Lock()
		rs.publishLocked(t.Context(), c, asks, true)
		c.mu.Unlock()
	}
	mustMove(t, v, "a changed snapshot", publish)
	mustHold(t, v, "an unchanged snapshot", publish)
	mustMove(t, v, "the lease's release", func() { rs.releaseLease(t.Context(), "wf-1") })
	mustHold(t, v, "a second release", func() { rs.releaseLease(t.Context(), "wf-1") })
	mustHold(t, v, "a trigger after the release", func() { rs.cellFor("wf-1") })
}

func TestSteerRecords_EveryMutationMovesTheSharedCounter(t *testing.T) {
	v := &subject.Versions{}
	b := newSteerRecords()
	b.versions = v
	mustMove(t, v, "SteerWaiting", func() { b.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "s1"}) })
	mustHold(t, v, "SteerWaiting with no id", func() { b.SteerWaiting("c1", &marotte.SteerQueuedPayload{}) })
	mustMove(t, v, "SteerRead", func() { b.SteerRead("c1", "s1") })
	mustHold(t, v, "SteerForgotten with nothing held", func() { b.SteerForgotten("c1", []string{"s1"}) })
	b.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "s2"})
	mustMove(t, v, "SteerForgotten", func() { b.SteerForgotten("c1", []string{"s2"}) })
	b.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "s3"})
	mustMove(t, v, "TakeAgentRows", func() { b.takeAgentRows("c1") })
	mustHold(t, v, "TakeAgentRows with nothing to drop", func() { b.takeAgentRows("c1") })
}

// TestPendingStores_ShareOneCounter pins one subject for all three stores: the client learns the
// whole pending set from one connect frame.
func TestPendingStores_ShareOneCounter(t *testing.T) {
	v := &subject.Versions{}
	tr := newPendingPermsTracker()
	tr.versions = v
	rs := &Runs{}
	rs.asks.versions = v
	if err := rs.leaseStore().Put(t.Context(), &runlease.Lease{WorkflowID: "wf-1", Origin: runlease.OriginManual}); err != nil {
		t.Fatalf("Setup: lease: %v", err)
	}
	b := newSteerRecords()
	b.versions = v
	tr.add(1, marotte.NewEvent(marotte.EventPermissionNeeded, "c1", marotte.PermissionNeededPayload{RequestID: 1}), nil)
	rs.cellFor("wf-1")
	b.SteerWaiting("c1", &marotte.SteerQueuedPayload{SteerID: "s1"})
	if got := pendingVersion(t, v); got != "3" {
		t.Errorf("pending version after one mutation per store = %q, want \"3\"", got)
	}
}
