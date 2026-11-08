package trigger

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// captureLogs sends the default logger to a buffer for the rest of the test; no test here runs in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func fifo(ev *launchrule.EventSpec, limit int) *launchrule.EventSpec {
	ev.Hold = &launchrule.Hold{Policy: launchrule.HoldFIFO, Cap: limit}
	return ev
}

// eligible is what the launcher may start of the rule's queue now.
func (h *harness) eligible(ruleID string) []string {
	h.t.Helper()
	r, ok := h.store.Get(ruleID)
	if !ok {
		h.t.Fatalf("no rule %s", ruleID)
	}
	p := &poller{c: h.c, now: func() time.Time { return h.now }}
	pr := p.load(&r, Feed{Connection: r.When.Event.Connection, Selector: r.When.Event.Repo}, h.now)
	if pr == nil {
		h.t.Fatalf("rule %s did not load", ruleID)
	}
	return pr.eligible()
}

// dequeue drops the rule's head, as a confirmed launch does, leaving room in its queue.
func (h *harness) dequeue(ruleID string) {
	h.t.Helper()
	es := h.state(ruleID)
	head := es.queue[0]
	es.queue = es.queue[1:]
	es.launched = append(es.launched, launchedEntry{Key: head.Key, CoalesceKey: head.CoalesceKey, Inputs: head.Inputs, WorkflowID: "wf"})
	h.seed(ruleID, &es)
}

func TestHoldNewestCoalesces(t *testing.T) {
	h, id := baselined(t, "success")
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.runPage(run(42, forgeapi.CheckPassing, created.Add(time.Minute)))
	h.runPage(run(43, forgeapi.CheckPassing, created.Add(2*time.Minute)))
	h.poll()
	k41 := h.queued(id)[0].Key
	h.poll()
	es := h.state(id)
	k42 := es.queue[0].Key
	if len(es.queue) != 1 || len(es.superseded) != 1 || es.superseded[0] != (supersededEntry{Key: k41, By: k42}) {
		t.Fatalf("after run 42: queue %v, superseded %+v; want 42 alone and 41 superseded by it with no run", keysOf(es.queue), es.superseded)
	}
	h.poll()
	es = h.state(id)
	k43 := es.queue[0].Key
	want := []supersededEntry{{Key: k41, By: k43}, {Key: k42, By: k43}}
	if len(es.queue) != 1 || !eq(es.superseded, want) {
		t.Errorf("after run 43: queue %v, superseded %+v; want 43 alone and both older keys naming it", keysOf(es.queue), es.superseded)
	}
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.poll()
	if got := keysOf(h.queued(id)); !eq(got, []string{k43}) {
		t.Errorf("a superseded run read again left the queue %v, want 43 alone", got)
	}
}

func TestNewestDoesNotSupersedeLaunchingEntry(t *testing.T) {
	h, id := baselined(t, "success")
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.poll()
	k41 := h.queued(id)[0].Key
	done := h.marks.mark(id, k41)
	h.runPage(run(42, forgeapi.CheckPassing, created.Add(time.Minute)))
	h.poll()
	es := h.state(id)
	if len(es.queue) != 2 || es.queue[0].Key != k41 || len(es.superseded) != 0 {
		t.Errorf("a match during 41's launch: queue %v, superseded %+v; want 42 queued beside 41", keysOf(es.queue), es.superseded)
	}
	if got := h.eligible(id); len(got) != 0 {
		t.Errorf("eligible while 41 launches = %v, want nothing", got)
	}
	done()

	t.Run("an entry holding a live record", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h, id, k41, k42 := launchedThenNewer(t, tc, nil)
			es := h.state(id)
			if !eq(keysOf(es.queue), []string{k41, k42}) || len(es.superseded) != 0 {
				t.Errorf("queue %v, superseded %+v; want 42 queued beside 41's live attempt", keysOf(es.queue), es.superseded)
			}
			if got := h.eligible(id); len(got) != 0 {
				t.Errorf("eligible while 41's attempt is live = %v, want nothing", got)
			}
		})
	})
	t.Run("the launching entry's confirmation leaves the newer one queued", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h, id, k41, k42 := launchedThenNewer(t, tc, nil)
			tc.ConfirmEvent(id, k41, "wf_1")
			synctest.Wait()
			es := h.state(id)
			if len(es.superseded) != 0 || len(es.launched) != 1 || es.launched[0].Key != k41 {
				t.Errorf("superseded %+v, launched %+v; want 41 launched and nothing superseded", es.superseded, es.launched)
			}
			if got := h.eligible(id); !eq(got, []string{k42}) {
				t.Errorf("eligible = %v, want 42 alone", got)
			}
		})
	})
	t.Run("an abandon supersedes the older key", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h, id, k41, k42 := launchedThenNewer(t, tc, nil)
			advance(launchConfirmBound + launchBackoff[0])
			es := h.state(id)
			if !eq(es.superseded, []supersededEntry{{Key: k41, By: k42}}) {
				t.Errorf("superseded %+v, want 41 superseded by 42", es.superseded)
			}
			if rec := tc.recordOf(id, "wf_2"); rec == nil || rec.key != k42 {
				t.Errorf("record of wf_2 = %+v, want 42 launched", rec)
			}
		})
	})
	t.Run("the mark clearing with no attempt supersedes the older key", func(t *testing.T) {
		inBubble(t, func(t *testing.T, tc *testConfirmer) {
			h, id, k41, k42 := launchedThenNewer(t, tc, errListTimedOut)
			es := h.state(id)
			if !eq(keysOf(es.queue), []string{k42}) || !eq(es.superseded, []supersededEntry{{Key: k41, By: k42}}) {
				t.Errorf("queue %v, superseded %+v; want 41 superseded by 42 once its launch ended", keysOf(es.queue), es.superseded)
			}
		})
	})
}

