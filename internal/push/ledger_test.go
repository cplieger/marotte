package push

import (
	"slices"
	"testing"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// The frame offered the notice to the profile's one page; once that page shows it, the profile has it and no push
// follows when the profile leaves.
func TestLedger_ShownNoticeIsNotPushedWhenTheProfileLeaves(t *testing.T) {
	r := newLedgerRig(t, epA)
	r.present(epA)
	r.view(epA, marotte.PageView{Page: "P1"})

	r.raise(t, finished("c1", "done"), 3)
	frames := r.framesNow()
	if len(frames) != 1 || frames[0].ID == "" {
		t.Fatalf("frames = %+v, want one carrying the notice's id", frames)
	}
	r.s.Shown(tagOf(epA), frames[0].ID)
	r.leave()

	if got := r.pushesTo(epA); len(got) != 0 {
		t.Errorf("pushes after the page showed the notice = %+v, want none", got)
	}
}

// A tab ack ends what it covers on every profile: B never saw the page, but its reader read the chat elsewhere.
func TestLedger_TabAckEndsAHeldNoticeOnEveryTag(t *testing.T) {
	r := newLedgerRig(t, epB)
	r.present(epB)

	r.raise(t, finished("c1", "done"), 3)
	r.s.Seen(marotte.TabKindChat, "c1", 3)
	r.leave()

	if got := r.pushesTo(epB); len(got) != 0 {
		t.Errorf("pushes after the chat was read up to the notice = %+v, want none", got)
	}
}

// A notice raised after the ack covering it announces nothing unread; one past the ack, or with no position, does.
func TestLedger_AckBeforeRaiseEndsTheNoticeAtRaise(t *testing.T) {
	for _, tc := range []struct {
		name     string
		position uint64
		pushed   int
	}{
		{"at the acked position", 3, 0},
		{"past the acked position", 4, 1},
		{"with no position", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newLedgerRig(t, epB)
			r.present(epB)
			r.s.Seen(marotte.TabKindChat, "c1", 3)

			r.raise(t, finished("c1", "done"), tc.position)
			r.leave()

			if got := r.pushesTo(epB); len(got) != tc.pushed {
				t.Errorf("pushes = %d, want %d", len(got), tc.pushed)
			}
		})
	}
}

// A page showing the notice's own chat is not offered it: the record is held, pushed if the profile leaves unread,
// ended if the reader acks.
func TestLedger_SubjectOnScreenHoldsTheNoticeForThatTag(t *testing.T) {
	onScreen := func(t *testing.T) *ledgerRig {
		t.Helper()
		r := newLedgerRig(t, epA)
		r.present(epA)
		r.view(epA, marotte.PageView{Page: "P1", Visible: true, Focused: true, Tab: chatTab("c1")})
		r.raise(t, finished("c1", "done"), 3)
		return r
	}
	t.Run("the profile leaves unread", func(t *testing.T) {
		r := onScreen(t)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 {
			t.Errorf("pushes = %d, want one: a locked phone never acked", len(got))
		}
	})
	t.Run("the reader acks", func(t *testing.T) {
		r := onScreen(t)
		r.s.Seen(marotte.TabKindChat, "c1", 3)
		r.leave()
		if got := r.pushesTo(epA); len(got) != 0 {
			t.Errorf("pushes after the ack = %+v, want none", got)
		}
	})
	t.Run("the page is offered nothing", func(t *testing.T) {
		r := onScreen(t)
		for _, f := range r.framesNow() {
			if slices.Contains(f.ShowIn, "P1") {
				t.Errorf("frame %+v offers the page already showing the chat", f)
			}
		}
	})
}

// An ask on screen is held like any notice, ended by its settle, and an ack ends no ask.
func TestLedger_AskOnScreenIsHeldUntilSettledOrGone(t *testing.T) {
	onScreen := func(t *testing.T) *ledgerRig {
		t.Helper()
		r := newLedgerRig(t, epA)
		r.present(epA)
		r.view(epA, marotte.PageView{Page: "P1", Visible: true, Focused: true, Tab: chatTab("c1")})
		r.raise(t, askNotice("c1", "a7"), 0)
		return r
	}
	t.Run("the profile leaves", func(t *testing.T) {
		r := onScreen(t)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 {
			t.Errorf("pushes = %d, want one", len(got))
		}
	})
	t.Run("the ask settles first", func(t *testing.T) {
		r := onScreen(t)
		r.s.Settled(marotte.ChatSubject("c1"), "a7")
		r.leave()
		if got := r.pushesTo(epA); len(got) != 0 {
			t.Errorf("pushes after the settle = %+v, want none", got)
		}
	})
	t.Run("an ack ends no ask", func(t *testing.T) {
		r := onScreen(t)
		r.s.Seen(marotte.TabKindChat, "c1", 3)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 {
			t.Errorf("pushes = %d, want one", len(got))
		}
	})
}

// Each profile is offered one page, a page on a tab that is neither a chat nor a run included: the focused visible
// one, else the hidden one it has.
func TestLedger_OffersEachTagOnePage(t *testing.T) {
	r := newLedgerRig(t, epA, epB)
	r.present(epA)
	r.present(epB)
	r.view(epA, marotte.PageView{Page: "P1", Tab: chatTab("c2")})
	r.view(epA, marotte.PageView{Page: "P2", Visible: true, Focused: true})
	r.view(epB, marotte.PageView{Page: "Q1"})

	r.raise(t, finished("c1", "done"), 3)

	frames := r.framesNow()
	if len(frames) != 1 {
		t.Fatalf("frames = %+v, want one", frames)
	}
	if got := slices.Sorted(slices.Values(frames[0].ShowIn)); !slices.Equal(got, []string{"P2", "Q1"}) {
		t.Errorf("show_in = %v, want [P2 Q1]", got)
	}
}

