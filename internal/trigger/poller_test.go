package trigger

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// startRunner runs tc's launcher over a new fake and the runner polling src, until the test ends. Polls start at
// once; nothing launches before tc.engineReady or tc.answerCancels. It runs inside synctest.Test only.
func (tc *testConfirmer) startRunner(src FeedSource, answer func(n int, call launchCall) (string, error)) *fakeLauncher {
	f := &fakeLauncher{tc: tc, answer: answer}
	tc.run()
	ll := NewLauncher(tc.Confirmer, f)
	rn := NewRunner(tc.Confirmer, ll, src, NewBudget(), tc.dir)
	ctx, cancel := context.WithCancel(context.WithoutCancel(tc.t.Context()))
	var wg sync.WaitGroup
	wg.Go(func() { _ = ll.Run(ctx, tc.ready) })
	wg.Go(func() { rn.Run(ctx) })
	tc.t.Cleanup(func() { cancel(); wg.Wait() })
	return f
}

// gatedSource answers through src, holding every call after the first free ones until open is closed, so a test
// can act while a poll's reads are in flight.
type gatedSource struct {
	*fakeSource
	open  chan struct{}
	free  int
	calls int
	mu    sync.Mutex
}

func (s *gatedSource) wait() {
	s.mu.Lock()
	s.calls++
	held := s.calls > s.free
	s.mu.Unlock()
	if held {
		<-s.open
	}
}

func (s *gatedSource) OpenIssues(ctx context.Context, f Feed, after forgeapi.Cursor) (forgeapi.Page[forgeapi.Issue], error) {
	s.wait()
	return s.fakeSource.OpenIssues(ctx, f, after)
}

func TestPollDropsOnlyTheEditedRuleOnSharedFeed(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		edited, kept := tc.createRule(labeledSpec()), tc.createRule(labeledSpec())
		src := &gatedSource{
			fakeSource: &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{
				{Items: []forgeapi.Issue{issue(7, "alice")}},
				{Items: []forgeapi.Issue{issue(7, "alice", testLabel)}},
			}},
			open: make(chan struct{}), free: 1,
		}
		tc.startRunner(src, nil)
		synctest.Wait()
		for _, r := range []launchrule.Rule{edited, kept} {
			if rc := tc.memoryState(r.ID).cursor[Feed{Selector: testRepo}.repoID()]; rc == nil || rc.Labeled == nil {
				t.Fatalf("Setup: rule %s's cursor = %+v after the first poll, want its baseline", r.ID, rc)
			}
		}
		advance(pollTick)
		if n := len(src.recorded()); n != 1 {
			t.Fatalf("Setup: %d list calls answered, want the second poll's held in flight", n)
		}
		edit(tc, &edited, 0, relabel)
		close(src.open)
		synctest.Wait()
		if n := len(src.recorded()); n != 2 {
			t.Errorf("%d issue list calls over two polls of two rules, want 2: one read a poll for the feed", n)
		}
		if q := tc.memoryState(kept.ID).queue; len(q) != 1 {
			t.Errorf("unedited rule's queue = %v, want #7's label gain from the shared read", keysOf(q))
		}
		es := tc.memoryState(edited.ID)
		if len(es.queue) != 0 {
			t.Errorf("edited rule's queue = %v, want empty: its part was read under the old label", keysOf(es.queue))
		}
		if rc := es.cursor[Feed{Selector: testRepo}.repoID()]; rc != nil && rc.Labeled != nil {
			t.Errorf("edited rule's labeled cursor = %+v, want the edit's reset left as it is", rc.Labeled)
		}
	})
}

