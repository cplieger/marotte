package trigger

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/marotte/internal/launchrule"
)

// pollStart is the first poll's start in every test; later polls advance from it.
var pollStart = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

const (
	testConn     = "github:github.com"
	testRepo     = "cplieger/animap"
	testWorkflow = ".github/workflows/weekly.yaml"
	testLabel    = "report:fast"
)

// harness polls a real launch-rule store in a temporary config dir through a fake source.
type harness struct {
	t     *testing.T
	store *launchrule.Store
	src   *fakeSource
	marks *launchMarks
	now   time.Time
	dir   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), src: &fakeSource{}, marks: newLaunchMarks(), now: pollStart}
	h.reopen()
	return h
}

// reopen loads the store again from disk, as a restart does.
func (h *harness) reopen() {
	h.t.Helper()
	st, err := launchrule.NewStore(h.dir)
	if err != nil {
		h.t.Fatalf("Setup: NewStore: %v", err)
	}
	h.store = st
}

func (h *harness) create(ev *launchrule.EventSpec) launchrule.Rule {
	h.t.Helper()
	r, err := h.store.Create(h.t.Context(), &launchrule.Rule{
		Source: "bundled://weekly", Name: "weekly", Enabled: true, When: launchrule.When{Event: ev},
	})
	if err != nil {
		h.t.Fatalf("Setup: Create: %v", err)
	}
	return r
}

// poll runs one poll of every feed at h.now, then moves h.now on by five minutes.
func (h *harness) poll() {
	h.t.Helper()
	p := &Poller{src: h.src, store: h.store, now: func() time.Time { return h.now }, marks: h.marks}
	for _, fr := range Feeds(h.store.List()) {
		p.Poll(h.t.Context(), fr)
	}
	h.now = h.now.Add(5 * time.Minute)
}

func (h *harness) state(ruleID string) eventState {
	h.t.Helper()
	st, err := h.store.State(ruleID)
	if err != nil {
		h.t.Fatalf("State(%s): %v", ruleID, err)
	}
	es, err := decodeState(&st)
	if err != nil {
		h.t.Fatalf("decodeState(%s): %v", ruleID, err)
	}
	return es
}

// seed writes es as the rule's state file.
func (h *harness) seed(ruleID string, es *eventState) {
	h.t.Helper()
	st, err := es.encode()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.WriteState(h.t.Context(), ruleID, &st); err != nil {
		h.t.Fatalf("Setup: WriteState: %v", err)
	}
}

func (h *harness) queued(ruleID string) []queueEntry {
	h.t.Helper()
	return h.state(ruleID).queue
}

func (h *harness) cursorOf(ruleID string) *repoCursor {
	h.t.Helper()
	return h.state(ruleID).cursor[Feed{Selector: testRepo}.RepoID()]
}

// The pages below are each one complete call of their list, served in order across polls.

func (h *harness) issuePage(items ...forgeapi.Issue) {
	h.src.issues = append(h.src.issues, forgeapi.Page[forgeapi.Issue]{Items: items})
}

func (h *harness) prPage(items ...forgeapi.PullRequest) {
	h.src.prs = append(h.src.prs, forgeapi.Page[forgeapi.PullRequest]{Items: items})
}

func (h *harness) runPage(items ...forgeapi.Run) {
	h.src.runs = append(h.src.runs, forgeapi.Page[forgeapi.Run]{Items: items})
}

func issue(number int, author string, labels ...string) forgeapi.Issue {
	ls := make([]forgeapi.Label, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, forgeapi.Label{Name: l})
	}
	return forgeapi.Issue{Ref: forgeapi.IssueRef{Number: number}, Author: author, Labels: ls}
}

func pull(number int, author, target string, labels ...string) forgeapi.PullRequest {
	ls := make([]forgeapi.Label, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, forgeapi.Label{Name: l})
	}
	return forgeapi.PullRequest{Ref: forgeapi.PRRef{Number: number}, Author: author, TargetBranch: target, Labels: ls}
}

const testSHA = "acb5820ced9479c074f688cc328bf03f341a511d"

// run is a run of the test workflow on main, started by a push.
func run(id int64, state forgeapi.CheckState, created time.Time) forgeapi.Run {
	return forgeapi.Run{
		Ref: forgeapi.RunRef{ID: id, Number: 7}, WorkflowPath: testWorkflow, Branch: "main", HeadSHA: testSHA,
		CreatedAt: created, State: state, Trigger: forgeapi.TriggerPush,
	}
}

func labeledSpec() *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: testRepo, Kind: launchrule.KindIssueLabeled,
		Filter: launchrule.Filter{Label: testLabel},
	}
}

func openedSpec(authors ...string) *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: testRepo, Kind: launchrule.KindIssueOpened,
		Filter: launchrule.Filter{Authors: authors},
	}
}

func runSpec(results ...string) *launchrule.EventSpec {
	return &launchrule.EventSpec{
		Connection: testConn, Repo: testRepo, Kind: launchrule.KindRunConcluded,
		Filter: launchrule.Filter{Workflow: testWorkflow, Branch: "main", Result: results},
	}
}

func inputOf(q []queueEntry, name string) []string {
	out := make([]string, 0, len(q))
	for i := range q {
		out = append(out, q[i].Inputs[name])
	}
	return out
}

func eq[T comparable](got, want []T) bool { return slices.Equal(got, want) }

// partialIssues is a call that stopped short for the budget, which ends the walk partial.
func partialIssues(items ...forgeapi.Issue) forgeapi.Page[forgeapi.Issue] {
	return forgeapi.Page[forgeapi.Issue]{Items: items, Partial: &forgeapi.Partial{Reason: forgeapi.PartialBudget}}
}

var errFake = errors.New("the forge answered 502")