// errListTimedOut is a launch that failed before workflow/new.
var errListTimedOut = errors.New("listRecipes timed out")

// launchedThenNewer queues run 41 on a newest rule and launches it, run 42 matching while the call is out. A nil
// failBefore names wf_1 for 41; otherwise the call fails with it before any run exists.
func launchedThenNewer(t *testing.T, tc *testConfirmer, failBefore error) (h *harness, id, k41, k42 string) {
	t.Helper()
	h = tc.pollerOn()
	id = h.create(runSpec("success")).ID
	h.runPage()
	h.poll()
	created := pollStart.Add(time.Minute)
	h.runPage(run(41, forgeapi.CheckPassing, created))
	h.poll()
	k41 = h.queued(id)[0].Key
	f := tc.startLauncher(func(n int, call launchCall) (string, error) {
		if n > 1 {
			return fmt.Sprintf("wf_%d", n), tc.NameAttempt(call.ruleID, call.key, fmt.Sprintf("wf_%d", n))
		}
		h.runPage(run(42, forgeapi.CheckPassing, created.Add(time.Minute)))
		h.poll()
		if failBefore != nil {
			return "", failBefore
		}
		return "wf_1", tc.NameAttempt(call.ruleID, call.key, "wf_1")
	})
	tc.letLaunch(nil)
	if calls := f.taken(); len(calls) == 0 || calls[0].key != k41 {
		t.Fatalf("calls = %+v, want 41 launched first", calls)
	}
	return h, id, k41, h.queued(id)[len(h.queued(id))-1].Key
}

func TestPruneKeepsOpenItems(t *testing.T) {
	numbered := func(from, to int) []forgeapi.Issue {
		out := make([]forgeapi.Issue, 0, to-from+1)
		for n := from; n <= to; n++ {
			out = append(out, issue(n, "alice"))
		}
		return out
	}
	t.Run("over the cap", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(numbered(1, 5001)...)
		h.issuePage(numbered(1, 5001)...)
		h.poll()
		h.poll()
		lc := h.cursorOf(r.ID).Labeled
		if len(lc.Items) != 5001 || !lc.TooMany {
			t.Errorf("entries %d, too many %v; want every open item kept and the rule blocked", len(lc.Items), lc.TooMany)
		}
		pr := (&poller{c: h.c}).load(&r, Feed{Connection: testConn, Selector: testRepo}, h.now)
		if got := pr.blocked(); got != "more than 5,000 open items" {
			t.Errorf("blocked = %q, want more than 5,000 open items", got)
		}
		h.issuePage(numbered(1, 4000)...)
		h.poll()
		if lc := h.cursorOf(r.ID).Labeled; lc.TooMany {
			t.Error("the block stayed after a complete walk under the cap")
		}
	})
	t.Run("closed entries go first", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(numbered(1, 6000)...)
		h.poll()
		h.now = pollStart.Add(24 * time.Hour)
		h.issuePage(numbered(1, 4000)...)
		h.poll()
		lc := h.cursorOf(r.ID).Labeled
		if len(lc.Items) != 5000 || lc.TooMany {
			t.Fatalf("entries %d, too many %v; want 5,000 and no block", len(lc.Items), lc.TooMany)
		}
		for n := 1; n <= 4000; n++ {
			if lc.Items[n] == nil {
				t.Fatalf("open item #%d was pruned", n)
			}
		}
		h.now = pollStart.Add(31 * 24 * time.Hour)
		h.issuePage(numbered(1, 4000)...)
		h.poll()
		if lc := h.cursorOf(r.ID).Labeled; len(lc.Items) != 4000 {
			t.Errorf("after 30 days absent: %d entries, want the 4,000 open ones", len(lc.Items))
		}
	})
	t.Run("a partial walk prunes nothing", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(issue(1, "alice"), issue(2, "alice"))
		h.poll()
		h.now = pollStart.Add(31 * 24 * time.Hour)
		h.src.issues = append(h.src.issues, partialIssues(issue(1, "alice")))
		h.poll()
		if lc := h.cursorOf(r.ID).Labeled; lc.Items[2] == nil {
			t.Error("a partial walk dropped #2's entry")
		}
	})
}

