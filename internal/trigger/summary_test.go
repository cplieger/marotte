package trigger

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

func TestSummarizeCountsAndHeldEvents(t *testing.T) {
	t.Run("labeled", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(issue(11, "a"), issue(12, "a"))
		h.issuePage(issue(11, "a"), issue(12, "a", testLabel))
		h.poll()
		at := h.now
		h.poll()
		st, err := h.store.State(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		held, c, err := Summarize(&st, h.now)
		if err != nil {
			t.Fatal(err)
		}
		if len(held) != 1 || held[0].Key != h.queued(r.ID)[0].Key || !held[0].ObservedAt.Equal(at) {
			t.Errorf("held = %+v, want #12's gain observed at %v", held, at)
		}
		if !c.Baselined || c.Items != 2 || c.Blocked != "" {
			t.Errorf("cursor = %+v, want baselined with 2 items and no block", c)
		}
	})
	t.Run("before the baseline", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(openedSpec("alice"))
		st, err := h.store.State(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if held, c, err := Summarize(&st, h.now); err != nil || len(held) != 0 || c.Baselined {
			t.Errorf("Summarize = %+v, %+v, %v; want nothing held and no baseline", held, c, err)
		}
	})
	t.Run("run notes", func(t *testing.T) {
		h, id, runs := overForge(t, runSpec("success"))
		*runs = append(*runs, run(41, forgeapi.CheckUnknown, pollStart.Add(time.Hour)))
		h.pollAt(pollStart.Add(2 * time.Hour))
		h.pollAt(pollStart.Add(27 * time.Hour))
		st, err := h.store.State(id)
		if err != nil {
			t.Fatal(err)
		}
		_, c, err := Summarize(&st, h.now)
		if err != nil || c.Watching != 1 || !eq(c.Notes, []string{"run 41 reads an unknown state"}) {
			t.Errorf("Summarize = %+v, %v; want run 41 watched and named", c, err)
		}
	})
}
