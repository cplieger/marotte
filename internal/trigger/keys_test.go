package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

func TestMixedCaseSelectorsShareFeedAndKey(t *testing.T) {
	h := newHarness(t)
	upper := runSpec("success")
	upper.Repo = "cplieger/Animap-Source"
	lower := runSpec("success")
	lower.Repo = "cplieger/animap-source"
	a, b := h.create(upper), h.create(lower)

	feeds := groupFeeds(h.store.List())
	if len(feeds) != 1 || len(feeds[0].Rules) != 2 || feeds[0].Feed.Selector != "cplieger/Animap-Source" {
		t.Fatalf("feeds = %+v, want one feed of both rules reading the earliest rule's selector", feeds)
	}
	h.runPage()
	h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(time.Minute)))
	h.poll()
	h.poll()
	if calls := h.src.recorded(); len(calls) != 2 {
		t.Errorf("two polls of the shared feed made %d calls, want one walk each", len(calls))
	}
	hex := forgeapi.RepoRef{Selector: "cplieger/animap-source"}.Encode()
	for _, id := range []string{a.ID, b.ID} {
		q := h.queued(id)
		if len(q) != 1 {
			t.Fatalf("rule %s queued %v, want run 41", id, keysOf(q))
		}
		if !strings.Contains(q[0].Key, "cplieger/animap-source") || strings.Contains(q[0].Key, hex) {
			t.Errorf("key %q, want the folded selector, not the repository id %q", q[0].Key, hex)
		}
		if got, want := q[0].Inputs["event_id"], "github.com/cplieger/animap-source/run/41/success"; got != want {
			t.Errorf("event_id = %q, want %q", got, want)
		}
	}
	if ka, kb := h.queued(a.ID)[0].Key, h.queued(b.ID)[0].Key; ka == kb {
		t.Errorf("both rules queued key %q, want each key to start with its own rule id", ka)
	}
}

func TestFeedsSkipPausedRules(t *testing.T) {
	h := newHarness(t)
	h.create(runSpec("success"))
	paused := h.create(labeledSpec())
	paused.Enabled = false
	if err := changeRule(t, h.c, paused.ID, 0, func(r *launchrule.Rule) { *r = paused }); err != nil {
		t.Fatal(err)
	}
	feeds := groupFeeds(h.store.List())
	if len(feeds) != 1 || len(feeds[0].Rules) != 1 || feeds[0].Rules[0].ID == paused.ID {
		t.Errorf("feeds = %+v, want the enabled run rule alone", feeds)
	}
}
