package trigger

import (
	"testing"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// prLabeledOn is a harness with a pr_labeled rule on connection.
func prLabeledOn(t *testing.T, connection string) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	ev := labeledSpec()
	ev.Kind, ev.Connection = launchrule.KindPRLabeled, connection
	return h, h.create(ev).ID
}

// marked is a pull request row carrying its own partial marker.
func marked(p forgeapi.PullRequest, reason forgeapi.PartialReason) forgeapi.PullRequest {
	p.Partial = &forgeapi.Partial{Reason: reason, OmittedAtLeast: 3}
	return p
}

func (h *harness) labelOf(ruleID string, number int) *labelEntry {
	h.t.Helper()
	rc := h.cursorOf(ruleID)
	if rc == nil || rc.Labeled == nil {
		return nil
	}
	return rc.Labeled.Items[number]
}

func TestPartialPageKeepsUnreadEntries(t *testing.T) {
	h := newHarness(t)
	r := h.create(labeledSpec())
	h.issuePage(issue(12, "alice", testLabel), issue(13, "bob"))
	h.poll()
	h.src.issues = append(h.src.issues, partialIssues(issue(13, "bob", testLabel)))
	h.poll()
	if e := h.labelOf(r.ID, 12); e == nil || e.HasLabel != labelYes || !e.LastSeen.Equal(pollStart) {
		t.Errorf("#12, unread by the partial walk = %+v, want it as the baseline left it", e)
	}
	if got := inputOf(h.queued(r.ID), "issue_number"); !eq(got, []string{"13"}) {
		t.Errorf("the partial walk queued %v, want the gain it read on #13", got)
	}
}

func TestPartialLabelsKeepEntry(t *testing.T) {
	h, id := prLabeledOn(t, "gitlab:gitlab.com")
	h.prPage(pull(5, "alice", "main", testLabel), pull(6, "alice", "main"))
	h.prPage(marked(pull(5, "alice", "main"), forgeapi.PartialPaginationCap),
		marked(pull(6, "alice", "main", testLabel), forgeapi.PartialPaginationCap))
	h.poll()
	h.poll()
	if e := h.labelOf(id, 5); e == nil || e.HasLabel != labelYes {
		t.Errorf("#5 after a cut row without the label = %+v, want has_label true kept", e)
	}
	if got := inputOf(h.queued(id), "pr_number"); !eq(got, []string{"6"}) {
		t.Errorf("queued %v, want #6, whose cut row carries the label", got)
	}
}

func TestCutBaselineRowUnknownSettlesSilently(t *testing.T) {
	h, id := prLabeledOn(t, "gitlab:gitlab.com")
	h.prPage(marked(pull(5, "alice", "main"), forgeapi.PartialPaginationCap),
		marked(pull(7, "alice", "main"), forgeapi.PartialPaginationCap))
	h.poll()
	for _, n := range []int{5, 7} {
		if e := h.labelOf(id, n); e == nil || e.HasLabel != labelUnknown {
			t.Fatalf("#%d at baseline = %+v, want has_label unknown", n, e)
		}
	}
	h.prPage(pull(5, "alice", "main", testLabel), pull(7, "alice", "main"))
	h.poll()
	if q := h.queued(id); len(q) != 0 {
		t.Errorf("settling queued %v, want nothing", keysOf(q))
	}
	if e5, e7 := h.labelOf(id, 5), h.labelOf(id, 7); e5.HasLabel != labelYes || e7.HasLabel != labelNo {
		t.Errorf("after whole rows: #5 %s, #7 %s; want true and false", e5.HasLabel, e7.HasLabel)
	}
	h.prPage(pull(5, "alice", "main", testLabel), pull(7, "alice", "main", testLabel))
	h.poll()
	if got := inputOf(h.queued(id), "pr_number"); !eq(got, []string{"7"}) {
		t.Errorf("queued %v, want #7's gain once it settled false", got)
	}
}