// An offer the page never answers moves to the profile's next page within one alive window.
func TestLedger_UnansweredOfferMovesToTheNextPage(t *testing.T) {
	r := newLedgerRig(t, epA)
	r.present(epA)
	r.view(epA, marotte.PageView{Page: "P1", Visible: true, Focused: true})
	r.view(epA, marotte.PageView{Page: "P2"})

	r.raise(t, finished("c1", "done"), 3)
	if frames := r.framesNow(); len(frames) != 1 || !slices.Equal(frames[0].ShowIn, []string{"P1"}) {
		t.Fatalf("frames = %+v, want one offering P1", frames)
	}
	// Two half windows with an ack between, so the profile stays present while the offer ages past one window.
	for range 2 {
		r.clock.Advance(liveness.AliveWindow/2 + time.Millisecond)
		r.s.presence.Alive(tagOf(epA))
	}

	waitFor(t, "a second frame", func() bool { return len(r.framesNow()) == 2 })
	if got := r.framesNow()[1]; !slices.Equal(got.ShowIn, []string{"P2"}) {
		t.Errorf("second frame show_in = %v, want [P2]", got.ShowIn)
	}
}

// A page's own gates refusing the notice leave the profile's push to go when it leaves.
func TestLedger_DeclinedOfferIsHeldForThePush(t *testing.T) {
	r := newLedgerRig(t, epA)
	r.present(epA)
	r.view(epA, marotte.PageView{Page: "P1"})

	r.raise(t, finished("c1", "done"), 3)
	frames := r.framesNow()
	if len(frames) != 1 || !slices.Equal(frames[0].ShowIn, []string{"P1"}) {
		t.Fatalf("frames = %+v, want one offering P1", frames)
	}
	r.s.Declined(tagOf(epA), frames[0].ID)
	r.leave()

	if got := r.awaitPushTo(t, epA); len(got) != 1 || got[0].ID != frames[0].ID {
		t.Errorf("pushes = %+v, want the declined notice once", got)
	}
}

// Undelivered records of one key push once, the newest payload; a record past its TTL is not pushed.
func TestLedger_TTLAndCoalescing(t *testing.T) {
	t.Run("coalescing", func(t *testing.T) {
		r := newLedgerRig(t, epA)
		r.present(epA)
		r.raise(t, finished("c1", "first"), 3)
		resetDebounce(r.s)
		r.raise(t, finished("c1", "second"), 4)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 || got[0].Title != "second" {
			t.Errorf("pushes = %+v, want the second alone", got)
		}
	})
	t.Run("ttl", func(t *testing.T) {
		r := newLedgerRig(t, epA)
		r.present(epA)
		r.raise(t, finished("c1", "done"), 3)
		r.clock.Advance(ttlAgentFinished + time.Millisecond)
		if got := r.pushesTo(epA); len(got) != 0 {
			t.Errorf("pushes past the TTL = %+v, want none", got)
		}
	})
}

// The debounce bounds pushes, never what a present profile is finally told.
func TestLedger_DebouncedNonAskOnAPresentTag(t *testing.T) {
	t.Run("present at both raises", func(t *testing.T) {
		r := newLedgerRig(t, epA)
		r.present(epA)
		r.raise(t, finished("c1", "first"), 3)
		r.raise(t, finished("c1", "second"), 4)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 || got[0].Title != "second" {
			t.Errorf("pushes = %+v, want one carrying the second", got)
		}
	})
	t.Run("gone, then present and debounced", func(t *testing.T) {
		r := newLedgerRig(t, epA)
		r.raise(t, finished("c1", "first"), 3)
		r.present(epA)
		r.raise(t, finished("c1", "second"), 4)
		r.leave()
		if got := r.awaitPushTo(t, epA); len(got) != 1 || got[0].Title != "first" {
			t.Errorf("pushes = %+v, want the first alone", got)
		}
	})
	t.Run("gone at both", func(t *testing.T) {
		r := newLedgerRig(t, epA)
		r.raise(t, finished("c1", "first"), 3)
		r.raise(t, finished("c1", "second"), 4)
		if got := r.awaitPushTo(t, epA); len(got) != 1 || got[0].Title != "first" {
			t.Errorf("pushes = %+v, want the first alone", got)
		}
	})
}

// A gone profile's page rows claim nothing: the notice is pushed at once and the rows are forgotten.
func TestLedger_GoneTagIgnoresItsPageRows(t *testing.T) {
	r := newLedgerRig(t, epA)
	r.present(epA)
	r.view(epA, marotte.PageView{Page: "P1", Visible: true, Focused: true, Tab: chatTab("c1")})
	r.s.presence.Observe(disconnected(tagOf(epA), sse.PresenceClosed))
	r.clock.Advance(liveness.ReconnectDelay + time.Millisecond)

	r.raise(t, finished("c1", "done"), 3)
	if got := r.pushes(); len(got) != 1 {
		t.Fatalf("pushes = %+v, want one at raise", got)
	}
	r.present(epA)
	r.raise(t, finished("c2", "other"), 3)
	for _, f := range r.framesNow() {
		if slices.Contains(f.ShowIn, "P1") {
			t.Errorf("frame %+v offers a page its gone profile reported before it left", f)
		}
	}
}
