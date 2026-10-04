package forges

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// walkPage is one page a walkSource serves.
type walkPage struct {
	err  error
	next forgeapi.Cursor
	prs  []WatchedPR
}

// walkSource serves one connection's authored pages by the cursor the poller
// hands it, as a forge does, and records every cursor it was handed.
type walkSource struct {
	pages  map[forgeapi.Cursor]walkPage
	afters []forgeapi.Cursor
	conn   PRConnection
	// gone answers no read at all, the shape of a disconnected connection.
	gone bool
}

func newWalkSource() *walkSource {
	return &walkSource{conn: testConn, pages: map[forgeapi.Cursor]walkPage{}}
}

func (w *walkSource) serve(after, next forgeapi.Cursor, prs ...WatchedPR) {
	w.pages[after] = walkPage{prs: prs, next: next}
}

func (w *walkSource) Read(_ context.Context, _ bool, after func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	if w.gone {
		return nil
	}
	c := after(w.conn, authoredScope)
	w.afters = append(w.afters, c)
	page, ok := w.pages[c]
	if !ok {
		page = walkPage{err: &forgeapi.Error{Kind: forgeapi.KindUnknown, Code: forgeapi.CodeCursorInvalid}}
	}
	return []ConnectionRead{authoredRead(w.conn, ScopePage{Err: page.err, Next: page.next}, page.prs)}
}

// TestSweep_ContinuesTheWalkOnTheNextCycle: a continuation is resumed only by
// another call, so the next sweep starts where the last page ended, and the sweep
// after the walk's last page starts it again.
func TestSweep_ContinuesTheWalkOnTheNextCycle(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending))
	src.serve("c2", "", pr(2, checkPending))
	p := newTestPoller(src, &fakeNotifier{}, &fakeGate{push: true})

	for range 3 {
		p.sweep(t.Context())
	}

	if want := []forgeapi.Cursor{"", "c2", ""}; !slices.Equal(src.afters, want) {
		t.Errorf("cursors over three sweeps = %q, want %q", src.afters, want)
	}
}

// TestSweep_NoPruneBetweenPagesOfOneWalk: a pull request absent from the second
// page is on the first, so the walk keeps its verdict and the next flip is news
// rather than a silent re-seed.
func TestSweep_NoPruneBetweenPagesOfOneWalk(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending))
	src.serve("c2", "", pr(2, checkPending))
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})

	p.sweep(t.Context())
	p.sweep(t.Context())
	if got := checkOf(p, 1); got != checkPending || len(p.seen) != 2 {
		t.Fatalf("after the walk's second page #1 = %q and %d tracked, want %q and 2: a page is not the whole walk",
			got, len(p.seen), checkPending)
	}
	src.serve("", "c2", pr(1, checkPassing))
	p.sweep(t.Context())

	if len(n.sent) != 1 || !strings.Contains(n.sent[0].body, "#1 checks passed") {
		t.Errorf("sent %+v after #1 went green on the walk's next first page, want one notice for #1", n.sent)
	}
}

// TestSweep_AClosedPRIsPrunedWhenTheWalkCompletes: a pull request missing from a
// walk is pruned by the page that ends the walk, never before.
func TestSweep_AClosedPRIsPrunedWhenTheWalkCompletes(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending), pr(2, checkPending))
	src.serve("c2", "", pr(3, checkPending))
	p := newTestPoller(src, &fakeNotifier{}, &fakeGate{push: true})
	p.sweep(t.Context())
	p.sweep(t.Context())
	if len(p.seen) != 3 {
		t.Fatalf("tracked %d after one whole walk, want 3", len(p.seen))
	}

	src.serve("", "c2", pr(1, checkPending))
	p.sweep(t.Context())
	if checkOf(p, 2) == "" {
		t.Errorf("#2 was pruned by the first page of a walk; only the page that ends it may prune")
	}
	p.sweep(t.Context())
	if got := checkOf(p, 2); got != "" || len(p.seen) != 2 {
		t.Errorf("after the walk ended without #2: #2 = %q and %d tracked, want it pruned and 2 left", got, len(p.seen))
	}
}

// TestSweep_CursorInvalidRestartsWithoutPruning: a continuation the forge refuses
// drops the walk's position and starts it again from the first page, and nothing
// the walk tracked is pruned on the way.
func TestSweep_CursorInvalidRestartsWithoutPruning(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending))
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})

	p.sweep(t.Context())
	p.sweep(t.Context())
	if got := checkOf(p, 1); got != checkPending {
		t.Fatalf("after a refused continuation #1 = %q, want %q: the refusal prunes nothing", got, checkPending)
	}
	src.serve("", "c2", pr(1, checkFailing))
	p.sweep(t.Context())

	if want := []forgeapi.Cursor{"", "c2", ""}; !slices.Equal(src.afters, want) {
		t.Errorf("cursors = %q, want %q: the sweep after the refusal starts from the first page", src.afters, want)
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0].body, "#1 checks failed") {
		t.Errorf("sent %+v, want one notice for #1 turning red", n.sent)
	}
}

