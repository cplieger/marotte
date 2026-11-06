package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/launchrule"
)

func TestLabelGainOnce(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"))
	h.issuePage(issue(12, "alice", "REPORT:Fast"))
	h.issuePage(issue(12, "alice", testLabel))
	h.poll()
	gainAt := h.now
	h.poll()
	h.poll()

	q := h.queued(r.ID)
	if len(q) != 1 {
		t.Fatalf("queued %v, want #12's gain once", keysOf(q))
	}
	gain := "20261105T100500000Z"
	if !gainAt.Equal(pollStart.Add(5*time.Minute)) || !strings.HasSuffix(q[0].Key, gain) {
		t.Errorf("key %q, want it to end with the gain minted at the second poll's start, %s", q[0].Key, gain)
	}
	want := "github.com/cplieger/animap/issue/12/labeled/" + r.ID + "/" + gain
	if got := q[0].Inputs["event_id"]; got != want {
		t.Errorf("event_id = %q, want %q", got, want)
	}
	if q[0].Inputs["issue_number"] != "12" || q[0].Inputs["label"] != testLabel {
		t.Errorf("inputs = %v, want issue_number 12 and the rule's own label", q[0].Inputs)
	}
}

func TestLabelRemoveReaddFiresAgain(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"))
	h.issuePage(issue(12, "alice", testLabel))
	h.issuePage(issue(12, "alice"))
	h.issuePage(issue(12, "alice", testLabel))
	h.issuePage(issue(12, "alice", testLabel))
	for range 5 {
		h.poll()
	}
	q := h.queued(r.ID)
	if len(q) != 2 {
		t.Fatalf("queued %v, want two gains: the label added, removed and added again", keysOf(q))
	}
	if q[0].Inputs["event_id"] == q[1].Inputs["event_id"] {
		t.Errorf("both gains carry event_id %q, want the second a new one", q[0].Inputs["event_id"])
	}
}

func TestLabeledEventIDNotReusedAfterRecreate(t *testing.T) {
	t.Run("a deleted and re-created rule", func(t *testing.T) {
		h := newHarness(t)
		first := h.create(labeledSpec())
		h.issuePage(issue(12, "alice"))
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		h.poll()
		old := inputOf(h.queued(first.ID), "event_id")
		deleteRule(t, h.c, first.ID)
		second := h.create(labeledSpec())
		h.now = pollStart
		h.issuePage(issue(12, "alice"))
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		h.poll()
		got := inputOf(h.queued(second.ID), "event_id")
		if len(old) != 1 || len(got) != 1 || got[0] == old[0] {
			t.Errorf("event ids before %v and after %v the re-create, want one each and different", old, got)
		}
	})
	t.Run("a rebaseline edit", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(issue(12, "alice"))
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		h.poll()
		old := inputOf(h.queued(r.ID), "event_id")
		edited := r
		edited.When.Event.Repo = "cplieger/other"
		if err := changeRule(t, h.c, r.ID, 1, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatal(err)
		}
		edited.When.Event.Repo = testRepo
		if err := changeRule(t, h.c, r.ID, 0, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatal(err)
		}
		h.now = pollStart
		h.issuePage(issue(12, "alice"))
		h.issuePage(issue(12, "alice", testLabel))
		h.issuePage(issue(12, "alice", testLabel))
		h.poll()
		h.poll()
		h.poll()
		got := inputOf(h.queued(r.ID), "event_id")
		if len(old) != 1 || len(got) != 1 || got[0] == old[0] {
			t.Errorf("event ids before %v and after %v the rebaseline, want one each and different", old, got)
		}
	})
}

func TestLabeledGainAfter30DayDropQueued(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"))
	h.issuePage(issue(12, "alice", testLabel))
	h.poll()
	h.poll()
	es := h.state(r.ID)
	if len(es.queue) != 1 {
		t.Fatalf("Setup: queued %v, want #12's gain", keysOf(es.queue))
	}
	// The gain launched, then #12 closed and its entry was dropped 30 days later.
	old := es.queue[0]
	es.launched = []launchedEntry{{Key: old.Key, CoalesceKey: old.CoalesceKey, Inputs: old.Inputs, WorkflowID: "wf-1"}}
	es.queue = nil
	delete(es.cursor[Feed{Selector: testRepo}.RepoID()].Labeled.Items, 12)
	h.seed(r.ID, &es)

	h.now = pollStart.Add(31 * 24 * time.Hour)
	h.issuePage(issue(12, "alice", testLabel))
	h.poll()
	q := h.queued(r.ID)
	if len(q) != 1 || q[0].Key == old.Key {
		t.Errorf("queued %v after the reopen, want one new gain beside the launched %q", keysOf(q), old.Key)
	}
}

func TestGainMintMonotonic(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"), issue(13, "bob"), issue(14, "carol"))
	h.issuePage(issue(12, "alice", testLabel), issue(13, "bob"), issue(14, "carol"))
	h.poll()
	h.poll()
	first := h.state(r.ID).raw.Status.LastMinted
	if want := pollStart.Add(5 * time.Minute); !first.Equal(want) {
		t.Fatalf("last_minted = %v, want the gain's poll start %v", first, want)
	}

	h.reopen()
	h.now = pollStart.Add(-time.Hour)
	h.issuePage(issue(12, "alice", testLabel), issue(13, "bob", testLabel), issue(14, "carol"))
	h.poll()
	second := h.state(r.ID).raw.Status.LastMinted
	if want := first.Add(time.Millisecond); !second.Equal(want) {
		t.Errorf("after a reload and a clock stepped back, last_minted = %v, want %v", second, want)
	}
	q := h.queued(r.ID)
	if len(q) != 2 || !strings.HasSuffix(q[1].Key, formatGain(second)) {
		t.Errorf("queued %v, want #13's gain minted at %s", keysOf(q), formatGain(second))
	}

	t.Run("kept across a rebaseline and dropped entries", func(t *testing.T) {
		edited := r
		edited.When.Event.Repo = "cplieger/other"
		if err := changeRule(t, h.c, r.ID, 2, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatal(err)
		}
		edited.When.Event.Repo = testRepo
		if err := changeRule(t, h.c, r.ID, 0, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatal(err)
		}
		if got := h.state(r.ID).raw.Status.LastMinted; !got.Equal(second) {
			t.Fatalf("the rebaseline left last_minted %v, want %v", got, second)
		}
		h.now = pollStart.Add(-2 * time.Hour)
		h.issuePage(issue(14, "carol"))
		h.issuePage(issue(14, "carol", testLabel))
		h.poll()
		h.poll()
		if got := h.state(r.ID).raw.Status.LastMinted; !got.Equal(second.Add(time.Millisecond)) {
			t.Errorf("the next mint after the rebaseline = %v, want %v", got, second.Add(time.Millisecond))
		}
	})
}

func TestMintGain(t *testing.T) {
	last := time.Date(2026, 11, 4, 10, 15, 30, 123_000_000, time.UTC)
	for name, tc := range map[string]struct{ start, want time.Time }{
		"later":        {last.Add(time.Second), last.Add(time.Second)},
		"equal":        {last, last.Add(time.Millisecond)},
		"stepped back": {last.Add(-time.Hour), last.Add(time.Millisecond)},
		"sub-ms later": {last.Add(400 * time.Microsecond), last.Add(time.Millisecond)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mintGain(tc.start, last); !got.Equal(tc.want) {
				t.Errorf("mintGain = %v, want %v", got, tc.want)
			}
		})
	}
	if got := formatGain(last); got != "20261104T101530123Z" {
		t.Errorf("formatGain = %q, want 20261104T101530123Z", got)
	}
}