func TestPollerSkipsUnreadableRule(t *testing.T) {
	inBubble(t, func(t *testing.T, tc *testConfirmer) {
		unreadable := tc.createRule(openedSpec("alice"))
		readable := tc.createRule(openedSpec("alice"))
		bootUnreadable(tc, &unreadable)
		src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{
			{Items: []forgeapi.Issue{issue(5, "alice")}},
			{Items: []forgeapi.Issue{issue(5, "alice"), issue(6, "alice")}},
		}}
		tc.startRunner(src, nil)
		synctest.Wait()
		advance(pollTick)
		if n := len(src.recorded()); n != 2 {
			t.Fatalf("%d issue list calls, want one a poll", n)
		}
		if q := tc.memoryState(readable.ID).queue; len(q) != 1 {
			t.Errorf("readable rule's queue = %v, want #6 from the feed's second poll", keysOf(q))
		}
		if got, err := os.ReadFile(statePath(tc, unreadable.ID)); err != nil || string(got) != unparseable {
			t.Errorf("unreadable rule's state file = %q, %v; want its bytes unchanged", got, err)
		}
		if es := tc.memoryState(unreadable.ID); len(es.cursor) != 0 || len(es.queue) != 0 {
			t.Errorf("unreadable rule in memory: cursor %d repositories, queue %v; want neither", len(es.cursor),
				keysOf(es.queue))
		}
		if n := tc.log.CountExact(logMsgNotWritten); n != 0 {
			t.Errorf("%d ERROR %q lines, want none: a write over the unreadable file was tried", n, logMsgNotWritten)
		}
	})
}

func TestBlockedRuleKeepsPolling(t *testing.T) {
	tests := []struct {
		setup  func(tc *testConfirmer, ruleID string)
		block  func(n int, call launchCall) (string, error)
		name   string
		reason string
	}{
		{
			name: "blocked by the 24-hour clock",
			setup: func(tc *testConfirmer, ruleID string) {
				tc.setStatus(ruleID, func(st *launchrule.Status) { st.FailingSince = time.Now().Add(-failingBlockAfter) })
			},
			block:  func(n int, _ launchCall) (string, error) { return "", fmt.Errorf("fault %d", n) },
			reason: "fault 1",
		},
		{
			name:   "blocked by a -32602 refusal",
			setup:  func(*testConfirmer, string) {},
			block:  func(int, launchCall) (string, error) { return "", invalidParams },
			reason: "inputs.run_id: Required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inBubble(t, func(t *testing.T, tc *testConfirmer) {
				r := tc.createRule(openedSpec("alice"))
				tt.setup(tc, r.ID)
				src := &fakeSource{issues: []forgeapi.Page[forgeapi.Issue]{
					{Items: []forgeapi.Issue{issue(5, "alice")}},
					{Items: []forgeapi.Issue{issue(5, "alice"), issue(6, "alice")}},
					{Items: []forgeapi.Issue{issue(5, "alice"), issue(6, "alice"), issue(7, "alice")}},
				}}
				f := tc.startRunner(src, func(n int, call launchCall) (string, error) {
					if n == 1 {
						return tt.block(n, call)
					}
					wf := fmt.Sprintf("wf_%d", n)
					return wf, tc.NameAttempt(call.ruleID, call.key, wf)
				})
				tc.letLaunch(nil)
				advance(pollTick)
				if _, st := tc.memory(r.ID); st.Blocked != tt.reason {
					t.Fatalf("Setup: block = %q after #6 launched, want %q", st.Blocked, tt.reason)
				}
				advance(pollTick)
				es := tc.memoryState(r.ID)
				if rc := es.cursor[Feed{Selector: testRepo}.repoID()]; rc == nil || rc.Opened == nil || rc.Opened.PendingFloor != 7 {
					t.Errorf("cursor = %+v after the blocked rule's poll, want it read up to #7", rc)
				}
				if len(es.queue) != 2 {
					t.Errorf("queue = %v, want #6 and #7 held", keysOf(es.queue))
				}
				if n := len(f.taken()); n != 1 {
					t.Errorf("%d calls while blocked, want only the one that blocked it", n)
				}
				edit(tc, &r, 0, func(r *launchrule.Rule) { r.Enabled = false })
				edit(tc, &r, 0, func(r *launchrule.Rule) { r.Enabled = true })
				synctest.Wait()
				if calls := f.taken(); len(calls) != 2 || calls[1].key != es.queue[0].Key {
					t.Errorf("calls after the resume = %+v, want one more, for the head of the queue", calls)
				}
			})
		})
	}
}