func TestCheckFoldPartialLabelsWhole(t *testing.T) {
	for name, tc := range map[string]struct {
		connection string
		reason     forgeapi.PartialReason
	}{
		"a GitHub check-name fold": {testConn, forgeapi.PartialPaginationCap},
		"a Gitea status fold":      {"gitea:gitea.example", forgeapi.PartialPaginationCap},
		"a refused check rollup":   {testConn, forgeapi.PartialForbidden},
		"a GitLab fork lookup":     {"gitlab:gitlab.com", forgeapi.PartialBudget},
	} {
		t.Run(name, func(t *testing.T) {
			h, id := prLabeledOn(t, tc.connection)
			h.prPage(pull(5, "alice", "main", testLabel))
			h.prPage(marked(pull(5, "alice", "main"), tc.reason))
			h.poll()
			h.poll()
			if e := h.labelOf(id, 5); e == nil || e.HasLabel != labelNo {
				t.Errorf("#5 after a whole row without the label = %+v, want has_label false", e)
			}
		})
	}
}

func TestGitHubCheckFoldPartialStillGainsLabel(t *testing.T) {
	h, id := prLabeledOn(t, testConn)
	fold := func(n int, labels ...string) forgeapi.PullRequest {
		return marked(pull(n, "alice", "main", labels...), forgeapi.PartialPaginationCap)
	}
	h.prPage(fold(5))
	h.prPage(fold(5, testLabel), fold(6, testLabel))
	h.prPage(fold(5), fold(6, testLabel))
	h.prPage(fold(5, testLabel), fold(6, testLabel))
	for range 4 {
		h.poll()
	}
	if got := inputOf(h.queued(id), "pr_number"); !eq(got, []string{"5", "6", "5"}) {
		t.Errorf("queued %v, want #5 from false, #6 from no entry, then #5 again after its removal", got)
	}
}

func TestGraphQLPartialPageReadAsCut(t *testing.T) {
	for _, connection := range []string{testConn, "gitlab:gitlab.com"} {
		t.Run(connection, func(t *testing.T) {
			h, id := prLabeledOn(t, connection)
			graphql := func(items ...forgeapi.PullRequest) forgeapi.Page[forgeapi.PullRequest] {
				return forgeapi.Page[forgeapi.PullRequest]{
					Items: items, Partial: &forgeapi.Partial{Reason: forgeapi.PartialGraphQLPartial},
				}
			}
			h.src.prs = append(h.src.prs, graphql(pull(5, "alice", "main", testLabel)))
			h.poll()
			if rc := h.cursorOf(id); rc != nil && rc.Labeled != nil {
				t.Fatalf("a GraphQL-partial walk took the baseline: %+v", rc.Labeled)
			}
			h.prPage(pull(5, "alice", "main", testLabel), pull(6, "alice", "main"))
			h.poll()
			h.src.prs = append(h.src.prs, graphql(pull(5, "alice", "main"), pull(6, "alice", "main", testLabel)))
			h.poll()
			if e := h.labelOf(id, 5); e == nil || e.HasLabel != labelYes {
				t.Errorf("#5 absent on a GraphQL-partial page = %+v, want has_label true kept", e)
			}
			if got := inputOf(h.queued(id), "pr_number"); !eq(got, []string{"6"}) {
				t.Errorf("queued %v, want #6, present on the partial page", got)
			}
		})
	}
	t.Run("an opened rule's floor stays", func(t *testing.T) {
		h := newHarness(t)
		ev := openedSpec("alice")
		ev.Kind = launchrule.KindPROpened
		r := h.create(ev)
		h.prPage(pull(5, "alice", "main"))
		h.poll()
		h.src.prs = append(h.src.prs, forgeapi.Page[forgeapi.PullRequest]{
			Items: []forgeapi.PullRequest{pull(8, "alice", "main")}, Partial: &forgeapi.Partial{Reason: forgeapi.PartialGraphQLPartial},
		})
		h.poll()
		if oc := h.cursorOf(r.ID).Opened; oc.Floor != 5 || oc.PendingFloor != 5 {
			t.Errorf("after a GraphQL-partial walk: %+v, want floor and pending_floor 5", oc)
		}
	})
}
