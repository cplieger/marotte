package trigger

import (
	"errors"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

func TestBaselineFiresNothing(t *testing.T) {
	t.Run("labeled", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(labeledSpec())
		h.issuePage(issue(12, "alice", testLabel), issue(13, "bob"))
		h.issuePage(issue(12, "alice", testLabel), issue(13, "bob"))
		h.poll()
		h.poll()
		if q := h.queued(r.ID); len(q) != 0 {
			t.Errorf("queued %v, want nothing: an item labelled at baseline is no gain", keysOf(q))
		}
		lc := h.cursorOf(r.ID).Labeled
		if lc == nil || lc.Items[12].HasLabel != labelYes || lc.Items[13].HasLabel != labelNo {
			t.Errorf("labeled cursor = %+v, want #12 true and #13 false", lc)
		}
	})
	t.Run("opened", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(openedSpec("alice"))
		h.issuePage(issue(9, "alice"), issue(12, "alice"))
		h.issuePage(issue(9, "alice"), issue(12, "alice"))
		h.poll()
		h.poll()
		if q := h.queued(r.ID); len(q) != 0 {
			t.Errorf("queued %v, want nothing: every open item existed at baseline", keysOf(q))
		}
		if oc := h.cursorOf(r.ID).Opened; oc == nil || oc.Floor != 12 {
			t.Errorf("opened cursor = %+v, want floor 12, the highest number read", oc)
		}
	})
	t.Run("run concluded", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(runSpec("success"))
		h.runPage(run(41, forgeapi.CheckPassing, pollStart.Add(-time.Hour)))
		h.poll()
		if q := h.queued(r.ID); len(q) != 0 {
			t.Errorf("queued %v, want nothing", keysOf(q))
		}
		rc := h.cursorOf(r.ID).Runs
		if rc == nil || !rc.BaselineAt.Equal(pollStart) || !rc.LastComplete.Equal(pollStart) {
			t.Errorf("run cursor = %+v, want baseline_at and last_complete at the poll's start", rc)
		}
	})
}

func TestPartialBaselineRetries(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.src.issues = append(h.src.issues, forgeapi.Page[forgeapi.Issue]{
		Items: []forgeapi.Issue{issue(12, "alice")}, Partial: &forgeapi.Partial{Reason: forgeapi.PartialBudget},
	})
	h.poll()
	if rc := h.cursorOf(r.ID); rc != nil && rc.Labeled != nil {
		t.Fatalf("a partial walk took the baseline: %+v", rc.Labeled)
	}

	h.issuePage(issue(12, "alice"), issue(13, "bob", testLabel))
	h.poll()
	if q := h.queued(r.ID); len(q) != 0 {
		t.Errorf("the retried baseline queued %v, want nothing", keysOf(q))
	}
	if rc := h.cursorOf(r.ID); rc == nil || rc.Labeled == nil || len(rc.Labeled.Items) != 2 {
		t.Fatalf("the complete walk did not take the baseline: %+v", rc)
	}

	h.issuePage(issue(12, "alice", testLabel), issue(13, "bob", testLabel))
	h.poll()
	if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"12"}) {
		t.Errorf("after the baseline queued issues %v, want only #12's gain", got)
	}

	t.Run("a read error is partial too", func(t *testing.T) {
		h := newHarness(t)
		r := h.create(openedSpec("alice"))
		h.src.errs = map[string]error{"issues": errors.New("502")}
		h.poll()
		if rc := h.cursorOf(r.ID); rc != nil && rc.Opened != nil {
			t.Errorf("a failed read took the baseline: %+v", rc.Opened)
		}
	})
}

func TestEditFilterKeepsCursor(t *testing.T) {
	h := newHarness(t)
	r := h.create(openedSpec("alice"))
	h.issuePage(issue(10, "alice"))
	h.poll()

	edited := r
	edited.When.Event.Filter.Authors = []string{"alice", "bob"}
	if err := changeRule(t, h.c, r.ID, 0, func(r *launchrule.Rule) { *r = edited }); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if oc := h.cursorOf(r.ID).Opened; oc == nil || oc.Floor != 10 {
		t.Fatalf("the filter edit left the opened cursor %+v, want floor 10 kept", oc)
	}

	h.issuePage(issue(10, "alice"), issue(11, "bob"))
	h.poll()
	if got := inputOf(h.queued(r.ID), "author"); !eq(got, []string{"bob"}) {
		t.Errorf("after the edit queued authors %v, want bob's #11, with no new baseline", got)
	}

	t.Run("a branch edit on a pull request rule", func(t *testing.T) {
		h := newHarness(t)
		ev := labeledSpec()
		ev.Kind = launchrule.KindPRLabeled
		r := h.create(ev)
		h.prPage(pull(5, "alice", "main"), pull(6, "alice", "dev"))
		h.poll()
		edited := r
		edited.When.Event.Filter.Branch = "dev"
		if err := changeRule(t, h.c, r.ID, 0, func(r *launchrule.Rule) { *r = edited }); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		h.prPage(pull(5, "alice", "main", testLabel), pull(6, "alice", "dev", testLabel))
		h.poll()
		if got := inputOf(h.queued(r.ID), "pr_number"); !eq(got, []string{"6"}) {
			t.Errorf("queued pull requests %v, want #6 into dev only, from the kept cursor", got)
		}
	})
}

func TestChangeRepoRebaselines(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice"))
	h.issuePage(issue(12, "alice", testLabel))
	h.poll()
	h.poll()
	if q := h.queued(r.ID); len(q) != 1 {
		t.Fatalf("Setup: queued %v, want #12's gain", keysOf(q))
	}
	minted := h.state(r.ID).raw.Status.LastMinted

	edited := r
	edited.When.Event.Repo = "cplieger/other"
	if err := changeRule(t, h.c, r.ID, 0, func(r *launchrule.Rule) { *r = edited }); !errors.As(err, new(*launchrule.DropError)) {
		t.Fatalf("Replace without confirm_drop = %v, want a DropError naming the held event", err)
	}
	if err := changeRule(t, h.c, r.ID, 1, func(r *launchrule.Rule) { *r = edited }); err != nil {
		t.Fatalf("Replace with confirm_drop 1: %v", err)
	}
	es := h.state(r.ID)
	if len(es.cursor) != 0 || len(es.queue) != 0 || !es.raw.Status.LastMinted.Equal(minted) {
		t.Errorf("after the repo change: cursor %v, queue %v, last_minted %v; want both empty and last_minted %v",
			es.cursor, keysOf(es.queue), es.raw.Status.LastMinted, minted)
	}

	h.issuePage(issue(3, "alice", testLabel))
	h.poll()
	if q := h.queued(r.ID); len(q) != 0 {
		t.Errorf("the new repository's first poll queued %v, want a baseline that fires nothing", keysOf(q))
	}
	other := Feed{Selector: "cplieger/other"}.repoID()
	if rc := h.state(r.ID).cursor[other]; rc == nil || rc.Labeled == nil || rc.Labeled.Items[3].HasLabel != labelYes {
		t.Errorf("the new repository's cursor = %+v, want its baseline", rc)
	}
}