func eventRule(id string, intervalMin int) launchrule.Rule {
	spec := openedSpec("alice")
	spec.Repo, spec.IntervalMin = id, intervalMin
	return launchrule.Rule{ID: id, Enabled: true, When: launchrule.When{Event: spec}}
}

func dueKeys(s *feedSchedule, rules []launchrule.Rule, now time.Time) []string {
	var out []string
	for _, fr := range s.due(groupFeeds(rules), now) {
		out = append(out, fr.Feed.Selector)
	}
	return out
}

func TestFeedSchedule(t *testing.T) {
	t0 := pollStart
	rules := []launchrule.Rule{eventRule("o/a", 5), eventRule("o/b", 5)}
	t.Run("the feeds of the first pass are staggered across their interval", func(t *testing.T) {
		s := newFeedSchedule()
		if got := dueKeys(s, rules, t0); !slices.Equal(got, []string{"o/a"}) {
			t.Errorf("due at the start = %v, want only the first feed", got)
		}
		if got := s.wait(t0); got != rescanEvery {
			t.Errorf("wait with the second feed 2m30s away = %v, want the rescan bound %v", got, rescanEvery)
		}
		if got := dueKeys(s, rules, t0.Add(150*time.Second-time.Second)); len(got) != 0 {
			t.Errorf("due just before half the interval = %v, want none", got)
		}
		if got := dueKeys(s, rules, t0.Add(150*time.Second)); !slices.Equal(got, []string{"o/b"}) {
			t.Errorf("due at half the interval = %v, want the second feed", got)
		}
	})
	t.Run("the interval is timed from the end of the last poll", func(t *testing.T) {
		s := newFeedSchedule()
		dueKeys(s, rules[:1], t0)
		if got := dueKeys(s, rules[:1], t0.Add(10*time.Minute)); len(got) != 0 {
			t.Errorf("due while the first poll is in flight = %v, want none", got)
		}
		s.ended(groupFeeds(rules[:1])[0].Feed.key(), t0.Add(time.Minute))
		if got := dueKeys(s, rules[:1], t0.Add(6*time.Minute-time.Second)); len(got) != 0 {
			t.Errorf("due under 5 minutes after the poll ended = %v, want none", got)
		}
		if got := s.wait(t0.Add(6*time.Minute - 30*time.Second)); got != 30*time.Second {
			t.Errorf("wait 30s before the feed is due = %v, want 30s", got)
		}
		if got := dueKeys(s, rules[:1], t0.Add(6*time.Minute)); !slices.Equal(got, []string{"o/a"}) {
			t.Errorf("due 5 minutes after the poll ended = %v, want the feed", got)
		}
	})
	t.Run("a feed polls at the shortest interval of its rules", func(t *testing.T) {
		s := newFeedSchedule()
		shared := []launchrule.Rule{eventRule("o/a", 5), eventRule("o/a", 2)}
		dueKeys(s, shared, t0)
		s.ended(groupFeeds(shared)[0].Feed.key(), t0)
		if got := dueKeys(s, shared, t0.Add(2*time.Minute)); !slices.Equal(got, []string{"o/a"}) {
			t.Errorf("due 2 minutes on with a 2-minute rule = %v, want the feed", got)
		}
	})
	t.Run("a feed found after the first pass polls at once", func(t *testing.T) {
		s := newFeedSchedule()
		dueKeys(s, rules[:1], t0)
		if got := dueKeys(s, rules, t0.Add(time.Second)); !slices.Equal(got, []string{"o/b"}) {
			t.Errorf("due once a second feed appears = %v, want it at once", got)
		}
	})
}