// TestSweep_AFailedCallKeepsTheWalksPosition: a forge that is down or rate
// limited answers nothing about the walk, so the next sweep asks for the same
// page rather than starting over.
func TestSweep_AFailedCallKeepsTheWalksPosition(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending))
	src.pages["c2"] = walkPage{err: &forgeapi.Error{Kind: forgeapi.KindRateLimited}}
	p := newTestPoller(src, &fakeNotifier{}, &fakeGate{push: true})

	p.sweep(t.Context())
	p.sweep(t.Context())
	src.serve("c2", "", pr(2, checkPending))
	p.sweep(t.Context())

	if want := []forgeapi.Cursor{"", "c2", "c2"}; !slices.Equal(src.afters, want) {
		t.Errorf("cursors = %q, want %q: a rate-limited page is asked for again", src.afters, want)
	}
}

// multiSource answers each walk source's page in one sweep, as several
// connections do.
type multiSource []*walkSource

func (m multiSource) Read(ctx context.Context, present bool, after func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	var out []ConnectionRead
	for _, w := range m {
		out = append(out, w.Read(ctx, present, after)...)
	}
	return out
}

// TestSweep_AWalkPrunesOnlyItsOwnConnection: one connection's walk ending says
// nothing about another connection's pull requests.
func TestSweep_AWalkPrunesOnlyItsOwnConnection(t *testing.T) {
	gh := newWalkSource()
	gh.serve("", "", pr(1, checkPending))
	gl := newWalkSource()
	gl.conn = PRConnection{ID: "gitlab:gitlab.com", Account: "bob", WebBase: "https://gitlab.com"}
	theirs := pr(2, checkPending)
	theirs.ForgeID = "gitlab:gitlab.com"
	gl.serve("", "c2", theirs)
	gl.serve("c2", "")
	p := newTestPoller(multiSource{gh, gl}, &fakeNotifier{}, &fakeGate{push: true})

	p.sweep(t.Context())
	p.sweep(t.Context())

	if len(p.seen) != 2 {
		t.Errorf("tracked %d after GitHub's walk ended twice and GitLab's reached its second page, want 2: %+v",
			len(p.seen), p.seen)
	}
}

// unreadSource answers testConn connected and, while unread is set, not read, the
// shape of a push-only cycle after the connection's last clone went away.
type unreadSource struct {
	walk   *walkSource
	unread bool
}

func (u *unreadSource) Read(ctx context.Context, present bool, after func(PRConnection, Scope) forgeapi.Cursor) []ConnectionRead {
	if u.unread {
		return []ConnectionRead{{Conn: u.walk.conn}}
	}
	return u.walk.Read(ctx, present, after)
}

// TestSweep_AnUnreadConnectionLosesItsSubjectsAndKeepsItsWalk: a connection still
// connected but not read this sweep has nothing that can be news, yet its walk
// is the inventory's next refill, so it continues where it was.
func TestSweep_AnUnreadConnectionLosesItsSubjectsAndKeepsItsWalk(t *testing.T) {
	w := newWalkSource()
	w.serve("", "c2", pr(1, checkPending))
	w.serve("c2", "", pr(2, checkPending))
	src := &unreadSource{walk: w}
	p := newTestPoller(src, &fakeNotifier{}, &fakeGate{push: true})
	p.sweep(t.Context())

	src.unread = true
	p.sweep(t.Context())
	if len(p.seen) != 0 {
		t.Errorf("tracked %d subjects of a connection the sweep did not read, want none", len(p.seen))
	}
	src.unread = false
	p.sweep(t.Context())
	if want := []forgeapi.Cursor{"", "c2"}; !slices.Equal(w.afters, want) {
		t.Errorf("cursors = %q, want %q: the unread sweep kept the walk's position", w.afters, want)
	}
}

// TestSweep_ReplacedConnectionDropsItsWalk: the connection signed in as another
// account is another walk, so its position is not reused and what the old account
// tracked is not compared with what the new one reads; a connection no longer
// read loses everything.
func TestSweep_ReplacedConnectionDropsItsWalk(t *testing.T) {
	src := newWalkSource()
	src.serve("", "c2", pr(1, checkPending))
	n := &fakeNotifier{}
	p := newTestPoller(src, n, &fakeGate{push: true})
	p.sweep(t.Context())

	src.conn.Account = "carol"
	src.serve("", "", pr(1, checkFailing))
	p.sweep(t.Context())
	if want := []forgeapi.Cursor{"", ""}; !slices.Equal(src.afters, want) {
		t.Errorf("cursors = %q, want %q: the replaced connection's walk starts again", src.afters, want)
	}
	if len(n.sent) != 0 {
		t.Errorf("sent %+v: a row the new account reads for the first time is a first sighting", n.sent)
	}

	src.gone = true
	p.sweep(t.Context())
	if len(p.seen) != 0 || len(p.walks) != 0 {
		t.Errorf("after the connection was no longer read: %d tracked and %d walks, want none", len(p.seen), len(p.walks))
	}
}