func TestHoldFifoKeepsOrder(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(11, "a"), issue(12, "a"), issue(13, "a"), issue(14, "a"))
	h.issuePage(issue(14, "a", testLabel), issue(12, "a", testLabel), issue(11, "a"), issue(13, "a"))
	h.issuePage(issue(14, "a", testLabel), issue(12, "a", testLabel), issue(13, "a", testLabel), issue(11, "a", testLabel))
	for range 3 {
		h.poll()
	}
	if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"12", "14", "11", "13"}) {
		t.Errorf("queue = %v, want oldest first, ascending within a poll", got)
	}
	if got := h.eligible(r.ID); !eq(got, []string{h.queued(r.ID)[0].Key}) {
		t.Errorf("eligible = %v, want the head alone", got)
	}
}

func TestFifoHeadHoldsLaterEntries(t *testing.T) {
	h := newHarness(t)
	a := h.create(labeledSpec())
	b := h.create(openedSpec("alice"))
	h.issuePage(issue(11, "alice"), issue(12, "alice"))
	h.issuePage(issue(11, "alice", testLabel), issue(12, "alice", testLabel), issue(13, "alice"))
	h.poll()
	h.poll()
	head := h.queued(a.ID)[0].Key

	done := h.marks.mark(a.ID, head)
	if got := h.eligible(a.ID); len(got) != 0 {
		t.Errorf("eligible while the head launches = %v, want nothing", got)
	}
	if got := h.eligible(b.ID); len(got) != 1 {
		t.Errorf("the other rule's eligible = %v, want its head: one rule's flight holds no other rule", got)
	}
	done()

	es := h.state(a.ID)
	es.queue[0].Live = "wf-1"
	h.seed(a.ID, &es)
	if got := h.eligible(a.ID); len(got) != 0 {
		t.Errorf("eligible while the head holds a live record = %v, want nothing", got)
	}
	es.queue[0].Live = ""
	h.seed(a.ID, &es)
	if got := h.eligible(a.ID); !eq(got, []string{head}) {
		t.Errorf("eligible after the head's record ended = %v, want the head alone", got)
	}

	t.Run("a newest rule holds every entry while one is in flight", func(t *testing.T) {
		h, id := baselined(t, "success")
		other := run(42, forgeapi.CheckPassing, pollStart.Add(2*time.Minute))
		other.Branch = "dev"
		ev := runSpec("success")
		ev.Filter.Branch = ""
		edited, _ := h.store.Get(id)
		edited.When.Event = ev
		if err := changeRule(t, h.c, id, 0, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatal(err)
		}
		h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute)), other)
		h.poll()
		q := h.queued(id)
		if len(q) != 2 || len(h.eligible(id)) != 2 {
			t.Fatalf("queue %v, eligible %v; want two coalesce keys, both eligible", keysOf(q), h.eligible(id))
		}
		defer h.marks.mark(id, q[1].Key)()
		if got := h.eligible(id); len(got) != 0 {
			t.Errorf("eligible while one entry launches = %v, want nothing", got)
		}
	})
}

func TestQueueFullShowsOnTheRow(t *testing.T) {
	const fullOfTwo = "queue full: 2 events held, newer events wait at the forge"
	full := func(t *testing.T, cap int) (h *harness, id string, all []forgeapi.Issue) {
		t.Helper()
		h = newHarness(t)
		r := h.create(fifo(labeledSpec(), cap))
		h.issuePage(issue(1, "a"), issue(2, "a"), issue(3, "a"))
		h.poll()
		return h, r.ID, []forgeapi.Issue{issue(1, "a", testLabel), issue(2, "a", testLabel), issue(3, "a", testLabel)}
	}
	rowText := func(h *harness, id string) string { return h.state(id).raw.Status.RowText }

	t.Run("until a poll leaves nothing at the forge", func(t *testing.T) {
		h, id, all := full(t, 2)
		h.issuePage(all...)
		h.poll()
		if got := rowText(h, id); got != fullOfTwo {
			t.Fatalf("row text = %q, want %q", got, fullOfTwo)
		}
		h.dequeue(id)
		h.issuePage(all...)
		h.poll()
		if got := rowText(h, id); got != "" {
			t.Errorf("with room for #3, row text = %q, want none", got)
		}
	})
	t.Run("one held event", func(t *testing.T) {
		h, id, all := full(t, 1)
		h.issuePage(all...)
		h.poll()
		if got, want := rowText(h, id), "queue full: 1 event held, newer events wait at the forge"; got != want {
			t.Errorf("row text = %q, want %q", got, want)
		}
	})
	t.Run("never over a launch's sentence", func(t *testing.T) {
		h, id, all := full(t, 2)
		es := h.state(id)
		es.raw.Status.RowText = "starting run wf_1"
		h.seed(id, &es)
		h.issuePage(all...)
		h.poll()
		if got := rowText(h, id); got != "starting run wf_1" {
			t.Errorf("full poll: row text = %q, want the launch's sentence kept", got)
		}
		h.dequeue(id)
		h.issuePage(all...)
		h.poll()
		if got := rowText(h, id); got != "starting run wf_1" {
			t.Errorf("poll with room: row text = %q, want the launch's sentence kept", got)
		}
	})
}

