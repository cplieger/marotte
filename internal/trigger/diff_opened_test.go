package trigger

import (
	"slices"
	"testing"
)

func TestOpenedAllowlistOnly(t *testing.T) {
	h := newHarness(t)
	r := h.create(openedSpec("alice"))
	h.issuePage(issue(10, "alice"))
	h.issuePage(issue(10, "alice"), issue(11, "alice"), issue(12, "mallory"))
	h.issuePage(issue(10, "alice"), issue(11, "alice"), issue(12, "mallory"))
	h.poll()
	h.poll()
	h.poll()
	q := h.queued(r.ID)
	if got := inputOf(q, "issue_number"); !eq(got, []string{"11"}) {
		t.Fatalf("queued issues %v, want only alice's #11, once", got)
	}
	if got, want := q[0].Inputs["event_id"], "github.com/cplieger/animap/issue/11/opened"; got != want {
		t.Errorf("event_id = %q, want %q", got, want)
	}
}

func TestOpenedCaseInsensitiveLogin(t *testing.T) {
	h := newHarness(t)
	r := h.create(openedSpec("Alice"))
	h.issuePage(issue(10, "bob"))
	h.issuePage(issue(11, "aLiCe"))
	h.poll()
	h.poll()
	if got := inputOf(h.queued(r.ID), "author"); !eq(got, []string{"Alice"}) {
		t.Errorf("queued authors %v, want the allowlist entry as typed, Alice", got)
	}
}

func TestOpenedFloorLagsOneWalk(t *testing.T) {
	h := newHarness(t)
	r := h.create(openedSpec("alice"))
	h.issuePage(issue(99, "alice"))
	h.poll()

	// Walk 1 is complete but misses #100, which moved past the cursor mid-walk.
	h.issuePage(issue(99, "alice"), issue(101, "alice"))
	h.poll()
	oc := h.cursorOf(r.ID).Opened
	if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"101"}) {
		t.Fatalf("walk 1 queued %v, want #101", got)
	}
	if oc.Floor != 99 || oc.PendingFloor != 101 {
		t.Errorf("after walk 1: floor %d, pending_floor %d; want 99 and 101", oc.Floor, oc.PendingFloor)
	}

	h.issuePage(issue(99, "alice"), issue(100, "alice"), issue(101, "alice"))
	h.poll()
	oc = h.cursorOf(r.ID).Opened
	if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"101", "100"}) {
		t.Errorf("walk 2 left the queue %v, want #100 queued once beside #101", got)
	}
	if oc.Floor != 101 || len(oc.Seen) != 0 {
		t.Errorf("after walk 2: floor %d, seen %v; want 101 and seen cleared below it", oc.Floor, oc.Seen)
	}

	t.Run("a partial walk moves no floor", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(openedSpec("alice"))
		h.issuePage(issue(5, "alice"))
		h.poll()
		h.src.issues = append(h.src.issues, partialIssues(issue(7, "alice")))
		h.poll()
		oc := h.cursorOf(r.ID).Opened
		if oc.Floor != 5 || oc.PendingFloor != 5 || !slices.Equal(oc.Seen, []int{7}) {
			t.Errorf("after a partial walk: %+v, want floor and pending_floor 5 with #7 in seen", oc)
		}
	})
}