func TestQueueFullLeavesEventsAtForge(t *testing.T) {
	t.Run("labeled", func(t *testing.T) {
		logs := captureLogs(t)
		h := newHarness(t)
		r := h.create(fifo(labeledSpec(), 2))
		h.issuePage(issue(1, "a"), issue(2, "a"), issue(3, "a"))
		all := []forgeapi.Issue{issue(1, "a", testLabel), issue(2, "a", testLabel), issue(3, "a", testLabel)}
		h.issuePage(all...)
		h.poll()
		h.poll()
		if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"1", "2"}) {
			t.Fatalf("queued %v, want #1 and #2 up to the cap", got)
		}
		if e := h.labelOf(r.ID, 3); e == nil || e.HasLabel != labelNo {
			t.Errorf("#3 left at the forge = %+v, want has_label false", e)
		}
		if got := logs.String(); !strings.Contains(got, `msg="trigger queue full"`) || !strings.Contains(got, "held=2 cap=2") {
			t.Errorf("logs = %s, want a queue full warning with held=2 cap=2", got)
		}
		h.dequeue(r.ID)
		h.issuePage(all...)
		h.poll()
		if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"2", "3"}) {
			t.Errorf("with room, queued %v, want #3 matched again behind #2", got)
		}
	})
	t.Run("opened", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(fifo(openedSpec("alice"), 2))
		h.issuePage(issue(10, "alice"))
		all := []forgeapi.Issue{issue(10, "alice"), issue(11, "alice"), issue(12, "alice"), issue(13, "alice")}
		h.issuePage(all...)
		h.poll()
		h.poll()
		oc := h.cursorOf(r.ID).Opened
		if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"11", "12"}) {
			t.Fatalf("queued %v, want #11 and #12 up to the cap", got)
		}
		if oc.Floor != 10 || oc.PendingFloor != 12 || !eq(oc.Seen, []int{11, 12}) {
			t.Errorf("cursor = %+v, want floors stopped below #13 and #13 out of seen", oc)
		}
		h.dequeue(r.ID)
		h.issuePage(all...)
		h.poll()
		if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"12", "13"}) {
			t.Errorf("with room, queued %v, want #13 behind #12", got)
		}
	})
	t.Run("run concluded", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(fifo(runSpec("success"), 2))
		h.runPage()
		h.poll()
		created := pollStart.Add(time.Minute)
		all := []forgeapi.Run{
			run(41, forgeapi.CheckPassing, created), run(42, forgeapi.CheckPassing, created.Add(time.Second)),
			run(43, forgeapi.CheckPassing, created.Add(2*time.Second)),
		}
		h.runPage(all...)
		h.poll()
		if got := inputOf(h.queued(r.ID), "run_id"); !eq(got, []string{"41", "42"}) {
			t.Fatalf("queued %v, want 41 and 42 up to the cap", got)
		}
		c := h.cursorOf(r.ID).Runs
		if e := c.Runs[43]; e == nil || e.Hold != holdAtForge {
			t.Errorf("run 43 = %+v, want held at the forge", e)
		}
		if _, fired := c.Fired[43]; fired {
			t.Error("run 43 is in fired, want it matched again later")
		}
		if got := runBound([]*pollRule{(&poller{c: h.c}).load(&r, Feed{Connection: testConn, Selector: testRepo}, h.now)}, h.now, horizon(h.now)); got.After(created) {
			t.Errorf("bound = %v, want it held back to run 43's creation", got)
		}
		h.dequeue(r.ID)
		h.runPage(all...)
		h.poll()
		if got := inputOf(h.queued(r.ID), "run_id"); !eq(got, []string{"42", "43"}) {
			t.Errorf("with room, queued %v, want 43 behind 42", got)
		}
	})
}
